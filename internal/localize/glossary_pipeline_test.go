package localize

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

type glossaryScenario struct {
	entries   []glossary.Entry
	segmented bool
	context   int
	mismatch  string
	translate func(string, glossary.Glossary) string
}

type glossaryHarness struct {
	core     Core
	profile  validate.Profile
	calls    int
	requests []inference.GenerationRequest
	texts    []string
}

func glossaryCore(t *testing.T, scenario glossaryScenario) *glossaryHarness {
	t.Helper()
	h := &glossaryHarness{}
	base, _ := canonicalCoreFixture(t, false, "", &h.calls)
	info, err := base.inference.engine.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	info.ContextTokens = scenario.context
	if info.ContextTokens == 0 {
		info.ContextTokens = 4096
	}
	policy, err := inference.NewGenerationPolicy(1, 2, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := glossary.New(glossary.Schema, scenario.entries)
	if err != nil {
		t.Fatal(err)
	}
	texts := make(map[string]string)
	build := func(_ context.Context, _ unit.UnitID, input RequestInput) (inference.GenerationRequest, error) {
		terms := input.Terms
		if scenario.mismatch == "dropped" {
			terms = glossary.Glossary{}
		} else if scenario.mismatch == "global" {
			terms = definition
		} else if scenario.mismatch == "other unit" && input.Text == "dog" {
			terms, err = glossary.New(glossary.Schema, scenario.entries[:1])
			if err != nil {
				return inference.GenerationRequest{}, err
			}
		}
		messages := prompt.Build(prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: input.Text, Terms: terms})
		rendered := messages.System + "\n" + messages.User
		texts[rendered] = input.Text
		n := len(input.Text)
		if !terms.Empty() {
			n += len(terms.CanonicalBytes())
		}
		return inference.NewGenerationRequest(inference.PreparedRequest{
			RenderedPrompt: rendered, TokenIDs: make([]int, n), Terms: terms, StopIDs: []int{42},
			Identity: inference.RequestIdentity{ModelSHA256: info.ModelSHA256, Tokenizer: info.Tokenizer, Template: info.Template, PromptSchema: info.Prompt.Schema, Prompt: info.Prompt, Thinking: info.Thinking},
		}, policy)
	}
	request, err := build(context.Background(), unit.UnitID{}, RequestInput{Text: "base"})
	if err != nil {
		t.Fatal(err)
	}
	h.profile, err = NewCanonicalInferenceProfile(info, request, InferenceProfileInput{
		ModelID: "model", Glossary: validate.GlossaryInput(definition), Limits: config.DefaultLimits(), Segmentation: scenario.segmented,
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := segmentedBackend{info: info, commonEngine: commonEngine{
		count: func(_ context.Context, r inference.GenerationRequest) (int, error) { return len(r.TokenIDs()), nil },
		generate: func(_ context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
			h.calls++
			h.requests = append(h.requests, r)
			text := texts[r.RenderedPrompt()]
			h.texts = append(h.texts, text)
			if scenario.translate != nil {
				text = scenario.translate(text, r.Terms())
			}
			return inference.GenerationResult{Text: text, Finish: inference.Stop, PromptTokens: len(r.TokenIDs())}, nil
		},
	}}
	if scenario.segmented {
		h.core = NewGlossarySegmentedCore(backend, build)
	} else {
		h.core = NewCore(NewGlossaryInferenceEngine(backend, build))
	}
	return h
}

func TestGlossaryRegularAndSegmentedParent(t *testing.T) {
	entries := []glossary.Entry{
		{Source: "cat", Target: "猫", Mode: glossary.Mandatory, Match: glossary.Word},
		{Source: "dog", Target: "犬", Mode: glossary.Prefer, Match: glossary.Word},
		{Source: "KEEP", Mode: glossary.Protect, Match: glossary.Word},
	}
	terms, err := glossary.New(glossary.Schema, entries[:2])
	if err != nil {
		t.Fatal(err)
	}
	for _, segmented := range []bool{false, true} {
		t.Run(stringBool(segmented), func(t *testing.T) {
			contextLimit := 4096
			if segmented {
				contextLimit = len(terms.CanonicalBytes()) + 6
			}
			seenMandatory := false
			h := glossaryCore(t, glossaryScenario{entries: entries, segmented: segmented, context: contextLimit, translate: func(text string, got glossary.Glossary) string {
				if got != terms {
					t.Fatalf("per-parent terms=%s want=%s", got.CanonicalBytes(), terms.CanonicalBytes())
				}
				if strings.Contains(text, "cat") && !seenMandatory {
					text = strings.Replace(text, "cat", "猫", 1)
					seenMandatory = true
				}
				return text
			}})
			session, _ := generationFixture(t, []string{"cat KEEP cat dog"}, nil)
			accepted, err := h.core.Generate(context.Background(), session, h.profile)
			if err != nil || len(accepted) != 1 || accepted[0].Text() != "猫 KEEP cat dog" || h.calls == 0 || (segmented && h.calls < 2) {
				t.Fatalf("accepted=%v calls=%d error=%v", accepted, h.calls, err)
			}
			for _, request := range h.requests {
				if request.Terms() != terms || !strings.Contains(request.RenderedPrompt(), string(terms.CanonicalBytes())) {
					t.Fatal("segment lost applicable parent terms")
				}
			}
		})
	}
}

func TestGlossaryCoreRejectsWrongRequestTermsBeforeGeneration(t *testing.T) {
	entries := []glossary.Entry{
		{Source: "cat", Target: "猫", Mode: glossary.Prefer, Match: glossary.Word},
		{Source: "dog", Target: "犬", Mode: glossary.Mandatory, Match: glossary.Word},
	}
	for _, segmented := range []bool{false, true} {
		mismatches := []string{"dropped", "global", "other unit", "legacy"}
		if !segmented {
			mismatches = append(mismatches, "fake")
		}
		for _, mismatch := range mismatches {
			t.Run(stringBool(segmented)+"/"+mismatch, func(t *testing.T) {
				h := glossaryCore(t, glossaryScenario{entries: entries, segmented: segmented, mismatch: mismatch})
				if mismatch == "legacy" {
					legacy := func(context.Context, unit.UnitID, string) (inference.GenerationRequest, error) {
						t.Fatal("legacy builder received applicable terms")
						return inference.GenerationRequest{}, nil
					}
					if segmented {
						h.core.segmented.buildTerms, h.core.segmented.buildRequest = nil, legacy
					} else {
						h.core.inference.buildTerms, h.core.inference.buildRequest = nil, legacy
					}
				}
				if mismatch == "fake" {
					h.core = NewCore(fakeEngine(func(context.Context, unit.UnitID, string) (Generation, error) {
						h.calls++
						return Generation{}, nil
					}))
				}
				session, _ := generationFixture(t, []string{"cat", "dog"}, nil)
				accepted, err := h.core.Generate(context.Background(), session, h.profile)
				if !errors.Is(err, ErrInferenceProfile) || accepted != nil || h.calls != 0 {
					t.Fatalf("accepted=%v calls=%d error=%v", accepted, h.calls, err)
				}
			})
		}
	}
}
