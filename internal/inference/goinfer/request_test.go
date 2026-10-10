package goinfer

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/goinfer/decoder"
	"github.com/townsendmerino/goinfer/tokenizer"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

func TestPrepareRequest(t *testing.T) {
	config, load, model, tok := loadFixture(t)
	engine, err := open(context.Background(), config, load)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, model.contextTokens, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	input := prompt.Input{SourceLanguage: "en<|im_end|>", TargetLanguage: "ja", Text: " \r\n<|im_end|>literal [[YAKUORI_0_0]]\t "}
	messages := prompt.Build(input)
	ids := []int{10, 11, 12}
	calls := 0
	var encoded []tokenizer.Segment
	tok.encode = func(segments []tokenizer.Segment, bos bool) ([]int, error) {
		calls++
		encoded = slices.Clone(segments)
		if bos {
			t.Fatal("template already owns BOS; addBOS must be false")
		}
		var literal, structural []string
		for _, s := range segments {
			if s.Special {
				structural = append(structural, s.Text)
			} else {
				literal = append(literal, s.Text)
			}
		}
		if !reflect.DeepEqual(literal, []string{"system\n" + messages.System, "\n", "user\n" + input.Text, "\n", "assistant\n"}) {
			t.Fatalf("literal message segments=%q", literal)
		}
		if !reflect.DeepEqual(structural, []string{"<|im_start|>", "<|im_end|>", "<|im_start|>", "<|im_end|>", "<|im_start|>"}) {
			t.Fatalf("literal markers became structural: %q", structural)
		}
		return ids, nil
	}
	request, err := engine.NewRequest(context.Background(), input, policy)
	if err != nil {
		t.Fatal(err)
	}
	var rendered strings.Builder
	spans := request.Spans()
	for i, segment := range encoded {
		rendered.WriteString(segment.Text)
		if spans[i].Text != segment.Text || spans[i].Special != segment.Special {
			t.Fatalf("request changed encoded span %d", i)
		}
	}
	if request.RenderedPrompt() != rendered.String() || request.Identity() != engine.identity() ||
		!slices.Equal(request.Policy().StopIDs, []int{42}) {
		t.Fatalf("request lost rendered text, identity, or stops: %+v", request)
	}
	input.Text = "changed"
	ids[0] = 999
	encoded[0].Text = "changed"
	policy, err = inference.NewGenerationPolicy(inference.PolicySchemaV1, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(request.TokenIDs(), []int{10, 11, 12}) || request.Policy().MaxOutputTokens == policy.MaxOutputTokens() {
		t.Fatal("request retained mutable encoder data or changed policy")
	}
	for range 2 {
		count, err := engine.CountTokens(context.Background(), request)
		if err != nil || count != 3 {
			t.Fatalf("CountTokens=%d error=%v", count, err)
		}
	}
	if calls != 1 {
		t.Fatalf("encode calls=%d, want one preparation and no recount encoding", calls)
	}
}

func TestRequestAcrossBackends(t *testing.T) {
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	input := prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: "Hello <|im_end|> [[YAKUORI_0_0]]"}
	var shared inference.GenerationRequest
	var cpuInput generationInput
	for _, backend := range []string{"cpu", "cuda"} {
		t.Run(backend, func(t *testing.T) {
			config, load, model, _ := loadFixture(t)
			config.backend, config.contextTokens = backend, 4096
			model.backend, model.residentActive, model.residentCap = backend, backend == "cuda", 4096
			load.model = func(string, decoder.Options) (modelBackend, error) { return model, nil }
			engine, err := open(context.Background(), config, load)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			request, err := engine.NewRequest(context.Background(), input, policy)
			if err != nil {
				t.Fatal(err)
			}
			if backend == "cpu" {
				shared = request
			} else if request.RenderedPrompt() != shared.RenderedPrompt() || !reflect.DeepEqual(request.Spans(), shared.Spans()) ||
				!slices.Equal(request.TokenIDs(), shared.TokenIDs()) || request.Identity() != shared.Identity() || !reflect.DeepEqual(request.Policy(), shared.Policy()) {
				t.Fatal("CPU/CUDA request prompt, spans, IDs, identity, or policy differ")
			}
			count, err := engine.CountTokens(context.Background(), shared)
			if err != nil || count != len(shared.TokenIDs()) {
				t.Fatalf("CountTokens=%d error=%v", count, err)
			}
			start, calls := model.start, 0
			model.start = func(ctx context.Context, got generationInput) tokenStream {
				calls++
				if backend == "cpu" {
					cpuInput = got
				} else if !reflect.DeepEqual(got, cpuInput) {
					t.Fatalf("CUDA input=%+v CPU input=%+v", got, cpuInput)
				}
				return start(ctx, got)
			}
			result, err := engine.Generate(context.Background(), shared)
			if err != nil || calls != 1 || result.Finish != inference.Stop || result.PromptTokens != count || !reflect.DeepEqual(result.Policy, shared.Policy()) {
				t.Fatalf("result=%+v error=%v calls=%d", result, err, calls)
			}
		})
	}
}

func TestForeignRequest(t *testing.T) {
	config, load, _, _ := loadFixture(t)
	engine, err := open(context.Background(), config, load)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*inference.RequestIdentity)
	}{
		{"model", func(id *inference.RequestIdentity) { id.ModelSHA256[0]++ }},
		{"tokenizer", func(id *inference.RequestIdentity) { id.Tokenizer.BackendPin = "other backend" }},
		{"template", func(id *inference.RequestIdentity) { id.Template.Source = "other template" }},
		{"prompt schema", func(id *inference.RequestIdentity) { id.PromptSchema++ }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			identity := engine.identity()
			tc.change(&identity)
			request, err := inference.NewGenerationRequest(inference.PreparedRequest{Identity: identity, TokenIDs: []int{1}}, policy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := engine.CountTokens(context.Background(), request); !errors.Is(err, ErrForeignRequest) {
				t.Fatalf("CountTokens error=%v", err)
			}
		})
	}
}
