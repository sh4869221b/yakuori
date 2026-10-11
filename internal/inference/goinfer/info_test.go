package goinfer

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/townsendmerino/goinfer/decoder"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
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
		Template:    inference.TemplateIdentity{Family: "chatml", Source: tok.source, RendererVersion: backendPin},
		Tokenizer:   inference.TokenizerIdentity{ModelSHA256: digest, BackendPin: backendPin},
		KVPrecision: "not-applicable", Prefill: inference.PrefillPolicy{Path: "cpu", CPUFastAttention: true, Fast: "off"},
		Prompt: prompt.PlainIdentity(), Thinking: inference.ThinkingDisabled,
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
			wantKV := model.kvPrecision
			if tc.backend == "cpu" {
				wantKV = "not-applicable"
			}
			if err != nil || info.ContextTokens != tc.effective || info.ModelContextTokens != tc.model ||
				info.ConfiguredContextTokens != tc.configured || info.RequestedBackend != tc.backend || info.Backend != tc.backend ||
				info.CUDABackendPin != cudaBackendPin || info.ResidentActive != model.residentActive || info.ResidentContextCap != tc.resident ||
				info.ResidentDecline != model.residentDecline || info.KVPrecision != wantKV || info.PolicySchema != inference.PolicySchemaV1 {
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

func TestInfoPrefillSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, backend     string
		exact, capability bool
		knobs             map[string]string
		want              inference.PrefillPolicy
	}{
		{"CPU default", "cpu", false, true, nil, inference.PrefillPolicy{Path: "cpu", CPUFastAttention: true, Fast: "off"}},
		{"CPU disabled", "cpu", false, true, map[string]string{"GOINFER_CPU_FAST_ATTENTION": "0"}, inference.PrefillPolicy{Path: "cpu", Fast: "off"}},
		{"CPU exact", "cpu", true, true, nil, inference.PrefillPolicy{Path: "cpu", Exact: true, Fast: "off"}},
		{"CUDA default", "cuda", false, true, nil, inference.PrefillPolicy{Path: "cuda", Batched: true, BatchedCapability: true, Fast: "both", Floor: 512, Chunk: 512}},
		{"CUDA normalized attn", "cuda", false, true, map[string]string{"GOINFER_CUDA_FAST_PREFILL": " ATTN ", "GOINFER_CUDA_FAST_PREFILL_FLOOR": "0", "GOINFER_PREFILL_CHUNK": "128"}, inference.PrefillPolicy{Path: "cuda", Batched: true, BatchedCapability: true, Fast: "attn", Chunk: 128}},
		{"CUDA gemm", "cuda", false, true, map[string]string{"GOINFER_CUDA_FAST_PREFILL": "gemm"}, inference.PrefillPolicy{Path: "cuda", Batched: true, BatchedCapability: true, Fast: "gemm", Floor: 512, Chunk: 512}},
		{"CUDA unknown and invalid limits", "cuda", false, true, map[string]string{"GOINFER_CUDA_FAST_PREFILL": "unknown", "GOINFER_CUDA_FAST_PREFILL_FLOOR": "-1", "GOINFER_PREFILL_CHUNK": "invalid"}, inference.PrefillPolicy{Path: "cuda", Batched: true, BatchedCapability: true, Fast: "both", Floor: 512, Chunk: 512}},
		{"CUDA fast off retains chunk", "cuda", false, true, map[string]string{"GOINFER_CUDA_FAST_PREFILL": " FALSE ", "GOINFER_PREFILL_CHUNK": "128"}, inference.PrefillPolicy{Path: "cuda", Batched: true, BatchedCapability: true, Fast: "off", Chunk: 128}},
		{"CUDA exact retains chunk", "cuda", true, true, map[string]string{"GOINFER_PREFILL_CHUNK": "128"}, inference.PrefillPolicy{Path: "cuda", Exact: true, Batched: true, BatchedCapability: true, Fast: "off", Chunk: 128}},
		{"CUDA disabled batched policy", "cuda", false, false, map[string]string{"GOINFER_BATCHED_PREFILL": "0"}, inference.PrefillPolicy{Path: "cuda", Fast: "off"}},
		{"CUDA no static capability", "cuda", false, false, nil, inference.PrefillPolicy{Path: "cuda", Batched: true, Fast: "off"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, load, model, _ := loadFixture(t)
			config.backend, model.backend = tc.backend, tc.backend
			model.residentActive, model.residentCap, model.kvPrecision = tc.backend == "cuda", 8192, "f16"
			model.exactPrefill, model.batchedCapability, model.knobs = tc.exact, tc.capability, tc.knobs
			load.model = func(string, decoder.Options) (modelBackend, error) { return model, nil }
			engine, err := open(context.Background(), config, load)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			model.exactPrefill, model.batchedCapability, model.prefillReason = !tc.exact, !tc.capability, "changed diagnostic"
			model.knobs = map[string]string{"GOINFER_CUDA_FAST_PREFILL": "off", "GOINFER_CPU_FAST_ATTENTION": "0"}
			t.Setenv("GOINFER_CUDA_FAST_PREFILL", "off")
			t.Setenv("GOINFER_CPU_FAST_ATTENTION", "0")
			info, err := engine.Info(context.Background())
			if err != nil || info.Prefill != tc.want {
				t.Fatalf("prefill=%+v want=%+v error=%v", info.Prefill, tc.want, err)
			}
		})
	}
}
