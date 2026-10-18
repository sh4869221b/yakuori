package w3strings

import (
	"bytes"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/sh4869221b/yakuori/internal/artifact"
)

func TestCompareStageTampering(t *testing.T) {
	raw := compoundSource(t)
	a := New()
	session := importSession(t, a, raw)
	accepted := acceptedTexts(t, session, nil)
	var output bytes.Buffer
	manifest, err := a.Export(&output, session, exportProfile(t), accepted)
	if err != nil || !bytes.Equal(output.Bytes(), raw) {
		t.Fatalf("compound no-op setup failed: %v", err)
	}
	expected, err := artifact.PrepareExport(session, exportProfile(t), accepted)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := a.Observe(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if err := artifact.CompareFinal(manifest, expected, baseline); err != nil {
		t.Fatal(err)
	}
	s := mustParse(t, raw)
	first, last, key := s.entries[0], s.entries[2], s.keys[0]
	entries := []testString{{10, 10, []uint16{'C'}}, {30, 1, []uint16{'A', 'B'}}, {20, 5, []uint16{'X', 'Y', 'Z'}}}
	keys := []testKey{{0x89abcdef, 30}, {0, 30}}
	type mutation struct {
		name       string
		edit       func([]byte) []byte
		parseError bool
		multiset   bool
	}
	mutations := []mutation{
		{"missing-ID", func(b []byte) []byte {
			b[10] = 2
			return append(b[:last.table], b[last.table+12:]...)
		}, false, false},
		{"duplicate-ID", func(b []byte) []byte { le.PutUint32(b[last.table:], first.id^s.magic); return b }, true, false},
		{"extra-ID", func(b []byte) []byte {
			b[10] = 4
			record := le.AppendUint32(nil, 40^s.magic)
			record = le.AppendUint32(record, 13)
			record = le.AppendUint32(record, 0)
			le.PutUint16(b[s.start+26:], 0)
			return slices.Insert(b, s.keyCountAt, record...)
		}, false, false},
		{"same-count-unknown-ID", func(b []byte) []byte { le.PutUint32(b[last.table:], 40^s.magic); return b }, false, false},
		{"valid-ciphertext-swapped-texts", func([]byte) []byte {
			return testFile("jp", []testString{{10, 10, []uint16{'C'}}, {30, 1, []uint16{'X', 'Y', 'Z'}}, {20, 6, []uint16{'A', 'B'}}}, keys, 14)
		}, false, false},
		{"other-artifact-candidate", func([]byte) []byte {
			other := New()
			foreign := importSession(t, other, raw)
			translations := acceptedTexts(t, foreign, map[string]string{"10": "D", "30": "EF", "20": "UVW"})
			var candidate bytes.Buffer
			if _, err := other.Export(&candidate, foreign, exportProfile(t), translations); err != nil {
				t.Fatal(err)
			}
			return candidate.Bytes()
		}, false, false},
		{"key-reassigned", func(b []byte) []byte { le.PutUint32(b[key.table+4:], 10^s.magic); return b }, false, false},
		{"key-missing", func(b []byte) []byte {
			b[s.keyCountAt] = 1
			return append(b[:key.table], b[key.table+8:]...)
		}, false, false},
		{"key-extra", func(b []byte) []byte {
			b[s.keyCountAt] = 3
			record := le.AppendUint32(nil, 1)
			record = le.AppendUint32(record, 10^s.magic)
			return slices.Insert(b, s.payloadCountAt, record...)
		}, false, false},
		{"key-hash-changed", func(b []byte) []byte { le.PutUint32(b[key.table:], 1); return b }, false, false},
		{"hash0-is-not-keyless", func(b []byte) []byte {
			b[s.keyCountAt] = 1
			return append(b[:s.keys[1].table], b[s.keys[1].table+8:]...)
		}, false, false},
		{"key-dangling", func(b []byte) []byte { le.PutUint32(b[key.table+4:], 99^s.magic); return b }, true, false},
		{"key-duplicate-hash", func(b []byte) []byte { le.PutUint32(b[s.keys[1].table:], key.hash); return b }, true, false},
		{"string-table-order-only", func(b []byte) []byte {
			record := bytes.Clone(b[first.table : first.table+12])
			copy(b[first.table:], b[last.table:last.table+12])
			copy(b[last.table:], record)
			return b
		}, false, true},
		{"key-table-order-only", func(b []byte) []byte {
			record := bytes.Clone(b[key.table : key.table+8])
			copy(b[key.table:], b[s.keys[1].table:s.keys[1].table+8])
			copy(b[s.keys[1].table:], record)
			return b
		}, false, true},
		{"wrong-language-valid-en-ciphertext", func([]byte) []byte { return testFile("en", entries, keys, 14) }, false, false},
		{"header", func(b []byte) []byte { b[0] ^= 1; return b }, true, false},
		{"version", func(b []byte) []byte { b[4] = 164; return b }, true, false},
		{"language-prefix", func(b []byte) []byte { b[8] ^= 1; return b }, true, false},
		{"language-suffix", func(b []byte) []byte { b[len(b)-1] ^= 1; return b }, true, false},
		{"offset-outside", func(b []byte) []byte { le.PutUint32(b[first.table+4:], ^uint32(0)); return b }, true, false},
		{"offset-valid-relocation", func(b []byte) []byte {
			copy(b[s.start+24:], b[s.start+20:s.start+24])
			le.PutUint32(b[first.table+4:], 12)
			return b
		}, false, true},
		{"length-outside", func(b []byte) []byte { le.PutUint32(b[first.table+8:], ^uint32(0)); return b }, true, false},
		{"length-valid-shrink", func([]byte) []byte {
			return testFile("jp", []testString{{10, 10, []uint16{'C'}}, {30, 1, []uint16{'A'}}, {20, 5, []uint16{'X', 'Y', 'Z'}}}, keys, 14)
		}, false, false},
		{"string-count", func(b []byte) []byte { b[10] = 63; return b }, true, false},
		{"key-count", func(b []byte) []byte { b[s.keyCountAt] = 63; return b }, true, false},
		{"payload-count", func(b []byte) []byte { b[s.payloadCountAt]++; return b }, true, false},
		{"payload-valid-growth", func(b []byte) []byte {
			b[s.payloadCountAt]++
			return slices.Insert(b, len(b)-2, 0x66, 0x66)
		}, false, true},
		{"nonminimal-count", func(b []byte) []byte { b[10] = 64; return slices.Insert(b, 11, 0) }, true, false},
		{"terminator", func(b []byte) []byte { b[s.start+2*int(first.offset+first.length)] = 1; return b }, true, false},
		{"trailing-byte", func(b []byte) []byte { return append(b, 0) }, true, false},
		{"truncated", func(b []byte) []byte { return b[:len(b)-1] }, true, false},
		{"embedded-NUL-with-valid-cipher", func([]byte) []byte {
			return testFile("jp", []testString{{7, 0, []uint16{0}}}, nil, 2)
		}, true, false},
		{"unpaired-surrogate-with-valid-cipher", func([]byte) []byte {
			return testFile("jp", []testString{{7, 0, []uint16{0xd800}}}, nil, 2)
		}, true, false},
	}
	for i, gap := range s.gaps {
		name := []string{"prefix-gap-only", "middle-gap-1-only", "middle-gap-2-only", "suffix-gap-only"}[i]
		mutations = append(mutations, mutation{name, func(b []byte) []byte {
			b[s.start+2*int(gap.offset)] ^= 1
			return b
		}, false, true})
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			changed := tc.edit(bytes.Clone(output.Bytes()))

			observed, err := a.Observe(bytes.NewReader(changed))

			if tc.parseError {
				if err == nil || !reflect.DeepEqual(observed, artifact.FinalObservation{}) {
					t.Fatal("malformed stage returned observation")
				}
				t.Logf("actual bytes rejected by Observe: %v", err)
				return
			}
			if err != nil {
				t.Fatalf("mutation must be structurally valid: %v", err)
			}
			if tc.multiset {
				unitSet := func(f artifact.FinalObservation) map[artifact.ObservedUnit]int {
					set := map[artifact.ObservedUnit]int{}
					for _, u := range f.Units {
						set[u]++
					}
					return set
				}
				keySet := func(f artifact.FinalObservation) map[artifact.KeyRelation]int {
					set := map[artifact.KeyRelation]int{}
					for _, k := range f.Keys {
						set[k]++
					}
					return set
				}
				if !reflect.DeepEqual(unitSet(baseline), unitSet(observed)) || !reflect.DeepEqual(keySet(baseline), keySet(observed)) {
					t.Fatal("metadata-only mutation changed ID/text/language/key multiset")
				}
				projection := observed
				projection.Metadata = baseline.Metadata
				if err := artifact.CompareFinal(manifest, expected, projection); err != nil {
					t.Fatalf("generic unordered comparison should accept unchanged multisets: %v", err)
				}
			}
			if tc.name == "valid-ciphertext-swapped-texts" && (observed.Units[1].Text != "XYZ" || observed.Units[2].Text != "AB") {
				t.Fatal("valid ciphertext did not actually swap decoded texts")
			}
			if tc.name == "wrong-language-valid-en-ciphertext" {
				for _, u := range observed.Units {
					if u.TargetLanguage != "en" {
						t.Fatal("language was copied from expected state")
					}
				}
			}
			if err := artifact.CompareFinal(manifest, expected, observed); !errors.Is(err, artifact.ErrFinalMismatch) {
				t.Fatalf("actual stage mutation accepted: %v", err)
			} else {
				t.Logf("actual valid bytes rejected by CompareFinal: %v; unchanged multisets=%t", err, tc.multiset)
			}
		})
	}
	t.Run("every-byte-covered", func(t *testing.T) {
		for i := range output.Len() {
			changed := bytes.Clone(output.Bytes())
			changed[i] ^= 1

			observed, err := a.Observe(bytes.NewReader(changed))

			if err == nil && !errors.Is(artifact.CompareFinal(manifest, expected, observed), artifact.ErrFinalMismatch) {
				t.Fatalf("changed byte %d escaped final comparison", i)
			}
		}
		t.Logf("all %d single-byte mutations rejected", output.Len())
	})
}
