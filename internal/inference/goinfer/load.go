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
	"golang.org/x/sys/unix"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

type modelConfig struct {
	path, backend, quant string
	contextTokens        int
	expectedSHA256       [32]byte
	checkSHA256          bool
	snapshotDir          string
}

func open(ctx context.Context, config modelConfig, load loaders) (engine *Engine, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if config.quant != "int4" {
		return nil, ErrUnsupportedQuant
	}
	if config.backend != "cpu" && config.backend != "cuda" {
		return nil, fmt.Errorf("%w: requested %q; choose cpu or cuda", ErrUnsupportedBackend, config.backend)
	}
	path, digest, err := snapshotModel(config.path, config.snapshotDir, config.checkSHA256)
	if err != nil {
		return nil, fmt.Errorf("read model identity: %w", err)
	}
	var model modelBackend
	defer func() {
		// Linux mappings remain valid after unlink; neither loader can reopen the
		// caller's mutable path once the private snapshot has been created.
		err = errors.Join(err, os.Remove(path))
		if err != nil {
			if model != nil {
				err = errors.Join(err, model.Close())
			}
			engine = nil
		}
	}()
	if config.checkSHA256 && digest != config.expectedSHA256 {
		return nil, fmt.Errorf("model SHA-256 mismatch: expected %x, got %x", config.expectedSHA256, digest)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options := decoder.Options{Backend: config.backend, Quant: config.quant}
	if config.backend == "cuda" {
		options.ResidentContext = config.contextTokens
	}
	model, err = load.model(path, options)
	if err != nil {
		return nil, fmt.Errorf("load %s model: %w", config.backend, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	backend, quant := model.EffectiveBackend(), model.Quant()
	if backend != config.backend {
		return nil, fmt.Errorf("%w: requested %s, effective %s", ErrUnsupportedBackend, config.backend, backend)
	}
	if quant != config.quant {
		return nil, ErrUnsupportedQuant
	}
	residentActive, residentCap, residentDecline := model.ResidentActive(), model.ResidentContextCap(), model.ResidentDecline()
	if backend == "cuda" && (!residentActive || residentCap <= 0 || residentDecline != "") {
		return nil, fmt.Errorf("%w: active=%t cap=%d decline=%s", ErrCUDAResidency, residentActive, residentCap, residentDecline)
	}
	modelContext := model.Config().MaxPositions
	if modelContext <= 0 {
		return nil, ErrUnknownContext
	}
	contextTokens := modelContext
	if config.contextTokens > 0 {
		contextTokens = min(contextTokens, config.contextTokens)
	}
	if backend == "cuda" {
		contextTokens = min(contextTokens, residentCap)
	}
	tok, err := load.tokenizer(path)
	if err != nil {
		return nil, fmt.Errorf("load model tokenizer: %w", err)
	}
	if tok.PreTokenizerDecline() != "" {
		return nil, ErrDeclinedTokenizer
	}
	source := tok.ChatTemplate()
	if source != qwenChatTemplate && source != indexChatTemplate {
		return nil, ErrUnknownTemplate
	}
	template := chat.ChatML()
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
	promptIdentity, prefix := prompt.PlainIdentity(), ""
	if source == indexChatTemplate {
		promptIdentity, prefix = prompt.IndexIdentity(), disabledThinkingPrefix
	}
	kvPrecision := model.ResidentKVPrecision()
	if backend == "cpu" {
		kvPrecision = "not-applicable"
	}
	return &Engine{
		model: model, tokenizer: tok, template: template, stopIDs: stopIDs,
		info: inference.ModelInfo{
			BackendPin: backendPin, CUDABackendPin: cudaBackendPin, RequestedBackend: config.backend, Backend: backend, ComputeQuant: quant,
			ModelSHA256: digest, ContextTokens: contextTokens, PolicySchema: inference.PolicySchemaV1,
			ModelContextTokens: modelContext, ConfiguredContextTokens: config.contextTokens,
			ResidentActive: residentActive, ResidentContextCap: residentCap, ResidentDecline: residentDecline, KVPrecision: kvPrecision,
			Template:  inference.TemplateIdentity{Family: template.Name(), Source: source, RendererVersion: backendPin},
			Tokenizer: inference.TokenizerIdentity{ModelSHA256: digest, BackendPin: backendPin},
			Prefill:   prefillPolicy(model, backend),
			Prompt:    promptIdentity, Thinking: inference.ThinkingDisabled, DisabledThinkingPrefix: prefix,
		},
	}, nil
}

func snapshotModel(path, dir string, noFollow bool) (snapshot string, digest [32]byte, err error) {
	flags := os.O_RDONLY | unix.O_NONBLOCK
	if noFollow {
		flags |= unix.O_NOFOLLOW
	}
	f, err := os.OpenFile(path, flags, 0)
	if err != nil {
		return "", digest, err
	}
	defer func() {
		err = errors.Join(err, f.Close())
		if err != nil && snapshot != "" {
			err = errors.Join(err, os.Remove(snapshot))
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return "", digest, err
	}
	if !info.Mode().IsRegular() {
		return "", digest, errors.New("model must be a regular file")
	}
	copyFile, err := os.CreateTemp(dir, "yakuori-model-*.gguf")
	if err != nil {
		return "", digest, err
	}
	snapshot = copyFile.Name()
	defer func() { err = errors.Join(err, copyFile.Close()) }()
	h := sha256.New()
	if _, err = io.Copy(io.MultiWriter(copyFile, h), f); err != nil {
		return snapshot, digest, err
	}
	if err = copyFile.Chmod(0400); err != nil {
		return snapshot, digest, err
	}
	copy(digest[:], h.Sum(nil))
	return snapshot, digest, nil
}
