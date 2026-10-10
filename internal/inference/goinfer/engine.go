package goinfer

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/townsendmerino/goinfer/chat"
	"github.com/townsendmerino/goinfer/decoder"
	"github.com/townsendmerino/goinfer/tokenizer"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

const backendPin = "github.com/townsendmerino/goinfer@v0.22.0"

var (
	ErrClosed             = errors.New("inference engine is closed")
	ErrForeignRequest     = errors.New("request belongs to another model, tokenizer, or template")
	ErrUnsupportedQuant   = errors.New("unsupported compute quantization")
	ErrUnsupportedBackend = errors.New("unsupported effective backend")
	ErrInvalidContext     = errors.New("context tokens must be positive")
	ErrCUDAResidency      = errors.New("unsupported resident CUDA")
	ErrUnknownContext     = errors.New("unknown model context")
	ErrDeclinedTokenizer  = errors.New("tokenizer declined model")
	ErrUnknownTemplate    = errors.New("unknown chat template")
	ErrUnknownStop        = errors.New("unresolved template stop token")
	ErrBusy               = errors.New("inference engine is busy")
	ErrContextLimit       = errors.New("generation exceeds model context")
	ErrInvalidOutput      = errors.New("invalid generation output")
)

type modelBackend interface {
	Config() *decoder.Config
	EffectiveBackend() string
	Quant() string
	ResidentActive() bool
	ResidentContextCap() int
	ResidentDecline() string
	ResidentKVPrecision() string
	Close() error
	generate(context.Context, generationInput) tokenStream
}

type requestTokenizer interface {
	ChatTemplate() string
	PreTokenizerDecline() string
	TokenID(string) (int, bool)
	EncodeSegments([]tokenizer.Segment, bool) ([]int, error)
	Decode([]int) (string, error)
}

type loaders struct {
	model     func(string, decoder.Options) (modelBackend, error)
	tokenizer func(string) (requestTokenizer, error)
}

type Engine struct {
	mu           sync.Mutex
	tokenizerMu  sync.Mutex
	model        modelBackend
	tokenizer    requestTokenizer
	template     *chat.Template
	info         inference.ModelInfo
	stopIDs      []int
	closing      bool
	closeDone    chan struct{}
	closeErr     error
	activeCancel context.CancelFunc
	activeDone   chan struct{}
}

type Options struct {
	Backend       string
	ComputeQuant  string
	ContextTokens int
}

func Open(ctx context.Context, modelPath, computeQuant string) (*Engine, error) {
	return open(ctx, modelConfig{path: modelPath, backend: "cpu", quant: computeQuant}, defaultLoaders())
}

func OpenWithOptions(ctx context.Context, modelPath string, options Options) (*Engine, error) {
	if options.ContextTokens <= 0 {
		return nil, ErrInvalidContext
	}
	return open(ctx, modelConfig{path: modelPath, backend: options.Backend, quant: options.ComputeQuant, contextTokens: options.ContextTokens}, defaultLoaders())
}

func defaultLoaders() loaders {
	return loaders{
		model: func(path string, options decoder.Options) (modelBackend, error) {
			if options.Backend == "cuda" && cudaBackendPin == "" {
				return nil, fmt.Errorf("%w: CUDA backend not built in; build with -tags cuda on linux/amd64", ErrUnsupportedBackend)
			}
			model, err := decoder.Load(path, options)
			if err != nil {
				return nil, err
			}
			return &decoderModel{Model: model}, nil
		},
		tokenizer: func(path string) (requestTokenizer, error) { return tokenizer.LoadGGUF(path) },
	}
}

func (e *Engine) Info(ctx context.Context) (inference.ModelInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing {
		return inference.ModelInfo{}, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return inference.ModelInfo{}, err
	}
	return e.info, nil
}

func (e *Engine) identity() inference.RequestIdentity {
	return inference.RequestIdentity{
		ModelSHA256: e.info.ModelSHA256, Tokenizer: e.info.Tokenizer,
		Template: e.info.Template, PromptSchema: prompt.SchemaV1,
	}
}

var _ inference.Engine = (*Engine)(nil)
