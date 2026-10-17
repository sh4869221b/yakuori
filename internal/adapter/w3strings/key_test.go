package w3strings_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func TestKeyRelations(t *testing.T) {
	// Hand-authored empty strings in table order 10,30,20, physical order
	// 30,20,10. ID30 has two real hashes (including zero); 10 and 20 are keyless.
	raw := []byte{'R', 'T', 'S', 'W', 162, 0, 0, 0, 0x83, 0x54, 3}
	for _, record := range [][3]uint32{{10 ^ 0x59825646, 2, 0}, {30 ^ 0x59825646, 0, 0}, {20 ^ 0x59825646, 1, 0}} {
		for _, value := range record {
			raw = binary.LittleEndian.AppendUint32(raw, value)
		}
	}
	raw = append(raw, 2)
	for _, value := range []uint32{0x89abcdef, 30 ^ 0x59825646, 0, 30 ^ 0x59825646} {
		raw = binary.LittleEndian.AppendUint32(raw, value)
	}
	raw = append(raw, 3, 0, 0, 0, 0, 0, 0, 0x93, 0x48)
	a := w3strings.New()
	session, err := a.Import(raw, validate.Profile{})
	if err != nil {
		t.Fatal(err)
	}
	id30, err := unit.NewUnitID("witcher3-w3strings", "w3strings-v162-en-jp-v1", "30")
	if err != nil {
		t.Fatal(err)
	}
	want := []artifact.KeyRelation{{UnitID: id30, Key: "\xef\xcd\xab\x89"}, {UnitID: id30, Key: "\x00\x00\x00\x00"}}

	keys, err := a.KeyRelations(session)

	if err != nil || !reflect.DeepEqual(keys, want) {
		t.Fatalf("exact ordered opaque/multiple/zero-hash relations lost: %v", err)
	}
	byID := map[unit.UnitID][][]byte{}
	for _, key := range keys {
		byID[key.UnitID] = append(byID[key.UnitID], []byte(key.Key))
	}
	actual := map[string]identity.SourceIdentity{}
	for _, u := range session.Units() {
		source, err := unit.NewSourceIdentity(session, u.ID(), "witcher3", "fixture", byID[u.ID()])
		if err != nil {
			t.Fatal(err)
		}
		actual[u.ID().StableID()] = source
	}
	if len(byID) != 1 || len(byID[id30]) != 2 || actual["10"] == actual["30"] || actual["20"] == actual["30"] {
		t.Fatal("keyless unit conflated with keyed unit")
	}
	// Hold artifact, source, ID and languages constant to isolate key identity.
	noKeys, err := unit.NewSourceIdentity(session, id30, "witcher3", "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	zeroHash, err := unit.NewSourceIdentity(session, id30, "witcher3", "fixture", byID[id30][1:])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(noKeys.CanonicalBytes(), zeroHash.CanonicalBytes()) ||
		bytes.Equal(zeroHash.CanonicalBytes(), actual["30"].CanonicalBytes()) {
		t.Fatal("no keys, real hash0 or multiple keys have equal canonical identity")
	}
	keys[0] = artifact.KeyRelation{}
	keyBytes := []byte(keys[1].Key)
	keyBytes[0] = 1
	again, err := a.KeyRelations(session)
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatalf("returned relations alias private state: %v", err)
	}
	foreign, err := unit.NewSession(session.Artifact(), session.Units())
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []unit.Session{{}, foreign} {
		if got, err := a.KeyRelations(invalid); !errors.Is(err, unit.ErrInvalidSession) || got != nil {
			t.Fatalf("zero or identical-byte foreign import accepted: %v", err)
		}
	}
	b := w3strings.New()
	second, err := b.Import(raw, validate.Profile{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := a.KeyRelations(second); !errors.Is(err, unit.ErrInvalidSession) || got != nil {
		t.Fatalf("another adapter import accepted: %v", err)
	}
	secondKeys, err := b.KeyRelations(second)
	if err != nil {
		t.Fatal(err)
	}
	var sourceKeys [][]byte
	for _, key := range secondKeys {
		if key.UnitID == id30 {
			sourceKeys = append(sourceKeys, []byte(key.Key))
		}
	}
	equalImport, err := unit.NewSourceIdentity(second, id30, "witcher3", "fixture", sourceKeys)
	if err != nil || equalImport != actual["30"] || second.SameImport(session) {
		t.Fatalf("persistent identity incorrectly includes import allocation: %v", err)
	}
	t.Log("keyless, real four-zero-byte hash, multiple opaque keys and canonical SourceIdentity remain distinct; foreign imports rejected")
}
