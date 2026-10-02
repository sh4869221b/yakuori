package main

import (
	"bytes"
	"os"
	"path/filepath"
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
	for _, lang := range []string{"en", "jp"} {
		b, _ := os.ReadFile("testdata/" + lang + ".w3strings")
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
