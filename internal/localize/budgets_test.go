package localize

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func budgetCore(t *testing.T, limits config.Limits, count func(context.Context, inference.GenerationRequest) (int, error), generate func(context.Context, inference.GenerationRequest) (inference.GenerationResult, error)) Core {
	t.Helper()
	backend := segmentedBackend{info: inference.ModelInfo{ContextTokens: 7, PolicySchema: 1}, commonEngine: commonEngine{count: count, generate: generate}}
	core, err := NewSegmentedCoreWithLimits(backend, func(_ context.Context, _ unit.UnitID, text string) (inference.GenerationRequest, error) {
		policy, err := inference.NewGenerationPolicy(1, 2, limits.RequestTimeout)
		if err != nil {
			return inference.GenerationRequest{}, err
		}
		return inference.NewGenerationRequest(inference.PreparedRequest{RenderedPrompt: text, TokenIDs: make([]int, len(text)), Identity: inference.RequestIdentity{PromptSchema: 1}}, policy)
	}, limits)
	if err != nil {
		t.Fatal(err)
	}
	return core
}

func TestCoreGenerationBudgets(t *testing.T) {
	for _, scenario := range []string{"already canceled", "planning", "request", "global mid segment", "success"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				limits := config.DefaultLimits()
				limits.GenerationTimeout, limits.RequestTimeout = 10*time.Second, 5*time.Second
				calls := 0
				core := budgetCore(t, limits, func(ctx context.Context, r inference.GenerationRequest) (int, error) {
					if scenario == "planning" {
						<-ctx.Done()
						return 0, ctx.Err()
					}
					return len(r.TokenIDs()), nil
				}, func(ctx context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
					calls++
					if scenario == "request" {
						<-ctx.Done()
					} else if scenario == "global mid segment" {
						select {
						case <-time.After(4 * time.Second):
						case <-ctx.Done():
						}
					}
					return segmentedStop(r), nil
				})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if scenario == "already canceled" {
					cancel()
				}
				sink, tm := &textSink{limit: -1}, &fakeTM{}
				result, err := core.Text(ctx, []byte("one two three"), textadapter.New("en", "ja"), segmentedProfile(t), tm, sink)
				wantCalls, wantErr := 0, context.DeadlineExceeded
				switch scenario {
				case "already canceled":
					wantErr = context.Canceled
				case "request":
					wantCalls = 1
				case "global mid segment":
					wantCalls = 3
				case "success":
					if err != nil || calls != 3 || !result.TMCommitted || tm.calls != 1 || sink.String() != "ONE TWO THREE" {
						t.Fatalf("result=%+v err=%v generate=%d commit=%d output=%q", result, err, calls, tm.calls, sink.String())
					}
					return
				}
				if !errors.Is(err, wantErr) || calls != wantCalls || result.TMCommitted || tm.calls != 0 || sink.Len() != 0 {
					t.Fatalf("result=%+v err=%v generate=%d commit=%d output=%q", result, err, calls, tm.calls, sink.String())
				}
			})
		})
	}
}

func TestRequestDeadlineWaitsForDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limits := config.DefaultLimits()
		limits.RequestTimeout = time.Second
		draining, release := make(chan struct{}), make(chan struct{})
		core := budgetCore(t, limits, func(_ context.Context, r inference.GenerationRequest) (int, error) { return len(r.TokenIDs()), nil }, func(ctx context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
			<-ctx.Done()
			close(draining)
			<-release
			return segmentedStop(r), nil
		})
		sink, tm := &textSink{limit: -1}, &fakeTM{}
		done := make(chan error, 1)
		go func() {
			_, err := core.Text(context.Background(), []byte("one two"), textadapter.New("en", "ja"), segmentedProfile(t), tm, sink)
			done <- err
		}()
		<-draining
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("returned before drain: %v", err)
		default:
		}
		if tm.calls != 0 || sink.Len() != 0 {
			t.Fatal("partial result finalized while draining")
		}
		close(release)
		if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	})
}

func TestLegacyRequestBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limits := config.DefaultLimits()
		limits.RequestTimeout = time.Second
		calls := 0
		core, err := NewCoreWithLimits(fakeEngine(func(ctx context.Context, _ unit.UnitID, text string) (Generation, error) {
			calls++
			<-ctx.Done()
			return Generation{Text: text, Finish: Stop}, nil
		}), limits)
		if err != nil {
			t.Fatal(err)
		}
		sink, tm := &textSink{limit: -1}, &fakeTM{}
		result, err := core.Text(context.Background(), []byte("one"), textadapter.New("en", "ja"), textProfile(t), tm, sink)
		if !errors.Is(err, context.DeadlineExceeded) || calls != 1 || result.TMCommitted || tm.calls != 0 || sink.Len() != 0 {
			t.Fatalf("result=%+v err=%v generate=%d commit=%d output=%q", result, err, calls, tm.calls, sink.String())
		}
	})
}

type contextTM func(context.Context, unit.Session, validate.Profile, []validate.AcceptedTranslation) error

func (f contextTM) Commit(ctx context.Context, s unit.Session, p validate.Profile, a []validate.AcceptedTranslation) error {
	return f(ctx, s, p, a)
}

func TestCommitBudgetAndGenerationContextOwnership(t *testing.T) {
	for _, scenario := range []string{"independent success", "deadline", "late success", "caller cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				limits := config.DefaultLimits()
				limits.GenerationTimeout, limits.RequestTimeout, limits.DBOperationTimeout = 5*time.Second, 5*time.Second, 2*time.Second
				started := time.Now()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				core, err := NewCoreWithLimits(fakeEngine(func(_ context.Context, _ unit.UnitID, text string) (Generation, error) {
					<-time.After(4 * time.Second)
					return Generation{Text: strings.ToUpper(text), Finish: Stop}, nil
				}), limits)
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				tm := contextTM(func(child context.Context, _ unit.Session, _ validate.Profile, _ []validate.AcceptedTranslation) error {
					calls++
					deadline, ok := child.Deadline()
					if !ok || deadline != started.Add(6*time.Second) || child.Err() != nil {
						t.Fatalf("commit inherited generation context: deadline=%v error=%v", deadline, child.Err())
					}
					switch scenario {
					case "independent success":
						<-time.After(1500 * time.Millisecond)
						return nil
					case "caller cancellation":
						cancel()
					}
					<-child.Done()
					if scenario == "late success" {
						return nil
					}
					return child.Err()
				})
				sink := &textSink{limit: -1}
				result, err := core.Text(ctx, []byte("one"), textadapter.New("en", "ja"), textProfile(t), tm, sink)
				if scenario == "independent success" {
					if err != nil || !result.TMCommitted || sink.String() != "ONE" || calls != 1 {
						t.Fatalf("result=%+v err=%v commit=%d output=%q", result, err, calls, sink.String())
					}
					return
				}
				want := context.DeadlineExceeded
				if scenario == "caller cancellation" {
					want = context.Canceled
				}
				if !errors.Is(err, want) || result.TMCommitted != (scenario == "late success") || calls != 1 || sink.Len() != 0 {
					t.Fatalf("result=%+v err=%v commit=%d output=%q", result, err, calls, sink.String())
				}
			})
		})
	}
}

func TestLegacyInferenceBuilderLimits(t *testing.T) {
	for _, bound := range []string{"output", "request", "context"} {
		t.Run(bound, func(t *testing.T) {
			limits := config.DefaultLimits()
			output, timeout, count := 16, time.Second, 3
			if bound == "output" {
				output = limits.MaxOutputTokens + 1
			} else if bound == "request" {
				timeout = limits.RequestTimeout + time.Nanosecond
			} else {
				count = limits.ContextTokens - output + 1
			}
			policy, err := inference.NewGenerationPolicy(1, output, timeout)
			if err != nil {
				t.Fatal(err)
			}
			request, err := inference.NewGenerationRequest(inference.PreparedRequest{TokenIDs: []int{1, 2, 3}}, policy)
			if err != nil {
				t.Fatal(err)
			}
			backend := commonEngine{count: func(context.Context, inference.GenerationRequest) (int, error) { return count, nil }, generate: func(context.Context, inference.GenerationRequest) (inference.GenerationResult, error) {
				t.Fatal("generation exceeded product policy")
				return inference.GenerationResult{}, nil
			}}
			engine := NewInferenceEngine(backend, func(context.Context, unit.UnitID, string) (inference.GenerationRequest, error) { return request, nil })
			_, err = engine.Generate(context.Background(), unit.UnitID{}, "hello")
			if err == nil {
				t.Fatalf("accepted oversized %s", bound)
			}
		})
	}
}
