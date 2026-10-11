package unit_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func identitySession(t *testing.T, artifact, source []byte) (unit.Session, unit.UnitID) {
	t.Helper()
	id, err := unit.NewUnitID("fixture", "v1", "record-1")
	if err != nil {
		t.Fatal(err)
	}
	u, err := unit.NewTranslationUnitWithProtection(id, source, "en", "ja", nil, []unit.ProtectionSpan{{Start: 0, End: len(source), Kind: unit.Placeholder}})
	if err != nil {
		t.Fatal(err)
	}
	session, err := unit.NewSession(artifact, []unit.TranslationUnit{u})
	if err != nil {
		t.Fatal(err)
	}
	return session, id
}

func TestSourceIdentitySessionScope(t *testing.T) {
	first, id := identitySession(t, []byte("first|second"), []byte("source"))
	second, _ := identitySession(t, []byte("first|second"), []byte("source"))
	base, err := unit.NewSourceIdentity(first, id, "game", "artifact", [][]byte{nil, []byte("key"), []byte("key")})
	if err != nil {
		t.Fatal(err)
	}
	other, err := unit.NewSourceIdentity(second, id, "game", "artifact", [][]byte{[]byte("key"), {}, []byte("key")})
	if err != nil {
		t.Fatal(err)
	}
	if first.SameImport(second) || other != base {
		t.Fatal("equal original bytes across imports must share canonical identity")
	}
	u, err := first.Unit(id)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := identity.NewSourceIdentity(identity.SourceInput{
		Version: 1, GameNamespace: "game", ArtifactNamespace: "artifact", OriginalArtifactDigest: first.ArtifactDigest(),
		Adapter: id.Adapter(), FormatSchema: id.FormatSchema(), StableID: id.StableID(), Keys: [][]byte{nil, []byte("key"), []byte("key")},
		RawSourceDigest: u.SourceDigest(), SourceLanguage: "en", TargetLanguage: "ja",
		ProtectionSpans: []identity.ProtectionSpan{{Start: 0, End: 6, Kind: "placeholder"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if expected != base {
		t.Fatal("builder did not use existing digests or unit protection fields")
	}
	changed, _ := identitySession(t, []byte("first|seconD"), []byte("source"))
	updated, err := unit.NewSourceIdentity(changed, id, "game", "artifact", [][]byte{nil, []byte("key"), []byte("key")})
	if err != nil {
		t.Fatal(err)
	}
	if updated == base {
		t.Fatal("change outside the unit source did not invalidate whole-artifact scope")
	}
}

func TestSourceIdentityRawBytes(t *testing.T) {
	for _, pair := range [][2]string{{"a\r\n", "a\n"}, {" a ", "a"}, {"e\u0301", "é"}, {"\xff", "\xef\xbf\xbd"}} {
		t.Run(pair[0], func(t *testing.T) {
			first, id := identitySession(t, []byte("artifact"), []byte(pair[0]))
			second, _ := identitySession(t, []byte("artifact"), []byte(pair[1]))
			a, err := unit.NewSourceIdentity(first, id, "game", "artifact", nil)
			if err != nil {
				t.Fatal(err)
			}
			b, err := unit.NewSourceIdentity(second, id, "game", "artifact", nil)
			if err != nil {
				t.Fatal(err)
			}
			if a == b || bytes.Equal(a.CanonicalBytes(), b.CanonicalBytes()) {
				t.Fatal("distinct raw bytes were normalized")
			}
		})
	}
}

func TestSourceIdentitySessionInvalid(t *testing.T) {
	session, id := identitySession(t, []byte("artifact"), []byte("source"))
	unknown, err := unit.NewUnitID("fixture", "v1", "unknown")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name           string
		session        unit.Session
		id             unit.UnitID
		game, artifact string
		want           error
	}{
		{"zero session", unit.Session{}, id, "game", "artifact", unit.ErrInvalidSession},
		{"unknown unit", session, unknown, "game", "artifact", unit.ErrUnknownUnit},
		{"empty game", session, id, "", "artifact", identity.ErrInvalidSource},
		{"empty artifact", session, id, "game", "", identity.ErrInvalidSource},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := unit.NewSourceIdentity(tc.session, tc.id, tc.game, tc.artifact, nil)
			if !errors.Is(err, tc.want) || got != (identity.SourceIdentity{}) {
				t.Fatalf("identity = %v, error = %v", got, err)
			}
		})
	}
}

func TestSourceIdentityWholeArtifactInvalidatesAllUnits(t *testing.T) {
	first, id := identitySession(t, []byte("artifact"), []byte("first"))
	secondID, err := unit.NewUnitID("fixture", "v1", "record-2")
	if err != nil {
		t.Fatal(err)
	}
	second, err := unit.NewTranslationUnit(secondID, []byte("second"), "en", "ja", nil)
	if err != nil {
		t.Fatal(err)
	}
	units := append(first.Units(), second)
	original, err := unit.NewSession([]byte("first|second|metadata"), units)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := unit.NewSession([]byte("first|second|metadatA"), units)
	if err != nil {
		t.Fatal(err)
	}
	for _, unitID := range []unit.UnitID{id, secondID} {
		t.Run(unitID.StableID(), func(t *testing.T) {
			before, err := unit.NewSourceIdentity(original, unitID, "game", "artifact", nil)
			if err != nil {
				t.Fatal(err)
			}
			after, err := unit.NewSourceIdentity(updated, unitID, "game", "artifact", nil)
			if err != nil {
				t.Fatal(err)
			}
			if before == after {
				t.Fatal("whole-artifact metadata change reused unit identity")
			}
		})
	}
}
