package inference

import (
	"slices"

	"github.com/sh4869221b/yakuori/internal/contextselection"
	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

const ThinkingDisabled = "disabled"

// PromptSpan keeps template control text distinct from literal message content.
type PromptSpan struct {
	Text    string
	Special bool
}

type RequestIdentity struct {
	RemoteModel            string
	ModelSHA256            [32]byte
	Tokenizer              TokenizerIdentity
	Template               TemplateIdentity
	PromptSchema           int
	Prompt                 prompt.Identity
	Thinking               string
	DisabledThinkingPrefix string
}

// PreparedRequest is produced by a backend after rendering and encoding once.
type PreparedRequest struct {
	RenderedPrompt  string
	Spans           []PromptSpan
	TokenIDs        []int
	Identity        RequestIdentity
	StopIDs         []int
	ProtectedSource string
	Terms           glossary.Glossary
	ResolvedContext contextselection.ResolvedContext
}

type GenerationRequest struct {
	renderedPrompt  string
	spans           []PromptSpan
	tokenIDs        []int
	identity        RequestIdentity
	policy          EffectivePolicy
	protectedSource string
	terms           glossary.Glossary
	resolvedContext contextselection.ResolvedContext
}

func NewGenerationRequest(prepared PreparedRequest, policy GenerationPolicy) (GenerationRequest, error) {
	// The zero value is not a policy: callers must explicitly choose both budgets.
	if policy.schema != PolicySchemaV1 {
		return GenerationRequest{}, ErrInvalidPolicy
	}
	return GenerationRequest{
		renderedPrompt:  prepared.RenderedPrompt,
		spans:           slices.Clone(prepared.Spans),
		tokenIDs:        slices.Clone(prepared.TokenIDs),
		identity:        prepared.Identity,
		policy:          effectivePolicy(prepared, policy),
		protectedSource: prepared.ProtectedSource,
		terms:           prepared.Terms,
		resolvedContext: prepared.ResolvedContext,
	}, nil
}

func (r GenerationRequest) RenderedPrompt() string    { return r.renderedPrompt }
func (r GenerationRequest) Spans() []PromptSpan       { return slices.Clone(r.spans) }
func (r GenerationRequest) TokenIDs() []int           { return slices.Clone(r.tokenIDs) }
func (r GenerationRequest) Identity() RequestIdentity { return r.identity }
func (r GenerationRequest) Terms() glossary.Glossary  { return r.terms }
func (r GenerationRequest) ProtectedSource() string   { return r.protectedSource }
func (r GenerationRequest) ResolvedContext() contextselection.ResolvedContext {
	return r.resolvedContext
}

func (r GenerationRequest) Policy() EffectivePolicy {
	p := r.policy
	p.StopIDs = slices.Clone(p.StopIDs)
	return p
}

// effectivePolicy preserves local defaults while marking the remote API sampling policy as provider-defined.
func effectivePolicy(prepared PreparedRequest, policy GenerationPolicy) EffectivePolicy {
	p := policy.snapshot(prepared.StopIDs)
	if prepared.Identity.RemoteModel != "" {
		p.SeedFixed = false
		p.SamplingMode = "provider-default"
	}
	return p
}
