package goinfer

import (
	"context"
	"errors"

	"github.com/sh4869221b/yakuori/internal/inference"
)

type tokenCounts struct{ emitted, requested int }

func finishReason(ctxErr error, state generationState, count tokenCounts) inference.Finish {
	if errors.Is(ctxErr, context.DeadlineExceeded) || errors.Is(state.err, context.DeadlineExceeded) {
		return inference.Timeout
	}
	if errors.Is(ctxErr, context.Canceled) || errors.Is(state.err, context.Canceled) {
		return inference.Canceled
	}
	if state.err != nil {
		return inference.DecodeError
	}
	if state.clamped {
		return inference.ContextLimit
	}
	if count.requested <= 0 || count.emitted < 0 || count.emitted > count.requested || state.budget != count.requested {
		return inference.InvalidOutput
	}
	if count.emitted == count.requested {
		return inference.MaxTokens
	}
	return inference.Stop
}

type generationError struct{ cause error }

func (e *generationError) Error() string { return "inference generation failed" }
func (e *generationError) Unwrap() error { return e.cause }
