package models

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sh4869221b/yakuori/internal/modelbundle"
)

func TestImportPLaMoBundleOwnsAllFiles(t *testing.T) {
	r, _ := fixture(t)
	source := t.TempDir()
	license, err := os.ReadFile("../../licenses/plamo-community/LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"config.json": []byte(`{"model_type":"plamo3"}`), "generation_config.json": []byte(`{}`), "tokenizer_config.json": []byte(`{}`), "tokenizer.jsonl": []byte("tokens"), "chat_template.jinja": []byte("template"), "model.safetensors": []byte("weights"), "LICENSE/en": license,
	} {
		path := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		write(t, path, data)
	}
	ctx := context.Background()
	entry, err := r.Import(ctx, "plamo3", source)
	if err != nil {
		t.Fatal(err)
	}
	if yes, err := modelbundle.Is(r.modelPath("plamo3")); err != nil || !yes {
		t.Fatal("managed model is not a sealed artifact")
	}
	if id, err := r.Selected(); err != nil || id != "" {
		t.Fatal("import selected a model")
	}
	write(t, filepath.Join(source, "tokenizer.jsonl"), []byte("source mutation"))
	got, err := r.Verify(ctx, "plamo3")
	if err != nil || got != entry {
		t.Fatalf("owned model changed: %+v %v", got, err)
	}
	if err := r.Use(ctx, "plamo3"); err != nil {
		t.Fatal(err)
	}
	write(t, r.modelPath("plamo3"), []byte("managed mutation"))
	if _, err := r.Verify(ctx, "plamo3"); err == nil {
		t.Fatal("modified managed bytes verified")
	}
	if _, _, err := r.OpenSelected(ctx, "cpu"); err == nil {
		t.Fatal("modified model reached inference")
	}
}
