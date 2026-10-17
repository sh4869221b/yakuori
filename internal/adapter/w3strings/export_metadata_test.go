package w3strings

import (
	"bytes"
	"encoding/hex"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/sh4869221b/yakuori/internal/artifact"
)

func literalBytes(t testing.TB, text string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(text)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Literal jp binary: table 10,30,20; physical 30,20,10. Four distinct gaps,
// real hash0 and a second hash both bind ID30; the other IDs are keyless.
// Source texts are C, AB, XYZ. No production parser/writer builds this fixture.
func compoundSource(t testing.TB) []byte {
	t.Helper()
	return literalBytes(t, "52545357a2000000835403"+
		"4c5682590a00000001000000"+
		"585682590100000002000000"+
		"525682590500000003000000"+
		"02efcdab89585682590000000058568259"+
		"0e11114387450e000022220009ed12322500003333ef040000444455559348")
}

func testExportCompound(t *testing.T) {
	raw := compoundSource(t)
	a := New()
	session := importSession(t, a, raw)
	targets := map[string]string{"10": "C", "30": "日本😀", "20": "名前"}
	accepted := acceptedTexts(t, session, targets)
	slices.Reverse(accepted)
	p := exportProfile(t)
	var output bytes.Buffer

	manifest, err := a.Export(&output, session, p, accepted)

	if err != nil {
		t.Fatal(err)
	}
	// AB grows by two units; XYZ shrinks by one. Subsequent offsets/gaps move,
	// while record order and the four opaque byte sequences stay unchanged.
	want := literalBytes(t, "52545357a2000000835403"+
		"4c5682590b00000001000000"+
		"585682590100000004000000"+
		"525682590700000002000000"+
		"02efcdab89585682590000000058568259"+
		"0f11114bee4d70fff68483000022220fd34a5c00003333ef040000444455559348")
	if !bytes.Equal(output.Bytes(), want) {
		t.Fatalf("compound literal mismatch:\n got %x\nwant %x", output.Bytes(), want)
	}
	metadata := map[string][]byte{
		"fixed-header":      literalBytes(t, "52545357a2000000"),
		"string-count":      {3},
		"key-count":         {2},
		"decoded-id-order":  literalBytes(t, "0a0000001e00000014000000"),
		"decoded-key-order": literalBytes(t, "efcdab891e000000000000001e000000"),
		"language":          literalBytes(t, "83549348"),
		"string-records": literalBytes(t, "4c5682590b00000001000000"+
			"585682590100000004000000525682590700000002000000"),
		"key-records":   literalBytes(t, "efcdab89585682590000000058568259"),
		"payload-count": {15},
		"terminators":   {0, 0, 0, 0, 0, 0},
		"opaque-layout": literalBytes(t, "00000000010000001111"+
			"06000000010000002222"+"0a000000010000003333"+"0d0000000200000044445555"),
	}
	units := session.Units()
	observation := artifact.FinalObservation{
		Adapter: "witcher3-w3strings", FormatSchema: "w3strings-v162-en-jp-v1",
		Units: []artifact.ObservedUnit{
			{ID: units[0].ID(), Text: "C", TargetLanguage: "ja"},
			{ID: units[1].ID(), Text: "日本😀", TargetLanguage: "ja"},
			{ID: units[2].ID(), Text: "名前", TargetLanguage: "ja"},
		},
		Keys: []artifact.KeyRelation{
			{UnitID: units[1].ID(), Key: "\xef\xcd\xab\x89"},
			{UnitID: units[1].ID(), Key: "\x00\x00\x00\x00"},
		},
		Metadata: metadata,
	}
	expected, err := artifact.PrepareExport(session, p, accepted)
	if err != nil {
		t.Fatal(err)
	}
	if err := artifact.CompareFinal(manifest, expected, observation); err != nil {
		t.Fatalf("manifest differs from independent literal metadata: %v", err)
	}
	// All eleven fields must actually participate in final comparison.
	for _, name := range slices.Sorted(maps.Keys(metadata)) {
		t.Run(name, func(t *testing.T) {
			changed := observation
			changed.Metadata = maps.Clone(metadata)
			changed.Metadata[name] = append(bytes.Clone(metadata[name]), 1)

			err := artifact.CompareFinal(manifest, expected, changed)

			if !errors.Is(err, artifact.ErrFinalMismatch) {
				t.Fatalf("changed metadata not rejected: %v", err)
			}
		})
	}
	t.Log("literal bytes and all 11 exact metadata fields matched; reordered Accepted accepted, opaque gap starts 0/6/10/13")
}
