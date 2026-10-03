package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func fixture(t *testing.T, lang string) []byte {
	t.Helper()
	b, e := os.ReadFile("testdata/" + lang + ".w3strings")
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestIndependentFixtures(t *testing.T) {
	for _, lang := range []string{"en", "jp"} {
		t.Run(lang, func(t *testing.T) {
			b := fixture(t, lang)
			a, err := parse(b)
			if err != nil {
				t.Fatal(err)
			}
			if a.Language != lang || len(a.Entries) != 4 || len(a.Keys) != 3 {
				t.Fatalf("metadata: %+v", a)
			}
			expected := map[uint32]string{1001: "確認用の日本語。😀", 1002: "", 1003: strings.Repeat("長文あいうえお。", 256), 4294967295: "  <b>{player}</b>|%s  "}
			for _, e := range a.Entries {
				want, ok := expected[e.ID]
				if !ok || e.Text != want {
					t.Fatalf("ID %d text mismatch", e.ID)
				}
				delete(expected, e.ID)
			}
			if len(expected) != 0 {
				t.Fatal("missing ID")
			}
			keys := map[uint32]uint32{0x12345678: 1001, 0x87654321: 1002, 0x11223344: 4294967295}
			for _, k := range a.Keys {
				if keys[k.Hash] != k.ID {
					t.Fatal("key mismatch")
				}
			}
			out, err := a.roundtrip()
			if err != nil || !bytes.Equal(out, b) {
				t.Fatalf("roundtrip %v", err)
			}
			// Prove the writer actually re-encrypts decoded text, rather than just copying.
			for i := range a.Entries {
				if a.Entries[i].Length > 0 {
					a.Entries[i].Text = strings.Repeat("x", int(a.Entries[i].Length))
					break
				}
			}
			if _, err = a.roundtrip(); err == nil {
				t.Fatal("changed decoded text escaped no-translation check")
			}
		})
	}
}

type modCSVRow struct {
	id, hash uint32
	text     string
}

func modCSV(data []byte) ([]modCSVRow, error) {
	var rows []modCSVRow
	ids, hashes := map[uint32]bool{}, map[uint32]bool{}
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, ";") {
			continue
		}
		fields := strings.SplitN(line, "|", 4)
		if len(fields) != 4 {
			return nil, fmt.Errorf("line %d: expected four columns", i+1)
		}
		id, err := strconv.ParseUint(strings.Trim(fields[0], " \t"), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("line %d ID: %w", i+1, err)
		}
		var hash uint64
		if hex := strings.Trim(fields[1], " \t"); hex != "" {
			hash, err = strconv.ParseUint(hex, 16, 32)
			if err != nil {
				return nil, fmt.Errorf("line %d key hash: %w", i+1, err)
			}
		}
		row := modCSVRow{id: uint32(id), hash: uint32(hash), text: fields[3]}
		if ids[row.id] || (row.hash != 0 && hashes[row.hash]) {
			return nil, fmt.Errorf("line %d: duplicate ID or key hash", i+1)
		}
		ids[row.id], hashes[row.hash] = true, true
		rows = append(rows, row)
	}
	return rows, nil
}

func compareMOD(a *artifact, source, expected []modCSVRow) error {
	for _, rows := range [][]modCSVRow{source, expected} {
		if len(a.Entries) != len(rows) {
			return fmt.Errorf("entry count: got %d, want %d", len(a.Entries), len(rows))
		}
		texts := map[uint32]string{}
		for _, row := range rows {
			if _, exists := texts[row.id]; exists {
				return fmt.Errorf("duplicate expected ID %d", row.id)
			}
			texts[row.id] = row.text
		}
		for _, entry := range a.Entries {
			text, exists := texts[entry.ID]
			if !exists || entry.Text != text {
				return fmt.Errorf("ID %d: missing or text mismatch", entry.ID)
			}
			delete(texts, entry.ID)
		}
		if len(texts) != 0 {
			return fmt.Errorf("missing entries: %d", len(texts))
		}
	}
	if len(a.Keys) != len(expected) {
		return fmt.Errorf("key count: got %d, want %d", len(a.Keys), len(expected))
	}
	keys := map[uint32]uint32{}
	for _, row := range expected {
		if _, exists := keys[row.hash]; exists {
			return fmt.Errorf("duplicate expected key hash %08x", row.hash)
		}
		keys[row.hash] = row.id
	}
	for _, key := range a.Keys {
		id, exists := keys[key.Hash]
		if !exists || key.ID != id {
			return fmt.Errorf("key %08x: missing or ID mismatch", key.Hash)
		}
		delete(keys, key.Hash)
	}
	if len(keys) != 0 {
		return fmt.Errorf("missing keys: %d", len(keys))
	}
	return nil
}

func TestMODFixtures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
	}{{"better-keybinds", 19}, {"monster-of-the-week", 30}} {
		t.Run(tc.name, func(t *testing.T) {
			b := fixture(t, tc.name+"/en")
			a, err := parse(b)
			if err != nil {
				t.Fatal(err)
			}
			if a.Language != "en" || len(a.Entries) != tc.count || len(a.Keys) != tc.count {
				t.Fatalf("metadata: language %s, entries %d, keys %d", a.Language, len(a.Entries), len(a.Keys))
			}
			var csvs [][]modCSVRow
			for _, kind := range []string{"source", "expected"} {
				data, err := os.ReadFile("testdata/" + tc.name + "/en." + kind + ".csv")
				if err != nil {
					t.Fatal(err)
				}
				rows, err := modCSV(data)
				if err != nil {
					t.Fatal(err)
				}
				csvs = append(csvs, rows)
			}
			source, expected := csvs[0], csvs[1]
			if err := compareMOD(a, source, expected); err != nil {
				t.Fatal(err)
			}
			out, err := a.roundtrip()
			if err != nil || !bytes.Equal(out, b) {
				t.Fatalf("roundtrip %v", err)
			}
			for _, kind := range []string{"text", "key"} {
				t.Run("reject-changed-"+kind, func(t *testing.T) {
					changed := slices.Clone(expected)
					switch kind {
					case "text":
						changed[0].text += " changed"
					case "key":
						changed[0].hash ^= 1
					}
					if err := compareMOD(a, source, changed); err == nil {
						t.Fatalf("accepted changed expected %s", kind)
					}
				})
			}
		})
	}
}

func TestRejectMalformed(t *testing.T) {
	b := fixture(t, "jp")
	a, err := parse(b)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func([]byte) []byte{
		"version":         func(v []byte) []byte { v[4] = 0; return v },
		"language":        func(v []byte) []byte { v[8] = 0; return v },
		"count":           func(v []byte) []byte { v[10] = 63; return v },
		"unsupported-vlq": func(v []byte) []byte { v[10] = 128; return v },
		"duplicate-id": func(v []byte) []byte {
			copy(v[a.Entries[1].table:], v[a.Entries[0].table:a.Entries[0].table+4])
			return v
		},
		"offset":        func(v []byte) []byte { le.PutUint32(v[a.Entries[0].table+4:], 0xffffffff); return v },
		"length":        func(v []byte) []byte { le.PutUint32(v[a.Entries[0].table+8:], 0xffffffff); return v },
		"overlap":       func(v []byte) []byte { le.PutUint32(v[a.Entries[1].table+4:], a.Entries[0].Offset); return v },
		"key-id":        func(v []byte) []byte { le.PutUint32(v[a.Keys[0].table+4:], 555^a.magic); return v },
		"duplicate-key": func(v []byte) []byte { le.PutUint32(v[a.Keys[1].table:], a.Keys[0].Hash); return v },
		"terminator":    func(v []byte) []byte { v[a.start+int(a.Entries[0].Offset+a.Entries[0].Length)*2] = 1; return v },
		"surrogate": func(v []byte) []byte {
			e := a.Entries[0]
			le.PutUint16(v[a.start+int(e.Offset)*2:], 0xdc00^uint16((e.Length+1)*uint32(uint16(a.magic>>8))))
			return v
		},
		"NUL": func(v []byte) []byte {
			e := a.Entries[0]
			le.PutUint16(v[a.start+int(e.Offset)*2:], uint16((e.Length+1)*uint32(uint16(a.magic>>8))))
			return v
		},
		"trailing": func(v []byte) []byte { return append(v, 0) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parse(mutate(bytes.Clone(b))); err == nil {
				t.Fatal("accepted malformed input")
			}
		})
	}
	for i := 0; i < len(b); i++ {
		if _, err := parse(b[:i]); err == nil {
			t.Fatalf("accepted truncation %d", i)
		}
	}
	if _, err := parse(make([]byte, maxFile+1)); err == nil {
		t.Fatal("accepted oversized input")
	}
}
func TestOutputProtection(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in")
	out := filepath.Join(dir, "out")
	b := fixture(t, "en")
	if err := os.WriteFile(in, b, 0600); err != nil {
		t.Fatal(err)
	}
	var report bytes.Buffer
	if err := run([]string{in, out}, &report); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{in, out}, &report); err == nil {
		t.Fatal("overwrote output")
	}
	if err := run([]string{in, in}, &report); err == nil {
		t.Fatal("overwrote input")
	}
	got, _ := os.ReadFile(in)
	if !bytes.Equal(got, b) {
		t.Fatal("input changed")
	}
}
func FuzzParse(f *testing.F) {
	for _, name := range []string{"en", "jp", "better-keybinds/en", "monster-of-the-week/en"} {
		b, err := os.ReadFile("testdata/" + name + ".w3strings")
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		a, err := parse(b)
		if err != nil {
			return
		}
		out, err := a.roundtrip()
		if err != nil || !bytes.Equal(out, b) {
			t.Fatalf("parse/roundtrip disagreement: %v", err)
		}
	})
}

func TestCountBoundaries(t *testing.T) {
	for _, tc := range []struct {
		encoded []byte
		want    uint32
	}{
		{[]byte{0}, 0}, {[]byte{1}, 1}, {[]byte{63}, 63}, {[]byte{64, 1}, 64},
		{[]byte{127, 1}, 127}, {[]byte{64, 2}, 128}, {[]byte{127, 63}, 4095},
	} {
		p := 0
		got, err := count(tc.encoded, &p)
		if err != nil || got != tc.want || p != len(tc.encoded) {
			t.Fatalf("%x: %d, %d, %v", tc.encoded, got, p, err)
		}
	}
	for _, encoded := range [][]byte{{}, {64}, {64, 0}, {128}, {64, 64}} {
		p := 0
		if _, err := count(encoded, &p); err == nil {
			t.Fatalf("accepted %x", encoded)
		}
	}
}
func TestLayoutPreservation(t *testing.T) {
	original := fixture(t, "jp")
	a, err := parse(original)
	if err != nil {
		t.Fatal(err)
	}
	reordered := bytes.Clone(original)
	// Swap records without changing the referenced string offsets or key relation.
	x, y := a.Entries[0].table, a.Entries[len(a.Entries)-1].table
	copy(reordered[x:x+12], original[y:y+12])
	copy(reordered[y:y+12], original[x:x+12])
	x, y = a.Keys[0].table, a.Keys[len(a.Keys)-1].table
	copy(reordered[x:x+8], original[y:y+8])
	copy(reordered[y:y+8], original[x:x+8])
	// Append two opaque payload bytes, increasing only the aggregate payload count.
	gap := append(bytes.Clone(original[:len(original)-2]), 0xa5, 0x5a)
	gap = append(gap, original[len(original)-2:]...)
	n := uint32((len(gap) - a.start - 2) / 2)
	if original[a.start-2] < 64 || original[a.start-1] >= 64 || n >= 4096 {
		t.Fatal("test requires two-byte payload count")
	}
	gap[a.start-2] = byte(n&63) | 64
	gap[a.start-1] = byte(n >> 6)
	for name, b := range map[string][]byte{"table-order": reordered, "opaque-payload-gap": gap} {
		t.Run(name, func(t *testing.T) {
			parsed, err := parse(b)
			if err != nil {
				t.Fatal(err)
			}
			expected := map[uint32]string{}
			for _, e := range a.Entries {
				expected[e.ID] = e.Text
			}
			for _, e := range parsed.Entries {
				if e.Text != expected[e.ID] {
					t.Fatal("layout changed text")
				}
			}
			out, err := parsed.roundtrip()
			if err != nil || !bytes.Equal(out, b) {
				t.Fatalf("layout normalized: %v", err)
			}
		})
	}
}
