package localize

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func TestRequestBuilderChecksExactTerms(t *testing.T) {
	terms, err := glossary.New(glossary.Schema, []glossary.Entry{{Source: "cat", Target: "猫", Mode: glossary.Prefer, Match: glossary.Word}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := glossary.New(glossary.Schema, []glossary.Entry{{Source: "cat", Target: "ねこ", Mode: glossary.Prefer, Match: glossary.Word}})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := inference.NewGenerationPolicy(1, 2, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, condition := range []string{"matching", "dropped", "wrong", "legacy"} {
		t.Run(condition, func(t *testing.T) {
			calls := 0
			build := TermsRequestBuilder(func(_ context.Context, _ unit.UnitID, input RequestInput) (inference.GenerationRequest, error) {
				calls++
				got := input.Terms
				if condition == "dropped" {
					got = glossary.Glossary{}
				} else if condition == "wrong" {
					got = other
				}
				return inference.NewGenerationRequest(inference.PreparedRequest{Terms: got, RenderedPrompt: input.Text}, policy)
			})
			legacy := func(context.Context, unit.UnitID, string) (inference.GenerationRequest, error) {
				calls++
				return inference.GenerationRequest{}, nil
			}
			if condition == "legacy" {
				build = nil
			}
			request, err := buildTermsRequest(context.Background(), unit.UnitID{}, RequestInput{Text: "cat", Terms: terms}, build, legacy)
			if condition == "matching" {
				if err != nil || request.Terms() != terms || calls != 1 {
					t.Fatalf("request=%v calls=%d error=%v", request, calls, err)
				}
			} else if !errors.Is(err, ErrInferenceProfile) || (condition == "legacy" && calls != 0) {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}
