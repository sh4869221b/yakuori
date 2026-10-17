package w3strings

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/sh4869221b/yakuori/internal/config"
)

func researchLimits() config.Limits {
	l := config.DefaultLimits()
	l.ArtifactBytes, l.Units = 65536, 4095
	l.UnitTextBytes, l.TotalTextBytes, l.Segments = 16384, 32768, 4095
	return l
}

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../../tools/w3spike/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustParse(t testing.TB, raw []byte) *parsedFile {
	t.Helper()
	l := researchLimits()
	a, err := parse(raw, l.ArtifactBytes, &l)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestParseFixtures(t *testing.T) {
	for _, tc := range []struct {
		name, csv, lang string
		strings, keys   int
	}{
		{"en", "en.csv", "en", 4, 3},
		{"jp", "jp.csv", "jp", 4, 3},
		{"better-keybinds/en", "better-keybinds/en.expected.csv", "en", 19, 19},
		{"monster-of-the-week/en", "monster-of-the-week/en.expected.csv", "en", 30, 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fixture(t, tc.name+".w3strings")
			expectedText, expectedKeys := map[uint32]string{}, map[uint32]uint32{}
			for line := range strings.SplitSeq(string(fixture(t, tc.csv)), "\n") {
				if line == "" || strings.HasPrefix(line, ";") {
					continue
				}
				fields := strings.SplitN(line, "|", 4)
				if len(fields) != 4 {
					t.Fatal("expected four saved CSV columns")
				}
				id, err := strconv.ParseUint(strings.TrimSpace(fields[0]), 10, 32)
				if err != nil {
					t.Fatal(err)
				}
				hash, err := strconv.ParseUint(strings.TrimSpace(fields[1]), 16, 32)
				if err != nil {
					t.Fatal(err)
				}
				if _, exists := expectedText[uint32(id)]; exists {
					t.Fatal("duplicate saved ID")
				}
				expectedText[uint32(id)] = fields[3]
				// Authored ID1003 is known keyless. CSV's zero projection
				// alone cannot distinguish it from a real zero hash.
				if tc.keys != 3 || id != 1003 {
					expectedKeys[uint32(hash)] = uint32(id)
				}
			}
			a := mustParse(t, raw)
			if a.language != tc.lang || len(a.entries) != tc.strings || len(a.keys) != tc.keys {
				t.Fatal("fixture language/table count mismatch")
			}
			gotText, gotKeys := map[uint32]string{}, map[uint32]uint32{}
			for _, e := range a.entries {
				gotText[e.id] = e.text
			}
			for _, k := range a.keys {
				gotKeys[k.hash] = k.id
			}
			if !reflect.DeepEqual(gotText, expectedText) || !reflect.DeepEqual(gotKeys, expectedKeys) {
				t.Fatal("decoded ID/text/key differs from saved expectations")
			}
			if !bytes.Equal(a.raw, raw) {
				t.Fatal("snapshot differs from source")
			}
			raw[0] ^= 1
			if a.raw[0] != 'R' {
				t.Fatal("snapshot aliases caller bytes")
			}
			t.Logf("%d exact ID/text values and %d exact key relations matched", len(gotText), len(gotKeys))
		})
	}
}

type testString struct {
	id, offset uint32
	units      []uint16
}

type testKey struct{ hash, id uint32 }

// Independently authored test bytes; no production count/decryption helper.
func testCount(n int) []byte {
	if n < 64 {
		return []byte{byte(n)}
	}
	return []byte{byte(n&63) | 64, byte(n >> 6)}
}

func testFile(lang string, entries []testString, keys []testKey, payload int) []byte {
	mask, prefix, suffix := uint32(0x59825646), uint16(0x5483), uint16(0x4893)
	if lang == "en" {
		mask, prefix, suffix = 0x79321793, 0x4397, 0x5139
	}
	raw := []byte{'R', 'T', 'S', 'W', 162, 0, 0, 0}
	raw = binary.LittleEndian.AppendUint16(raw, prefix)
	raw = append(raw, testCount(len(entries))...)
	for _, e := range entries {
		raw = binary.LittleEndian.AppendUint32(raw, e.id^mask)
		raw = binary.LittleEndian.AppendUint32(raw, e.offset)
		raw = binary.LittleEndian.AppendUint32(raw, uint32(len(e.units)))
	}
	raw = append(raw, testCount(len(keys))...)
	for _, k := range keys {
		raw = binary.LittleEndian.AppendUint32(raw, k.hash)
		raw = binary.LittleEndian.AppendUint32(raw, k.id^mask)
	}
	raw = append(raw, testCount(payload)...)
	start := len(raw)
	for range payload {
		raw = binary.LittleEndian.AppendUint16(raw, 0xa55a)
	}
	for _, e := range entries {
		state := uint16(mask >> 8)
		for j, u := range e.units {
			binary.LittleEndian.PutUint16(raw[start+(int(e.offset)+j)*2:], u^uint16(uint32(len(e.units)+1)*uint32(state)))
			state = state<<1 | state>>15
		}
		binary.LittleEndian.PutUint16(raw[start+(int(e.offset)+len(e.units))*2:], 0)
	}
	return binary.LittleEndian.AppendUint16(raw, suffix)
}

func TestParseRejectMalformed(t *testing.T) {
	jp := fixture(t, "jp.w3strings")
	a := mustParse(t, jp)
	e, k := a.entries[0], a.keys[0]
	mutations := []struct {
		name string
		edit func([]byte) []byte
	}{
		{"magic", func(b []byte) []byte { b[0] = 0; return b }},
		{"version", func(b []byte) []byte { b[4] = 164; return b }},
		{"language-prefix", func(b []byte) []byte { b[8] = 0; return b }},
		{"language-suffix", func(b []byte) []byte { b[len(b)-1] = 0; return b }},
		{"string-count-width", func(b []byte) []byte { b[10] = 63; return b }},
		{"string-count-4095", func(b []byte) []byte { b[10], b[11] = 127, 63; return b }},
		{"key-count-width", func(b []byte) []byte { b[a.keyCountAt] = 63; return b }},
		{"key-count-4095", func(b []byte) []byte { b[a.keyCountAt], b[a.keyCountAt+1] = 127, 63; return b }},
		{"unsupported-count", func(b []byte) []byte { b[10] = 128; return b }},
		{"nonminimal-count", func(b []byte) []byte { b[10], b[11] = 64, 0; return b }},
		{"duplicate-id", func(b []byte) []byte { le.PutUint32(b[a.entries[1].table:], e.id^a.magic); return b }},
		{"offset-max", func(b []byte) []byte { le.PutUint32(b[e.table+4:], ^uint32(0)); return b }},
		{"length-max", func(b []byte) []byte { le.PutUint32(b[e.table+8:], ^uint32(0)); return b }},
		{"offset-length-max", func(b []byte) []byte {
			le.PutUint32(b[e.table+4:], ^uint32(0))
			le.PutUint32(b[e.table+8:], ^uint32(0))
			return b
		}},
		{"offset-at-end", func(b []byte) []byte { le.PutUint32(b[e.table+4:], a.payloadUnits); return b }},
		{"terminator-past-end", func(b []byte) []byte { le.PutUint32(b[e.table+8:], a.payloadUnits-e.offset); return b }},
		{"overlap", func(b []byte) []byte { le.PutUint32(b[a.entries[1].table+4:], e.offset); return b }},
		{"shared-terminator", func(b []byte) []byte { le.PutUint32(b[a.entries[1].table+4:], e.offset+e.length); return b }},
		{"dangling-key", func(b []byte) []byte { le.PutUint32(b[k.table+4:], 555^a.magic); return b }},
		{"duplicate-hash", func(b []byte) []byte { le.PutUint32(b[a.keys[1].table:], k.hash); return b }},
		{"terminator", func(b []byte) []byte { b[a.start+int(e.offset+e.length)*2] = 1; return b }},
		{"payload-count", func(b []byte) []byte { b[a.payloadCountAt] ^= 1; return b }},
		{"trailing-byte", func(b []byte) []byte { return append(b, 0) }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			assertRejected(t, tc.edit(bytes.Clone(jp)), nil)
		})
	}
	for _, tc := range []struct {
		name  string
		units []uint16
	}{
		{"NUL", []uint16{0}},
		{"low-surrogate", []uint16{0xdc00}},
		{"high-surrogate-at-end", []uint16{0xd800}},
		{"high-surrogate-before-ascii", []uint16{0xd800, 'A'}},
		{"two-high-surrogates", []uint16{0xd800, 0xd800}},
		{"pair-then-low", []uint16{0xd800, 0xdc00, 0xdc00}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertRejected(t, testFile("jp", []testString{{1, 0, tc.units}}, nil, len(tc.units)+1), nil)
		})
	}
	t.Run("every-jp-truncation", func(t *testing.T) {
		for i := range len(jp) {
			assertRejected(t, jp[:i], nil)
		}
		t.Logf("%d strict prefixes rejected", len(jp))
	})
	t.Run("empty-file", func(t *testing.T) { assertRejected(t, nil, nil) })
	t.Run("key-without-string", func(t *testing.T) {
		assertRejected(t, testFile("jp", nil, []testKey{{0, 1}}, 0), nil)
	})
	t.Run("late-text-error", func(t *testing.T) {
		raw := testFile("jp", []testString{{1, 0, []uint16{'A'}}, {2, 2, []uint16{0}}}, nil, 4)
		assertRejected(t, raw, nil)
	})
}

func assertRejected(t testing.TB, raw []byte, limits *config.Limits) {
	t.Helper()
	a, err := parse(raw, 65536, limits)
	if err == nil || a != nil {
		t.Fatal("invalid input returned success or retained parse state")
	}
}

func TestParseLimits(t *testing.T) {
	raw := testFile("jp", []testString{{1, 0, utf16.Encode([]rune("A😀"))}, {2, 4, []uint16{'é'}}}, nil, 6)
	for _, tc := range []struct {
		name   string
		actual int
		set    func(*config.Limits, int)
	}{
		{"artifact bytes", len(raw), func(l *config.Limits, n int) { l.ArtifactBytes = n }},
		{"units", 2, func(l *config.Limits, n int) { l.Units = n }},
		{"unit text bytes", 5, func(l *config.Limits, n int) { l.UnitTextBytes = n }},
		{"total text bytes", 7, func(l *config.Limits, n int) { l.TotalTextBytes = n }},
	} {
		for _, delta := range []int{-1, 0, 1} {
			t.Run(fmt.Sprintf("%s/maximum=%d", tc.name, tc.actual+delta), func(t *testing.T) {
				l := researchLimits()
				tc.set(&l, tc.actual+delta)
				a, err := parse(raw, 65536, &l)
				if delta < 0 {
					var limit *config.LimitError
					if a != nil || !errors.Is(err, config.ErrLimitExceeded) || !errors.As(err, &limit) ||
						limit.Limit != tc.name || limit.Actual != tc.actual || limit.Maximum != tc.actual-1 {
						t.Fatalf("wrong early limit rejection: %v", err)
					}
				} else if err != nil || len(a.entries) != 2 {
					t.Fatalf("legal exact UTF-8 budget rejected: %v", err)
				}
			})
		}
	}
	t.Run("finite-raw-budget-without-input-limits", func(t *testing.T) {
		for _, max := range []int{-1, 0, len(raw) - 1, len(raw), len(raw) + 1} {
			a, err := parse(raw, max, nil)
			if max < len(raw) {
				if err == nil || a != nil {
					t.Fatalf("accepted raw budget %d", max)
				}
			} else if err != nil || a.entries[0].text != "A😀" {
				t.Fatalf("bounded output parse failed: %v", err)
			}
		}
	})
	t.Run("authored-research-not-default-support", func(t *testing.T) {
		raw := fixture(t, "en.w3strings")
		defaults := config.DefaultLimits()
		assertRejected(t, raw, &defaults)
		mustParse(t, raw)
		a, err := parse(raw, 65536, nil)
		if err != nil {
			t.Fatalf("output observation inherited input budget: %v", err)
		}
		foundLong := false
		for _, e := range a.entries {
			if e.id == 1003 && len(e.text) > defaults.UnitTextBytes {
				foundLong = true
			}
		}
		if !foundLong {
			t.Fatal("output observation lost long ID1003")
		}
	})
	t.Run("late-unit-limit", func(t *testing.T) {
		l := researchLimits()
		l.UnitTextBytes = 1
		assertRejected(t, testFile("jp", []testString{{1, 0, []uint16{'A'}}, {2, 2, []uint16{'B', 'C'}}}, nil, 5), &l)
	})
}

func TestCountBoundaries(t *testing.T) {
	for _, n := range []int{0, 1, 63, 64, 127, 128, 4095} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			raw, p := testCount(n), 0
			got, err := count(raw, &p)
			if err != nil || got != uint32(n) || p != len(raw) {
				t.Fatalf("count=%d consumed=%d: %v", got, p, err)
			}
		})
	}
	for _, raw := range [][]byte{nil, {64}, {64, 0}, {127, 0}, {128}, {255}, {64, 64}, {127, 255}} {
		t.Run(fmt.Sprintf("reject-%x", raw), func(t *testing.T) {
			p := 0
			if _, err := count(raw, &p); err == nil {
				t.Fatal("accepted unsupported/nonminimal count")
			}
		})
	}
	t.Run("maximum-payload-and-tables", func(t *testing.T) {
		entries, keys := make([]testString, 4095), make([]testKey, 4095)
		for i := range entries {
			entries[i] = testString{uint32(i), uint32(i), nil}
			keys[i] = testKey{uint32(i), uint32(i)}
		}
		raw := testFile("jp", entries, keys, 4095)
		// Observation's derived structural maximum, not an Import override.
		a, err := parse(raw, 10+2+12*4095+2+8*4095+2+2*4095+2, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(a.entries) != 4095 || len(a.keys) != 4095 || a.payloadUnits != 4095 {
			t.Fatal("maximum structural counts not retained")
		}
	})
	t.Run("payload-count-widths", func(t *testing.T) {
		for _, n := range []int{0, 63, 64, 4095} {
			a := mustParse(t, testFile("jp", nil, nil, n))
			if a.payloadUnits != uint32(n) || len(a.gaps) != 1 || a.gaps[0].length != uint32(n) {
				t.Fatal("empty tables or opaque payload not preserved")
			}
		}
	})
}

func TestParseLayout(t *testing.T) {
	entries := []testString{
		{10, 8, []uint16{'é'}},
		{30, 1, utf16.Encode([]rune("A😀"))},
		{20, 6, nil},
	}
	keys := []testKey{{15, 30}, {0, 30}}
	raw := testFile("jp", entries, keys, 12)
	a := mustParse(t, raw)
	if a.language != "jp" || !reflect.DeepEqual(a.physical, []int{1, 2, 0}) ||
		!reflect.DeepEqual(a.gaps, []payloadGap{{0, 1}, {5, 1}, {7, 1}, {10, 2}}) {
		t.Fatal("table order and physical gaps conflated")
	}
	for i, e := range entries {
		if a.entries[i].id != e.id || a.entries[i].offset != e.offset || a.entries[i].text != string(utf16.Decode(e.units)) {
			t.Fatal("unsorted table was normalized")
		}
	}
	if a.keys[0].hash != 15 || a.keys[1].hash != 0 || a.keys[1].id != 30 {
		t.Fatal("key order/multiplicity/real hash0 lost")
	}
	for _, gap := range a.gaps {
		for i := uint32(0); i < gap.length; i++ {
			if le.Uint16(a.raw[a.start+int(gap.offset+i)*2:]) != 0xa55a {
				t.Fatal("opaque gap bytes lost")
			}
		}
	}
	if !bytes.Equal(a.raw, raw) {
		t.Fatal("layout snapshot changed")
	}
}
