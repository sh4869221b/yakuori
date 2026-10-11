package identity_test

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/sh4869221b/yakuori/internal/identity"
)

func TestExactKeyCanonicalBlobs(t *testing.T) {
	source := makeSource(t, sourceInput())
	profile := makeProfile(t, profileInput())
	sourceBytes, err := os.ReadFile("testdata/source-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	profileBytes, err := os.ReadFile("testdata/profile-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	key, err := identity.NewExactKey(source, profile)
	if err != nil {
		t.Fatal(err)
	}
	if key.Schema() != 1 || !bytes.Equal(key.SourceBytes(), sourceBytes) || !bytes.Equal(key.ProfileBytes(), profileBytes) {
		t.Fatal("key does not contain schema and full canonical BLOBs")
	}
	other, err := identity.NewExactKey(makeSource(t, sourceInput()), makeProfile(t, profileInput()))
	if err != nil {
		t.Fatal(err)
	}
	if key != other {
		t.Fatal("independently constructed equal inputs changed key")
	}
	returnedSource, returnedProfile := key.SourceBytes(), key.ProfileBytes()
	returnedSource[0] = '!'
	returnedProfile[0] = '!'
	if !bytes.Equal(key.SourceBytes(), sourceBytes) || !bytes.Equal(key.ProfileBytes(), profileBytes) {
		t.Fatal("returned BLOB mutation changed key")
	}
}
func TestExactKeySourceChange(t *testing.T) {
	profile := makeProfile(t, profileInput())
	base, err := identity.NewExactKey(makeSource(t, sourceInput()), profile)
	if err != nil {
		t.Fatal(err)
	}
	input := sourceInput()
	input.StableID = "record-2"
	other, err := identity.NewExactKey(makeSource(t, input), profile)
	if err != nil {
		t.Fatal(err)
	}
	if base == other || bytes.Equal(base.SourceBytes(), other.SourceBytes()) {
		t.Fatal("source change retained key")
	}
}
func TestExactKeyRejectsZeroIdentity(t *testing.T) {
	source := makeSource(t, sourceInput())
	profile := makeProfile(t, profileInput())
	for _, tt := range []struct {
		name    string
		source  identity.SourceIdentity
		profile identity.ProfileIdentity
	}{{"source", identity.SourceIdentity{}, profile}, {"profile", source, identity.ProfileIdentity{}}, {"both", identity.SourceIdentity{}, identity.ProfileIdentity{}}} {
		t.Run(tt.name, func(t *testing.T) {
			key, err := identity.NewExactKey(tt.source, tt.profile)
			if !errors.Is(err, identity.ErrInvalidKey) || key != (identity.ExactKey{}) {
				t.Fatalf("key=%#v, err=%v", key, err)
			}
		})
	}
}
