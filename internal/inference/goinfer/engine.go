package goinfer

import (
	"context"
	"errors"
	"sync"

	"github.com/townsendmerino/goinfer/chat"
	"github.com/townsendmerino/goinfer/decoder"
	"github.com/townsendmerino/goinfer/tokenizer"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

const backendPin = "github.com/townsendmerino/goinfer@v0.20.0"

var (
	ErrClosed             = errors.New("inference engine is closed")
	ErrForeignRequest     = errors.New("request belongs to another model, tokenizer, or template")
	ErrUnsupportedQuant   = errors.New("unsupported compute quantization")
	ErrUnsupportedBackend = errors.New("unsupported effective backend")
	ErrUnknownContext     = errors.New("unknown model context")
	ErrDeclinedTokenizer  = errors.New("tokenizer declined model")
	ErrUnknownTemplate    = errors.New("unknown chat template")
	ErrUnknownStop        = errors.New("unresolved template stop token")
)

type modelBackend interface {
	Config() *decoder.Config
	EffectiveBackend() string
	Quant() string
	Close() error
}

type requestTokenizer interface {
	ChatTemplate() string
	PreTokenizerDecline() string
	TokenID(string) (int, bool)
	EncodeSegments([]tokenizer.Segment, bool) ([]int, error)
}

type loaders struct {
	model     func(string, decoder.Options) (modelBackend, error)
	tokenizer func(string) (requestTokenizer, error)
}

type Engine struct {
	mu        sync.Mutex
	model     modelBackend
	tokenizer requestTokenizer
	template  *chat.Template
	info      inference.ModelInfo
	stopIDs   []int
	closed    bool
	closeErr  error
}

func Open(ctx context.Context, modelPath, computeQuant string) (*Engine, error) {
	return open(ctx, modelConfig{path: modelPath, quant: computeQuant}, loaders{
		model: func(path string, options decoder.Options) (modelBackend, error) {
			return decoder.Load(path, options)
		},
		tokenizer: func(path string) (requestTokenizer, error) { return tokenizer.LoadGGUF(path) },
	})
}

func (e *Engine) Info(ctx context.Context) (inference.ModelInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return inference.ModelInfo{}, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return inference.ModelInfo{}, err
	}
	return e.info, nil
}

func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closed {
		e.closed = true
		e.closeErr = e.model.Close()
	}
	return e.closeErr
}

func (e *Engine) identity() inference.RequestIdentity {
	return inference.RequestIdentity{
		ModelSHA256: e.info.ModelSHA256, Tokenizer: e.info.Tokenizer,
		Template: e.info.Template, PromptSchema: prompt.SchemaV1,
	}
}
