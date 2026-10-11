package localize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/segment"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func canonicalCoreFixture(t *testing.T, segmented bool, mismatch string, calls *int) (Core, validate.Profile) {
	t.Helper()
	info := inference.ModelInfo{
		ModelSHA256: [32]byte{1}, Backend: "cpu", BackendPin: "goinfer-v1", ComputeQuant: "int4", ContextTokens: 7,
		Tokenizer:    inference.TokenizerIdentity{ModelSHA256: [32]byte{1}, BackendPin: "goinfer-v1"},
		Template:     inference.TemplateIdentity{Family: "chatml", Source: "embedded", RendererVersion: "v1"},
		PolicySchema: inference.PolicySchemaV1, Prompt: prompt.PlainIdentity(), Thinking: inference.ThinkingDisabled,
		Prefill: inference.PrefillPolicy{Path: "cpu", Fast: "off"},
	}
	request := func(text string, timeout time.Duration) (inference.GenerationRequest, error) {
		policy, err := inference.NewGenerationPolicy(1, 2, timeout)
		if err != nil {
			return inference.GenerationRequest{}, err
		}
		return inference.NewGenerationRequest(inference.PreparedRequest{RenderedPrompt: text, TokenIDs: make([]int, len(text)), StopIDs: []int{42}, Identity: inference.RequestIdentity{
			ModelSHA256: info.ModelSHA256, Tokenizer: info.Tokenizer, Template: info.Template,
			PromptSchema: info.Prompt.Schema, Prompt: info.Prompt, Thinking: info.Thinking,
		}}, policy)
	}
	base, err := request("one", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewCanonicalInferenceProfile(info, base, InferenceProfileInput{ModelID: "model", Glossary: identity.GlossaryInput{Mode: "none"}, Limits: config.DefaultLimits(), Segmentation: segmented})
	if err != nil {
		t.Fatal(err)
	}
	backend := segmentedBackend{info: info, commonEngine: commonEngine{
		count: func(_ context.Context, r inference.GenerationRequest) (int, error) { return len(r.TokenIDs()), nil },
		generate: func(_ context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
			*calls++
			return inference.GenerationResult{Text: r.RenderedPrompt(), Finish: inference.Stop, PromptTokens: len(r.TokenIDs())}, nil
		},
	}}
	if mismatch == "model" {
		backend.info.ComputeQuant = "different"
	}
	build := func(_ context.Context, _ unit.UnitID, text string) (inference.GenerationRequest, error) {
		timeout := time.Second
		if mismatch == "later policy" && text == "two" {
			timeout = 2 * time.Second
		}
		return request(text, timeout)
	}
	if segmented {
		return NewSegmentedCore(backend, build), profile
	}
	return NewCore(NewInferenceEngine(backend, build)), profile
}

func TestCanonicalInferenceProfilePreflightsAllRequests(t *testing.T) {
	for _, segmented := range []bool{false, true} {
		for _, mismatch := range []string{"later policy", "model"} {
			t.Run(stringBool(segmented)+"/"+mismatch, func(t *testing.T) {
				calls := 0
				core, profile := canonicalCoreFixture(t, segmented, mismatch, &calls)
				sources := []string{"one", "two"}
				if segmented {
					sources = []string{"one two three"}
				}
				session, _ := generationFixture(t, sources, nil)
				accepted, err := core.Generate(context.Background(), session, profile)
				want := ErrInferenceProfile
				if segmented && mismatch == "later policy" {
					want = segment.ErrInvalidRequest
				}
				if !errors.Is(err, want) || accepted != nil || calls != 0 {
					t.Fatalf("error=%v accepted=%v generation calls=%d", err, accepted, calls)
				}
				memory := &fakeTM{}
				var output bytes.Buffer
				var adapter Adapter = textadapter.New("en", "ja")
				input := []byte(sources[0])
				if !segmented {
					adapter = &fakeAdapter{t: t, tm: memory}
					input, err = json.Marshal(fakeImage{Adapter: "fixture", Schema: "v1", Language: "en", Records: []fakeRecord{{"a", "one"}, {"b", "two"}}})
					if err != nil {
						t.Fatal(err)
					}
				}
				result, err := core.Text(context.Background(), input, adapter, profile, memory, &output)
				if !errors.Is(err, want) || calls != 0 || memory.calls != 0 || result.TMCommitted || output.Len() != 0 {
					t.Fatalf("error=%v generate=%d commit=%d result=%+v output=%q", err, calls, memory.calls, result, output.String())
				}
			})
		}
	}
}

func TestCanonicalInferenceProfileSegmentedLaterUnitAndFullLimits(t *testing.T) {
	for _, mismatch := range []string{"", "later policy"} {
		t.Run(mismatch, func(t *testing.T) {
			calls := 0
			core, profile := canonicalCoreFixture(t, true, mismatch, &calls)
			session, _ := generationFixture(t, []string{"one", "two"}, nil)
			accepted, err := core.Generate(context.Background(), session, profile)
			if mismatch != "" {
				if !errors.Is(err, ErrInferenceProfile) || accepted != nil || calls != 0 {
					t.Fatalf("error=%v accepted=%v generation calls=%d", err, accepted, calls)
				}
				memory := &fakeTM{}
				input, err := json.Marshal(fakeImage{Adapter: "fixture", Schema: "v1", Language: "en", Records: []fakeRecord{{"a", "one"}, {"b", "two"}}})
				if err != nil {
					t.Fatal(err)
				}
				var output bytes.Buffer
				result, err := core.Text(context.Background(), input, &fakeAdapter{t: t, tm: memory}, profile, memory, &output)
				if !errors.Is(err, ErrInferenceProfile) || calls != 0 || memory.calls != 0 || result.TMCommitted || output.Len() != 0 {
					t.Fatalf("error=%v generate=%d commit=%d result=%+v output=%q", err, calls, memory.calls, result, output.String())
				}
			} else if err != nil || len(accepted) != 2 || calls != 2 {
				t.Fatalf("error=%v accepted=%v generation calls=%d", err, accepted, calls)
			}
		})
	}
}

func TestCanonicalInferenceProfileFakeEntry(t *testing.T) {
	calls := 0
	_, profile := canonicalCoreFixture(t, false, "", &calls)
	core := NewCore(fakeEngine(func(_ context.Context, _ unit.UnitID, text string) (Generation, error) {
		calls++
		return Generation{Text: text, Finish: Stop}, nil
	}))
	session, _ := generationFixture(t, []string{"Hello {p}", ""}, map[int][]unit.ProtectionSpan{0: {{Start: 6, End: 9, Kind: unit.Placeholder}}})
	accepted, err := core.Generate(context.Background(), session, profile)
	if err != nil || calls != 1 || len(accepted) != 2 || accepted[0].Text() != "Hello {p}" {
		t.Fatalf("error=%v generation calls=%d accepted=%v", err, calls, accepted)
	}
}

func TestCanonicalInferenceProfileEntrySchemaAndLimits(t *testing.T) {
	for _, condition := range []string{"protection schema", "validation fixtures", "segmentation limits"} {
		t.Run(condition, func(t *testing.T) {
			calls := 0
			core, profile := canonicalCoreFixture(t, condition == "segmentation limits", "", &calls)
			if condition == "segmentation limits" {
				core.limits.Segments--
			} else {
				info, err := core.inference.engine.Info(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				request, err := core.inference.buildRequest(context.Background(), unit.UnitID{}, "one")
				if err != nil {
					t.Fatal(err)
				}
				input, err := EffectiveInferenceProfile(info, request, core.limits, false)
				if err != nil {
					t.Fatal(err)
				}
				input.ModelID, input.Glossary = "model", identity.GlossaryInput{Mode: "none"}
				if condition == "protection schema" {
					input.Protection.Schema += "changed"
				} else {
					input.Validation.FixtureSet += "changed"
				}
				profile, err = validate.NewCanonicalProfile(input)
				if err != nil {
					t.Fatal(err)
				}
				core = NewCore(fakeEngine(func(_ context.Context, _ unit.UnitID, text string) (Generation, error) {
					calls++
					return Generation{Text: text, Finish: Stop}, nil
				}))
			}
			session, _ := generationFixture(t, []string{""}, nil)
			accepted, err := core.Generate(context.Background(), session, profile)
			if !errors.Is(err, ErrInferenceProfile) || accepted != nil || calls != 0 {
				t.Fatalf("error=%v accepted=%v generation calls=%d", err, accepted, calls)
			}
		})
	}
}

func stringBool(value bool) string {
	if value {
		return "segmented"
	}
	return "unsegmented"
}

func TestCanonicalInferenceProfileGenerationAndExport(t *testing.T) {
	for _, segmented := range []bool{false, true} {
		t.Run(stringBool(segmented), func(t *testing.T) {
			calls := 0
			core, profile := canonicalCoreFixture(t, segmented, "", &calls)
			memory := &fakeTM{}
			var output bytes.Buffer
			input := "one"
			wantCalls := 1
			if segmented {
				input = "one two three"
				wantCalls = 3
			}
			result, err := core.Text(context.Background(), []byte(input), textadapter.New("en", "ja"), profile, memory, &output)
			if err != nil || !result.TMCommitted || memory.calls != 1 || calls != wantCalls || output.String() != input {
				t.Fatalf("error=%v result=%+v generate=%d commit=%d output=%q", err, result, calls, memory.calls, output.String())
			}
		})
	}
}

func TestCanonicalInferenceProfileNoGenerationEntry(t *testing.T) {
	for _, segmented := range []bool{false, true} {
		for _, mismatch := range []string{"", "model"} {
			t.Run(stringBool(segmented)+"/"+mismatch, func(t *testing.T) {
				calls := 0
				core, profile := canonicalCoreFixture(t, segmented, mismatch, &calls)
				session, _ := generationFixture(t, []string{"", "{p}"}, map[int][]unit.ProtectionSpan{1: {{Start: 0, End: 3, Kind: unit.Placeholder}}})
				accepted, err := core.Generate(context.Background(), session, profile)
				if mismatch == "model" {
					if !errors.Is(err, ErrInferenceProfile) || accepted != nil || calls != 0 {
						t.Fatalf("error=%v accepted=%v generate=%d", err, accepted, calls)
					}
				} else if err != nil || len(accepted) != 2 || accepted[1].Text() != "{p}" || calls != 0 {
					t.Fatalf("error=%v accepted=%v generate=%d", err, accepted, calls)
				}
			})
		}
	}
}
