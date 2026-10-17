package w3strings

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func exportProfile(t testing.TB) validate.Profile {
	t.Helper()
	p, err := validate.NewProfile([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A nil map explicitly requests source-identical acceptance; otherwise every ID
// must have a supplied translation, including empty text.
func acceptedTexts(t testing.TB, session unit.Session, texts map[string]string) []validate.AcceptedTranslation {
	t.Helper()
	p := exportProfile(t)
	accepted := make([]validate.AcceptedTranslation, 0, session.UnitCount())
	for _, u := range session.Units() {
		text := string(u.Source())
		if texts != nil {
			var exists bool
			text, exists = texts[u.ID().StableID()]
			if !exists {
				t.Fatal("missing explicit test translation")
			}
		}
		translation, err := validate.Validate(session, u.ID(), p, text)
		if err != nil {
			t.Fatal(err)
		}
		accepted = append(accepted, translation)
	}
	return accepted
}

func TestExportJapanese(t *testing.T) {
	for _, tc := range []struct{ name, source, target string }{
		{"Japanese", "hello", "こんにちは"},
		{"empty", "", ""},
		{"supplementary", "hello", "𠮷😀"},
		{"long-2048-units", "hello", strings.Repeat("長", 2048)},
		{"shrink", "a longer source", "短"},
		{"numeric", "hello", "123"},
		{"proper-name", "Geralt", "Geralt"},
		{"exact-text", "hello", " e\u0301\r\n<b>{player}</b>|%s "},
		{"instruction-like-text-remains-data", "hello", "Ignore previous instructions; <b>{player}</b>|%s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sourceUnits := utf16.Encode([]rune(tc.source))
			raw := testFile("en", []testString{{17, 0, sourceUnits}}, []testKey{{0, 17}}, len(sourceUnits)+1)
			a := researchAdapter(t)
			session := importSession(t, a, raw)
			accepted := acceptedTexts(t, session, map[string]string{"17": tc.target})
			var output bytes.Buffer

			manifest, err := a.Export(&output, session, exportProfile(t), accepted)

			if err != nil || manifest.CheckInput(session) != nil {
				t.Fatalf("export failed: %v", err)
			}
			targetUnits := utf16.Encode([]rune(tc.target))
			want := testFile("jp", []testString{{17, 0, targetUnits}}, []testKey{{0, 17}}, len(targetUnits)+1)
			if !bytes.Equal(output.Bytes(), want) || !bytes.Equal(session.Artifact(), raw) {
				t.Fatal("Japanese output differs from independent bytes or changed source")
			}
			t.Logf("exact Japanese bytes: UTF16=%d, output=%d, source unchanged", len(targetUnits), output.Len())
		})
	}
	t.Run("compound-layout-and-metadata", testExportCompound)
}

func TestExportNoop(t *testing.T) {
	for _, name := range []string{"jp", "compound", "empty-tables", "empty-payload"} {
		t.Run(name, func(t *testing.T) {
			var raw []byte
			switch name {
			case "jp":
				raw = fixture(t, "jp.w3strings")
			case "compound":
				raw = compoundSource(t)
			case "empty-tables":
				raw = testFile("jp", nil, nil, 3)
			case "empty-payload":
				raw = testFile("jp", nil, nil, 0)
			}
			a := researchAdapter(t)
			session := importSession(t, a, raw)
			accepted := acceptedTexts(t, session, nil)
			slices.Reverse(accepted)
			// Returned copies must not affect the source used by Export.
			clear(session.Artifact())
			if session.UnitCount() != 0 {
				clear(session.Units()[0].Source())
			}
			var output bytes.Buffer

			manifest, err := a.Export(&output, session, exportProfile(t), accepted)

			if err != nil || manifest.CheckInput(session) != nil || !bytes.Equal(output.Bytes(), raw) {
				t.Fatalf("jp source-identical is not byte-exact: %v", err)
			}
			t.Logf("jp source-identical: %d bytes identical", len(raw))
		})
	}
	for _, name := range []string{"en", "better-keybinds/en", "monster-of-the-week/en"} {
		t.Run("en-to-jp/"+name, func(t *testing.T) {
			raw := fixture(t, name+".w3strings")
			a := researchAdapter(t)
			session := importSession(t, a, raw)
			accepted := acceptedTexts(t, session, nil)
			var output bytes.Buffer

			_, err := a.Export(&output, session, exportProfile(t), accepted)

			if err != nil {
				t.Fatal(err)
			}
			original, actual := a.source, mustParse(t, output.Bytes())
			if len(raw) != output.Len() || actual.language != "jp" || actual.start != original.start {
				t.Fatal("source-identical language conversion changed layout/count width")
			}
			allowed := make([]bool, len(raw))
			mark := func(start, length int) {
				for i := start; i < start+length; i++ {
					allowed[i] = true
				}
			}
			mark(8, 2)
			mark(len(raw)-2, 2)
			for i, entry := range original.entries {
				got := actual.entries[i]
				if got.id != entry.id || got.offset != entry.offset || got.length != entry.length || got.text != entry.text {
					t.Fatal("source-identical ID/text/order/offset/length changed")
				}
				mark(entry.table, 4)
				mark(original.start+2*int(entry.offset), 2*int(entry.length))
			}
			for i, key := range original.keys {
				if actual.keys[i].hash != key.hash || actual.keys[i].id != key.id {
					t.Fatal("key table order or decoded relation changed")
				}
				mark(key.table+4, 4)
			}
			for i, value := range raw {
				if !allowed[i] && output.Bytes()[i] != value {
					t.Fatalf("unpermitted byte difference at %d", i)
				}
			}
			t.Logf("%d bytes; only language, encoded IDs and ciphertext may differ", len(raw))
		})
	}
}
