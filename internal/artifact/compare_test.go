package artifact_test

import (
	"errors"
	"testing"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func comparisonFixture(t *testing.T) (artifact.Manifest, artifact.ExpectedTranslations, artifact.FinalObservation) {
	t.Helper()
	session, ids, profile, accepted := exportFixture(t, "first", "second")
	expected, err := artifact.PrepareExport(session, profile, accepted)
	if err != nil {
		t.Fatal(err)
	}
	keys := []artifact.KeyRelation{{UnitID: ids[0], Key: "key"}, {UnitID: ids[0], Key: "key"}, {UnitID: ids[0], Key: "alias"}}
	manifest, err := artifact.NewManifest(session, profile, "fixture", "v1", keys, map[string][]byte{"header": {1, 2}}, map[string][]byte{"offset": {20}, "language": {3}})
	if err != nil {
		t.Fatal(err)
	}
	final := artifact.FinalObservation{
		Adapter: "fixture", FormatSchema: "v1",
		Units: []artifact.ObservedUnit{
			{ID: ids[0], Text: "訳文a", TargetLanguage: "ja"},
			{ID: ids[1], Text: "訳文b", TargetLanguage: "ja"},
		},
		Keys:     keys,
		Metadata: map[string][]byte{"header": {1, 2}, "offset": {20}, "language": {3}},
	}
	return manifest, expected, final
}

func TestCompareFinalExpectedChanges(t *testing.T) {
	manifest, expected, final := comparisonFixture(t)
	final.Units[0], final.Units[1] = final.Units[1], final.Units[0]
	final.Keys[0], final.Keys[2] = final.Keys[2], final.Keys[0]
	if err := artifact.CompareFinal(manifest, expected, final); err != nil {
		t.Fatalf("exact translated content, keyless unit, key multiplicity and recalculated metadata rejected: %v", err)
	}
}

func TestCompareFinalRejectsMutation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*artifact.FinalObservation)
	}{
		{"missing unit", func(f *artifact.FinalObservation) { f.Units = f.Units[:1] }},
		{"duplicate unit", func(f *artifact.FinalObservation) { f.Units[1] = f.Units[0] }},
		{"extra unit", func(f *artifact.FinalObservation) { f.Units = append(f.Units, f.Units[0]) }},
		{"unknown unit same count", func(f *artifact.FinalObservation) { f.Units[1].ID = unit.UnitID{} }},
		{"swapped texts", func(f *artifact.FinalObservation) {
			f.Units[0].Text, f.Units[1].Text = f.Units[1].Text, f.Units[0].Text
		}},
		{"wrong target language", func(f *artifact.FinalObservation) { f.Units[0].TargetLanguage = "en" }},
		{"lost key multiplicity", func(f *artifact.FinalObservation) { f.Keys = f.Keys[1:] }},
		{"extra key multiplicity", func(f *artifact.FinalObservation) { f.Keys = append(f.Keys, f.Keys[0]) }},
		{"key reassigned to other unit", func(f *artifact.FinalObservation) { f.Keys[0].UnitID = f.Units[1].ID }},
		{"changed key", func(f *artifact.FinalObservation) { f.Keys[0].Key = "changed" }},
		{"wrong adapter", func(f *artifact.FinalObservation) { f.Adapter = "other" }},
		{"wrong schema", func(f *artifact.FinalObservation) { f.FormatSchema = "v2" }},
		{"preserved bytes", func(f *artifact.FinalObservation) { f.Metadata["header"][0] = 9 }},
		{"wrong recalculated metadata", func(f *artifact.FinalObservation) { f.Metadata["offset"][0] = 21 }},
		{"wrong language metadata", func(f *artifact.FinalObservation) { f.Metadata["language"][0] = 4 }},
		{"unknown metadata addition", func(f *artifact.FinalObservation) { f.Metadata["unknown"] = []byte{1} }},
		{"metadata missing", func(f *artifact.FinalObservation) { delete(f.Metadata, "header") }},
		{"unknown metadata replaces known", func(f *artifact.FinalObservation) { delete(f.Metadata, "header"); f.Metadata["unknown"] = []byte{1, 2} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manifest, expected, final := comparisonFixture(t)
			tt.mutate(&final)
			if err := artifact.CompareFinal(manifest, expected, final); !errors.Is(err, artifact.ErrFinalMismatch) {
				t.Fatalf("mutation accepted: %v", err)
			}
		})
	}
}

func TestCompareFinalRejectsUnboundExpectations(t *testing.T) {
	session, ids, profile, accepted := exportFixture(t, "first", "second")
	manifest, err := artifact.NewManifest(session, profile, "fixture", "v1", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := artifact.PrepareExport(session, profile, accepted)
	if err != nil {
		t.Fatal(err)
	}
	final := artifact.FinalObservation{
		Adapter: "fixture", FormatSchema: "v1",
		Units: []artifact.ObservedUnit{{ID: ids[0], Text: "訳文a", TargetLanguage: "ja"}, {ID: ids[1], Text: "訳文b", TargetLanguage: "ja"}},
	}
	otherSession, _, _, otherAccepted := exportFixture(t, "first", "second")
	otherExpected, err := artifact.PrepareExport(otherSession, profile, otherAccepted)
	if err != nil {
		t.Fatal(err)
	}
	otherProfile, err := validate.NewProfile([32]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		accepted[i], err = validate.Validate(session, id, otherProfile, "訳文"+string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
	}
	otherProfileExpected, err := artifact.PrepareExport(session, otherProfile, accepted)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name     string
		manifest artifact.Manifest
		expected artifact.ExpectedTranslations
		want     error
	}{
		{"zero manifest", artifact.Manifest{}, expected, artifact.ErrInvalidManifest},
		{"zero expected", manifest, artifact.ExpectedTranslations{}, artifact.ErrInvalidExpected},
		{"other import expected", manifest, otherExpected, artifact.ErrInvalidExpected},
		{"other profile expected", manifest, otherProfileExpected, artifact.ErrInvalidExpected},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := artifact.CompareFinal(tt.manifest, tt.expected, final); !errors.Is(err, tt.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCompareFinalSegmentationProfileIdentity(t *testing.T) {
	session, ids, legacy, _ := exportFixture(t, "source")
	input := validate.SegmentationInput{Schema: "safe-boundaries-v1", BoundaryFixtures: "safe-boundaries-fixtures-v1"}
	profile, err := validate.NewProfileWithSegmentation(legacy.Digest(), input)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := validate.Validate(session, ids[0], profile, "訳文")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := artifact.PrepareExport(session, profile, []validate.AcceptedTranslation{accepted})
	if err != nil {
		t.Fatal(err)
	}
	final := artifact.FinalObservation{
		Adapter: "fixture", FormatSchema: "v1",
		Units: []artifact.ObservedUnit{{ID: ids[0], Text: "訳文", TargetLanguage: "ja"}},
	}
	for _, tt := range []struct {
		name  string
		input validate.SegmentationInput
		want  error
	}{
		{"matching", input, nil},
		{"legacy", validate.SegmentationInput{}, artifact.ErrInvalidExpected},
		{"schema", validate.SegmentationInput{Schema: "other", BoundaryFixtures: input.BoundaryFixtures}, artifact.ErrInvalidExpected},
		{"fixtures", validate.SegmentationInput{Schema: input.Schema, BoundaryFixtures: "other"}, artifact.ErrInvalidExpected},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manifestProfile := legacy
			if tt.input != (validate.SegmentationInput{}) {
				manifestProfile, err = validate.NewProfileWithSegmentation(legacy.Digest(), tt.input)
				if err != nil {
					t.Fatal(err)
				}
			}
			manifest, err := artifact.NewManifest(session, manifestProfile, "fixture", "v1", nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := artifact.CompareFinal(manifest, expected, final); !errors.Is(err, tt.want) {
				t.Fatalf("error=%v want=%v", err, tt.want)
			}
		})
	}
}
