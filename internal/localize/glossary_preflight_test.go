package localize

import (
	"context"
	"errors"
	"testing"

	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func TestGlossaryCoreWordAndCJKBoundaries(t *testing.T) {
	for _, segmented := range []bool{false, true} {
		t.Run(stringBool(segmented), func(t *testing.T) {
			h := glossaryCore(t, glossaryScenario{segmented: segmented, entries: []glossary.Entry{
				{Source: "cat", Mode: glossary.Protect, Match: glossary.Word},
				{Source: "猫", Target: "ネコ", Mode: glossary.Prefer, Match: glossary.Word},
				{Source: "京", Target: "京", Mode: glossary.Mandatory, Match: glossary.Literal},
			}})
			sources := []string{"cat scatter", "cat猫 猫cat 猫_", "猫", "東京"}
			session, _ := generationFixture(t, sources, nil)
			accepted, err := h.core.Generate(context.Background(), session, h.profile)
			if err != nil || len(accepted) != len(sources) || h.calls != 4 {
				t.Fatalf("accepted=%v calls=%d error=%v", accepted, h.calls, err)
			}
			for i, want := range sources {
				if accepted[i].Text() != want {
					t.Fatalf("text=%q want=%q", accepted[i].Text(), want)
				}
			}
			if h.texts[0] != "[[YAKUORI_0_0]] scatter" || !h.requests[0].Terms().Empty() || !h.requests[1].Terms().Empty() {
				t.Fatalf("texts=%q requests=%v", h.texts, h.requests)
			}
			for i, mode := range []glossary.Mode{glossary.Prefer, glossary.Mandatory} {
				entries, err := h.requests[i+2].Terms().Entries()
				if err != nil || len(entries) != 1 || entries[0].Mode != mode {
					t.Fatalf("terms=%+v error=%v", entries, err)
				}
			}
		})
	}
}

func TestGlossaryCoreGenerationFreePreflight(t *testing.T) {
	for _, condition := range []string{"protect only", "compatible adapter", "unsupported schema", "conflict"} {
		t.Run(condition, func(t *testing.T) {
			entries := []glossary.Entry{{Source: "KEEP", Mode: glossary.Protect, Match: glossary.Word}}
			if condition == "conflict" {
				entries = append(entries, glossary.Entry{Source: "KEEP", Target: "other", Mode: glossary.Mandatory, Match: glossary.Word})
			}
			if condition == "compatible adapter" {
				entries = append(entries, glossary.Entry{Source: "KEEP", Target: "KEEP", Mode: glossary.Mandatory, Match: glossary.Word})
			}
			h := glossaryCore(t, glossaryScenario{entries: entries})
			if condition == "unsupported schema" {
				info, err := h.core.inference.engine.Info(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				request, err := h.core.inference.buildTerms(context.Background(), unit.UnitID{}, RequestInput{Text: "base"})
				if err != nil {
					t.Fatal(err)
				}
				input, err := EffectiveInferenceProfile(info, request, h.core.limits, false)
				if err != nil {
					t.Fatal(err)
				}
				input.ModelID = "model"
				input.Glossary = identity.GlossaryInput{Mode: "provided", Schema: "future", Entries: []byte("[]")}
				h.profile, err = validate.NewCanonicalProfile(input)
				if err != nil {
					t.Fatal(err)
				}
			}
			h.core = NewCore(fakeEngine(func(context.Context, unit.UnitID, string) (Generation, error) {
				t.Fatal("generation-free source generated")
				return Generation{}, nil
			}))
			spans := map[int][]unit.ProtectionSpan{}
			if condition == "compatible adapter" {
				spans[1] = []unit.ProtectionSpan{{Start: 0, End: 4, Kind: unit.Placeholder}}
			}
			session, _ := generationFixture(t, []string{"", "KEEP"}, spans)
			accepted, err := h.core.Generate(context.Background(), session, h.profile)
			switch condition {
			case "unsupported schema":
				if accepted != nil || !errors.Is(err, glossary.ErrInvalidGlossary) {
					t.Fatalf("accepted=%v error=%v", accepted, err)
				}
			case "conflict":
				if accepted != nil || !errors.Is(err, glossary.ErrConflict) {
					t.Fatalf("accepted=%v error=%v", accepted, err)
				}
			default:
				if err != nil || len(accepted) != 2 || accepted[1].Text() != "KEEP" {
					t.Fatalf("accepted=%v error=%v", accepted, err)
				}
			}
		})
	}
}
