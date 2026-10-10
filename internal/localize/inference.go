package localize

import (
	"context"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/segment"
	"github.com/sh4869221b/yakuori/internal/unit"
)

// InferenceEngine connects generation to Core without taking ownership of the model.
type InferenceEngine struct {
	engine       inference.Engine
	buildRequest func(context.Context, unit.UnitID, string) (inference.GenerationRequest, error)
}

func NewInferenceEngine(engine inference.Engine, buildRequest func(context.Context, unit.UnitID, string) (inference.GenerationRequest, error)) InferenceEngine {
	return InferenceEngine{engine: engine, buildRequest: buildRequest}
}

func (e InferenceEngine) Generate(ctx context.Context, id unit.UnitID, text string) (Generation, error) {
	request, err := e.buildRequest(ctx, id, text)
	if err != nil {
		return Generation{}, err
	}
	limits := config.DefaultLimits()
	policy := request.Policy()
	if policy.MaxOutputTokens > limits.MaxOutputTokens || policy.RequestTimeout > limits.RequestTimeout {
		return Generation{Finish: InvalidOutput}, inference.ErrInvalidPolicy
	}
	count, err := e.engine.CountTokens(ctx, request)
	if err != nil {
		return Generation{}, err
	}
	if count > limits.ContextTokens-policy.MaxOutputTokens {
		return Generation{Finish: ContextLimit}, segment.ErrContextLimit
	}
	if err := ctx.Err(); err != nil {
		return Generation{}, err
	}
	result, err := e.engine.Generate(ctx, request)
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
	if result.PromptTokens != count {
		generation.Finish = InvalidOutput
	}
	return generation, err
}
