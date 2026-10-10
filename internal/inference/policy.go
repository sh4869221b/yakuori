package inference

import (
	"errors"
	"slices"
	"time"
)

const PolicySchemaV1 = 1

var ErrInvalidPolicy = errors.New("invalid generation policy")

type GenerationPolicy struct {
	schema          int
	maxOutputTokens int
	requestTimeout  time.Duration
}

func NewGenerationPolicy(schema, maxOutputTokens int, requestTimeout time.Duration) (GenerationPolicy, error) {
	if schema != PolicySchemaV1 || maxOutputTokens <= 0 || requestTimeout <= 0 {
		return GenerationPolicy{}, ErrInvalidPolicy
	}
	return GenerationPolicy{schema: schema, maxOutputTokens: maxOutputTokens, requestTimeout: requestTimeout}, nil
}

func (p GenerationPolicy) Schema() int                   { return p.schema }
func (p GenerationPolicy) MaxOutputTokens() int          { return p.maxOutputTokens }
func (p GenerationPolicy) RequestTimeout() time.Duration { return p.requestTimeout }

// EffectivePolicy records the fixed plain-generation path, including disabled features.
// It is a returned snapshot, not an input for choosing sampling behavior.
type EffectivePolicy struct {
	Schema                    int
	MaxOutputTokens           int
	RequestTimeout            time.Duration
	Temperature               float64
	TopK                      int
	TopP                      float64
	MinP                      float64
	Seed                      int64
	SeedFixed                 bool
	RepeatPenalty             float64
	PresencePenalty           float64
	FrequencyPenalty          float64
	RepeatLastN               int
	LogitBiasEnabled          bool
	Logprobs                  bool
	TopLogprobs               int
	LogitProcessorEnabled     bool
	LogitProcessorGateEnabled bool
	SpeculationEnabled        bool
	SessionEnabled            bool
	BatchingEnabled           bool
	StopIDs                   []int
}

func (p GenerationPolicy) snapshot(stopIDs []int) EffectivePolicy {
	return EffectivePolicy{
		Schema: p.schema, MaxOutputTokens: p.maxOutputTokens, RequestTimeout: p.requestTimeout,
		Temperature: 0, TopK: 0, TopP: 0, MinP: 0, Seed: 0, SeedFixed: true,
		RepeatPenalty: 0, PresencePenalty: 0, FrequencyPenalty: 0, RepeatLastN: 0,
		LogitBiasEnabled: false, Logprobs: false, TopLogprobs: 0,
		LogitProcessorEnabled: false, LogitProcessorGateEnabled: false,
		SpeculationEnabled: false, SessionEnabled: false, BatchingEnabled: false,
		StopIDs: slices.Clone(stopIDs),
	}
}
