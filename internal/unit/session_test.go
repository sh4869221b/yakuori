package unit_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"reflect"
	"testing"

	"github.com/sh4869221b/yakuori/internal/unit"
)

func TestSessionSnapshot(t *testing.T) {
	id, err := unit.NewUnitID("fixture", "v1", "first")
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := unit.NewUnitID("fixture", "v1", "second")
	if err != nil {
		t.Fatal(err)
	}
	source := []byte(" e\u0301\r\n")
	origin := map[string]string{"path": "input.bin"}
	first, err := unit.NewTranslationUnit(id, source, "en", "ja", origin)
	if err != nil {
		t.Fatal(err)
	}
	second, err := unit.NewTranslationUnit(otherID, []byte("next"), "en", "ja", nil)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte{0, 255, 1, 2}
	units := []unit.TranslationUnit{first, second}
	session, err := unit.NewSession(artifact, units)
	if err != nil {
		t.Fatal(err)
	}

	source[0] = 'x'
	origin["path"] = "changed.bin"
	artifact[0] = 10
	units[0] = second
	session.Artifact()[1] = 0
	session.Units()[0] = second
	first.Source()[0] = 'y'
	first.Origin()["path"] = "accessor.bin"

	got, err := session.Unit(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID() != id || got.SourceLanguage() != "en" || got.TargetLanguage() != "ja" {
		t.Fatalf("unit identity/languages changed: %#v", got)
	}
	if !bytes.Equal(got.Source(), []byte(" e\u0301\r\n")) || !reflect.DeepEqual(got.Origin(), map[string]string{"path": "input.bin"}) {
		t.Fatalf("unit snapshot changed: source %q, origin %v", got.Source(), got.Origin())
	}
	if got.SourceDigest() != sha256.Sum256([]byte(" e\u0301\r\n")) || session.ArtifactDigest() != sha256.Sum256([]byte{0, 255, 1, 2}) {
		t.Fatal("digest does not identify original bytes")
	}
	if !bytes.Equal(session.Artifact(), []byte{0, 255, 1, 2}) || len(session.Units()) != 2 || session.Units()[0].ID() != id || session.Units()[1].ID() != otherID {
		t.Fatal("artifact or unit list snapshot changed")
	}
	got.Source()[0] = 'z'
	got.Origin()["path"] = "lookup.bin"
	again, err := session.Unit(id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again.Source(), []byte(" e\u0301\r\n")) || again.Origin()["path"] != "input.bin" {
		t.Fatal("lookup accessor mutation changed session")
	}
}

func TestSessionIsolationAndInvalidInput(t *testing.T) {
	id, err := unit.NewUnitID("fixture", "v1", "first")
	if err != nil {
		t.Fatal(err)
	}
	u, err := unit.NewTranslationUnit(id, []byte("source"), "en", "ja", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("equal bytes are distinct imports", func(t *testing.T) {
		first, err := unit.NewSession([]byte("artifact"), []unit.TranslationUnit{u})
		if err != nil {
			t.Fatal(err)
		}
		second, err := unit.NewSession([]byte("artifact"), []unit.TranslationUnit{u})
		if err != nil {
			t.Fatal(err)
		}
		copy := first
		if first.SameImport(second) || !first.SameImport(copy) || first.SameImport(unit.Session{}) {
			t.Fatal("import identity does not distinguish sessions from copies")
		}
	})
	t.Run("duplicate ID rejected", func(t *testing.T) {
		session, err := unit.NewSession(nil, []unit.TranslationUnit{u, u})
		if !errors.Is(err, unit.ErrDuplicateUnit) || !errors.Is(session.Check(), unit.ErrInvalidSession) {
			t.Fatalf("session = %v, error = %v", session, err)
		}
	})
	t.Run("zero session rejected", func(t *testing.T) {
		var zero unit.Session
		_, err := zero.Unit(id)
		if !errors.Is(err, unit.ErrInvalidSession) || !errors.Is(zero.Check(), unit.ErrInvalidSession) || zero.SameImport(zero) {
			t.Fatalf("zero session accepted: %v", err)
		}
	})
	t.Run("unknown unit rejected", func(t *testing.T) {
		session, err := unit.NewSession(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = session.Unit(id)
		if !errors.Is(err, unit.ErrUnknownUnit) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("zero unit rejected", func(t *testing.T) {
		_, err := unit.NewSession(nil, []unit.TranslationUnit{{}})
		if !errors.Is(err, unit.ErrInvalidUnitID) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestSourceBytesAreNotNormalized(t *testing.T) {
	id, err := unit.NewUnitID("fixture", "v1", "first")
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"a\r\n", "a\n"}, {" a ", "a"}, {"e\u0301", "é"}, {"\xff", "\xef\xbf\xbd"}} {
		t.Run(pair[0], func(t *testing.T) {
			first, err := unit.NewTranslationUnit(id, []byte(pair[0]), "en", "ja", nil)
			if err != nil {
				t.Fatal(err)
			}
			second, err := unit.NewTranslationUnit(id, []byte(pair[1]), "en", "ja", nil)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first.Source(), []byte(pair[0])) || !bytes.Equal(second.Source(), []byte(pair[1])) || first.SourceDigest() == second.SourceDigest() {
				t.Fatal("distinct original source bytes were normalized")
			}
		})
	}
}

func TestUnitIDRejectsMissingIdentity(t *testing.T) {
	for _, fields := range [][3]string{{"", "v1", "first"}, {"fixture", "", "first"}, {"fixture", "v1", ""}} {
		if _, err := unit.NewUnitID(fields[0], fields[1], fields[2]); !errors.Is(err, unit.ErrInvalidUnitID) {
			t.Fatalf("missing identity accepted: %v", err)
		}
	}
	if _, err := unit.NewTranslationUnit(unit.UnitID{}, nil, "en", "ja", nil); !errors.Is(err, unit.ErrInvalidUnitID) {
		t.Fatalf("zero ID accepted: %v", err)
	}
}
