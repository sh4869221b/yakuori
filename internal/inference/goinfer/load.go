package goinfer

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/townsendmerino/goinfer/chat"
	"github.com/townsendmerino/goinfer/decoder"

	"github.com/sh4869221b/yakuori/internal/inference"
)

type modelConfig struct{ path, quant string }

func open(ctx context.Context, config modelConfig, load loaders) (_ *Engine, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if config.quant != "int4" {
		return nil, ErrUnsupportedQuant
	}
	digest, err := modelIdentity(config.path)
	if err != nil {
		return nil, fmt.Errorf("read model identity: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	model, err := load.model(config.path, decoder.Options{Backend: "cpu", Quant: config.quant})
	if err != nil {
		return nil, fmt.Errorf("load CPU model: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, model.Close())
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	backend, quant := model.EffectiveBackend(), model.Quant()
	if backend != "cpu" {
		return nil, ErrUnsupportedBackend
	}
	if quant != config.quant {
		return nil, ErrUnsupportedQuant
	}
	contextTokens := model.Config().MaxPositions
	if contextTokens <= 0 {
		return nil, ErrUnknownContext
	}
	tok, err := load.tokenizer(config.path)
	if err != nil {
		return nil, fmt.Errorf("load model tokenizer: %w", err)
	}
	if tok.PreTokenizerDecline() != "" {
		return nil, ErrDeclinedTokenizer
	}
	source := tok.ChatTemplate()
	if source == "" {
		return nil, ErrUnknownTemplate
	}
	template, err := chat.Detect(chat.Meta{ChatTemplate: source})
	if err != nil {
		return nil, ErrUnknownTemplate
	}
	stops := template.Stops().Strings
	if len(stops) == 0 {
		return nil, ErrUnknownStop
	}
	stopIDs := make([]int, len(stops))
	for i, stop := range stops {
		id, ok := tok.TokenID(stop)
		if !ok {
			return nil, ErrUnknownStop
		}
		stopIDs[i] = id
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Engine{
		model: model, tokenizer: tok, template: template, stopIDs: stopIDs,
		info: inference.ModelInfo{
			BackendPin: backendPin, Backend: backend, ComputeQuant: quant,
			ModelSHA256: digest, ContextTokens: contextTokens, PolicySchema: inference.PolicySchemaV1,
			Template:  inference.TemplateIdentity{Family: template.Name(), Source: source, RendererVersion: backendPin},
			Tokenizer: inference.TokenizerIdentity{ModelSHA256: digest, BackendPin: backendPin},
		},
	}, nil
}

func modelIdentity(path string) (digest [32]byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return digest, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return digest, err
	}
	copy(digest[:], h.Sum(nil))
	return digest, nil
}
