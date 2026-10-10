package localize

import (
	"context"
	"errors"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/segment"
	"github.com/sh4869221b/yakuori/internal/unit"
)

type segmentedGenerator struct {
	engine       inference.Engine
	buildRequest func(context.Context, unit.UnitID, string) (inference.GenerationRequest, error)
}

// NewSegmentedCore plans all requests before sequential generation. The caller owns the engine.
func NewSegmentedCore(engine inference.Engine, buildRequest func(context.Context, unit.UnitID, string) (inference.GenerationRequest, error)) Core {
	return Core{segmented: &segmentedGenerator{engine: engine, buildRequest: buildRequest}, limits: config.DefaultLimits()}
}

// NewSegmentedCoreWithLimits keeps explicit finite experimental limits separate from product defaults.
func NewSegmentedCoreWithLimits(engine inference.Engine, buildRequest func(context.Context, unit.UnitID, string) (inference.GenerationRequest, error), limits config.Limits) (Core, error) {
	if err := limits.Check(); err != nil {
		return Core{}, err
	}
	core := NewSegmentedCore(engine, buildRequest)
	core.limits = limits
	return core, nil
}

func segmentedFailure(err error) (Generation, error) {
	finish := InvalidOutput
	switch {
	case errors.Is(err, segment.ErrContextLimit):
		finish = ContextLimit
	case errors.Is(err, context.DeadlineExceeded):
		finish = Timeout
	case errors.Is(err, context.Canceled):
		finish = Canceled
	}
	return Generation{Finish: finish}, err
}

func (g *segmentedGenerator) plan(ctx context.Context, id unit.UnitID, prepared protect.Prepared, limits config.Limits) (segment.Plan, error) {
	info, err := g.engine.Info(ctx)
	if err != nil {
		return segment.Plan{}, err
	}
	return segment.BuildWithLimits(ctx, id, prepared, info, func(ctx context.Context, text string) (inference.GenerationRequest, error) {
		return g.buildRequest(ctx, id, text)
	}, g.engine.CountTokens, limits)
}

func (g *segmentedGenerator) generate(ctx context.Context, plan segment.Plan) (Generation, error) {
	var results []segment.Result
	for _, piece := range plan.Segments() {
		if err := ctx.Err(); err != nil {
			return segmentedFailure(err)
		}
		text := piece.Text()
		if piece.GenerationRequired() {
			request := piece.Request()
			requestCtx, cancel := context.WithTimeout(ctx, request.Policy().RequestTimeout)
			if err := requestCtx.Err(); err != nil {
				cancel()
				return segmentedFailure(err)
			}
			result, err := g.engine.Generate(requestCtx, request)
			err = errors.Join(err, requestCtx.Err())
			cancel()
			if err != nil {
				switch result.Finish {
				case inference.ContextLimit:
					err = errors.Join(segment.ErrContextLimit, err)
				case inference.Timeout:
					err = errors.Join(context.DeadlineExceeded, err)
				case inference.Canceled:
					err = errors.Join(context.Canceled, err)
				}
				return segmentedFailure(err)
			}
			if err := ctx.Err(); err != nil {
				return segmentedFailure(err)
			}
			if result.Finish != inference.Stop {
				switch result.Finish {
				case inference.ContextLimit:
					return segmentedFailure(segment.ErrContextLimit)
				case inference.Timeout:
					return segmentedFailure(context.DeadlineExceeded)
				case inference.Canceled:
					return segmentedFailure(context.Canceled)
				default:
					return Generation{Finish: InvalidOutput}, ErrIncompleteGeneration
				}
			}
			if result.PromptTokens != piece.PromptTokens() {
				return segmentedFailure(segment.ErrInvalidResult)
			}
			text = result.Text
		}
		results = append(results, segment.Result{Segment: piece, Text: text})
	}
	if err := ctx.Err(); err != nil {
		return segmentedFailure(err)
	}
	text, err := segment.Join(plan, results)
	if err != nil {
		return segmentedFailure(err)
	}
	if err := ctx.Err(); err != nil {
		return segmentedFailure(err)
	}
	return Generation{Text: text, Finish: Stop}, nil
}
