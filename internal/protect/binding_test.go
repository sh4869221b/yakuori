package protect_test

import (
	"errors"
	"testing"

	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func TestProtectionRejectsInvalidBinding(t *testing.T) {
	session, id := fixtureSession(t, "Hello {p}", []unit.ProtectionSpan{{Start: 6, End: 9, Kind: unit.Placeholder}})
	prepared, err := protect.Prepare(session, id)
	if err != nil {
		t.Fatal(err)
	}
	foreign, foreignID := fixtureSession(t, "Changed {p}", []unit.ProtectionSpan{{Start: 8, End: 11, Kind: unit.Placeholder}})
	identicalImport, _ := fixtureSession(t, "Hello {p}", []unit.ProtectionSpan{{Start: 6, End: 9, Kind: unit.Placeholder}})
	otherID, err := unit.NewUnitID("fixture", "v1", "other")
	if err != nil {
		t.Fatal(err)
	}
	otherUnit, err := unit.NewTranslationUnit(otherID, []byte("other"), "en", "ja", nil)
	if err != nil {
		t.Fatal(err)
	}
	sameImport, err := unit.NewSession(nil, append(session.Units(), otherUnit))
	if err != nil {
		t.Fatal(err)
	}
	samePrepared, err := protect.Prepare(sameImport, id)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		prepared protect.Prepared
		session  unit.Session
		id       unit.UnitID
	}{
		{"zero prepared", protect.Prepared{}, session, id}, {"zero session", prepared, unit.Session{}, id},
		{"foreign source", prepared, foreign, foreignID}, {"identical foreign import", prepared, identicalImport, id},
		{"other unit same import", samePrepared, sameImport, otherID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.prepared.Restore(tt.session, tt.id, "こんにちは [[YAKUORI_0_0]]")
			if !errors.Is(err, protect.ErrInvalidPrepared) || got != "" {
				t.Fatalf("restored = %q, error = %v", got, err)
			}
			if err := tt.prepared.CheckRestored(tt.session, tt.id, "こんにちは {p}"); !errors.Is(err, protect.ErrInvalidPrepared) {
				t.Fatalf("check error = %v", err)
			}
		})
	}
	t.Run("copied session and prepared retain binding", func(t *testing.T) {
		copySession, copyPrepared := session, prepared
		got, err := copyPrepared.Restore(copySession, id, "こんにちは [[YAKUORI_0_0]]")
		if err != nil || got != "こんにちは {p}" {
			t.Fatalf("restored = %q, error = %v", got, err)
		}
	})
}

func TestProtectionPrepareRejectsInvalidSessionOrUnit(t *testing.T) {
	session, _ := fixtureSession(t, "Hello", nil)
	id, err := unit.NewUnitID("fixture", "v1", "unknown")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		session unit.Session
		want    error
	}{
		{"invalid session", unit.Session{}, unit.ErrInvalidSession}, {"unknown unit", session, unit.ErrUnknownUnit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := protect.Prepare(tt.session, id)
			if !errors.Is(err, tt.want) || got != (protect.Prepared{}) {
				t.Fatalf("prepared = %#v, error = %v", got, err)
			}
		})
	}
}
