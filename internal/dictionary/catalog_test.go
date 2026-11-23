package dictionary

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/glossary"
)

var inn = Query{
	Game: "fixture-game", Artifact: "fixture-artifact", SourceLanguage: "en", TargetLanguage: "ja",
	Adapter: "witcher3-w3strings", FormatSchema: "w3strings-v162-en-jp-v1", StableID: "1",
	Source: "Welcome cat TOKEN", Scene: "inn", Speaker: "keeper",
}

func readFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/neutral-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func importEntries(t *testing.T, catalog *Catalog, entries ...Entry) []Diagnostic {
	t.Helper()
	data, err := json.Marshal(Document{Version: Version, Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	diagnostics, err := catalog.Import(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return diagnostics
}

func reviewEntry(t *testing.T, c *Catalog, id string, review Review) {
	t.Helper()
	if err := c.Review(id, review); err != nil {
		t.Fatal(err)
	}
}

func translationFixture() Entry {
	return Entry{
		ID: "inn-welcome", Kind: Translation, SourceLanguage: "en", TargetLanguage: "ja",
		Source: inn.Source, Target: "宿へようこそ 猫 TOKEN",
		Provenance: "Hand-authored dictionary fixture",
		License:    License{Name: "CC0-1.0", Permission: Allowed}, Confirmation: Confirmed,
		Scope:   Scope{Game: inn.Game, Artifact: inn.Artifact},
		Unit:    Unit{Adapter: inn.Adapter, FormatSchema: inn.FormatSchema, StableID: inn.StableID},
		Context: Context{Scene: inn.Scene, Speaker: inn.Speaker},
	}
}

func TestCatalogFixtureDistinguishesReferencesAndExactTranslations(t *testing.T) {
	var c Catalog
	if diagnostics, err := c.Import(bytes.NewReader(readFixture(t))); err != nil || len(diagnostics) != 0 {
		t.Fatalf("import = %v, %v", diagnostics, err)
	}
	if got := c.References(inn); len(got) != 7 {
		t.Fatalf("references = %d, want seven distinct rows", len(got))
	}
	if got, err := c.Select(inn); err != nil || got.Target != "" || len(got.Terms) != 0 {
		t.Fatalf("external confirmation without a local decision = %#v, %v", got, err)
	}

	for _, id := range []string{"inn-welcome", "gate-welcome", "unkeyed-welcome", "blank-target"} {
		reviewEntry(t, &c, id, Review{Decision: Accept})
	}
	reviewEntry(t, &c, "inn-cat", Review{Decision: Accept, Mode: glossary.Prefer, Match: glossary.Word})
	reviewEntry(t, &c, "gate-cat", Review{Decision: Accept, Mode: glossary.Mandatory, Match: glossary.Word})
	reviewEntry(t, &c, "token", Review{Decision: Accept, Mode: glossary.Protect, Match: glossary.Word})

	got, err := c.Select(inn)
	wantTerms := []glossary.Entry{
		{Source: "TOKEN", Mode: glossary.Protect, Match: glossary.Word},
		{Source: "cat", Target: "猫", Mode: glossary.Prefer, Match: glossary.Word},
	}
	if err != nil || got.Target != "宿へようこそ 猫 TOKEN" || !slices.Equal(got.Terms, wantTerms) {
		t.Fatalf("inn selection = %#v, %v", got, err)
	}
	for _, diagnostic := range []Diagnostic{
		{ID: "gate-cat", Reason: ReasonContextMismatch},
		{ID: "unkeyed-welcome", Reason: ReasonReferenceOnly},
		{ID: "blank-target", Reason: ReasonReferenceOnly},
	} {
		if !slices.Contains(got.Diagnostics, diagnostic) {
			t.Errorf("selection omits diagnostic %#v", diagnostic)
		}
	}

	gate := inn
	gate.Scene, gate.Speaker, gate.StableID = "gate", "guard", "2"
	got, err = c.Select(gate)
	wantTerms[1].Mode, wantTerms[1].Target = glossary.Mandatory, "野良猫"
	if err != nil || got.Target != "門へようこそ 猫 TOKEN" || !slices.Equal(got.Terms, wantTerms) {
		t.Fatalf("gate selection = %#v, %v", got, err)
	}

	word := inn
	word.Source = "Welcome scatter TOKEN"
	got, err = c.Select(word)
	if err != nil || got.Target != "" || !slices.Equal(got.Terms, wantTerms[:1]) {
		t.Fatalf("word match with a substring = %#v, %v", got, err)
	}
}

func TestCatalogTranslationEligibility(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Entry)
	}{
		{"no source", func(e *Entry) { e.Source = "" }},
		{"different source", func(e *Entry) { e.Source = "Welcome different cat TOKEN" }},
		{"no artifact scope", func(e *Entry) { e.Scope.Artifact = "" }},
		{"different game", func(e *Entry) { e.Scope.Game = "other game" }},
		{"partial ID", func(e *Entry) { e.Unit.FormatSchema = "" }},
		{"different ID", func(e *Entry) { e.Unit.StableID = "2" }},
		{"no scene", func(e *Entry) { e.Context.Scene = "" }},
		{"different scene", func(e *Entry) { e.Context.Scene = "gate" }},
		{"different speaker", func(e *Entry) { e.Context.Speaker = "guard" }},
		{"different language", func(e *Entry) { e.TargetLanguage = "de" }},
		{"no provenance", func(e *Entry) { e.Provenance = "" }},
		{"unknown permission", func(e *Entry) { e.License.Permission = UnknownPermission }},
		{"unconfirmed", func(e *Entry) { e.Confirmation = Unconfirmed }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c Catalog
			e := translationFixture()
			tt.edit(&e)
			importEntries(t, &c, e)
			reviewEntry(t, &c, e.ID, Review{Decision: Accept})
			got, err := c.Select(inn)
			if err != nil || got.Target != "" {
				t.Fatalf("selection = %#v, %v, want no exact target", got, err)
			}
		})
	}
}

func TestCatalogReimportPreservesLocalDecisionsAndEditsAcrossReload(t *testing.T) {
	var c Catalog
	first, second := translationFixture(), translationFixture()
	second.ID, second.Target = "other-translation", "別訳 猫 TOKEN"
	importEntries(t, &c, first, second)
	edited := "手動編集 猫 TOKEN"
	reviewEntry(t, &c, first.ID, Review{Decision: Accept, Target: &edited})
	reviewEntry(t, &c, second.ID, Review{Decision: Reject})
	edited = "replacement outside catalog"

	state, err := json.Marshal(c.Reviews())
	if err != nil {
		t.Fatal(err)
	}
	var reviews []ReviewedEntry
	if err := json.Unmarshal(state, &reviews); err != nil {
		t.Fatal(err)
	}
	restored, err := NewCatalog(reviews)
	if err != nil {
		t.Fatal(err)
	}
	if got := importEntries(t, restored, first, second); len(got) != 0 {
		t.Fatalf("unchanged import = %v", got)
	}

	first.Target, second.Provenance = "変更済み外部訳 TOKEN", "Changed external attribution"
	changed := importEntries(t, restored, first, second)
	want := []Diagnostic{{ID: first.ID, Reason: ReasonImportConflict}, {ID: second.ID, Reason: ReasonImportConflict}}
	if !slices.Equal(changed, want) {
		t.Fatalf("changed import = %#v, want %#v", changed, want)
	}
	if got, err := restored.Select(inn); err != nil || got.Target != "手動編集 猫 TOKEN" {
		t.Fatalf("restored target = %#v, %v", got, err)
	}
	saved, err := json.Marshal(restored.Reviews())
	if err != nil || !bytes.Equal(saved, state) {
		t.Fatalf("restored decisions changed: %v", err)
	}
}

func TestCatalogIndependentReviewsAndTranslationConflicts(t *testing.T) {
	var c Catalog
	first, duplicate := translationFixture(), translationFixture()
	duplicate.ID = "different-id"
	importEntries(t, &c, first)
	reviewEntry(t, &c, first.ID, Review{Decision: Accept})
	importEntries(t, &c, duplicate)
	if got := c.Reviews(); len(got) != 1 || got[0].Entry.ID != first.ID {
		t.Fatalf("new ID inherited a local review: %#v", got)
	}
	reviewEntry(t, &c, duplicate.ID, Review{Decision: Reject})
	if got, err := c.Select(inn); err != nil || got.Target != first.Target {
		t.Fatalf("rejecting a duplicate ID affected the adopted entry: %#v, %v", got, err)
	}
	reviewEntry(t, &c, duplicate.ID, Review{Decision: Accept})
	if got, err := c.Select(inn); err != nil || got.Target != first.Target {
		t.Fatalf("identical targets caused ambiguity: %#v, %v", got, err)
	}

	conflict := first
	conflict.ID, conflict.Target = "conflict", "別訳 猫 TOKEN"
	importEntries(t, &c, conflict)
	reviewEntry(t, &c, conflict.ID, Review{Decision: Accept})
	got, err := c.Select(inn)
	if !errors.Is(err, ErrAmbiguousTranslation) || got.Target != "" || strings.Contains(err.Error(), conflict.Target) {
		t.Fatalf("conflicting exact targets = %#v, %v", got, err)
	}
}

func TestCatalogGlossaryConflict(t *testing.T) {
	var c Catalog
	first, second := translationFixture(), translationFixture()
	first.Kind, first.Source, first.Target = Term, "cat", "猫"
	second.ID, second.Kind, second.Source, second.Target = "other-term", Term, "cat", "野良猫"
	importEntries(t, &c, first, second)

	if err := c.Review(first.ID, Review{Decision: Accept}); err == nil {
		t.Fatal("adoption without an explicit glossary rule succeeded")
	}
	for _, id := range []string{first.ID, second.ID} {
		reviewEntry(t, &c, id, Review{Decision: Accept, Mode: glossary.Prefer, Match: glossary.Word})
	}
	if _, err := c.Select(inn); !errors.Is(err, glossary.ErrConflict) || strings.Contains(err.Error(), first.Source) {
		t.Fatalf("conflicting glossary rules = %v", err)
	}
}
