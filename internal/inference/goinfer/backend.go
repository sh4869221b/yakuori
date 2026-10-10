package goinfer

import (
	"context"

	"github.com/townsendmerino/goinfer/decoder"
)

type generationInput struct {
	ids      []int
	max      int
	sampling decoder.SamplingParams
}

type generationState struct {
	budget  int
	clamped bool
	err     error
}

type tokenStream struct {
	tokens  <-chan int
	outcome func() generationState
}

type decoderModel struct{ *decoder.Model }

func (m *decoderModel) generate(ctx context.Context, input generationInput) tokenStream {
	tokens, generation := m.Generate(ctx, input.ids, input.max, input.sampling)
	return tokenStream{tokens: tokens, outcome: func() generationState {
		return generationState{budget: generation.Budget, clamped: generation.BudgetClamped, err: generation.Err()}
	}}
}
