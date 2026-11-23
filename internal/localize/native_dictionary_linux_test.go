//go:build linux

package localize

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/sh4869221b/yakuori/internal/contextpack"
	"github.com/sh4869221b/yakuori/internal/dictionary"
	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func nativeDictionaryEntry(f nativeContextFixture, id, kind, source, target, scene, speaker string) map[string]any {
	return map[string]any{
		"id": id, "kind": kind, "source_language": "en", "target_language": "ja",
		"source": source, "target": target, "provenance": "hand-authored Native fixture",
		"license": map[string]string{"name": "CC0-1.0", "permission": "allowed"}, "confirmation": "confirmed",
		"scope":   map[string]string{"game": f.scope.GameNamespace, "artifact": f.scope.ArtifactNamespace},
		"context": map[string]string{"scene": scene, "speaker": speaker},
	}
}

func nativeDictionaryTranslation(f nativeContextFixture, index int, id, target string) map[string]any {
	scene, speaker := "inn", "keeper"
	if index == 1 {
		scene, speaker = "gate", "guard"
	}
	u := f.session.Units()[index]
	entry := nativeDictionaryEntry(f, id, "translation", string(u.Source()), target, scene, speaker)
	entry["unit"] = map[string]string{"adapter": u.ID().Adapter(), "format_schema": u.ID().FormatSchema(), "stable_id": u.ID().StableID()}
	return entry
}

func importNativeDictionary(t *testing.T, catalog *dictionary.Catalog, entries ...map[string]any) []dictionary.Diagnostic {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"version": 1, "entries": entries})
	if err != nil {
		t.Fatal(err)
	}
	diagnostics, err := catalog.Import(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return diagnostics
}

func reviewNativeDictionary(t *testing.T, catalog *dictionary.Catalog, id string, review dictionary.Review) {
	t.Helper()
	if err := catalog.Review(id, review); err != nil {
		t.Fatal(err)
	}
}

func TestNativeDictionary(t *testing.T) {
	t.Run("exact-and-reference", func(t *testing.T) {
		f := newNativeContextFixture(t)
		h := nativeContextEngine(t, false, "猫", nil)
		first := nativeDictionaryTranslation(f, 0, "inn-line", "宿の辞書訳 猫 TOKEN")
		second := nativeDictionaryTranslation(f, 1, "gate-line", "門の辞書訳 猫 TOKEN")
		missingUnit := nativeDictionaryTranslation(f, 1, "missing-unit", "識別子なし 猫 TOKEN")
		delete(missingUnit, "unit")
		missingSource := nativeDictionaryTranslation(f, 1, "missing-source", "原文なし 猫 TOKEN")
		delete(missingSource, "source")
		missingContext := nativeDictionaryTranslation(f, 1, "missing-context", "場面なし 猫 TOKEN")
		delete(missingContext, "context")
		unreviewed := nativeDictionaryTranslation(f, 1, "unreviewed", "未採否 猫 TOKEN")
		unconfirmed := nativeDictionaryTranslation(f, 1, "unconfirmed", "未確認 猫 TOKEN")
		unconfirmed["confirmation"] = "unconfirmed"
		var catalog dictionary.Catalog
		importNativeDictionary(t, &catalog, first, missingUnit, missingSource, missingContext, unreviewed, unconfirmed)
		for _, id := range []string{"inn-line", "missing-unit", "missing-source", "missing-context", "unconfirmed"} {
			reviewNativeDictionary(t, &catalog, id, dictionary.Review{Decision: dictionary.Accept})
		}
		var diagnostics []dictionary.Diagnostic
		pack := f.pack(t, f.scope, nil)
		pack.Dictionary = &catalog
		pack.OnDictionaryDiagnostic = func(id unit.UnitID, diagnostic dictionary.Diagnostic) {
			if id.StableID() == "2" {
				diagnostics = append(diagnostics, diagnostic)
			}
		}
		want := maps.Clone(nativeContextTexts)
		want["1"] = first["target"].(string)
		observation := f.success(t, h, pack, f.scope, false, 1, want)
		assertNativeContextKinds(t, observation, []string{"dictionary", "generated", "unchanged"})
		if observation.Validation != (ValidationCounts{Attempted: 3, Passed: 3}) {
			t.Fatalf("validation counts = %+v", observation.Validation)
		}
		for _, id := range []string{"missing-unit", "missing-source", "missing-context", "unreviewed", "unconfirmed"} {
			if !slices.ContainsFunc(diagnostics, func(d dictionary.Diagnostic) bool {
				return d.ID == id && d.Reason == dictionary.ReasonReferenceOnly
			}) {
				t.Errorf("unit 2 has no reference diagnostic for %s: %v", id, diagnostics)
			}
		}

		importNativeDictionary(t, &catalog, second)
		reviewNativeDictionary(t, &catalog, "gate-line", dictionary.Review{Decision: dictionary.Accept})
		want["2"] = second["target"].(string)
		observation = f.success(t, h, pack, f.scope, false, 0, want)
		assertNativeContextKinds(t, observation, []string{"tm", "dictionary", "tm"})
		if observation.Validation != (ValidationCounts{Attempted: 1, Passed: 1}) {
			t.Fatalf("validation counts = %+v", observation.Validation)
		}

		// The same source and unit ID with a changed, confirmed speaker may
		// use newly generated text, but must not reuse the old dictionary row.
		changed := f.pack(t, f.scope, func(p *contextpack.Pack) {
			p.Units[1].SpeakerID = new("keeper")
		})
		changed.Dictionary = &catalog
		want["2"] = nativeContextTexts["1"]
		observation = f.success(t, h, changed, f.scope, false, 1, want)
		assertNativeContextKinds(t, observation, []string{"tm", "generated", "tm"})
	})

	t.Run("reimport-and-dictionaryless", func(t *testing.T) {
		f := newNativeContextFixture(t)
		h := nativeContextEngine(t, false, "猫", nil)
		baseline := f.pack(t, f.scope, nil)
		f.success(t, h, baseline, f.scope, false, 2, nativeContextTexts)
		ordinaryRows := w3SQLiteRows(t, f.db)

		entry := nativeDictionaryTranslation(f, 0, "inn-line", "宿の取込み訳 猫 TOKEN")
		var catalog dictionary.Catalog
		importNativeDictionary(t, &catalog, entry)
		manual := "宿の手動修正 猫 TOKEN"
		reviewNativeDictionary(t, &catalog, "inn-line", dictionary.Review{Decision: dictionary.Accept, Target: &manual})
		pack := baseline
		pack.Dictionary = &catalog
		want := maps.Clone(nativeContextTexts)
		want["1"] = manual
		observation := f.success(t, h, pack, f.scope, false, 0, want)
		assertNativeContextKinds(t, observation, []string{"dictionary", "tm", "tm"})
		rows := w3SQLiteRows(t, f.db)
		if len(rows) != len(ordinaryRows)+1 {
			t.Fatalf("stored rows = %d, want %d", len(rows), len(ordinaryRows)+1)
		}

		entry["target"] = "再取込みの別訳 猫 TOKEN"
		diagnostics := importNativeDictionary(t, &catalog, entry)
		if !slices.ContainsFunc(diagnostics, func(d dictionary.Diagnostic) bool {
			return d.ID == "inn-line" && d.Reason == dictionary.ReasonImportConflict
		}) {
			t.Fatalf("missing reimport conflict diagnostic: %v", diagnostics)
		}
		observation = f.success(t, h, pack, f.scope, false, 0, want)
		assertNativeContextKinds(t, observation, []string{"tm", "tm", "tm"})
		w3SQLiteAssertRows(t, f.db, rows)

		source, profile := f.row(t, manual)
		// Check both a valid translation that differs from the reviewed text
		// and a row that lost the protected placeholder at the same key.
		for _, stored := range []string{"別の保存済み訳 猫 TOKEN", "宿の手動修正 猫"} {
			writeNativeContextRow(t, f.db, "UPDATE tm SET translated_text=? WHERE key_schema=? AND source_identity=? AND translation_profile=?",
				stored, identity.KeySchema, source, profile)
			observation = f.success(t, h, pack, f.scope, false, 0, want)
			assertNativeContextKinds(t, observation, []string{"dictionary", "tm", "tm"})
			if observation.TMValidation != (ValidationCounts{Attempted: 3, Passed: 2, Failed: 1}) ||
				observation.Validation != (ValidationCounts{Attempted: 1, Passed: 1}) {
				t.Fatalf("validation counts: TM=%+v current=%+v", observation.TMValidation, observation.Validation)
			}
			w3SQLiteAssertRows(t, f.db, rows)
		}

		reviewNativeDictionary(t, &catalog, "inn-line", dictionary.Review{Decision: dictionary.Reject})
		importNativeDictionary(t, &catalog, entry)
		observation = f.success(t, h, pack, f.scope, false, 0, nativeContextTexts)
		assertNativeContextKinds(t, observation, []string{"tm", "tm", "tm"})
		observation = f.success(t, h, baseline, f.scope, false, 0, nativeContextTexts)
		assertNativeContextKinds(t, observation, []string{"tm", "tm", "tm"})
		w3SQLiteAssertRows(t, f.db, rows)
	})

	t.Run("scoped-prefer", func(t *testing.T) {
		f := newNativeContextFixture(t)
		h := nativeContextEngine(t, false, "猫", nil)
		var catalog dictionary.Catalog
		term := nativeDictionaryEntry(f, "prefer-welcome", "term", "Welcome", "挨拶", "inn", "keeper")
		importNativeDictionary(t, &catalog, term)
		pack := f.pack(t, f.scope, nil)
		pack.Dictionary = &catalog
		f.success(t, h, pack, f.scope, false, 2, nativeContextTexts)
		preferred := glossary.Entry{Source: "Welcome", Target: "挨拶", Mode: glossary.Prefer, Match: glossary.Word}
		for _, req := range h.requests {
			entries, err := req.Terms().Entries()
			if err != nil || slices.Contains(entries, preferred) {
				t.Fatalf("unreviewed term was activated: %v, %v", entries, err)
			}
		}
		reviewNativeDictionary(t, &catalog, "prefer-welcome", dictionary.Review{
			Decision: dictionary.Accept, Mode: glossary.Prefer, Match: glossary.Word,
		})
		var mismatch bool
		pack.OnDictionaryDiagnostic = func(id unit.UnitID, d dictionary.Diagnostic) {
			if id.StableID() == "2" && d.ID == "prefer-welcome" && d.Reason == dictionary.ReasonContextMismatch {
				mismatch = true
			}
		}
		observation := f.success(t, h, pack, f.scope, false, 1, nativeContextTexts)
		assertNativeContextKinds(t, observation, []string{"generated", "tm", "tm"})
		entries, err := h.requests[0].Terms().Entries()
		if err != nil || !slices.Contains(entries, preferred) {
			t.Fatalf("scoped request lacks preferred term: %v, %v", entries, err)
		}
		if !mismatch {
			t.Fatal("other scene has no scoped term mismatch diagnostic")
		}
	})

	t.Run("conflicting-terms", func(t *testing.T) {
		f := newNativeContextFixture(t)
		h := nativeContextEngine(t, false, "猫", nil)
		pack := f.pack(t, f.scope, nil)
		f.success(t, h, pack, f.scope, false, 2, nativeContextTexts)
		before := w3SQLiteRows(t, f.db)
		var catalog dictionary.Catalog
		term := nativeDictionaryEntry(f, "other-cat", "term", "cat", "犬", "inn", "keeper")
		importNativeDictionary(t, &catalog, term)
		reviewNativeDictionary(t, &catalog, "other-cat", dictionary.Review{
			Decision: dictionary.Accept, Mode: glossary.Mandatory, Match: glossary.Word,
		})
		pack.Dictionary = &catalog
		assertNativeDictionaryFailure(t, f, h, pack, before, "protect", glossary.ErrConflict)
	})
}

func TestNativeDictionaryRevalidation(t *testing.T) {
	// The fixture protects its TOKEN placeholder with a profile glossary rule.
	for _, tc := range []struct {
		name, candidate string
		termMode        glossary.Mode
		termTarget      string
		changedProfile  bool
	}{
		{name: "missing-protected-placeholder", candidate: "宿の辞書訳 猫"},
		{name: "missing-dictionary-protect", candidate: "宿の辞書訳 猫 TOKEN", termMode: glossary.Protect},
		{name: "missing-dictionary-mandatory", candidate: "宿の辞書訳 猫 TOKEN", termMode: glossary.Mandatory, termTarget: "歓迎"},
		{name: "changed-profile", candidate: "宿の辞書訳 猫 TOKEN", changedProfile: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNativeContextFixture(t)
			h := nativeContextEngine(t, false, "猫", nil)
			pack := f.pack(t, f.scope, nil)
			var catalog dictionary.Catalog
			entry := nativeDictionaryTranslation(f, 0, "inn-line", tc.candidate)
			if tc.termMode == "" {
				importNativeDictionary(t, &catalog, entry)
			} else {
				term := nativeDictionaryEntry(f, "term", "term", "Welcome", tc.termTarget, "inn", "keeper")
				importNativeDictionary(t, &catalog, entry, term)
				reviewNativeDictionary(t, &catalog, "term", dictionary.Review{
					Decision: dictionary.Accept, Mode: tc.termMode, Match: glossary.Word,
				})
			}
			reviewNativeDictionary(t, &catalog, "inn-line", dictionary.Review{Decision: dictionary.Accept})
			if tc.changedProfile {
				pack.Dictionary = &catalog
				want := maps.Clone(nativeContextTexts)
				want["1"] = tc.candidate
				f.success(t, h, pack, f.scope, false, 1, want)
				h = nativeContextEngine(t, false, "ねこ", nil)
			} else {
				f.success(t, h, pack, f.scope, false, 2, nativeContextTexts)
				pack.Dictionary = &catalog
			}
			before := w3SQLiteRows(t, f.db)
			observed := assertNativeDictionaryFailure(t, f, h, pack, before, "validate", validate.ErrInvalidCandidate)
			if observed.Validation != (ValidationCounts{Attempted: 1, Failed: 1}) {
				t.Fatalf("validation counts = %+v", observed.Validation)
			}
		})
	}
}

func assertNativeDictionaryFailure(t *testing.T, f nativeContextFixture, h *evaluationHarness, options ContextPackOptions, before []w3SQLiteRow, phase string, want error) *AcceptanceObservation {
	t.Helper()
	paths, observed, result, err := f.run(t, h, options, f.scope, false)
	var failure *Error
	if !errors.Is(err, want) || !errors.As(err, &failure) || failure.Phase != phase || failure.UnitID.StableID() != "1" ||
		result.TMCommitted || result.Publication.State != publication.NotPublished ||
		len(h.requests) != 0 || observed.FinalValidated || len(observed.Units) != 0 {
		t.Fatalf("result=%+v error=%v Generate=%d observed=%+v", result, err, len(h.requests), observed)
	}
	w3SQLiteAssertNoOutput(t, paths.Output)
	w3SQLiteAssertRows(t, f.db, before)
	return observed
}
