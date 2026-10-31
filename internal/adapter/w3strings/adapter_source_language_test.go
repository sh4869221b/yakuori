package w3strings

import (
	"bytes"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"unicode/utf16"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func TestImportSourceLanguage(t *testing.T) {
	t.Run("matching", func(t *testing.T) {
		hello, konnichiwa := utf16.Encode([]rune("Hello")), utf16.Encode([]rune("こんにちは"))
		for _, tc := range []struct {
			name, binary, declared string
			entries                []testString
			keys                   []testKey
			payload                int
			texts                  []string
		}{
			{"en", "en", "en", []testString{{7, 0, hello}}, []testKey{{0x11223344, 7}}, len(hello) + 1, []string{"Hello"}},
			{"jp-as-ja", "jp", "ja", []testString{{1003, 0, konnichiwa}}, []testKey{{0x89abcdef, 1003}}, len(konnichiwa) + 1, []string{"こんにちは"}},
			{"en-zero-table", "en", "en", nil, nil, 3, nil},
			{"jp-zero-table", "jp", "ja", nil, nil, 3, nil},
		} {
			raw := testFile(tc.binary, tc.entries, tc.keys, tc.payload)
			a, err := NewForSource(tc.declared, config.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			session := importSession(t, a, raw)

			if session.UnitCount() != len(tc.entries) {
				t.Fatalf("unit count = %d, want %d", session.UnitCount(), len(tc.entries))
			}
			for i, u := range session.Units() {
				if u.SourceLanguage() != tc.declared || u.TargetLanguage() != "ja" ||
					string(u.Source()) != tc.texts[i] ||
					u.ID().StableID() != strconv.FormatUint(uint64(tc.entries[i].id), 10) {
					t.Fatalf("unit %d = %s/%s/%q/%s", i, u.SourceLanguage(), u.TargetLanguage(),
						u.Source(), u.ID().StableID())
				}
			}
			keys := relations(t, a, session)
			if len(keys) != len(tc.keys) {
				t.Fatalf("key count = %d, want %d", len(keys), len(tc.keys))
			}
			for i, key := range keys {
				var want [4]byte
				le.PutUint32(want[:], tc.keys[i].hash)
				if key.Key != string(want[:]) ||
					key.UnitID.StableID() != strconv.FormatUint(uint64(tc.keys[i].id), 10) {
					t.Fatalf("key %d = %x/%s", i, key.Key, key.UnitID.StableID())
				}
			}
			if !bytes.Equal(session.Artifact(), raw) {
				t.Fatal("matching import did not retain the source artifact")
			}

			var output bytes.Buffer
			accepted := acceptedTexts(t, session, nil)
			manifest, err := a.Export(&output, session, exportProfile(t), accepted)
			if err != nil || manifest.CheckInput(session) != nil {
				t.Fatalf("export after matching import: %v", err)
			}
			exported := mustParse(t, output.Bytes())
			if exported.language != "jp" || len(exported.entries) != len(tc.texts) {
				t.Fatalf("exported header/entries = %s/%d", exported.language, len(exported.entries))
			}
			for i, entry := range exported.entries {
				if entry.text != tc.texts[i] {
					t.Fatalf("exported text %d = %q, want %q", i, entry.text, tc.texts[i])
				}
			}
			t.Logf("%s matching: %d units, %d keys, exported %d bytes", tc.name, session.UnitCount(), len(keys), output.Len())
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		text := utf16.Encode([]rune("Hello"))
		for _, tc := range []struct {
			name, binary, declared string
			entries                []testString
			keys                   []testKey
			payload                int
		}{
			{"en-as-ja", "en", "ja", []testString{{7, 0, text}}, []testKey{{0x11223344, 7}}, len(text) + 1},
			{"jp-as-en", "jp", "en", []testString{{7, 0, text}}, []testKey{{0x89abcdef, 7}}, len(text) + 1},
			{"en-zero-as-ja", "en", "ja", nil, nil, 3},
			{"jp-zero-as-en", "jp", "en", nil, nil, 3},
		} {
			a, err := NewForSource(tc.declared, config.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}

			session, err := a.Import(testFile(tc.binary, tc.entries, tc.keys, tc.payload), validate.Profile{})

			if err == nil || session.Check() == nil {
				t.Fatalf("%s mismatched header accepted: %v", tc.name, err)
			}
			if a.session.Check() == nil || a.source != nil || a.keys != nil {
				t.Fatalf("%s rejected header published import state", tc.name)
			}
			t.Logf("%s mismatch: rejected before publishing", tc.name)
		}
	})

	t.Run("constructor", func(t *testing.T) {
		for _, language := range []string{"", "jp", "EN", "fr", " ja"} {
			a, err := NewForSource(language, config.DefaultLimits())
			if err == nil || a != nil {
				t.Fatalf("source declaration %q accepted", language)
			}
		}
		invalidGeneration := config.DefaultLimits()
		invalidGeneration.GenerationTimeout = 0
		for _, limits := range []config.Limits{{}, invalidGeneration} {
			a, err := NewForSource("en", limits)
			if !errors.Is(err, config.ErrInvalidLimits) || a != nil {
				t.Fatalf("invalid limits accepted: %v", err)
			}
		}
		for _, language := range []string{"en", "ja"} {
			a, err := NewForSource(language, config.DefaultLimits())
			if err != nil || a == nil || a.expectedSource != language || a.limits != config.DefaultLimits() {
				t.Fatalf("valid source declaration %q: %v", language, err)
			}
		}

		legacy, err := NewWithLimits(config.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		for _, binary := range []string{"en", "jp"} {
			for _, a := range []*Adapter{New(), legacy} {
				session := importSession(t, a, testFile(binary, nil, nil, 3))
				if session.UnitCount() != 0 || a.source.language != binary {
					t.Fatalf("legacy constructor changed the %s empty table", binary)
				}
				if a.expectedSource != "" {
					t.Fatal("legacy constructor restricted source language")
				}
				if _, err := a.KeyRelations(session); err != nil {
					t.Fatal(err)
				}
			}
		}
	})

	t.Run("preserve_previous", func(t *testing.T) {
		a, err := NewForSource("en", config.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		raw := testFile("en", []testString{{7, 0, utf16.Encode([]rune("Hello"))}}, []testKey{{0x11223344, 7}}, 6)
		first := importSession(t, a, raw)
		keys := relations(t, a, first)
		export := func(session unit.Session) []byte {
			t.Helper()
			var output bytes.Buffer
			manifest, err := a.Export(&output, session, exportProfile(t), acceptedTexts(t, session, nil))
			if err != nil || manifest.CheckInput(session) != nil {
				t.Fatalf("export current import: %v", err)
			}
			return bytes.Clone(output.Bytes())
		}
		before := export(first)

		mismatched := testFile("jp", []testString{{7, 0, utf16.Encode([]rune("こんにちは"))}}, []testKey{{0x89abcdef, 7}}, 6)
		rejected, err := a.Import(mismatched, validate.Profile{})

		if err == nil || rejected.Check() == nil {
			t.Fatalf("mismatched header accepted: %v", err)
		}
		if !a.session.SameImport(first) || !bytes.Equal(a.source.raw, raw) ||
			!reflect.DeepEqual(relations(t, a, first), keys) {
			t.Fatal("mismatched import replaced prior session, source or keys")
		}
		if got, err := a.KeyRelations(rejected); !errors.Is(err, unit.ErrInvalidSession) || got != nil {
			t.Fatalf("rejected session gained key access: %v", err)
		}
		if after := export(first); !bytes.Equal(before, after) {
			t.Fatal("mismatched import changed the prior export result")
		}
		t.Logf("prior %d-unit import, %d keys and %d-byte export preserved", first.UnitCount(), len(keys), len(before))
	})
}
