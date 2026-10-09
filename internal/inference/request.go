package inference

import "slices"

// PromptSpan keeps template control text distinct from literal message content.
type PromptSpan struct {
	Text    string
	Special bool
}

type RequestIdentity struct {
	ModelSHA256  [32]byte
	Tokenizer    TokenizerIdentity
	Template     TemplateIdentity
	PromptSchema int
}

// PreparedRequest is produced by a backend after rendering and encoding once.
type PreparedRequest struct {
	RenderedPrompt string
	Spans          []PromptSpan
	TokenIDs       []int
	Identity       RequestIdentity
	StopIDs        []int
}

type GenerationRequest struct {
	renderedPrompt string
	spans          []PromptSpan
	tokenIDs       []int
	identity       RequestIdentity
	policy         EffectivePolicy
}

func NewGenerationRequest(prepared PreparedRequest, policy GenerationPolicy) (GenerationRequest, error) {
	// The zero value is not a policy: callers must explicitly choose both budgets.
	if policy.schema != PolicySchemaV1 {
		return GenerationRequest{}, ErrInvalidPolicy
	}
	return GenerationRequest{
		renderedPrompt: prepared.RenderedPrompt,
		spans:          slices.Clone(prepared.Spans),
		tokenIDs:       slices.Clone(prepared.TokenIDs),
		identity:       prepared.Identity,
		policy:         policy.snapshot(prepared.StopIDs),
	}, nil
}

func (r GenerationRequest) RenderedPrompt() string    { return r.renderedPrompt }
func (r GenerationRequest) Spans() []PromptSpan       { return slices.Clone(r.spans) }
func (r GenerationRequest) TokenIDs() []int           { return slices.Clone(r.tokenIDs) }
func (r GenerationRequest) Identity() RequestIdentity { return r.identity }

func (r GenerationRequest) Policy() EffectivePolicy {
	p := r.policy
	p.StopIDs = slices.Clone(p.StopIDs)
	return p
}
