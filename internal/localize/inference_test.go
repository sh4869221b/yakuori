package localize

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/unit"
)

type commonEngine struct {
	inference.Engine
	count    func(context.Context, inference.GenerationRequest) (int, error)
	generate func(context.Context, inference.GenerationRequest) (inference.GenerationResult, error)
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
