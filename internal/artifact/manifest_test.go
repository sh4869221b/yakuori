package artifact_test

import (
	"errors"
	"testing"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func TestManifestSnapshot(t *testing.T) {
	id, err := unit.NewUnitID("fixture", "v1", "a")
	if err != nil {
		t.Fatal(err)
	}
	source := []byte("source")
	u, err := unit.NewTranslationUnit(id, source, "en", "ja", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("input artifact")
	session, err := unit.NewSession(raw, []unit.TranslationUnit{u})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := validate.NewProfile([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := validate.Validate(session, id, profile, "訳文")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := artifact.PrepareExport(session, profile, []validate.AcceptedTranslation{accepted})
	if err != nil {
		t.Fatal(err)
	}
	keys := []artifact.KeyRelation{{UnitID: id, Key: "key"}, {UnitID: id, Key: "key"}}
	preserved := map[string][]byte{"header": {1, 2}}
	metadata := map[string][]byte{"offset": {3, 4}}
	manifest, err := artifact.NewManifest(session, profile, "fixture", "v1", keys, preserved, metadata)
	if err != nil {
		t.Fatal(err)
	}
	source[0] = 'X'
	raw[0] = 'X'
	keys[0].Key = "changed"
	preserved["header"][0] = 9
	preserved["extra"] = []byte{9}
	metadata["offset"][0] = 9
	delete(metadata, "offset")
	if err := manifest.CheckInput(session); err != nil {
		t.Fatal(err)
	}
	final := artifact.FinalObservation{
		Adapter: "fixture", FormatSchema: "v1",
		Units:    []artifact.ObservedUnit{{ID: id, Text: "訳文", TargetLanguage: "ja"}},
		Keys:     []artifact.KeyRelation{{UnitID: id, Key: "key"}, {UnitID: id, Key: "key"}},
		Metadata: map[string][]byte{"header": {1, 2}, "offset": {3, 4}},
	}
	if err := artifact.CompareFinal(manifest, expected, final); err != nil {
		t.Fatalf("snapshot changed: %v", err)
	}
}

func TestManifestRejectsDifferentSource(t *testing.T) {
	session, ids, profile, _ := exportFixture(t, "source")
	manifest, err := artifact.NewManifest(session, profile, "fixture", "v1", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, raw, source, adapter, schema, sourceLanguage, targetLanguage string
	}{
		{"artifact only", "different artifact", "source", "fixture", "v1", "en", "ja"},
		{"source only", "input artifact", "changed source", "fixture", "v1", "en", "ja"},
		{"schema only", "input artifact", "source", "fixture", "v2", "en", "ja"},
		{"adapter only", "input artifact", "source", "other", "v1", "en", "ja"},
		{"source language", "input artifact", "source", "fixture", "v1", "de", "ja"},
		{"target language", "input artifact", "source", "fixture", "v1", "en", "fr"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id, err := unit.NewUnitID(tt.adapter, tt.schema, ids[0].StableID())
			if err != nil {
				t.Fatal(err)
			}
			u, err := unit.NewTranslationUnit(id, []byte(tt.source), tt.sourceLanguage, tt.targetLanguage, nil)
			if err != nil {
				t.Fatal(err)
			}
			input, err := unit.NewSession([]byte(tt.raw), []unit.TranslationUnit{u})
			if err != nil {
				t.Fatal(err)
			}
			if err := manifest.CheckInput(input); !errors.Is(err, artifact.ErrInputMismatch) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	missing, err := unit.NewSession([]byte("input artifact"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manifest.CheckInput(missing); !errors.Is(err, artifact.ErrInputMismatch) {
		t.Fatalf("missing input error = %v", err)
	}
	if err := manifest.CheckInput(unit.Session{}); !errors.Is(err, unit.ErrInvalidSession) {
		t.Fatalf("zero input error = %v", err)
	}
	if err := (artifact.Manifest{}).CheckInput(session); !errors.Is(err, artifact.ErrInvalidManifest) {
		t.Fatalf("zero manifest error = %v", err)
	}
}

func TestManifestRejectsInvalidDefinition(t *testing.T) {
	session, _, profile, _ := exportFixture(t, "source")
	for _, tt := range []struct {
		name                string
		session             unit.Session
		profile             validate.Profile
		adapter, schema     string
		keys                []artifact.KeyRelation
		preserved, expected map[string][]byte
		want                error
	}{
		{"zero session", unit.Session{}, profile, "fixture", "v1", nil, nil, nil, unit.ErrInvalidSession},
		{"zero profile", session, validate.Profile{}, "fixture", "v1", nil, nil, nil, validate.ErrInvalidProfile},
		{"schema disagreement", session, profile, "fixture", "v2", nil, nil, nil, artifact.ErrInvalidManifest},
		{"missing schema", session, profile, "fixture", "", nil, nil, nil, artifact.ErrInvalidManifest},
		{"unknown key unit", session, profile, "fixture", "v1", []artifact.KeyRelation{{Key: "key"}}, nil, nil, artifact.ErrInvalidManifest},
		{"preserved metadata override", session, profile, "fixture", "v1", nil, map[string][]byte{"field": {1}}, map[string][]byte{"field": {2}}, artifact.ErrInvalidManifest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := artifact.NewManifest(tt.session, tt.profile, tt.adapter, tt.schema, tt.keys, tt.preserved, tt.expected)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
