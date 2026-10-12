package cli

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
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
