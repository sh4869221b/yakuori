package localize

import (
	"context"
	"errors"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/segment"
	"github.com/sh4869221b/yakuori/internal/unit"
)

// InferenceEngine connects generation to Core without taking ownership of the model.
type InferenceEngine struct {
	limits       config.Limits
	engine       inference.Engine
	buildRequest func(context.Context, unit.UnitID, string) (inference.GenerationRequest, error)
	buildTerms   TermsRequestBuilder
}

func NewInferenceEngine(engine inference.Engine, buildRequest func(context.Context, unit.UnitID, string) (inference.GenerationRequest, error)) InferenceEngine {
	return InferenceEngine{engine: engine, buildRequest: buildRequest, limits: config.DefaultLimits()}
}

func (e InferenceEngine) Generate(ctx context.Context, id unit.UnitID, text string) (Generation, error) {
	plan, err := e.plan(ctx, id, text)
	if err != nil {
		if errors.Is(err, segment.ErrContextLimit) {
			return Generation{Finish: ContextLimit}, err
		}
		if errors.Is(err, inference.ErrInvalidPolicy) {
			return Generation{Finish: InvalidOutput}, err
		}
		return Generation{}, err
	}
	return e.generate(ctx, plan)
}

type inferencePlan struct {
	request inference.GenerationRequest
	count   int
}

func (e InferenceEngine) plan(ctx context.Context, id unit.UnitID, text string) (inferencePlan, error) {
	return e.planWithTerms(ctx, id, RequestInput{Text: text})
}

func (e InferenceEngine) planWithTerms(ctx context.Context, id unit.UnitID, input RequestInput) (inferencePlan, error) {
	request, err := buildTermsRequest(ctx, id, input, e.buildTerms, e.buildRequest)
	if err != nil {
		return inferencePlan{}, err
	}
	limits := e.limits
	policy := request.Policy()
	if policy.MaxOutputTokens > limits.MaxOutputTokens || policy.RequestTimeout > limits.RequestTimeout {
		return inferencePlan{}, inference.ErrInvalidPolicy
	}
	count, err := e.engine.CountTokens(ctx, request)
	if err != nil {
		return inferencePlan{}, err
	}
	if count > limits.ContextTokens-policy.MaxOutputTokens {
		return inferencePlan{}, segment.ErrContextLimit
	}
	return inferencePlan{request: request, count: count}, ctx.Err()
}

type RequestInput struct {
	Text  string
	Terms glossary.Glossary
}

type TermsRequestBuilder func(context.Context, unit.UnitID, RequestInput) (inference.GenerationRequest, error)

func NewGlossaryInferenceEngine(engine inference.Engine, build TermsRequestBuilder) InferenceEngine {
	return InferenceEngine{engine: engine, buildTerms: build, limits: config.DefaultLimits()}
}

func buildTermsRequest(ctx context.Context, id unit.UnitID, input RequestInput, build TermsRequestBuilder, legacy func(context.Context, unit.UnitID, string) (inference.GenerationRequest, error)) (inference.GenerationRequest, error) {
	var request inference.GenerationRequest
	var err error
	if build != nil {
		request, err = build(ctx, id, input)
	} else {
		if !input.Terms.Empty() || legacy == nil {
			return inference.GenerationRequest{}, ErrInferenceProfile
		}
		request, err = legacy(ctx, id, input.Text)
	}
	if err != nil {
		return inference.GenerationRequest{}, err
	}
	if request.Terms() != input.Terms {
		return inference.GenerationRequest{}, ErrInferenceProfile
	}
	return request, nil
}

func (e InferenceEngine) generate(ctx context.Context, plan inferencePlan) (Generation, error) {
	if err := ctx.Err(); err != nil {
		return Generation{}, err
	}
	result, err := e.engine.Generate(ctx, plan.request)
	generation := Generation{Text: result.Text}
	switch result.Finish {
	case inference.Stop:
		generation.Finish = Stop
	case inference.MaxTokens:
		generation.Finish = MaxTokens
	case inference.ContextLimit:
		generation.Finish = ContextLimit
	case inference.Timeout:
		generation.Finish = Timeout
	case inference.Canceled:
		generation.Finish = Canceled
	case inference.DecodeError:
		generation.Finish = DecodeError
	case inference.InvalidOutput:
		generation.Finish = InvalidOutput
	default:
		generation.Finish = InvalidOutput
	}
	if result.PromptTokens != plan.count {
		generation.Finish = InvalidOutput
	}
	return generation, err
}
