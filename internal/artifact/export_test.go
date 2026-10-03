package artifact_test

import (
	"errors"
	"testing"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func exportFixture(t *testing.T, sources ...string) (unit.Session, []unit.UnitID, validate.Profile, []validate.AcceptedTranslation) {
	t.Helper()
	units := make([]unit.TranslationUnit, len(sources))
	ids := make([]unit.UnitID, len(sources))
	for i, source := range sources {
		id, err := unit.NewUnitID("fixture", "v1", string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
		units[i], err = unit.NewTranslationUnit(id, []byte(source), "en", "ja", nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	session, err := unit.NewSession([]byte("input artifact"), units)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := validate.NewProfile([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	accepted := make([]validate.AcceptedTranslation, len(sources))
	for i, id := range ids {
		candidate := "訳文" + string(rune('a'+i))
		if sources[i] == "" {
			candidate = ""
		}
		accepted[i], err = validate.Validate(session, id, profile, candidate)
		if err != nil {
			t.Fatal(err)
		}
	}
	return session, ids, profile, accepted
}

func TestPrepareExportComplete(t *testing.T) {
	session, ids, profile, accepted := exportFixture(t, "first", "second")
	accepted[0], accepted[1] = accepted[1], accepted[0]
	expected, err := artifact.PrepareExport(session, profile, accepted)
	if err != nil {
		t.Fatal(err)
	}
	accepted[0] = validate.AcceptedTranslation{}
	if expected.Len() != 2 {
		t.Fatalf("expected count = %d", expected.Len())
	}
	for i, id := range ids {
		text, exists := expected.Text(id)
		if !exists || text != "訳文"+string(rune('a'+i)) {
			t.Fatalf("ID %s text = %q, exists = %v", id.StableID(), text, exists)
		}
	}
	if _, exists := expected.Text(unit.UnitID{}); exists {
		t.Fatal("unknown ID present")
	}
}

func TestPrepareExportRejectsMismatch(t *testing.T) {
	session, _, profile, accepted := exportFixture(t, "first", "second")
	_, _, _, otherAccepted := exportFixture(t, "first", "second", "third")
	changedSource, _, _, _ := exportFixture(t, "changed", "second")
	otherProfile, err := validate.NewProfile([32]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name     string
		session  unit.Session
		profile  validate.Profile
		accepted []validate.AcceptedTranslation
		want     error
	}{
		{"missing", session, profile, accepted[:1], artifact.ErrIncompleteExport},
		{"duplicate", session, profile, []validate.AcceptedTranslation{accepted[0], accepted[0]}, artifact.ErrDuplicateTranslation},
		{"extra unknown unit", session, profile, append(append([]validate.AcceptedTranslation{}, accepted...), otherAccepted[2]), unit.ErrUnknownUnit},
		{"zero accepted", session, profile, []validate.AcceptedTranslation{{}, accepted[1]}, unit.ErrUnknownUnit},
		{"different session", session, profile, otherAccepted[:2], validate.ErrBindingMismatch},
		{"different source", changedSource, profile, accepted, validate.ErrBindingMismatch},
		{"different profile", session, otherProfile, accepted, validate.ErrBindingMismatch},
		{"zero profile", session, validate.Profile{}, accepted, validate.ErrInvalidProfile},
		{"zero session", unit.Session{}, profile, accepted, unit.ErrInvalidSession},
	} {
		t.Run(tt.name, func(t *testing.T) {
			expected, err := artifact.PrepareExport(tt.session, tt.profile, tt.accepted)
			if !errors.Is(err, tt.want) || expected.Len() != 0 {
				t.Fatalf("count = %d, error = %v", expected.Len(), err)
			}
			if _, exists := expected.Text(accepted[0].UnitID()); exists {
				t.Fatal("failed export exposed a usable subset")
			}
		})
	}
}
