//go:build cuda && cudasmoke

package goinfer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/townsendmerino/goinfer/chat"
	_ "github.com/townsendmerino/goinfer/cuda"
	"github.com/townsendmerino/goinfer/decoder"
	"github.com/townsendmerino/goinfer/tokenizer"

	"github.com/sh4869221b/yakuori/internal/inference"
)

type cudaSetup struct {
	RequestedBackend string  `json:"requested_backend"`
	EffectiveBackend string  `json:"effective_backend"`
	Quant            string  `json:"quant"`
	ResidentActive   bool    `json:"resident_active"`
	ResidentDecline  string  `json:"resident_decline"`
	ResidentCap      int     `json:"resident_context_cap"`
	ModelContext     int     `json:"model_context_tokens"`
	EffectiveContext int     `json:"context_tokens"`
	PrefillBatched   bool    `json:"static_prefill_batched"`
	PrefillReason    string  `json:"static_prefill_reason"`
	KVPrecision      string  `json:"resident_kv_precision"`
	TuningPolicy     string  `json:"tuning_policy"`
	LoadMS           float64 `json:"load_ms"`
}

func cudaContext(setup cudaSetup) (int, error) {
	if setup.ModelContext <= 0 {
		return 0, ErrUnknownContext
	}
	cap := min(4096, setup.ModelContext)
	if setup.ResidentCap > 0 {
		cap = min(cap, setup.ResidentCap)
	}
	if setup.EffectiveBackend != setup.RequestedBackend || setup.Quant != "int4" {
		return cap, fmt.Errorf("unsupported effective backend/quant: %s/%s", setup.EffectiveBackend, setup.Quant)
	}
	if setup.RequestedBackend == "cuda" && (!setup.ResidentActive || setup.ResidentCap <= 0) {
		return cap, fmt.Errorf("unsupported resident CUDA: active=%t cap=%d decline=%s", setup.ResidentActive, setup.ResidentCap, setup.ResidentDecline)
	}
	return cap, nil
}

func newCUDAEvaluation(ctx context.Context, source, backend string) (engine *Engine, setup cudaSetup, err error) {
	started := time.Now()
	setup.RequestedBackend, setup.TuningPolicy = backend, "upstream defaults; inherited GOINFER_* cleared by runner; resident context pinned to 4096"
	defer func() { setup.LoadMS = float64(time.Since(started)) / float64(time.Millisecond) }()
	if err = ctx.Err(); err != nil {
		return
	}
	path, digest, err := snapshotModel(source)
	if err != nil {
		return nil, setup, fmt.Errorf("snapshot model: %w", err)
	}
	var model *decoder.Model
	defer func() {
		err = errors.Join(err, os.Remove(path))
		if err != nil && model != nil {
			err = errors.Join(err, model.Close())
			engine = nil
		}
	}()
	model, err = decoder.Load(path, decoder.Options{Backend: backend, Quant: "int4", ResidentContext: 4096})
	if err != nil {
		return nil, setup, fmt.Errorf("load %s model: %w", backend, err)
	}
	setup.EffectiveBackend, setup.Quant = model.EffectiveBackend(), model.Quant()
	setup.ResidentActive, setup.ResidentDecline, setup.ResidentCap = model.ResidentActive(), model.ResidentDecline(), model.ResidentContextCap()
	setup.ModelContext = model.Config().MaxPositions
	setup.PrefillBatched, setup.PrefillReason = model.PrefillPath()
	setup.KVPrecision = model.ResidentKVPrecision()
	setup.EffectiveContext, err = cudaContext(setup)
	if err != nil {
		return nil, setup, err
	}
	tok, err := tokenizer.LoadGGUF(path)
	if err != nil {
		return nil, setup, err
	}
	if tok.PreTokenizerDecline() != "" {
		return nil, setup, ErrDeclinedTokenizer
	}
	sourceTemplate := tok.ChatTemplate()
	if sourceTemplate != qwenChatTemplate && sourceTemplate != indexChatTemplate {
		return nil, setup, ErrUnknownTemplate
	}
	template := chat.ChatML()
	stops := template.Stops().Strings
	if len(stops) == 0 {
		return nil, setup, ErrUnknownStop
	}
	ids := make([]int, len(stops))
	for i, stop := range stops {
		id, ok := tok.TokenID(stop)
		if !ok {
			return nil, setup, ErrUnknownStop
		}
		ids[i] = id
	}
	if err = ctx.Err(); err != nil {
		return nil, setup, err
	}
	engine = &Engine{model: &cudaObserver{modelBackend: &decoderModel{model}}, tokenizer: tok, template: template, stopIDs: ids, info: inference.ModelInfo{
		BackendPin: backendPin, Backend: setup.EffectiveBackend, ComputeQuant: setup.Quant, ModelSHA256: digest, ContextTokens: setup.EffectiveContext, PolicySchema: inference.PolicySchemaV1,
		Template: inference.TemplateIdentity{Family: template.Name(), Source: sourceTemplate, RendererVersion: backendPin}, Tokenizer: inference.TokenizerIdentity{ModelSHA256: digest, BackendPin: backendPin},
	}}
	return engine, setup, nil
}
