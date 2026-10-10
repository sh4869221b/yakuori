package goinfer

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/sh4869221b/yakuori/internal/inference"
)

func TestModelInfo(t *testing.T) {
	config, load, model, tok := loadFixture(t)
	engine, err := open(context.Background(), config, load)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	digest := sha256.Sum256([]byte(fixtureModel))
	want := inference.ModelInfo{
		BackendPin: backendPin, Backend: "cpu", ComputeQuant: "int4", ModelSHA256: digest,
		ContextTokens: model.contextTokens, PolicySchema: inference.PolicySchemaV1,
		Template:  inference.TemplateIdentity{Family: "chatml", Source: tok.source, RendererVersion: backendPin},
		Tokenizer: inference.TokenizerIdentity{ModelSHA256: digest, BackendPin: backendPin},
	}
	info, err := engine.Info(context.Background())
	if err != nil || info != want {
		t.Fatalf("info=%+v error=%v", info, err)
	}
	info.Template.Source = "changed"
	info.ModelSHA256[0]++
	got, err := engine.Info(context.Background())
	if err != nil || got != want {
		t.Fatalf("Info shared writable state: %+v, %v", got, err)
	}
}

func TestCloseIdle(t *testing.T) {
	config, load, model, _ := loadFixture(t)
	engine, err := open(context.Background(), config, load)
	if err != nil {
		t.Fatal(err)
	}
	model.closeErr = errors.New("close failed")
	for range 2 {
		if err := engine.Close(); !errors.Is(err, model.closeErr) {
			t.Fatalf("Close error=%v", err)
		}
	}
	if model.closes != 1 {
		t.Fatalf("backend Close calls=%d", model.closes)
	}
	if _, err := engine.Info(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Info after Close error=%v", err)
	}
}
