package w3strings

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/sh4869221b/yakuori/internal/artifact"
)

func FuzzParse(f *testing.F) {
	for _, name := range []string{"en", "jp", "better-keybinds/en", "monster-of-the-week/en"} {
		f.Add(fixture(f, name+".w3strings"))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		l := researchLimits()
		a, err := parse(raw, l.ArtifactBytes, &l)
		if err != nil {
			if a != nil {
				t.Fatal("rejected input retained state")
			}
			return
		}
		if len(raw) > l.ArtifactBytes || !bytes.Equal(a.raw, raw) || a.payloadUnits > 4095 ||
			a.start+int(a.payloadUnits)*2+2 != len(raw) || len(a.entries) > l.Units || len(a.keys) > 4095 {
			t.Fatal("successful parse violated byte/count bounds")
		}
		ids, hashes := map[uint32]bool{}, map[uint32]bool{}
		total := 0
		for _, e := range a.entries {
			units := utf16.Encode([]rune(e.text))
			if ids[e.id] || !utf8.ValidString(e.text) || strings.ContainsRune(e.text, 0) ||
				uint64(e.offset)+uint64(e.length)+1 > uint64(a.payloadUnits) ||
				len(units) != int(e.length) || len(e.text) > l.UnitTextBytes {
				t.Fatal("invalid successful string")
			}
			if le.Uint32(raw[e.table:])^a.magic != e.id || le.Uint32(raw[e.table+4:]) != e.offset ||
				le.Uint32(raw[e.table+8:]) != e.length || le.Uint16(raw[a.start+int(e.offset+e.length)*2:]) != 0 {
				t.Fatal("record/snapshot disagreement")
			}
			state := uint16(a.magic >> 8)
			for j, u := range units {
				want := u ^ uint16(uint32(len(units)+1)*uint32(state))
				if le.Uint16(raw[a.start+(int(e.offset)+j)*2:]) != want {
					t.Fatal("decoded text/ciphertext disagreement")
				}
				state = state<<1 | state>>15
			}
			ids[e.id] = true
			total += len(e.text)
		}
		if total > l.TotalTextBytes {
			t.Fatal("total UTF-8 bound exceeded")
		}
		for _, k := range a.keys {
			if !ids[k.id] || hashes[k.hash] || le.Uint32(raw[k.table:]) != k.hash ||
				le.Uint32(raw[k.table+4:])^a.magic != k.id {
				t.Fatal("key relation/snapshot disagreement")
			}
			hashes[k.hash] = true
		}
		end := uint32(0)
		for j, i := range a.physical {
			e, gap := a.entries[i], a.gaps[j]
			if e.offset < end || gap.offset != end || gap.length != e.offset-end {
				t.Fatal("overlap or lost physical gap")
			}
			end = e.offset + e.length + 1
		}
		if a.gaps[len(a.physical)] != (payloadGap{end, a.payloadUnits - end}) {
			t.Fatal("lost trailing gap")
		}
		observed, err := parse(raw, l.ArtifactBytes, nil)
		if err != nil || !reflect.DeepEqual(a, observed) {
			t.Fatalf("input/output parser disagreement: %v", err)
		}
		if a.language == "jp" {
			adapter := researchAdapter(t)
			session := importSession(t, adapter, raw)
			accepted := acceptedTexts(t, session, nil)
			var output bytes.Buffer
			manifest, err := adapter.Export(&output, session, exportProfile(t), accepted)
			if err != nil || !bytes.Equal(output.Bytes(), raw) {
				t.Fatalf("jp source-identical reconstruction changed bytes: %v", err)
			}
			final, err := adapter.Observe(bytes.NewReader(output.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			expected, err := artifact.PrepareExport(session, exportProfile(t), accepted)
			if err != nil {
				t.Fatal(err)
			}
			if err := artifact.CompareFinal(manifest, expected, final); err != nil {
				t.Fatal(err)
			}
		}
		if len(raw) != 0 {
			raw[0] ^= 1
			if a.raw[0] != 'R' {
				t.Fatal("snapshot aliases input")
			}
		}
	})
}
