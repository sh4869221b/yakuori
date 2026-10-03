package validate

import (
	"errors"
	"testing"

	"github.com/sh4869221b/yakuori/internal/unit"
)

func fixture(t *testing.T, sources ...string) (unit.Session, []unit.UnitID, Profile) {
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
	session, err := unit.NewSession([]byte("artifact"), units)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewProfile([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	return session, ids, profile
}

func TestValidateCurrentUnit(t *testing.T) {
	for _, tt := range []struct{ name, source, candidate string }{
		{"Japanese candidate", "Hello", "こんにちは"},
		{"empty source", "", ""},
		{"exact whitespace and Unicode", "source", " e\u0301\r\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			session, ids, profile := fixture(t, tt.source)
			accepted, err := Validate(session, ids[0], profile, tt.candidate)
			if err != nil {
				t.Fatal(err)
			}
			if accepted.Text() != tt.candidate || accepted.UnitID() != ids[0] {
				t.Fatalf("accepted text = %q, unit = %v", accepted.Text(), accepted.UnitID())
			}
			if err := CheckBinding(session, ids[0], profile, accepted); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateRejectsInvalidCandidate(t *testing.T) {
	for _, tt := range []struct {
		name, source, candidate string
		want                    error
	}{
		{"candidate invalid UTF8", "source", "\xff", ErrInvalidCandidate},
		{"candidate NUL", "source", "a\x00b", ErrInvalidCandidate},
		{"nonempty source empty candidate", "source", "", ErrInvalidCandidate},
		{"empty source nonempty candidate", "", "text", ErrInvalidCandidate},
		{"source invalid UTF8", "\xff", "text", ErrInvalidSource},
		{"source NUL", "a\x00b", "text", ErrInvalidSource},
	} {
		t.Run(tt.name, func(t *testing.T) {
			session, ids, profile := fixture(t, tt.source)
			accepted, err := Validate(session, ids[0], profile, tt.candidate)
			if !errors.Is(err, tt.want) || accepted != (AcceptedTranslation{}) {
				t.Fatalf("accepted = %v, error = %v", accepted, err)
			}
		})
	}
	session, ids, profile := fixture(t, "source")
	for _, tt := range []struct {
		name    string
		session unit.Session
		id      unit.UnitID
		profile Profile
		want    error
	}{
		{"zero session", unit.Session{}, ids[0], profile, unit.ErrInvalidSession},
		{"unknown unit", session, unit.UnitID{}, profile, unit.ErrUnknownUnit},
		{"zero profile", session, ids[0], Profile{}, ErrInvalidProfile},
	} {
		t.Run(tt.name, func(t *testing.T) {
			accepted, err := Validate(tt.session, tt.id, tt.profile, "text")
			if !errors.Is(err, tt.want) || accepted != (AcceptedTranslation{}) {
				t.Fatalf("accepted = %v, error = %v", accepted, err)
			}
		})
	}
}

func TestBindingRejectsMismatch(t *testing.T) {
	session, ids, profile := fixture(t, "source", "other source")
	accepted, err := Validate(session, ids[0], profile, "訳文")
	if err != nil {
		t.Fatal(err)
	}
	otherSession, _, _ := fixture(t, "source", "other source")
	changedSource, _, _ := fixture(t, "changed source")
	otherProfile, err := NewProfile([32]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	sourceMismatch := accepted
	sourceMismatch.binding.source = [32]byte{9}
	for _, tt := range []struct {
		name     string
		session  unit.Session
		id       unit.UnitID
		profile  Profile
		accepted AcceptedTranslation
		want     error
	}{
		{"zero accepted", session, ids[0], profile, AcceptedTranslation{}, ErrInvalidAccepted},
		{"different import equal bytes", otherSession, ids[0], profile, accepted, ErrBindingMismatch},
		{"different unit source", session, ids[1], profile, accepted, ErrBindingMismatch},
		{"different source snapshot", changedSource, ids[0], profile, accepted, ErrBindingMismatch},
		{"source alone", session, ids[0], profile, sourceMismatch, ErrBindingMismatch},
		{"different profile", session, ids[0], otherProfile, accepted, ErrBindingMismatch},
		{"zero profile", session, ids[0], Profile{}, accepted, ErrInvalidProfile},
		{"zero session", unit.Session{}, ids[0], profile, accepted, unit.ErrInvalidSession},
		{"unknown unit", session, unit.UnitID{}, profile, accepted, unit.ErrUnknownUnit},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := CheckBinding(tt.session, tt.id, tt.profile, tt.accepted); !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestProfileSnapshot(t *testing.T) {
	digest := [32]byte{1, 2, 3}
	profile, err := NewProfile(digest)
	if err != nil {
		t.Fatal(err)
	}
	digest[0] = 10
	returned := profile.Digest()
	returned[1] = 20
	if profile.Digest() != ([32]byte{1, 2, 3}) {
		t.Fatal("profile digest changed")
	}
	if _, err := NewProfile([32]byte{}); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("zero profile error = %v", err)
	}
}
