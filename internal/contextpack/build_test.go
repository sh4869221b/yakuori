package contextpack

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/unit"
)

var fixtureScope = BuildScope{
	GameNamespace: "sample-game", ArtifactNamespace: "dialogue",
	SourceLanguage: "en", TargetLanguage: "ja",
}

type buildRecord struct {
	ID, Text string
	Origin   map[string]string
}

// These records stand in for structured metadata emitted by an adapter.
func buildFixture(t *testing.T, records []buildRecord) unit.Session {
	t.Helper()
	raw, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	units := make([]unit.TranslationUnit, len(records))
	for i, record := range records {
		units[i] = newBuildUnit(t, "fixture", "v1", record, "ja")
	}
	session, err := unit.NewSession(raw, units)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func newBuildUnit(t *testing.T, adapter, schema string, record buildRecord, target string) unit.TranslationUnit {
	t.Helper()
	id, err := unit.NewUnitID(adapter, schema, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	u, err := unit.NewTranslationUnit(id, []byte(record.Text), "en", target, record.Origin)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestBuildUsesExplicitMetadata(t *testing.T) {
	records := []buildRecord{
		{"inn-2", "Follow me.", map[string]string{"speaker": "keeper", "scene": "inn", "previous_unit": "inn-1"}},
		{"gate-1", "Welcome.", map[string]string{"speaker": "guard", "scene": "gate"}},
		{"inn-3", "A visitor arrives.", map[string]string{"scene": "inn"}},
		{"unknown", "Welcome.", map[string]string{"dialogue_index": "1"}},
		{"inn-1", "Welcome.", map[string]string{"speaker": "keeper", "scene": "inn", "quest": "visit-inn", "world": "The inn opens at dusk."}},
	}
	session := buildFixture(t, records)
	doc, err := Build(session, fixtureScope)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := Read(bytes.NewReader(doc.Canonical))
	if err != nil || !bytes.Equal(validated.Canonical, doc.Canonical) || validated.Fingerprint != doc.Fingerprint {
		t.Fatalf("candidate validation differs: %v", err)
	}
	pack := doc.Pack
	artifactDigest := session.ArtifactDigest()
	if pack.Scope.GameNamespace != "sample-game" || pack.SourceLanguage != "en" || pack.TargetLanguage != "ja" ||
		!slices.Equal(pack.Scope.Artifacts, []Artifact{{"dialogue", hex.EncodeToString(artifactDigest[:])}}) {
		t.Fatalf("wrong candidate scope: %+v", pack.Scope)
	}
	units := unitsByStableID(pack.Units)
	if len(units) != len(records) || len(pack.Characters) != 2 || len(pack.Scenes) != 2 || len(pack.World) != 1 {
		t.Fatalf("wrong candidate counts: %+v", pack)
	}
	inn, next, gate, silent, unknown := units["inn-1"], units["inn-2"], units["gate-1"], units["inn-3"], units["unknown"]
	if inn.RawSourceDigest != gate.RawSourceDigest || inn.RawSourceDigest != unknown.RawSourceDigest || inn.ID == gate.ID ||
		value(inn.SceneID) != "scene/dialogue/fixture/v1/inn" || value(gate.SceneID) != "scene/dialogue/fixture/v1/gate" ||
		value(inn.SpeakerID) != "character/dialogue/fixture/v1/keeper" || value(gate.SpeakerID) != "character/dialogue/fixture/v1/guard" {
		t.Fatal("same source merged distinct unit, scene or speaker identities")
	}
	if value(next.SceneID) != value(inn.SceneID) || value(next.PreviousUnitID) != inn.ID || inn.PreviousUnitID != nil || gate.PreviousUnitID != nil ||
		value(silent.SceneID) != value(inn.SceneID) || silent.SpeakerID != nil || silent.PreviousUnitID != nil || len(silent.Tags) != 0 ||
		unknown.SceneID != nil || unknown.SpeakerID != nil || unknown.PreviousUnitID != nil || len(unknown.Tags) != 0 ||
		!slices.Equal(inn.Tags, []string{"quest:visit-inn"}) || len(gate.Tags) != 0 || len(next.Tags) != 0 {
		t.Fatal("explicit or missing metadata produced incorrect unit links")
	}
	for _, u := range pack.Units {
		if u.ArtifactNamespace != "dialogue" || u.Adapter != "fixture" || u.FormatSchema != "v1" {
			t.Fatalf("unit lost identity or scope: %+v", u)
		}
	}
	artifact := "dialogue"
	artifactSource := Source{Kind: "artifact", ArtifactNamespace: &artifact}
	metadata := func(keys ...string) []Source {
		sources := make([]Source, 0, len(keys))
		for _, key := range keys {
			locator := "units/fixture/v1/" + key
			sources = append(sources, Source{Kind: "metadata", ArtifactNamespace: &artifact, MetadataKey: &locator})
		}
		return sources
	}
	assertProvenance := func(p Provenance, sources []Source) {
		t.Helper()
		if p.Method != "extraction" || p.Version != "context-build-v1" || p.Status != "candidate" || p.UserEdited == nil || *p.UserEdited ||
			!reflect.DeepEqual(p.Sources, sources) {
			t.Fatalf("wrong candidate provenance: %+v, source keys: %v", p, p.Sources)
		}
	}
	assertProvenance(inn.Provenance, append([]Source{artifactSource}, metadata("inn-1/origin/quest", "inn-1/origin/scene", "inn-1/origin/speaker", "inn-1/origin/world")...))
	assertProvenance(next.Provenance, append([]Source{artifactSource}, metadata("inn-2/origin/previous_unit", "inn-2/origin/scene", "inn-2/origin/speaker")...))
	assertProvenance(unknown.Provenance, []Source{artifactSource})
	keeper := pack.Characters[1]
	if keeper.ID != value(inn.SpeakerID) || keeper.Name != nil || keeper.Description != nil || !slices.Equal(keeper.Artifacts, []string{artifact}) {
		t.Fatalf("wrong character candidate: %+v", keeper)
	}
	assertProvenance(keeper.Provenance, metadata("inn-1/origin/speaker", "inn-2/origin/speaker"))
	innScene := pack.Scenes[1]
	if innScene.ID != value(inn.SceneID) || innScene.Summary != nil || !slices.Equal(innScene.SpeakerIDs, []string{keeper.ID}) ||
		!slices.Equal(innScene.Artifacts, []string{artifact}) {
		t.Fatalf("wrong scene candidate: %+v", innScene)
	}
	assertProvenance(innScene.Provenance, metadata("inn-1/origin/scene", "inn-1/origin/speaker", "inn-2/origin/scene", "inn-2/origin/speaker", "inn-3/origin/scene"))
	world := pack.World[0]
	if world.Text != "The inn opens at dusk." || !slices.Equal(world.Artifacts, []string{artifact}) {
		t.Fatalf("wrong world candidate: %+v", world)
	}
	assertProvenance(world.Provenance, metadata("inn-1/origin/world"))

	// Reorder the translated units without changing the source artifact snapshot.
	ordered := session.Units()
	slices.Reverse(ordered)
	reordered, err := unit.NewSession(session.Artifact(), ordered)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Build(reordered, fixtureScope)
	if err != nil || !bytes.Equal(again.Canonical, doc.Canonical) || again.Fingerprint != doc.Fingerprint {
		t.Fatalf("unit iteration order affected candidate: %v", err)
	}
}

func TestBuildUpdatedSnapshot(t *testing.T) {
	records := []buildRecord{
		{"changed", "Welcome.", map[string]string{"speaker": "guard", "scene": "gate"}},
		{"unchanged", "Follow me.", map[string]string{"speaker": "keeper", "scene": "inn"}},
		{"unrelated", "Welcome.", nil},
	}
	build := func(records []buildRecord) *Document {
		t.Helper()
		doc, err := Build(buildFixture(t, records), fixtureScope)
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	original := build(records)
	for _, tt := range []struct {
		name    string
		record  buildRecord
		wantRaw bool
	}{
		{"source only", buildRecord{"changed", "Good evening.", records[0].Origin}, true},
		{"metadata only", buildRecord{"changed", records[0].Text, map[string]string{"speaker": "guard", "scene": "courtyard"}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			updatedRecords := slices.Clone(records)
			updatedRecords[0] = tt.record
			updated := build(updatedRecords)
			if updated.Fingerprint == original.Fingerprint || updated.Pack.Scope.Artifacts[0].OriginalArtifactDigest == original.Pack.Scope.Artifacts[0].OriginalArtifactDigest {
				t.Fatal("candidate kept the original artifact identity")
			}
			before, after := unitsByStableID(original.Pack.Units), unitsByStableID(updated.Pack.Units)
			if !reflect.DeepEqual(before["unchanged"], after["unchanged"]) || !reflect.DeepEqual(before["unrelated"], after["unrelated"]) {
				t.Fatal("update changed unrelated unit references")
			}
			want, got := before["changed"], after["changed"]
			if tt.wantRaw {
				if got.RawSourceDigest == want.RawSourceDigest {
					t.Fatal("updated source retained the old digest")
				}
				want.RawSourceDigest = got.RawSourceDigest
			} else {
				scene := "scene/dialogue/fixture/v1/courtyard"
				want.SceneID = &scene
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("update changed unexpected unit fields: %+v", got)
			}
			again := build(updatedRecords)
			if !bytes.Equal(again.Canonical, updated.Canonical) || again.Fingerprint != updated.Fingerprint {
				t.Fatal("updated candidate is not deterministic")
			}
		})
	}
}

func unitsByStableID(units []Unit) map[string]Unit {
	indexed := make(map[string]Unit, len(units))
	for _, u := range units {
		indexed[u.StableID] = u
	}
	return indexed
}

func TestBuildRejectsInvalidIdentityAndMetadata(t *testing.T) {
	for _, tt := range []struct {
		name    string
		records []buildRecord
		want    string
	}{
		{"blank stable ID", []buildRecord{{" \t", "", nil}}, "invalid stable unit identity"},
		{"NUL stable ID", []buildRecord{{"secret\x00", "", nil}}, "invalid stable unit identity"},
		{"invalid UTF-8 stable ID", []buildRecord{{"secret\xff", "", nil}}, "invalid stable unit identity"},
		{"blank speaker", []buildRecord{{"secret", "", map[string]string{"speaker": " "}}}, "invalid origin metadata for speaker"},
		{"NUL scene", []buildRecord{{"secret", "", map[string]string{"scene": "secret\x00"}}}, "invalid origin metadata for scene"},
		{"invalid UTF-8 quest", []buildRecord{{"secret", "", map[string]string{"quest": "secret\xff"}}}, "invalid origin metadata for quest"},
		{"unresolved previous", []buildRecord{{"secret", "", map[string]string{"previous_unit": "secret-missing"}}}, "unresolved previous unit reference"},
		{"cross-scene previous", []buildRecord{{"secret", "", map[string]string{"scene": "a", "previous_unit": "b"}}, {"b", "", map[string]string{"scene": "b"}}}, "previous unit crosses artifact or scene scope"},
		{"previous cycle", []buildRecord{{"secret", "", map[string]string{"previous_unit": "b"}}, {"b", "", map[string]string{"previous_unit": "secret"}}}, "previous unit cycle"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := Build(buildFixture(t, tt.records), fixtureScope)
			if doc != nil || err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unexpected build result: doc=%v err=%v", doc, err)
			}
		})
	}
	mismatch, err := unit.NewSession(nil, []unit.TranslationUnit{newBuildUnit(t, "fixture", "v1", buildRecord{ID: "a"}, "de")})
	if err != nil {
		t.Fatal(err)
	}
	if doc, err := Build(mismatch, fixtureScope); doc != nil || err == nil || !strings.Contains(err.Error(), "unit language does not match build scope") {
		t.Fatalf("unexpected language mismatch result: doc=%v err=%v", doc, err)
	}
}

func TestBuildSeparatesEscapedIDsAndAdapterScopes(t *testing.T) {
	units := []unit.TranslationUnit{
		newBuildUnit(t, "adapter/a", "v1", buildRecord{"x/y", "", map[string]string{"scene": "s/t"}}, "ja"),
		newBuildUnit(t, "adapter/a", "v1", buildRecord{"x%2Fy", "", map[string]string{"scene": "s%2Ft"}}, "ja"),
		newBuildUnit(t, "adapter", "a/v1", buildRecord{"x/y", "", map[string]string{"scene": "s/t"}}, "ja"),
	}
	session, err := unit.NewSession(nil, units)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Build(session, fixtureScope)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"unit/dialogue/adapter%2Fa/v1/x%252Fy",
		"unit/dialogue/adapter%2Fa/v1/x%2Fy",
		"unit/dialogue/adapter/a%2Fv1/x%2Fy",
	}
	if len(doc.Pack.Scenes) != len(want) || len(doc.Pack.Units) != len(want) {
		t.Fatalf("identities combined across escaped IDs or adapter scopes: %+v", doc.Pack)
	}
	for i, u := range doc.Pack.Units {
		if u.ID != want[i] {
			t.Fatalf("unit ID %q, want %q", u.ID, want[i])
		}
	}
}
