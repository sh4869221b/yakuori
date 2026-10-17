package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/publication"
	"golang.org/x/sys/unix"
)

func translationPublicationSupported(t *testing.T, dir string) bool {
	t.Helper()
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		t.Fatal(err)
	}
	// This only selects the refusal expectation for an unsupported magic.
	// A qualified magic still requires every production mount/capability check
	// to succeed; an unexpected Prepare error must not become a passing test.
	supported := st.Type == unix.EXT4_SUPER_MAGIC || st.Type == unix.BTRFS_SUPER_MAGIC
	t.Logf("fixture filesystem magic=%x supported_magic=%t", st.Type, supported)
	return supported
}

func checkTranslationFilesystemRefusal(t *testing.T, result localize.Result, err error) {
	t.Helper()
	var failure *localize.Error
	if !errors.Is(err, publication.ErrUnsupportedFilesystem) || !errors.As(err, &failure) || failure.Phase != "prepare" ||
		result.TMCommitted || result.BytesWritten != 0 || result.Publication.State != publication.NotPublished {
		t.Fatalf("want typed unsupported filesystem refusal: result=%+v err=%v", result, err)
	}
	t.Log("typed ErrUnsupportedFilesystem refusal; generation/publication scenario not reached")
}

func translationFixture(t *testing.T, source string) (translationCommand, config.Paths, *translationEngine) {
	t.Helper()
	return translationFixtureAt(t, t.TempDir(), source)
}

func translationFixtureAt(t *testing.T, dir, source string) (translationCommand, config.Paths, *translationEngine) {
	t.Helper()
	paths := config.Paths{Data: filepath.Join(dir, "data"), Config: filepath.Join(dir, "config"), Cache: filepath.Join(dir, "cache"), State: filepath.Join(dir, "state")}
	if err := os.Mkdir(paths.Data, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	command, _, err := parseTranslation("translate", translationArgs("translate", path)[1:])
	if err != nil {
		t.Fatal(err)
	}
	engine := &translationEngine{text: "訳文", finish: inference.Stop, info: inference.ModelInfo{
		ModelSHA256: [32]byte{1}, Backend: "cpu", BackendPin: "goinfer-v1", ComputeQuant: "int4", ContextTokens: 4096,
		Tokenizer:    inference.TokenizerIdentity{ModelSHA256: [32]byte{1}, BackendPin: "goinfer-v1"},
		Template:     inference.TemplateIdentity{Family: "chatml", Source: "embedded", RendererVersion: "v1"},
		PolicySchema: inference.PolicySchemaV1, Prompt: prompt.PlainIdentity(), Thinking: inference.ThinkingDisabled,
		Prefill: inference.PrefillPolicy{Path: "cpu", Fast: "off"},
	}}
	return command, paths, engine
}
