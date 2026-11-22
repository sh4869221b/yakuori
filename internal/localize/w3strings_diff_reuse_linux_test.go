package localize

import (
	"bytes"
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func TestW3StringsDifferentialReuse(t *testing.T) {
	gate := w3SQLiteEntry{id: 1, text: "Open gate", keys: []uint32{0x10203040}}
	torch := w3SQLiteEntry{id: 2, text: "Light torch", keys: []uint32{0x50607080}}
	chest := w3SQLiteEntry{id: 3, text: "Close chest"}
	baseline := []w3SQLiteEntry{gate, torch, chest}
	baseRaw := w3SQLiteArtifactBytes(baseline)
	baseScope := tm.Scope{GameNamespace: w3SQLiteGame, ArtifactNamespace: w3SQLiteArtifact}
	calls := 0
	_, profile := canonicalCoreFixture(t, false, "", &calls)
	oldSession, err := w3strings.New().Import(baseRaw, profile)
	if err != nil {
		t.Fatal(err)
	}

	unchanged := map[string]string{"1": "Unchanged", "2": "Unchanged", "3": "Unchanged"}
	for _, tc := range []struct {
		name              string
		entries           []w3SQLiteEntry
		classes           map[string]string
		artifactNamespace string
		sameBytes         bool
		wantHits          bool
	}{
		{
			name: "identical", entries: baseline, classes: unchanged, sameBytes: true, wantHits: true,
		},
		{
			name:    "modified",
			entries: []w3SQLiteEntry{gate, {id: torch.id, text: "Raise torch", keys: torch.keys}, chest},
			classes: map[string]string{"1": "Unchanged", "2": "Modified", "3": "Unchanged"},
		},
		{
			name:    "added",
			entries: []w3SQLiteEntry{gate, torch, chest, {id: 4, text: "Read note"}},
			classes: map[string]string{"1": "Unchanged", "2": "Unchanged", "3": "Unchanged", "4": "Added"},
		},
		{
			name:    "removed",
			entries: []w3SQLiteEntry{gate, torch},
			classes: map[string]string{"1": "Unchanged", "2": "Unchanged", "3": "Removed"},
		},
		{
			name: "table-reorder", entries: []w3SQLiteEntry{chest, gate, torch}, classes: unchanged,
		},
		{
			name:    "key-metadata",
			entries: []w3SQLiteEntry{gate, {id: torch.id, text: torch.text, keys: []uint32{0x50607081}}, chest},
			classes: unchanged,
		},
		{
			name: "artifact-namespace", entries: baseline, classes: unchanged,
			artifactNamespace: "moved-w3", sameBytes: true,
		},
		{
			name:    "same-source-new-id",
			entries: []w3SQLiteEntry{gate, torch, {id: 4, text: chest.text}},
			classes: map[string]string{"1": "Unchanged", "2": "Unchanged", "3": "Removed", "4": "Added"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := w3SQLiteArtifactBytes(tc.entries)
			currentSession, err := w3strings.New().Import(raw, profile)
			if err != nil {
				t.Fatal(err)
			}
			if same := oldSession.ArtifactDigest() == currentSession.ArtifactDigest(); same != tc.sameBytes {
				t.Fatalf("equal artifact digests = %t, want %t", same, tc.sameBytes)
			}
			classes := w3RevisionClasses(oldSession, currentSession)
			if !maps.Equal(classes, tc.classes) {
				t.Fatalf("revision classes = %v, want %v", classes, tc.classes)
			}

			// Seed only the old revision through the same pipeline used for the new one.
			store, db := tmStoreFixture(t)
			run := func(raw []byte, scope tm.Scope, label string) ([]string, artifact.FinalObservation, ValidationCounts) {
				t.Helper()
				var generated []string
				observation := &AcceptanceObservation{}
				core := NewCore(fakeEngine(func(_ context.Context, id unit.UnitID, text string) (Generation, error) {
					generated = append(generated, id.StableID())
					return Generation{Text: label + id.StableID() + " " + text, Finish: Stop}, nil
				})).WithAcceptanceObservation(observation)
				adapter := w3strings.New()
				var output bytes.Buffer
				result, err := core.TextWithTM(t.Context(), raw, adapter, profile, store, scope, &output)
				if err != nil || !result.TMCommitted {
					t.Fatalf("localize result = %+v, error = %v", result, err)
				}
				observed, err := adapter.Observe(&output)
				if err != nil {
					t.Fatal(err)
				}
				return generated, observed, observation.TMValidation
			}
			run(baseRaw, baseScope, "旧訳")
			if rows := tmRowCount(t, db); rows != len(baseline) {
				t.Fatalf("baseline TM rows = %d, want %d", rows, len(baseline))
			}

			scope := baseScope
			if tc.artifactNamespace != "" {
				scope.ArtifactNamespace = tc.artifactNamespace
			}
			generated, observed, validation := run(raw, scope, "新訳")
			if len(observed.Units) != len(tc.entries) {
				t.Fatalf("output units = %v, want %d units", observed.Units, len(tc.entries))
			}
			var wantGenerated []string
			var wantKeys []artifact.KeyRelation
			label := "新訳"
			wantValidation := ValidationCounts{}
			if tc.wantHits {
				label = "旧訳"
				wantValidation = ValidationCounts{Attempted: len(tc.entries), Passed: len(tc.entries)}
			}
			for i, entry := range tc.entries {
				id := w3SQLiteStable(entry.id)
				if !tc.wantHits {
					wantGenerated = append(wantGenerated, id)
				}
				got := observed.Units[i]
				if want := label + id + " " + entry.text; got.ID.StableID() != id || got.Text != want || got.TargetLanguage != "ja" {
					t.Fatalf("output unit %d = %v, want ID %s with text %q in ja", i, got, id, want)
				}
				for _, key := range w3SQLiteKeysBytes(entry) {
					wantKeys = append(wantKeys, artifact.KeyRelation{UnitID: got.ID, Key: string(key)})
				}
			}
			if !slices.Equal(observed.Keys, wantKeys) {
				t.Fatalf("output key relations = %v, want %v", observed.Keys, wantKeys)
			}
			if !slices.Equal(generated, wantGenerated) || validation != wantValidation {
				t.Fatalf("generated IDs = %v, TM validation = %+v, want %v and %+v", generated, validation, wantGenerated, wantValidation)
			}
			if rows, want := tmRowCount(t, db), len(baseline)+len(wantGenerated); rows != want {
				t.Fatalf("TM rows after revision = %d, want %d", rows, want)
			}
			t.Logf("classes=%v artifact_digest_equal=%t namespace_equal=%t generated=%v revalidated=%d",
				classes, tc.sameBytes, scope.ArtifactNamespace == baseScope.ArtifactNamespace, generated, validation.Passed)
		})
	}
}

func w3RevisionClasses(old, current unit.Session) map[string]string {
	previous := make(map[unit.UnitID]unit.TranslationUnit, old.UnitCount())
	for _, u := range old.Units() {
		previous[u.ID()] = u
	}
	classes := make(map[string]string)
	for _, u := range current.Units() {
		id := u.ID()
		prior, exists := previous[id]
		switch {
		case !exists:
			classes[id.StableID()] = "Added"
		case prior.SourceDigest() != u.SourceDigest():
			classes[id.StableID()] = "Modified"
		default:
			classes[id.StableID()] = "Unchanged"
		}
		delete(previous, id)
	}
	for id := range previous {
		classes[id.StableID()] = "Removed"
	}
	return classes
}
