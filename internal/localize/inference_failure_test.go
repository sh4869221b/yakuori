package localize

import (
	"context"
	"errors"
	"testing"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func TestInferenceCoreFailure(t *testing.T) {
	injected := errors.New("private backend diagnostic")
	type failureCase struct {
		name      string
		finish    inference.Finish
		stage     string
		want      error
		count     int
		cancel    bool
		candidate string
		corrupt   bool
	}
	cases := []failureCase{
		{name: "builder error", stage: "build", want: injected, count: 3},
		{name: "count error", stage: "count", want: injected, count: 3},
		{name: "partial plus error", stage: "generate", finish: inference.Stop, want: injected, count: 3},
		{name: "prompt count mismatch", finish: inference.Stop, want: ErrIncompleteGeneration, count: 2},
		{name: "unknown finish", finish: "future", want: ErrIncompleteGeneration, count: 3},
		{name: "cancellation after generation", finish: inference.Stop, want: context.Canceled, count: 3, cancel: true},
		{name: "candidate validation", finish: inference.Stop, want: validate.ErrInvalidCandidate, count: 3, candidate: "Translation: 訳文"},
		{name: "final validation", finish: inference.Stop, want: artifact.ErrFinalMismatch, count: 3, corrupt: true},
	}
	for _, finish := range []inference.Finish{inference.MaxTokens, inference.ContextLimit, inference.Timeout, inference.Canceled, inference.DecodeError, inference.InvalidOutput} {
		cases = append(cases, failureCase{name: string(finish), finish: finish, want: ErrIncompleteGeneration, count: 3})
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			counts, generates := 0, 0
			backend := commonEngine{
				count: func(context.Context, inference.GenerationRequest) (int, error) {
					counts++
					if tt.stage == "count" {
						return 0, injected
					}
					return tt.count, nil
				},
				generate: func(context.Context, inference.GenerationRequest) (inference.GenerationResult, error) {
					generates++
					if tt.cancel {
						cancel()
					}
					candidate := tt.candidate
					if candidate == "" {
						candidate = "途中の訳文"
					}
					result := inference.GenerationResult{Text: candidate, Finish: tt.finish, PromptTokens: 3}
					if tt.stage == "generate" {
						return result, injected
					}
					return result, nil
				},
			}
			engine := NewInferenceEngine(backend, func(context.Context, unit.UnitID, string) (inference.GenerationRequest, error) {
				if tt.stage == "build" {
					return inference.GenerationRequest{}, injected
				}
				return inferenceRequest(t, "Hello"), nil
			})
			sink, memory := &textSink{limit: -1}, &fakeTM{}
			a := &textProbe{Adapter: textadapter.New("en", "ja"), t: t, sink: sink, tm: memory, replace: tt.corrupt}
			result, err := NewCore(engine).Text(ctx, []byte("Hello"), a, textProfile(t), memory, sink)
			if !errors.Is(err, tt.want) || result.TMCommitted || result.BytesWritten != 0 || sink.calls != 0 || memory.calls != 0 || len(memory.rows) != 0 {
				t.Fatalf("result = %+v, error = %v, writes = %d, commits = %d, rows = %v", result, err, sink.calls, memory.calls, memory.rows)
			}
			var diagnostic *Error
			phase := "generate"
			if tt.stage == "build" || tt.stage == "count" {
				phase = "plan"
			}
			if !errors.As(err, &diagnostic) || (tt.stage != "" && diagnostic.Phase != phase) || (tt.stage != "" && diagnostic.UnitID.StableID() != "document") {
				t.Fatalf("diagnostic = %v", err)
			}
			wantCounts, wantGenerates := 1, 1
			if tt.stage == "build" {
				wantCounts, wantGenerates = 0, 0
			}
			if tt.stage == "count" {
				wantGenerates = 0
			}
			if counts != wantCounts || generates != wantGenerates {
				t.Fatalf("count/generate calls = %d/%d, want %d/%d", counts, generates, wantCounts, wantGenerates)
			}
		})
	}
}
