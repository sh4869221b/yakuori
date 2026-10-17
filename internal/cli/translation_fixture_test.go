package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

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
