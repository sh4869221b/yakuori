package w3strings

import (
	"errors"
	"slices"
	"testing"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func TestExportBindings(t *testing.T) {
	for _, name := range []string{
		"missing", "duplicate", "extra", "zero-accepted", "equal-byte-foreign-accepted",
		"changed-source", "different-profile", "zero-profile", "zero-session",
		"equal-byte-foreign-session", "stale-import",
	} {
		t.Run(name, func(t *testing.T) {
			raw := testFile("jp", []testString{{7, 0, []uint16{'A'}}, {8, 2, []uint16{'B'}}}, nil, 4)
			a := New()
			session := importSession(t, a, raw)
			accepted := acceptedTexts(t, session, nil)
			p := exportProfile(t)
			switch name {
			case "missing":
				accepted = accepted[:1]
			case "duplicate":
				accepted = append(accepted, accepted[0])
			case "extra":
				other := New()
				foreign := importSession(t, other, testFile("jp", []testString{{9, 0, []uint16{'C'}}}, nil, 2))
				accepted = append(accepted, acceptedTexts(t, foreign, nil)[0])
			case "zero-accepted":
				accepted[1] = validate.AcceptedTranslation{}
			case "equal-byte-foreign-accepted":
				other := New()
				foreign := importSession(t, other, raw)
				accepted = acceptedTexts(t, foreign, nil)
			case "changed-source":
				other := New()
				foreign := importSession(t, other, testFile("jp", []testString{{7, 0, []uint16{'C'}}, {8, 2, []uint16{'B'}}}, nil, 4))
				accepted = acceptedTexts(t, foreign, nil)
			case "different-profile":
				var err error
				p, err = validate.NewProfile([32]byte{2})
				if err != nil {
					t.Fatal(err)
				}
			case "zero-profile":
				p = validate.Profile{}
			case "zero-session":
				session = unit.Session{}
			case "equal-byte-foreign-session":
				var err error
				session, err = unit.NewSession(session.Artifact(), session.Units())
				if err != nil {
					t.Fatal(err)
				}
			case "stale-import":
				importSession(t, a, raw)
			}
			writer := &exportWriter{}

			manifest, err := a.Export(writer, session, p, accepted)

			if err == nil || writer.calls != 0 || !errors.Is(manifest.CheckInput(session), artifact.ErrInvalidManifest) {
				t.Fatalf("invalid binding reached writer or returned success manifest: %v", err)
			}
		})
	}
	for _, tc := range []struct{ name, text string }{
		{"NUL", "A\x00B"}, {"invalid-UTF8", "A\xffB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := New()
			session := importSession(t, a, testFile("jp", []testString{{7, 0, []uint16{'A'}}}, nil, 2))
			p := exportProfile(t)
			// Public Accepted construction itself rejects these characters.
			// A rejected value must never become writable via Export.
			accepted, validationErr := validate.Validate(session, session.Units()[0].ID(), p, tc.text)
			if !errors.Is(validationErr, validate.ErrInvalidCandidate) {
				t.Fatalf("invalid text became Accepted: %v", validationErr)
			}
			writer := &exportWriter{}

			manifest, err := a.Export(writer, session, p, []validate.AcceptedTranslation{accepted})

			if err == nil || writer.calls != 0 || !errors.Is(manifest.CheckInput(session), artifact.ErrInvalidManifest) {
				t.Fatal("invalid text reached writer")
			}
		})
	}
	t.Run("order-only-is-valid", func(t *testing.T) {
		a := New()
		session := importSession(t, a, compoundSource(t))
		accepted := acceptedTexts(t, session, nil)
		slices.Reverse(accepted)
		writer := &exportWriter{n: -1}

		manifest, err := a.Export(writer, session, exportProfile(t), accepted)

		if err != nil || writer.calls != 1 || manifest.CheckInput(session) != nil {
			t.Fatalf("legal reordered Accepted rejected: %v", err)
		}
	})
}
