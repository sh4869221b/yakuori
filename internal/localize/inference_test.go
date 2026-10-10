package localize

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/segment"
	"github.com/sh4869221b/yakuori/internal/unit"
)

type commonEngine struct {
	inference.Engine
	count    func(context.Context, inference.GenerationRequest) (int, error)
	generate func(context.Context, inference.GenerationRequest) (inference.GenerationResult, error)
}

func TestInferenceCorePreflightsAllRequests(t *testing.T) {
	injected := errors.New("request preparation failed")
	for _, failure := range []string{"context", "policy", "build", "count", "success"} {
		t.Run(failure, func(t *testing.T) {
			var events []string
			backend := commonEngine{
				count: func(_ context.Context, request inference.GenerationRequest) (int, error) {
					text := request.RenderedPrompt()
					events = append(events, "count "+text)
					if text == "two" {
						if failure == "count" {
							return 0, injected
						}
						if failure == "context" {
							return config.DefaultLimits().ContextTokens, nil
						}
					}
					return 3, nil
				},
				generate: func(_ context.Context, request inference.GenerationRequest) (inference.GenerationResult, error) {
					events = append(events, "generate "+request.RenderedPrompt())
					return inference.GenerationResult{Text: "訳文", Finish: inference.Stop, PromptTokens: 3}, nil
				},
			}
			engine := NewInferenceEngine(backend, func(_ context.Context, _ unit.UnitID, text string) (inference.GenerationRequest, error) {
				events = append(events, "build "+text)
				if text == "two" && failure == "build" {
					return inference.GenerationRequest{}, injected
				}
				if text == "two" && failure == "policy" {
					policy, err := inference.NewGenerationPolicy(1, config.DefaultLimits().MaxOutputTokens+1, time.Second)
					if err != nil {
						t.Fatal(err)
					}
					return inference.NewGenerationRequest(inference.PreparedRequest{RenderedPrompt: text, TokenIDs: []int{1, 2, 3}}, policy)
				}
				return inferenceRequest(t, text), nil
			})
			session, profile := generationFixture(t, []string{"one", "two"}, nil)
			accepted, err := NewCore(engine).Generate(context.Background(), session, profile)
			if failure == "success" {
				want := []string{"build one", "count one", "build two", "count two", "generate one", "generate two"}
				if err != nil || len(accepted) != 2 || !slices.Equal(events, want) {
					t.Fatalf("accepted=%v err=%v events=%v", accepted, err, events)
				}
				return
			}
			wantErr := injected
			if failure == "context" {
				wantErr = segment.ErrContextLimit
			} else if failure == "policy" {
				wantErr = inference.ErrInvalidPolicy
			}
			var diagnostic *Error
			if !errors.Is(err, wantErr) || !errors.As(err, &diagnostic) || diagnostic.Phase != "plan" || diagnostic.UnitID != session.Units()[1].ID() || accepted != nil {
				t.Fatalf("accepted=%v err=%v", accepted, err)
			}
			for _, event := range events {
				if strings.HasPrefix(event, "generate ") {
					t.Fatalf("generation before preflight completed: %v", events)
				}
			}
		})
	}
}

func TestInferenceCoreUsesSelectedLimits(t *testing.T) {
	for _, pointer := range []bool{false, true} {
		for _, field := range []string{"output", "context", "request", "higher experiment"} {
			t.Run(fmt.Sprintf("%s/pointer=%t", field, pointer), func(t *testing.T) {
				limits := config.DefaultLimits()
				output, timeout, count := 16, time.Second, 3
				switch field {
				case "output":
					limits.MaxOutputTokens = output - 1
				case "context":
					limits.ContextTokens, limits.MaxOutputTokens = 18, output
				case "request":
					limits.RequestTimeout = timeout / 2
				case "higher experiment":
					output = limits.MaxOutputTokens + 1
					timeout = limits.RequestTimeout + time.Second
					count = limits.ContextTokens
					limits.ContextTokens, limits.MaxOutputTokens, limits.RequestTimeout = count+output, output, timeout
				}
				policy, err := inference.NewGenerationPolicy(1, output, timeout)
				if err != nil {
					t.Fatal(err)
				}
				request, err := inference.NewGenerationRequest(inference.PreparedRequest{RenderedPrompt: "one", TokenIDs: make([]int, count)}, policy)
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				backend := commonEngine{count: func(context.Context, inference.GenerationRequest) (int, error) { return count, nil }, generate: func(context.Context, inference.GenerationRequest) (inference.GenerationResult, error) {
					calls++
					return inference.GenerationResult{Text: "訳文", Finish: inference.Stop, PromptTokens: count}, nil
				}}
				original := NewInferenceEngine(backend, func(context.Context, unit.UnitID, string) (inference.GenerationRequest, error) { return request, nil })
				var engine Engine = original
				if pointer {
					engine = &original
				}
				core, err := NewCoreWithLimits(engine, limits)
				if err != nil {
					t.Fatal(err)
				}
				session, profile := generationFixture(t, []string{"one"}, nil)
				accepted, err := core.Generate(context.Background(), session, profile)
				if field == "higher experiment" {
					if err != nil || len(accepted) != 1 || calls != 1 {
						t.Fatalf("accepted=%v calls=%d err=%v", accepted, calls, err)
					}
				} else if err == nil || calls != 0 || accepted != nil {
					t.Fatalf("accepted=%v calls=%d err=%v", accepted, calls, err)
				}
				_, originalErr := original.Generate(context.Background(), unit.UnitID{}, "one")
				if field == "higher experiment" {
					if !errors.Is(originalErr, inference.ErrInvalidPolicy) {
						t.Fatalf("original adapter lost default limits: %v", originalErr)
					}
				} else if originalErr != nil {
					t.Fatal(originalErr)
				}
			})
		}
	}
}

func (e commonEngine) CountTokens(ctx context.Context, request inference.GenerationRequest) (int, error) {
	return e.count(ctx, request)
}

func (e commonEngine) Generate(ctx context.Context, request inference.GenerationRequest) (inference.GenerationResult, error) {
	return e.generate(ctx, request)
}

func inferenceRequest(t *testing.T, text string) inference.GenerationRequest {
	t.Helper()
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 16, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request, err := inference.NewGenerationRequest(inference.PreparedRequest{
		RenderedPrompt: text, TokenIDs: []int{1, 2, 3},
		Spans: []inference.PromptSpan{{Text: text}}, StopIDs: []int{4},
	}, policy)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestInferenceCoreSuccess(t *testing.T) {
	var events []string
	request := inferenceRequest(t, "Hello")
	backend := commonEngine{
		count: func(_ context.Context, got inference.GenerationRequest) (int, error) {
			events = append(events, "count")
			if !reflect.DeepEqual(got, request) {
				t.Fatal("count received a different request")
			}
			return 3, nil
		},
		generate: func(_ context.Context, got inference.GenerationRequest) (inference.GenerationResult, error) {
			events = append(events, "generate")
			if !reflect.DeepEqual(got, request) {
				t.Fatal("generate received a different request")
			}
			return inference.GenerationResult{Text: "こんにちは", Finish: inference.Stop, PromptTokens: 3}, nil
		},
	}
	engine := NewInferenceEngine(backend, func(_ context.Context, id unit.UnitID, text string) (inference.GenerationRequest, error) {
		events = append(events, "build")
		if id.StableID() != "document" || text != "Hello" {
			t.Fatalf("builder input = %v, %q", id, text)
		}
		return request, nil
	})
	sink, memory := &textSink{limit: -1}, &fakeTM{}
	a := &textProbe{Adapter: textadapter.New("en", "ja"), t: t, sink: sink, tm: memory,
		afterObserve: func() { events = append(events, "validate final") }}
	tm := hookedTM{fakeTM: memory, after: func() { events = append(events, "commit") }}
	sink.after = func() { events = append(events, "stdout") }
	result, err := NewCore(engine).Text(context.Background(), []byte("Hello"), a, textProfile(t), tm, sink)
	if err != nil || !result.TMCommitted || sink.String() != "こんにちは" || memory.calls != 1 || len(memory.rows) != 1 || memory.rows[0].Text() != "こんにちは" {
		t.Fatalf("result = %+v, error = %v, stdout = %q, rows = %v", result, err, sink.String(), memory.rows)
	}
	if !slices.Equal(events, []string{"build", "count", "generate", "validate final", "commit", "stdout"}) {
		t.Fatalf("events = %v", events)
	}
}

func TestInferenceSkipsProtectedOnly(t *testing.T) {
	session, profile := generationFixture(t, []string{"Hello {p}", "", " {p}\r\n"}, map[int][]unit.ProtectionSpan{
		0: {{Start: 6, End: 9, Kind: unit.Placeholder}},
		2: {{Start: 1, End: 4, Kind: unit.Placeholder}},
	})
	builds, counts, generates := 0, 0, 0
	backend := commonEngine{
		count: func(_ context.Context, r inference.GenerationRequest) (int, error) {
			counts++
			return len(r.TokenIDs()), nil
		},
		generate: func(_ context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
			generates++
			return inference.GenerationResult{Text: strings.ReplaceAll(r.RenderedPrompt(), "Hello", "こんにちは"), Finish: inference.Stop, PromptTokens: 3}, nil
		},
	}
	engine := NewInferenceEngine(backend, func(_ context.Context, id unit.UnitID, text string) (inference.GenerationRequest, error) {
		builds++
		if id != session.Units()[0].ID() || text != "Hello [[YAKUORI_0_0]]" {
			t.Fatalf("builder input = %v, %q", id, text)
		}
		return inferenceRequest(t, text), nil
	})
	accepted, err := NewCore(engine).Generate(context.Background(), session, profile)
	if err != nil || builds != 1 || counts != 1 || generates != 1 || len(accepted) != 3 {
		t.Fatalf("error = %v, calls = %d/%d/%d, accepted = %v", err, builds, counts, generates, accepted)
	}
	for i, want := range []string{"こんにちは {p}", "", " {p}\r\n"} {
		if accepted[i].Text() != want {
			t.Fatalf("translation %d = %q, want %q", i, accepted[i].Text(), want)
		}
	}
}
