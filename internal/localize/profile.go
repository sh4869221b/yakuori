package localize

import (
	"errors"
	"fmt"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/segment"
	"github.com/sh4869221b/yakuori/internal/validate"
)

const ProtectionFixtures = "explicit-spans-fixtures-v1"
const ValidationFixtures = "restored-content-fixtures-v1"

var ErrInferenceProfile = errors.New("inference profile does not match effective conditions")

type InferenceProfileInput struct {
	ModelID           string
	Glossary          identity.GlossaryInput
	AdditionalContext []byte
	Limits            config.Limits
	Segmentation      bool
}

func NewCanonicalInferenceProfile(info inference.ModelInfo, request inference.GenerationRequest, input InferenceProfileInput) (validate.Profile, error) {
	if len(input.AdditionalContext) != 0 {
		return validate.Profile{}, fmt.Errorf("%w: additional context is unsupported by the current prompt", ErrInferenceProfile)
	}
	effective, err := EffectiveInferenceProfile(info, request, input.Limits, input.Segmentation)
	if err != nil {
		return validate.Profile{}, err
	}
	effective.ModelID, effective.Glossary = input.ModelID, input.Glossary
	return validate.NewCanonicalProfile(effective)
}

// EffectiveInferenceProfile projects only common runtime conditions. The caller
// supplies ModelID and Glossary when constructing a persistent profile.
func EffectiveInferenceProfile(info inference.ModelInfo, request inference.GenerationRequest, limits config.Limits, segmented bool) (identity.ProfileInput, error) {
	input, err := EffectiveModelProfile(info, limits, segmented)
	if err != nil {
		return identity.ProfileInput{}, err
	}
	id, policy := request.Identity(), request.Policy()
	if id.ModelSHA256 != info.ModelSHA256 || id.Tokenizer != info.Tokenizer || id.Template != info.Template ||
		id.PromptSchema != info.Prompt.Schema || id.Prompt != info.Prompt || id.Thinking != info.Thinking || id.DisabledThinkingPrefix != info.DisabledThinkingPrefix ||
		policy.Schema != info.PolicySchema || policy.Schema != inference.PolicySchemaV1 || policy.MaxOutputTokens <= 0 || policy.MaxOutputTokens > limits.MaxOutputTokens ||
		policy.RequestTimeout <= 0 || policy.RequestTimeout > limits.RequestTimeout {
		return identity.ProfileInput{}, ErrInferenceProfile
	}
	input.Generation = identity.GenerationInput{
		Schema: policy.Schema, MaxOutputTokens: policy.MaxOutputTokens, RequestTimeoutNS: int64(policy.RequestTimeout),
		Seed: policy.Seed, SeedFixed: policy.SeedFixed, StopIDs: policy.StopIDs, Thinking: id.Thinking, Prefill: input.Generation.Prefill,
	}
	return input, nil
}

// EffectiveModelProfile also supports checking the entry conditions of an import
// with no generation requests, without fabricating a request policy.
func EffectiveModelProfile(info inference.ModelInfo, limits config.Limits, segmented bool) (identity.ProfileInput, error) {
	if err := limits.Check(); err != nil {
		return identity.ProfileInput{}, err
	}
	if info.ModelSHA256 == ([32]byte{}) || info.Tokenizer.ModelSHA256 != info.ModelSHA256 || info.PolicySchema != inference.PolicySchemaV1 || info.ContextTokens <= 0 || info.Prefill.Path != info.Backend ||
		info.Prompt.Schema != prompt.SchemaV1 || (info.Prompt != prompt.PlainIdentity() && info.Prompt != prompt.IndexIdentity()) || info.Thinking != inference.ThinkingDisabled {
		return identity.ProfileInput{}, ErrInferenceProfile
	}
	kv, cudaPin := info.KVPrecision, info.CUDABackendPin
	switch info.Backend {
	case "cpu":
		kv, cudaPin = "not-applicable", ""
	case "cuda":
		if kv == "" || kv == "not-applicable" || cudaPin == "" {
			return identity.ProfileInput{}, ErrInferenceProfile
		}
	default:
		return identity.ProfileInput{}, ErrInferenceProfile
	}
	segmentation := identity.SegmentationInput{Mode: "none"}
	if segmented {
		segmentation = identity.SegmentationInput{Mode: "enabled", Schema: segment.Schema, BoundaryFixtures: segment.BoundaryFixtures, UnitTextBytes: limits.UnitTextBytes, Segments: limits.Segments}
	}
	p := info.Prefill
	return identity.ProfileInput{
		Version: identity.ProfileVersion, ModelDigest: info.ModelSHA256, Backend: info.Backend, BackendPin: info.BackendPin, CUDABackendPin: cudaPin,
		ComputeQuant: info.ComputeQuant, KVPrecision: kv, Tokenizer: identity.TokenizerInput{ModelDigest: info.Tokenizer.ModelSHA256, BackendPin: info.Tokenizer.BackendPin},
		Template:     identity.TemplateInput{Family: info.Template.Family, Source: info.Template.Source, RendererVersion: info.Template.RendererVersion},
		Prompt:       identity.PromptInput{Schema: info.Prompt.Schema, Variant: info.Prompt.Variant, SystemTemplate: []byte(info.Prompt.SystemTemplate), UserTemplate: []byte(info.Prompt.UserTemplate + info.Prompt.TermsTemplate), LanguageAliases: info.Prompt.LanguageAliases, DisabledThinkingPrefix: []byte(info.DisabledThinkingPrefix)},
		Generation:   identity.GenerationInput{Schema: info.PolicySchema, Thinking: info.Thinking, Prefill: identity.PrefillInput{Exact: p.Exact, Path: p.Path, CPUFastAttention: p.CPUFastAttention, Batched: p.Batched, BatchedCapability: p.BatchedCapability, Fast: p.Fast, Floor: p.Floor, Chunk: p.Chunk}},
		Segmentation: segmentation, Protection: identity.SchemaInput{Schema: protect.Schema, FixtureSet: ProtectionFixtures}, Validation: identity.SchemaInput{Schema: validate.Schema, FixtureSet: ValidationFixtures},
		Context: identity.ContextInput{Tokens: min(limits.ContextTokens, info.ContextTokens), Mode: "none"},
	}, nil
}
