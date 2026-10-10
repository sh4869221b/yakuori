// Package inference defines the backend-independent generation boundary.
package inference

import (
	"context"
	"time"
)

type Finish string

const (
	Stop          Finish = "stop"
	MaxTokens     Finish = "max_tokens"
	ContextLimit  Finish = "context_limit"
	Timeout       Finish = "timeout"
	Canceled      Finish = "canceled"
	DecodeError   Finish = "decode_error"
	InvalidOutput Finish = "invalid_output"
)

type TokenizerIdentity struct {
	ModelSHA256 [32]byte
	BackendPin  string
}

type TemplateIdentity struct {
	Family          string
	Source          string
	RendererVersion string
}

type ModelInfo struct {
	BackendPin              string
	CUDABackendPin          string
	RequestedBackend        string
	Backend                 string
	ComputeQuant            string
	ModelSHA256             [32]byte
	ModelContextTokens      int
	ConfiguredContextTokens int
	ContextTokens           int
	ResidentActive          bool
	ResidentContextCap      int
	ResidentDecline         string
	KVPrecision             string
	Template                TemplateIdentity
	Tokenizer               TokenizerIdentity
	PolicySchema            int
}

type GenerationResult struct {
	Text         string
	Finish       Finish
	PromptTokens int
	OutputTokens int
	Policy       EffectivePolicy
	Deadline     time.Time
}

type Engine interface {
	Info(context.Context) (ModelInfo, error)
	CountTokens(context.Context, GenerationRequest) (int, error)
	Generate(context.Context, GenerationRequest) (GenerationResult, error)
	Close() error
}
