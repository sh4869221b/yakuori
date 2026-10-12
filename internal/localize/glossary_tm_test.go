package localize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/segment"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func TestGlossaryAllUnitsPreflightBeforeTMBegin(t *testing.T) {
	for _, segmented := range []bool{false, true} {
		for _, condition := range []string{"conflict", "dropped", "other unit"} {
			t.Run(stringBool(segmented)+"/"+condition, func(t *testing.T) {
				store, db := tmStoreFixture(t)
				entries := []glossary.Entry{
					{Source: "cat", Target: "猫", Mode: glossary.Prefer, Match: glossary.Word},
					{Source: "dog", Target: "犬", Mode: glossary.Mandatory, Match: glossary.Word},
				}
				want := ErrInferenceProfile
				if condition == "conflict" {
					entries = append(entries, glossary.Entry{Source: "dog", Target: "other", Mode: glossary.Prefer, Match: glossary.Word})
					want = glossary.ErrConflict
				}
				h := glossaryCore(t, glossaryScenario{entries: entries, segmented: segmented, mismatch: condition})
				adapter := &fakeAdapter{t: t, tm: &fakeTM{}}
				input, err := json.Marshal(fakeImage{Adapter: "fixture", Schema: "v1", Language: "en", Records: []fakeRecord{{"a", "one"}, {"b", "dog"}}})
				if err != nil {
					t.Fatal(err)
				}
				session, err := adapter.Import(input, h.profile)
				if err != nil {
					t.Fatal(err)
				}
				seedTMRow(t, db, session, h.profile, 0, "cached")
				memory := &tmExecution{store: store, scope: tmScope()}
				var output bytes.Buffer
				result, err := h.core.text(context.Background(), input, adapter, h.profile, nil, memory, &output)
				if !errors.Is(err, want) || h.calls != 0 || memory.run != nil || result.TMCommitted || output.Len() != 0 || tmRowCount(t, db) != 1 {
					t.Fatalf("error=%v calls=%d began=%t result=%+v output=%q", err, h.calls, memory.run != nil, result, output.String())
				}
			})
		}
	}
}

func TestGlossaryExactTMHitRevalidationAndAtomicFailure(t *testing.T) {
	entries := []glossary.Entry{
		{Source: "cat", Target: "猫", Mode: glossary.Mandatory, Match: glossary.Word},
		{Source: "dog", Target: "犬", Mode: glossary.Prefer, Match: glossary.Word},
		{Source: "KEEP", Mode: glossary.Protect, Match: glossary.Word},
	}
	for _, condition := range []string{"valid", "missing mandatory", "broken protect", "invalid mandatory generation", "invalid protect generation"} {
		t.Run(condition, func(t *testing.T) {
			store, db := tmStoreFixture(t)
			h := glossaryCore(t, glossaryScenario{entries: entries, translate: func(text string, _ glossary.Glossary) string {
				text = strings.ReplaceAll(text, "cat", "猫")
				if condition == "invalid mandatory generation" {
					text = strings.ReplaceAll(text, "猫", "cat")
				} else if condition == "invalid protect generation" {
					text = strings.ReplaceAll(text, "[[YAKUORI_0_0]]", "lost")
				}
				return text
			}})
			input := []byte("cat dog KEEP")
			adapter := textadapter.New("en", "ja")
			session, err := adapter.Import(input, h.profile)
			if err != nil {
				t.Fatal(err)
			}
			cached := "cat dog KEEP"
			if condition == "valid" {
				cached = "猫 dog KEEP"
			} else if condition == "broken protect" {
				cached = "猫 dog lost"
			}
			seedTMRow(t, db, session, h.profile, 0, cached)
			var output bytes.Buffer
			result, err := h.core.TextWithTM(context.Background(), input, adapter, h.profile, store, tmScope(), &output)
			if strings.HasPrefix(condition, "invalid") {
				want := validate.ErrInvalidCandidate
				if condition == "invalid protect generation" {
					want = protect.ErrInvalidCandidate
				}
				if !errors.Is(err, want) || h.calls != 1 || result.TMCommitted || output.Len() != 0 {
					t.Fatalf("error=%v calls=%d result=%+v output=%q", err, h.calls, result, output.String())
				}
			} else {
				wantCalls := 1
				if condition == "valid" {
					wantCalls = 0
				}
				if err != nil || h.calls != wantCalls || !result.TMCommitted || output.String() != "猫 dog KEEP" {
					t.Fatalf("error=%v calls=%d result=%+v output=%q", err, h.calls, result, output.String())
				}
				cached = "猫 dog KEEP"
			}
			var stored string
			if err := db.QueryRow("SELECT translated_text FROM tm").Scan(&stored); err != nil || stored != cached || tmRowCount(t, db) != 1 {
				t.Fatalf("stored=%q want=%q error=%v", stored, cached, err)
			}
		})
	}
}

func TestGlossaryChangeMissesExistingTMKey(t *testing.T) {
	store, db := tmStoreFixture(t)
	old := glossaryCore(t, glossaryScenario{entries: []glossary.Entry{{Source: "dog", Target: "犬", Mode: glossary.Prefer, Match: glossary.Word}}})
	current := glossaryCore(t, glossaryScenario{entries: []glossary.Entry{{Source: "dog", Target: "イヌ", Mode: glossary.Prefer, Match: glossary.Word}}})
	adapter := textadapter.New("en", "ja")
	input := []byte("dog")
	session, err := adapter.Import(input, old.profile)
	if err != nil {
		t.Fatal(err)
	}
	seedTMRow(t, db, session, old.profile, 0, "犬")
	var output bytes.Buffer
	result, err := current.core.TextWithTM(context.Background(), input, adapter, current.profile, store, tmScope(), &output)
	if err != nil || current.calls != 1 || !result.TMCommitted || output.String() != "dog" || tmRowCount(t, db) != 2 || old.profile == current.profile {
		t.Fatalf("error=%v calls=%d result=%+v output=%q", err, current.calls, result, output.String())
	}
}

func TestGlossaryPromptCostRejectsBeforeTMBegin(t *testing.T) {
	entries := []glossary.Entry{{Source: "New York", Target: "ニューヨーク", Mode: glossary.Prefer, Match: glossary.Literal}}
	terms, err := glossary.New(glossary.Schema, entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, segmented := range []bool{false, true} {
		t.Run(stringBool(segmented), func(t *testing.T) {
			store, db := tmStoreFixture(t)
			h := glossaryCore(t, glossaryScenario{entries: entries, segmented: segmented, context: len(terms.CanonicalBytes()) + len("New York") + 1})
			input := []byte("New York")
			adapter := textadapter.New("en", "ja")
			memory := &tmExecution{store: store, scope: tmScope()}
			var output bytes.Buffer
			result, err := h.core.text(context.Background(), input, adapter, h.profile, nil, memory, &output)
			if !errors.Is(err, segment.ErrContextLimit) || h.calls != 0 || memory.run != nil || result.TMCommitted || output.Len() != 0 || tmRowCount(t, db) != 0 {
				t.Fatalf("error=%v calls=%d began=%t result=%+v output=%q", err, h.calls, memory.run != nil, result, output.String())
			}
			if !segmented && h.core.inference.limits != h.core.limits {
				t.Fatal("model cap mutated the shared inference limits")
			}
		})
	}
}
