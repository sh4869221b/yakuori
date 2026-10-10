package localize

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/segment"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

type segmentedBackend struct {
	commonEngine
	info inference.ModelInfo
}

func (e segmentedBackend) Info(context.Context) (inference.ModelInfo, error) { return e.info, nil }

func segmentedProfile(t *testing.T) validate.Profile {
	t.Helper()
	p, err := validate.NewProfileWithSegmentation([32]byte{1}, segment.ProfileInput())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func segmentedFixture(t *testing.T, size func(string) int, generate func(context.Context, inference.GenerationRequest) (inference.GenerationResult, error)) Core {
	t.Helper()
	info := inference.ModelInfo{ContextTokens: 7, PolicySchema: 1}
	backend := segmentedBackend{info: info, commonEngine: commonEngine{
		count:    func(_ context.Context, r inference.GenerationRequest) (int, error) { return len(r.TokenIDs()), nil },
		generate: generate,
	}}
	return NewSegmentedCore(backend, func(_ context.Context, _ unit.UnitID, text string) (inference.GenerationRequest, error) {
		policy, err := inference.NewGenerationPolicy(1, 2, time.Second)
		if err != nil {
			return inference.GenerationRequest{}, err
		}
		return inference.NewGenerationRequest(inference.PreparedRequest{RenderedPrompt: text, TokenIDs: make([]int, size(text)), Identity: inference.RequestIdentity{PromptSchema: 1}}, policy)
	})
}

func segmentedStop(r inference.GenerationRequest) inference.GenerationResult {
	return inference.GenerationResult{Text: strings.ToUpper(r.RenderedPrompt()), Finish: inference.Stop, PromptTokens: len(r.TokenIDs())}
}

func byteLength(text string) int { return len(text) }

func TestSegmentedCoreSuccess(t *testing.T) {
	var events []string
	core := segmentedFixture(t, byteLength, func(_ context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
		events = append(events, r.RenderedPrompt())
		return segmentedStop(r), nil
	})
	sink, memory := &textSink{limit: -1}, &fakeTM{}
	a := &textProbe{Adapter: textadapter.New("en", "ja"), t: t, sink: sink, tm: memory, afterObserve: func() { events = append(events, "validated") }}
	tm := hookedTM{fakeTM: memory, after: func() { events = append(events, "commit") }}
	sink.after = func() { events = append(events, "stdout") }
	result, err := core.Text(context.Background(), []byte("one\r\ntwo three"), a, segmentedProfile(t), tm, sink)
	if err != nil || !result.TMCommitted || sink.String() != "ONE\r\nTWO THREE" || memory.calls != 1 || len(memory.rows) != 1 || memory.rows[0].Text() != sink.String() || memory.rows[0].UnitID().StableID() != "document" {
		t.Fatalf("result=%+v err=%v stdout=%q rows=%v", result, err, sink.String(), memory.rows)
	}
	if !slices.Equal(events, []string{"one", "two", "three", "validated", "commit", "stdout"}) {
		t.Fatalf("events=%v", events)
	}
}

func TestSegmentedCoreFailure(t *testing.T) {
	injected := errors.New("backend failed")
	for _, name := range []string{"max", "context", "context error", "timeout", "canceled", "decode", "invalid", "unknown", "partial error", "count", "token"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			want := ErrIncompleteGeneration
			core := segmentedFixture(t, byteLength, func(_ context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
				calls++
				result := segmentedStop(r)
				if calls != 2 {
					return result, nil
				}
				switch name {
				case "max":
					result.Finish = inference.MaxTokens
				case "context":
					result.Finish, want = inference.ContextLimit, segment.ErrContextLimit
				case "context error":
					result.Finish, want = inference.ContextLimit, segment.ErrContextLimit
					return result, injected
				case "timeout":
					result.Finish, want = inference.Timeout, context.DeadlineExceeded
				case "canceled":
					result.Finish, want = inference.Canceled, context.Canceled
				case "decode":
					result.Finish = inference.DecodeError
				case "invalid":
					result.Finish = inference.InvalidOutput
				case "unknown":
					result.Finish = "future"
				case "partial error":
					want = injected
					return result, injected
				case "count":
					result.PromptTokens++
					want = segment.ErrInvalidResult
				case "token":
					result.Text = "[[YAKUORI_0_9]]"
					want = protect.ErrInvalidCandidate
				}
				return result, nil
			})
			sink, memory := &textSink{limit: -1}, &fakeTM{}
			result, err := core.Text(context.Background(), []byte("one two three"), textadapter.New("en", "ja"), segmentedProfile(t), memory, sink)
			wantCalls := 2
			if name == "token" {
				wantCalls = 3
			}
			if !errors.Is(err, want) || calls != wantCalls || result.TMCommitted || memory.calls != 0 || sink.Len() != 0 {
				t.Fatalf("err=%v want=%v calls=%d result=%+v rows=%v stdout=%q", err, want, calls, result, memory.rows, sink.String())
			}
			if name == "context error" && !errors.Is(err, injected) {
				t.Fatalf("backend cause lost: %v", err)
			}
			calls = 0
			session, _ := generationFixture(t, []string{"one two three"}, nil)
			accepted, err := core.Generate(context.Background(), session, segmentedProfile(t))
			if err == nil || accepted != nil {
				t.Fatalf("accepted=%v err=%v", accepted, err)
			}
		})
	}
}

func TestSegmentedCancel(t *testing.T) {
	for _, timing := range []string{"before", "during", "after"} {
		t.Run(timing, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started, draining, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			calls := 0
			core := segmentedFixture(t, byteLength, func(ctx context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
				calls++
				if timing == "during" {
					close(started)
					<-ctx.Done()
					close(draining)
					<-release
				} else {
					cancel()
				}
				return segmentedStop(r), nil
			})
			sink, memory := &textSink{limit: -1}, &fakeTM{}
			if timing == "before" {
				cancel()
			}
			done := make(chan error, 1)
			go func() {
				_, err := core.Text(ctx, []byte("one two three"), textadapter.New("en", "ja"), segmentedProfile(t), memory, sink)
				done <- err
			}()
			if timing == "during" {
				<-started
				cancel()
				<-draining
				select {
				case err := <-done:
					t.Fatalf("returned before drain: %v", err)
				default:
				}
				if memory.calls != 0 || sink.Len() != 0 {
					t.Fatal("partial result committed while draining")
				}
				close(release)
			}
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("err=%v", err)
			}
			wantCalls := 1
			if timing == "before" {
				wantCalls = 0
			}
			if calls != wantCalls || memory.calls != 0 || sink.Len() != 0 {
				t.Fatalf("calls=%d TM=%d stdout=%q", calls, memory.calls, sink.String())
			}
			session, _ := generationFixture(t, []string{"one two three"}, nil)
			accepted, err := core.Generate(ctx, session, segmentedProfile(t))
			if accepted != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("accepted=%v err=%v", accepted, err)
			}
		})
	}
}

func TestSegmentedProfileMismatch(t *testing.T) {
	wrong := segment.ProfileInput()
	wrong.Schema = "other"
	wrongProfile, err := validate.NewProfileWithSegmentation([32]byte{1}, wrong)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []validate.Profile{textProfile(t), wrongProfile} {
		for _, source := range []string{"one two", "", " {p}"} {
			session, _ := generationFixture(t, []string{source}, nil)
			core := NewSegmentedCore(nil, nil)
			accepted, err := core.Generate(context.Background(), session, p)
			if !errors.Is(err, validate.ErrInvalidProfile) || accepted != nil {
				t.Fatalf("accepted=%v err=%v", accepted, err)
			}
		}
	}
}

func TestLegacyCoreRejectsSegmentationProfile(t *testing.T) {
	for _, source := range []string{"one two", ""} {
		t.Run(source, func(t *testing.T) {
			session, _ := generationFixture(t, []string{source}, nil)
			calls := 0
			core := NewCore(fakeEngine(func(_ context.Context, _ unit.UnitID, text string) (Generation, error) {
				calls++
				return Generation{Text: text, Finish: Stop}, nil
			}))
			accepted, err := core.Generate(context.Background(), session, segmentedProfile(t))
			var diagnostic *Error
			if !errors.Is(err, validate.ErrInvalidProfile) || !errors.As(err, &diagnostic) || diagnostic.Phase != "input" || accepted != nil || calls != 0 {
				t.Fatalf("accepted=%v err=%v calls=%d", accepted, err, calls)
			}
		})
	}
}

func TestSegmentedContextLimit(t *testing.T) {
	core := segmentedFixture(t, byteLength, func(context.Context, inference.GenerationRequest) (inference.GenerationResult, error) {
		t.Fatal("generation on failed plan")
		return inference.GenerationResult{}, nil
	})
	session, _ := generationFixture(t, []string{"unbreakable"}, nil)
	accepted, err := core.Generate(context.Background(), session, segmentedProfile(t))
	var diagnostic *Error
	if accepted != nil || !errors.Is(err, segment.ErrContextLimit) || !errors.As(err, &diagnostic) || diagnostic.Phase != "plan" || diagnostic.UnitID != session.Units()[0].ID() {
		t.Fatalf("accepted=%v err=%v", accepted, err)
	}
	p, err := protect.Prepare(session, session.Units()[0].ID())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := core.segmented.plan(context.Background(), session.Units()[0].ID(), p, core.limits)
	result, err := segmentedFailure(err)
	if err == nil {
		result, err = core.segmented.generate(context.Background(), plan)
	}
	if result.Finish != ContextLimit || !errors.Is(err, segment.ErrContextLimit) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestSegmentedProtectedOnly(t *testing.T) {
	session, _ := generationFixture(t, []string{"", " {p}\r\n", "one {p} two"}, map[int][]unit.ProtectionSpan{1: {{Start: 1, End: 4, Kind: unit.Placeholder}}, 2: {{Start: 4, End: 7, Kind: unit.Placeholder}}})
	var calls []string
	core := segmentedFixture(t, func(text string) int {
		if strings.Contains(text, "[[") {
			return 99
		}
		return len(text)
	}, func(_ context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
		calls = append(calls, r.RenderedPrompt())
		return segmentedStop(r), nil
	})
	accepted, err := core.Generate(context.Background(), session, segmentedProfile(t))
	if err != nil || len(accepted) != 3 || !slices.Equal(calls, []string{"one", "two"}) {
		t.Fatalf("accepted=%v calls=%v err=%v", accepted, calls, err)
	}
	for i, want := range []string{"", " {p}\r\n", "ONE {p} TWO"} {
		if accepted[i].Text() != want {
			t.Fatalf("text=%q want=%q", accepted[i].Text(), want)
		}
	}
}

func TestSegmentedParentValidation(t *testing.T) {
	core := segmentedFixture(t, byteLength, func(_ context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
		result := segmentedStop(r)
		result.Text = "repeat"
		return result, nil
	})
	sink, memory := &textSink{limit: -1}, &fakeTM{}
	_, err := core.Text(context.Background(), []byte("one\ntwo\nthree"), textadapter.New("en", "ja"), segmentedProfile(t), memory, sink)
	var diagnostic *Error
	if !errors.Is(err, validate.ErrInvalidCandidate) || !errors.As(err, &diagnostic) || diagnostic.Phase != "validate" || memory.calls != 0 || sink.Len() != 0 {
		t.Fatalf("err=%v TM=%d stdout=%q", err, memory.calls, sink.String())
	}
}

func TestSegmentedRequestsPlannedBeforeGeneration(t *testing.T) {
	core := segmentedFixture(t, byteLength, nil)
	var counted []inference.GenerationRequest
	backend := core.segmented.engine.(segmentedBackend)
	backend.count = func(_ context.Context, r inference.GenerationRequest) (int, error) {
		counted = append(counted, r)
		return len(r.TokenIDs()), nil
	}
	backend.generate = func(_ context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
		if counted[len(counted)-1].RenderedPrompt() != "three" {
			t.Fatal("started before full plan")
		}
		if !slices.ContainsFunc(counted, func(prior inference.GenerationRequest) bool { return reflect.DeepEqual(prior, r) }) {
			t.Fatal("request changed after counting")
		}
		return segmentedStop(r), nil
	}
	core.segmented.engine = backend
	session, _ := generationFixture(t, []string{"one two three"}, nil)
	if _, err := core.Generate(context.Background(), session, segmentedProfile(t)); err != nil {
		t.Fatal(err)
	}
}
