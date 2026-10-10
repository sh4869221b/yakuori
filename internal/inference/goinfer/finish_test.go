package goinfer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/inference"
)

func TestFinishReasons(t *testing.T) {
	privateErr := errors.New("private source and generated output")
	cases := []struct {
		name            string
		emitted, budget int
		clamped, cancel bool
		backend, decode error
		finish          inference.Finish
		want            error
	}{
		{name: "stop", emitted: 1, budget: 2, finish: inference.Stop},
		{name: "max tokens", emitted: 2, budget: 2, finish: inference.MaxTokens},
		{name: "clamped", emitted: 1, budget: 1, clamped: true, finish: inference.ContextLimit, want: ErrContextLimit},
		{name: "clamp before excess", emitted: 3, budget: 1, clamped: true, finish: inference.ContextLimit, want: ErrContextLimit},
		{name: "unknown budget", emitted: 1, budget: 0, finish: inference.InvalidOutput, want: ErrInvalidOutput},
		{name: "excess output", emitted: 3, budget: 2, finish: inference.InvalidOutput, want: ErrInvalidOutput},
		{name: "partial backend error", emitted: 1, budget: 2, backend: privateErr, finish: inference.DecodeError, want: privateErr},
		{name: "decode error", emitted: 1, budget: 2, decode: privateErr, finish: inference.DecodeError, want: privateErr},
		{name: "backend deadline", emitted: 1, budget: 2, backend: context.DeadlineExceeded, finish: inference.Timeout, want: context.DeadlineExceeded},
		{name: "backend cancel", emitted: 1, budget: 2, backend: context.Canceled, finish: inference.Canceled, want: context.Canceled},
		{name: "nil backend cancel race", emitted: 1, budget: 2, cancel: true, finish: inference.Canceled, want: context.Canceled},
		{name: "cancel survives decode error", emitted: 1, budget: 2, cancel: true, decode: privateErr, finish: inference.Canceled, want: context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine, model, tok, request := generationFixture(t)
			defer engine.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			model.start = func(context.Context, generationInput) tokenStream {
				tokens := make(chan int, tc.emitted)
				for range tc.emitted {
					tokens <- 20
				}
				close(tokens)
				return tokenStream{tokens: tokens, outcome: func() generationState {
					return generationState{budget: tc.budget, clamped: tc.clamped, err: tc.backend}
				}}
			}
			tok.decode = func([]int) (string, error) {
				if tc.cancel {
					cancel()
				}
				return "partial text", tc.decode
			}
			result, err := engine.Generate(ctx, request)
			if result.Finish != tc.finish || !errors.Is(err, tc.want) || result.Text != "partial text" || result.OutputTokens != tc.emitted {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if err != nil && (strings.Contains(err.Error(), "private source") || strings.Contains(err.Error(), result.Text)) {
				t.Fatalf("diagnostic leaked model text: %v", err)
			}
			if tc.decode != nil && !errors.Is(err, tc.decode) {
				t.Fatalf("decode cause lost: %v", err)
			}
		})
	}
}
