//go:build cuda && cudasmoke

package goinfer

import (
	"context"
	"errors"
	"time"

	"github.com/sh4869221b/yakuori/internal/config"
)

type cudaSetup struct {
	RequestedBackend  string  `json:"requested_backend"`
	EffectiveBackend  string  `json:"effective_backend"`
	BackendPin        string  `json:"backend_pin"`
	CUDABackendPin    string  `json:"cuda_backend_pin"`
	Quant             string  `json:"quant"`
	ResidentActive    bool    `json:"resident_active"`
	ResidentDecline   string  `json:"resident_decline"`
	ResidentCap       int     `json:"resident_context_cap"`
	ModelContext      int     `json:"model_context_tokens"`
	ConfiguredContext int     `json:"configured_context_tokens"`
	EffectiveContext  int     `json:"context_tokens"`
	PrefillBatched    bool    `json:"static_prefill_batched"`
	PrefillReason     string  `json:"static_prefill_reason"`
	KVPrecision       string  `json:"resident_kv_precision"`
	TuningPolicy      string  `json:"tuning_policy"`
	LoadMS            float64 `json:"load_ms"`
}

func newCUDAEvaluation(ctx context.Context, source, backend string) (engine *Engine, setup cudaSetup, err error) {
	started := time.Now()
	setup.RequestedBackend, setup.TuningPolicy = backend, "upstream defaults; inherited GOINFER_* cleared by runner; configured context 4096; CUDA resident context pinned to 4096"
	defer func() { setup.LoadMS = float64(time.Since(started)) / float64(time.Millisecond) }()
	limits := config.DefaultLimits()
	engine, err = OpenWithOptions(ctx, source, Options{Backend: backend, ComputeQuant: "int4", ContextTokens: limits.ContextTokens})
	if err != nil {
		return nil, setup, err
	}
	info, err := engine.Info(ctx)
	if err != nil {
		return nil, setup, errors.Join(err, engine.Close())
	}
	setup.RequestedBackend, setup.EffectiveBackend, setup.Quant = info.RequestedBackend, info.Backend, info.ComputeQuant
	setup.BackendPin, setup.CUDABackendPin = info.BackendPin, info.CUDABackendPin
	setup.ResidentActive, setup.ResidentDecline, setup.ResidentCap = info.ResidentActive, info.ResidentDecline, info.ResidentContextCap
	setup.ModelContext, setup.ConfiguredContext, setup.EffectiveContext = info.ModelContextTokens, info.ConfiguredContextTokens, info.ContextTokens
	setup.KVPrecision = info.KVPrecision
	setup.PrefillBatched, setup.PrefillReason = engine.model.(*decoderModel).PrefillPath()
	engine.model = &cudaObserver{modelBackend: engine.model}
	return engine, setup, nil
}
