package goinfer

import (
	"context"
	"errors"

	"github.com/townsendmerino/goinfer/decoder"

	"github.com/sh4869221b/yakuori/internal/inference"
)

func (e *Engine) Generate(ctx context.Context, request inference.GenerationRequest) (inference.GenerationResult, error) {
	policy := request.Policy()
	ctx, cancel := context.WithTimeout(ctx, policy.RequestTimeout)
	defer cancel()
	ids := request.TokenIDs()
	deadline, _ := ctx.Deadline()
	result := inference.GenerationResult{
		Finish: inference.InvalidOutput, PromptTokens: len(ids), Policy: policy, Deadline: deadline,
	}
	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		return result, ErrClosed
	}
	if e.activeDone != nil {
		e.mu.Unlock()
		return result, ErrBusy
	}
	if request.Identity() != e.identity() {
		e.mu.Unlock()
		return result, ErrForeignRequest
	}
	if err := ctx.Err(); err != nil {
		e.mu.Unlock()
		result.Finish = finishReason(err, generationState{}, tokenCounts{})
		return result, err
	}
	limit := e.info.ContextTokens
	if len(ids) <= 0 || policy.MaxOutputTokens <= 0 || len(ids) > limit || policy.MaxOutputTokens > limit-len(ids) {
		e.mu.Unlock()
		result.Finish = inference.ContextLimit
		return result, ErrContextLimit
	}
	done := make(chan struct{})
	e.activeCancel, e.activeDone = cancel, done
	e.mu.Unlock()
	state := generationState{}
	var generated []int
	if ctx.Err() == nil {
		stream := e.model.generate(ctx, generationInput{ids: ids, max: policy.MaxOutputTokens, sampling: decoder.SamplingParams{
			Temperature: policy.Temperature, TopK: policy.TopK, TopP: policy.TopP, MinP: policy.MinP,
			Seed: policy.Seed, RepeatPenalty: policy.RepeatPenalty, PresencePenalty: policy.PresencePenalty,
			FrequencyPenalty: policy.FrequencyPenalty, RepeatLastN: policy.RepeatLastN,
			Logprobs: policy.Logprobs, TopLogprobs: policy.TopLogprobs, StopIDs: policy.StopIDs,
		}})
		for id := range stream.tokens {
			generated = append(generated, id)
		}
		state = stream.outcome()
		e.tokenizerMu.Lock()
		text, err := e.tokenizer.Decode(generated)
		e.tokenizerMu.Unlock()
		result.Text = text
		state.err = errors.Join(state.err, err)
	}
	result.OutputTokens = len(generated)
	e.mu.Lock()
	ctxErr := ctx.Err()
	if e.closing && ctxErr == nil {
		ctxErr = context.Canceled
	}
	result.Finish = finishReason(ctxErr, state, tokenCounts{emitted: len(generated), requested: policy.MaxOutputTokens})
	err := errors.Join(ctxErr, state.err)
	if result.Finish == inference.ContextLimit {
		err = errors.Join(err, ErrContextLimit)
	}
	if result.Finish == inference.InvalidOutput {
		err = errors.Join(err, ErrInvalidOutput)
	}
	e.activeCancel, e.activeDone = nil, nil
	close(done)
	e.mu.Unlock()
	if err != nil {
		return result, &generationError{cause: err}
	}
	return result, nil
}
