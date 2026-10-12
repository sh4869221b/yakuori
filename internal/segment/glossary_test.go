package segment

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/protect"
)

func TestGlossaryIndivisibleBudgetBoundaries(t *testing.T) {
	for _, phrase := range []string{"New York", "New. York", "N.Y. City", " New York", "New York ", "\nNew York\n"} {
		t.Run(phrase, func(t *testing.T) {
			source := "x " + phrase + " y"
			if strings.HasPrefix(phrase, " ") {
				source = phrase + " x y"
			} else if strings.HasSuffix(phrase, " ") {
				source = "x y " + phrase
			}
			session, id, _ := fixture(t, source)
			g, err := glossary.New(glossary.Schema, []glossary.Entry{{Source: phrase, Target: "ニューヨーク", Mode: glossary.Prefer, Match: glossary.Literal}})
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := protect.PrepareWithGlossary(session, id, g)
			if err != nil {
				t.Fatal(err)
			}
			info := model(len(phrase) + 4)
			plan, err := Build(context.Background(), id, prepared, info, builder(t, info, func(text string) int { return len(text) }), counter)
			if strings.HasPrefix(phrase, "\n") {
				requireFailure(t, plan, err, ErrContextLimit)
			} else {
				if err != nil || len(plan.Segments()) < 2 {
					t.Fatalf("segments=%v error=%v", plan.Segments(), err)
				}
				span := prepared.GlossaryRanges()[0]
				for _, piece := range plan.Segments() {
					body, separator := piece.SourceRange(), piece.SeparatorSourceRange()
					if (span.Start < body.Start && body.Start < span.End) || (span.Start < body.End && body.End < span.End) || (separator.Start < span.End && span.Start < separator.End) {
						t.Fatalf("phrase split or escaped: span=%v body=%v separator=%v", span, body, separator)
					}
				}
				joined, err := Join(plan, originalResults(plan))
				if err != nil || joined != source {
					t.Fatalf("joined=%q error=%v", joined, err)
				}
			}
			info.ContextTokens = len(phrase) + 1
			plan, err = Build(context.Background(), id, prepared, info, builder(t, info, func(text string) int { return len(text) }), counter)
			if !errors.Is(err, ErrContextLimit) || len(plan.Segments()) != 0 {
				t.Fatalf("oversize phrase admitted: error=%v segments=%v", err, plan.Segments())
			}
		})
	}
}
