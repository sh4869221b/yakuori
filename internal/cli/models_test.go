package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/models"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func modelFixture(t *testing.T) config.Paths {
	t.Helper()
	root := t.TempDir()
	paths := config.Paths{Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"), State: filepath.Join(root, "state")}
	for _, dir := range []string{paths.Config, filepath.Join(paths.Data, "models", "test-model"), filepath.Join(paths.Cache, "model-snapshots")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	data := []byte("private-model-text")
	writeFixture(t, filepath.Join(paths.Data, "models", "test-model", "model.gguf"), string(data))
	writeFixture(t, filepath.Join(paths.Data, "registry.json"), fmt.Sprintf(`{"schema":1,"models":{"test-model":{"sha256":"%x","size_bytes":%d}}}`, sha256.Sum256(data), len(data)))
	return paths
}

func writeFixture(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func storageRun(args []string, paths config.Paths) (int, string, string) {
	var out, diag bytes.Buffer
	code := run(args, &out, &diag, func() (config.Paths, error) { return paths, nil })
	return code, out.String(), diag.String()
}

func TestModelsCLI(t *testing.T) {
	paths := modelFixture(t)
	for _, action := range []string{"verify", "use", "verify"} {
		code, out, diag := storageRun([]string{"models", action, "test-model"}, paths)
		if code != ExitOK || diag != "" || !strings.Contains(out, action+": test-model") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, diag)
		}
		if action == "verify" && (!strings.Contains(out, "model_sha256:") || !strings.Contains(out, "size_bytes: 18")) {
			t.Fatalf("metadata=%q", out)
		}
	}
	selection, err := os.ReadFile(filepath.Join(paths.Config, "config.json"))
	if err != nil || !bytes.Contains(selection, []byte(`"selected_model":"test-model"`)) {
		t.Fatalf("selection=%q err=%v", selection, err)
	}
	writeFixture(t, filepath.Join(paths.Data, "models", "test-model", "model.gguf"), "changed-model-text")
	for _, action := range []string{"verify", "use"} {
		code, out, diag := storageRun([]string{"models", action, "test-model"}, paths)
		if code != ExitFailure || out != "" || !strings.Contains(diag, "identity mismatch") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, diag)
		}
	}
	current, err := os.ReadFile(filepath.Join(paths.Config, "config.json"))
	if err != nil || !bytes.Equal(current, selection) {
		t.Fatalf("selection changed: %q err=%v", current, err)
	}
	writeFixture(t, filepath.Join(paths.Data, "registry.json"), `{"private-text":"translation-prompt"}`)
	code, out, diag := storageRun([]string{"models", "verify", "test-model"}, paths)
	if code != ExitFailure || out != "" || strings.Contains(diag, "private-text") || strings.Contains(diag, "translation-prompt") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, diag)
	}
}

func TestModelsCLIParseBeforeIO(t *testing.T) {
	for _, args := range [][]string{
		{"models", "import", "private-text"}, {"models", "import", "--id", "../private-text", "file"},
		{"models", "import", "file", "--id", "test"}, {"models", "use"}, {"models", "verify", "id", "private-text"},
		{"models", "private-text"}, {"doctor", "--backend", "cpu"}, {"doctor", "--check-model", "--backend", "private-text"},
		{"doctor", "--private-text"}, {"doctor", "--check-model", "private-text"},
		{"doctor", "--help", "private-text"}, {"models", "import", "--help", "private-text"},
		{"models", "use", "-h", "private-text"}, {"models", "verify", "--help", "private-text"},
	} {
		var out, diag bytes.Buffer
		code := run(args, &out, &diag, nil)
		if code != ExitUsage || out.Len() != 0 || diag.Len() == 0 || strings.Contains(diag.String(), "private-text") {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, out.String(), diag.String())
		}
	}
	for _, args := range [][]string{{"models", "--help"}, {"models", "import", "--help"}, {"models", "use", "-h"}, {"models", "verify", "--help"}, {"doctor", "--help"}} {
		var out, diag bytes.Buffer
		if code := run(args, &out, &diag, nil); code != ExitOK || out.Len() == 0 || diag.Len() != 0 {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, out.String(), diag.String())
		}
	}
}

func TestModelsCLIImportFailure(t *testing.T) {
	paths := modelFixture(t)
	for _, source := range []string{filepath.Join(t.TempDir(), "private-text-missing"), filepath.Join(t.TempDir(), "private-text-header")} {
		if strings.HasSuffix(source, "header") {
			writeFixture(t, source, "private-text invalid model header")
		}
		code, out, diag := storageRun([]string{"models", "import", "--id", "new-model", source}, paths)
		if code != ExitFailure || out != "" || diag == "" || strings.Contains(diag, "private-text") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, diag)
		}
	}
	if _, err := os.Stat(filepath.Join(paths.Config, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("import selected model: %v", err)
	}
}

func testTranslationAdmission(t *testing.T) {
	for _, name := range []string{"missing", "corrupt", "capability", "request", "info", "profile", "cuda"} {
		t.Run(name, func(t *testing.T) {
			command, paths, engine := translationFixture(t, "Hello")
			open := selectedEngineOpener(engine.open)
			switch name {
			case "missing", "corrupt":
				paths = modelFixture(t)
				if err := models.New(paths).Use(context.Background(), "test-model"); err != nil {
					t.Fatal(err)
				}
				modelPath := filepath.Join(paths.Data, "models", "test-model", "model.gguf")
				if name == "missing" {
					if err := os.Remove(modelPath); err != nil {
						t.Fatal(err)
					}
				} else {
					writeFixture(t, modelPath, "private corrupt model")
				}
				open = models.New(paths).OpenSelected
			case "capability":
				open = func(context.Context, string) (string, inference.Engine, error) {
					return "selected-model", struct{ inference.Engine }{engine}, nil
				}
			case "request":
				engine.requestErr = errors.New("private request")
			case "info":
				engine.infoErr = errors.New("private info")
			case "profile":
				engine.info.Tokenizer.ModelSHA256 = [32]byte{2}
			case "cuda":
				command.backend = "cuda"
			}
			var out, diag bytes.Buffer
			result, err := command.execute(context.Background(), &out, paths, open)
			if name == "cuda" {
				if err != nil || engine.info.Backend != "cuda" || !result.TMCommitted {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				return
			}
			if reportPipeline(&diag, result, err) != 1 || out.Len() != 0 || engine.generated != 0 || strings.Contains(diag.String(), "private") {
				t.Fatalf("result=%+v err=%v diag=%q", result, err, diag.String())
			}
			if _, err := os.Lstat(config.ResolveStoragePaths(paths).TMFile); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("TM was created: %v", err)
			}
			if name != "missing" && name != "corrupt" && engine.closed != 1 {
				t.Fatalf("engine closed=%d", engine.closed)
			}
		})
	}
}

func checkTranslationRow(t *testing.T, command translationCommand, paths config.Paths, engine inference.Engine, want string) {
	t.Helper()
	ctx := context.Background()
	info, err := engine.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	limits := config.DefaultLimits()
	policy, err := inference.NewGenerationPolicy(1, limits.MaxOutputTokens, limits.RequestTimeout)
	if err != nil {
		t.Fatal(err)
	}
	request, err := engine.(requestFactory).NewRequest(ctx, prompt.Input{SourceLanguage: command.sourceLanguage, TargetLanguage: command.targetLanguage}, policy)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := localize.NewCanonicalInferenceProfile(info, request, localize.InferenceProfileInput{ModelID: "selected-model", Glossary: identity.GlossaryInput{Mode: "none"}, Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(command.options.Source)
	if err != nil {
		t.Fatal(err)
	}
	session, err := textadapter.New(command.sourceLanguage, command.targetLanguage).Import(raw, profile)
	if err != nil {
		t.Fatal(err)
	}
	source, err := unit.NewSourceIdentity(session, session.Units()[0].ID(), command.scope.GameNamespace, command.scope.ArtifactNamespace, nil)
	if err != nil {
		t.Fatal(err)
	}
	key, err := profile.ExactKey(source)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+config.ResolveStoragePaths(paths).TMFile+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	var got string
	if err := db.QueryRow("SELECT translated_text FROM tm WHERE key_schema=? AND source_identity=? AND translation_profile=?", key.Schema(), key.SourceBytes(), key.ProfileBytes()).Scan(&got); err != nil || got != want {
		t.Fatalf("canonical row=%q err=%v want=%q", got, err, want)
	}
}

func TestTranslationRealTMReadback(t *testing.T) {
	root := os.Getenv("YAKUORI_TASK2_QA_ROOT")
	if root == "" {
		t.Skip("explicit Task2 real CPU readback only")
	}
	paths, err := config.Resolve(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	_, engine, err := models.New(paths).OpenSelected(context.Background(), "cpu")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := engine.Close(); err != nil {
			t.Error(err)
		}
	}()
	command, _, err := parseTranslation("translate", translationArgs("translate", filepath.Join(root, "source.txt"))[1:])
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(os.Getenv("YAKUORI_TASK2_QA_STDOUT"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || bytes.Equal(raw, []byte("Hello.\n")) || !strings.ContainsAny(string(raw), "こんにちは今日は日本訳私世界") {
		t.Fatalf("not Japanese non-echo: %q", raw)
	}
	checkTranslationRow(t, command, paths, engine, string(raw))
	t.Logf("canonical exact source/profile TM row matches %d stdout bytes: %q", len(raw), raw)
}
