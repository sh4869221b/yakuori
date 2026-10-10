package goinfer

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/townsendmerino/goinfer/decoder"
	"github.com/townsendmerino/goinfer/tokenizer"

	"github.com/sh4869221b/yakuori/internal/inference"
)

func TestGenerateStop(t *testing.T) {
	engine, _, _, request := generationFixture(t)
	defer engine.Close()
	result, err := engine.Generate(context.Background(), request)
	if err != nil || result.Finish != inference.Stop || result.Text != "translated" || result.PromptTokens != 3 || result.OutputTokens != 1 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if !reflect.DeepEqual(result.Policy, request.Policy()) || result.Deadline.IsZero() {
		t.Fatalf("result lost effective policy or deadline: %+v", result)
	}
}

func TestGenerateUsesRequest(t *testing.T) {
	engine, model, tok, request := generationFixture(t)
	defer engine.Close()
	tok.encode = func([]tokenizer.Segment, bool) ([]int, error) {
		t.Fatal("Generate must not render or re-encode")
		return nil, nil
	}
	start := model.start
	model.start = func(ctx context.Context, input generationInput) tokenStream {
		wantSampling := decoder.SamplingParams{StopIDs: []int{42}}
		if !slices.Equal(input.ids, request.TokenIDs()) || input.max != 2 || !reflect.DeepEqual(input.sampling, wantSampling) {
			t.Fatalf("backend input=%+v", input)
		}
		input.ids[0] = 999
		return start(ctx, input)
	}
	tok.decode = func(ids []int) (string, error) {
		if !slices.Equal(ids, []int{20}) {
			t.Fatalf("decode ids=%v", ids)
		}
		return "translated", nil
	}
	count, err := engine.CountTokens(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Generate(context.Background(), request)
	if err != nil || result.PromptTokens != count || !slices.Equal(request.TokenIDs(), []int{10, 11, 12}) {
		t.Fatalf("result=%+v error=%v request ids=%v", result, err, request.TokenIDs())
	}
}

func TestEffectiveContextBudget(t *testing.T) {
	for _, tc := range []struct {
		backend  string
		resident int
	}{
		{"cpu", 0},
		{"cuda", 4096},
		{"cuda", 2048},
	} {
		t.Run(fmt.Sprintf("%s/resident%d", tc.backend, tc.resident), func(t *testing.T) {
			config, load, model, _ := loadFixture(t)
			config.backend, config.contextTokens = tc.backend, 4096
			model.backend, model.contextTokens = tc.backend, 262144
			model.residentActive, model.residentCap = tc.backend == "cuda", tc.resident
			load.model = func(string, decoder.Options) (modelBackend, error) { return model, nil }
			engine, err := open(context.Background(), config, load)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			limit := 4096
			if tc.backend == "cuda" {
				limit = min(limit, tc.resident)
			}
			for extra := range 2 {
				t.Run(fmt.Sprintf("over%d", extra), func(t *testing.T) {
					policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 1, time.Minute)
					if err != nil {
						t.Fatal(err)
					}
					request, err := inference.NewGenerationRequest(inference.PreparedRequest{Identity: engine.identity(), TokenIDs: make([]int, limit-1+extra)}, policy)
					if err != nil {
						t.Fatal(err)
					}
					calls, start := 0, model.start
					model.start = func(ctx context.Context, input generationInput) tokenStream {
						calls++
						return start(ctx, input)
					}
					defer func() { model.start = start }()
					result, err := engine.Generate(context.Background(), request)
					if extra == 0 {
						if err != nil || calls != 1 {
							t.Fatalf("boundary result=%+v error=%v calls=%d", result, err, calls)
						}
					} else if !errors.Is(err, ErrContextLimit) || result.Finish != inference.ContextLimit || calls != 0 {
						t.Fatalf("over boundary result=%+v error=%v calls=%d", result, err, calls)
					}
				})
			}
		})
	}
}

func TestContextBudget(t *testing.T) {
	engine, model, _, _ := generationFixture(t)
	defer engine.Close()
	cases := []struct {
		name           string
		prompt, output int
		allowed        bool
	}{
		{"boundary", 32767, 1, true},
		{"over boundary", 32768, 1, false},
		{"overflow output", 1, int(^uint(0) >> 1), false},
		{"empty prompt", 0, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, tc.output, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			request, err := inference.NewGenerationRequest(inference.PreparedRequest{Identity: engine.identity(), TokenIDs: make([]int, tc.prompt)}, policy)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			model.start = func(_ context.Context, input generationInput) tokenStream {
				calls++
				tokens := make(chan int)
				close(tokens)
				return tokenStream{tokens: tokens, outcome: func() generationState { return generationState{budget: input.max} }}
			}
			result, err := engine.Generate(context.Background(), request)
			if tc.allowed {
				if err != nil || result.Finish != inference.Stop || calls != 1 {
					t.Fatalf("result=%+v error=%v calls=%d", result, err, calls)
				}
			} else if !errors.Is(err, ErrContextLimit) || result.Finish != inference.ContextLimit || calls != 0 {
				t.Fatalf("result=%+v error=%v calls=%d", result, err, calls)
			}
		})
	}
}
