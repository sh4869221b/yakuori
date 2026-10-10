package goinfer

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/townsendmerino/goinfer/decoder"

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
		BackendPin: backendPin, CUDABackendPin: cudaBackendPin, RequestedBackend: "cpu", Backend: "cpu", ComputeQuant: "int4", ModelSHA256: digest,
		ModelContextTokens: model.contextTokens,
		ContextTokens:      model.contextTokens, PolicySchema: inference.PolicySchemaV1,
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

func TestEffectiveContextInfo(t *testing.T) {
	for _, tc := range []struct {
		name, backend                          string
		model, configured, resident, effective int
	}{
		{"legacy CPU context", "cpu", 262144, 0, 0, 262144},
		{"configured CPU context", "cpu", 262144, 4096, 0, 4096},
		{"CPU model cap", "cpu", 2048, 4096, 0, 2048},
		{"configured CUDA context", "cuda", 262144, 4096, 4096, 4096},
		{"CUDA resident cap", "cuda", 262144, 4096, 2048, 2048},
		{"CUDA model cap", "cuda", 1024, 4096, 2048, 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, load, model, _ := loadFixture(t)
			config.backend, config.contextTokens = tc.backend, tc.configured
			model.backend, model.contextTokens = tc.backend, tc.model
			model.residentActive, model.residentCap = tc.backend == "cuda", tc.resident
			if tc.backend == "cuda" {
				model.kvPrecision = "f16"
			}
			load.model = func(string, decoder.Options) (modelBackend, error) { return model, nil }
			engine, err := open(context.Background(), config, load)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			info, err := engine.Info(context.Background())
			if err != nil || info.ContextTokens != tc.effective || info.ModelContextTokens != tc.model ||
				info.ConfiguredContextTokens != tc.configured || info.RequestedBackend != tc.backend || info.Backend != tc.backend ||
				info.CUDABackendPin != cudaBackendPin || info.ResidentActive != model.residentActive || info.ResidentContextCap != tc.resident ||
				info.ResidentDecline != model.residentDecline || info.KVPrecision != model.kvPrecision || info.PolicySchema != inference.PolicySchemaV1 {
				t.Fatalf("info=%+v error=%v", info, err)
			}
		})
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
