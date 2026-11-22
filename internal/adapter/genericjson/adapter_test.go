package genericjson

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

var expected = Metadata{
	SourceLanguage: "en", TargetLanguage: "ja",
	GameNamespace: "sample-game", ArtifactNamespace: "dialogue",
}

const header = `{"schema":"yakuori.generic-json","version":1,"source_language":"en","target_language":"ja","scope":{"game_namespace":"sample-game","artifact_namespace":"dialogue"},"units":`

func setup(t *testing.T, limits config.Limits) *Adapter {
	t.Helper()
	a, err := New(expected, limits)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func importOK(t *testing.T, a *Adapter, raw []byte) unit.Session {
	t.Helper()
	s, err := a.Import(raw, validate.Profile{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
	return s
}

func reject(t *testing.T, a *Adapter, raw []byte, want error) {
	t.Helper()
	s, err := a.Import(raw, validate.Profile{})
	if err == nil || (want != nil && !errors.Is(err, want)) || s.Check() == nil {
		t.Fatalf("Import error = %v, session error = %v, want %v and no session", err, s.Check(), want)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatalf("diagnostic disclosed input: %v", err)
	}
}

func TestImportMinimalV1(t *testing.T) {
	raw, err := os.ReadFile("testdata/minimal-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	wantArtifact := bytes.Clone(raw)
	a := setup(t, config.DefaultLimits())
	s := importOK(t, a, raw)
	raw[0] = '?'
	s.Artifact()[0] = '!'
	if !bytes.Equal(s.Artifact(), wantArtifact) || s.ArtifactBytes() != 652 || s.UnitCount() != 2 {
		t.Fatal("incorrect artifact snapshot")
	}

	wantSource := []byte("  Cafe\u0301 {name}\r\n")
	wantSpans := []unit.ProtectionSpan{{Start: 9, End: 15, Kind: unit.Placeholder}}
	wantContext := []map[string]string{
		{"speaker": "keeper", "scene": "inn"},
		{"speaker": "guard", "scene": "gate"},
	}
	for i, stableID := range []string{"001", "002"} {
		id, err := unit.NewUnitID("generic-json", "generic-json-v1", stableID)
		if err != nil {
			t.Fatal(err)
		}
		u, err := s.Unit(id)
		if err != nil {
			t.Fatal(err)
		}
		if s.Units()[i].ID() != id || !bytes.Equal(u.Source(), wantSource) ||
			u.SourceLanguage() != "en" || u.TargetLanguage() != "ja" ||
			!slices.Equal(u.ProtectionSpans(), wantSpans) || !maps.Equal(u.Origin(), wantContext[i]) {
			t.Fatalf("incorrect snapshot for unit %d", i)
		}
		u.Origin()["speaker"] = "changed"
		if !maps.Equal(u.Origin(), wantContext[i]) {
			t.Fatalf("origin changed for unit %d", i)
		}
	}

	id := s.Units()[0].ID()
	original, err := unit.NewSourceIdentity(s, id, expected.GameNamespace, expected.ArtifactNamespace, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range [][2]string{
		{"other-game", expected.ArtifactNamespace},
		{expected.GameNamespace, "other-artifact"},
	} {
		other, err := unit.NewSourceIdentity(s, id, scope[0], scope[1], nil)
		if err != nil {
			t.Fatal(err)
		}
		if other == original {
			t.Fatal("source identity reused across a scope change")
		}
	}
	other, err := unit.NewSourceIdentity(s, s.Units()[1].ID(), expected.GameNamespace, expected.ArtifactNamespace, nil)
	if err != nil || other == original {
		t.Fatalf("source identity reused across stable IDs, error = %v", err)
	}
	again := importOK(t, a, wantArtifact)
	matching, err := unit.NewSourceIdentity(again, id, expected.GameNamespace, expected.ArtifactNamespace, nil)
	if err != nil || matching != original || s.SameImport(again) {
		t.Fatalf("identical artifact did not retain its source identity across distinct imports, error = %v", err)
	}
}

func TestImportUnicodeAndOptionalTranslation(t *testing.T) {
	raw := []byte(header + `[{"id":" 0\u0031 ","source":"\ud83d\ude00\ufffd�\\ud800","translation":"unverified"},
		{"id":"blank","source":""}]}`)
	s := importOK(t, setup(t, config.DefaultLimits()), raw)
	units := s.Units()
	if units[0].ID().StableID() != " 01 " || string(units[0].Source()) != "😀��\\ud800" ||
		units[1].SourceBytes() != 0 || !bytes.Equal(s.Artifact(), raw) {
		t.Fatal("Unicode, optional translation or empty source was changed")
	}
}

func TestImportRejectsMalformedV1(t *testing.T) {
	const valid = header + `[{"id":"private-id","source":"private text"}]}`
	a := setup(t, config.DefaultLimits())
	for _, test := range []struct {
		name, raw string
		want      error
	}{
		{"unknown-schema", strings.Replace(valid, "yakuori.generic-json", "private schema", 1), errVersion},
		{"unknown-version", strings.Replace(valid, `"version":1`, `"version":2`, 1), errVersion},
		{"version-string", strings.Replace(valid, `"version":1`, `"version":"1"`, 1), errJSON},
		{"version-fraction", strings.Replace(valid, `"version":1`, `"version":1.0`, 1), errJSON},
		{"version-exponent", strings.Replace(valid, `"version":1`, `"version":1e0`, 1), errJSON},
		{"malformed", valid[:len(valid)-1], errJSON},
		{"trailing-value", valid + "{}\n", errJSON},
		{"trailing-garbage", valid + " private", errJSON},
		{"duplicate-top", strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1), errDuplicate},
		{"duplicate-scope", strings.Replace(valid, `"game_namespace":"sample-game"`, `"game_namespace":"sample-game","game_namespace":"sample-game"`, 1), errDuplicate},
		{"duplicate-escaped-key", header + `[{"id":"a","\u0069d":"b","source":""}]}`, errDuplicate},
		{"duplicate-context-key", header + `[{"id":"a","source":"","context":{"speaker":"a","speaker":"b"}}]}`, errDuplicate},
		{"duplicate-span-key", header + `[{"id":"a","source":"x","protection":[{"start":0,"start":0,"end":1,"kind":"placeholder","ordered":false}]}]}`, errDuplicate},
		{"duplicate-id", header + `[{"id":"private","source":""},{"id":"pr\u0069vate","source":"x"}]}`, unit.ErrDuplicateUnit},
		{"missing-id", header + `[{"source":"x"}]}`, errRequired},
		{"empty-id", header + `[{"id":"","source":"x"}]}`, errJSON},
		{"extra-id-field", header + `[{"id":"a","ID":"b","source":"x"}]}`, errJSON},
		{"numeric-id", header + `[{"id":1,"source":"x"}]}`, errJSON},
		{"missing-source", header + `[{"id":"a"}]}`, errRequired},
		{"missing-scope-field", strings.Replace(valid, `,"artifact_namespace":"dialogue"`, "", 1), errRequired},
		{"unknown-field", strings.Replace(valid, `"version":1`, `"version":1,"private":false`, 1), errJSON},
		{"unknown-context-field", header + `[{"id":"a","source":"","context":{"speaker":"a","private":"b"}}]}`, errJSON},
		{"unknown-span-field", header + `[{"id":"a","source":"x","protection":[{"start":0,"end":1,"kind":"placeholder","ordered":false,"private":1}]}]}`, errJSON},
		{"no-units", header + `[]}`, errJSON},
		{"null-units", header + `null}`, errJSON},
		{"null-unit", header + `[null]}`, errJSON},
		{"null-source", header + `[{"id":"a","source":null}]}`, errJSON},
		{"null-translation", header + `[{"id":"a","source":"","translation":null}]}`, errJSON},
		{"null-protection", header + `[{"id":"a","source":"","protection":null}]}`, errJSON},
		{"null-context", header + `[{"id":"a","source":"","context":null}]}`, errJSON},
		{"null-reference", header + `[{"id":"a","source":"","context":{"scene":null}}]}`, errJSON},
		{"empty-reference", header + `[{"id":"a","source":"","context":{"scene":""}}]}`, errJSON},
		{"source-language-mismatch", strings.Replace(valid, `"en"`, `"EN"`, 1), errMetadataMismatch},
		{"target-language-mismatch", strings.Replace(valid, `"ja"`, `"ja "`, 1), errMetadataMismatch},
		{"game-mismatch", strings.Replace(valid, `"sample-game"`, `"sample-game "`, 1), errMetadataMismatch},
		{"artifact-mismatch", strings.Replace(valid, `"dialogue"`, `"other"`, 1), errMetadataMismatch},
		{"empty-language", strings.Replace(valid, `"en"`, `""`, 1), errJSON},
		{"raw-utf8", header + `[{"id":"a","source":"` + "\xff" + `"}]}`, errJSON},
		{"lone-high-surrogate", header + `[{"id":"a","source":"\uD800\uFFFD"}]}`, errJSON},
		{"lone-low-surrogate", header + `[{"id":"a","source":"\uDE00"}]}`, errJSON},
		{"escaped-lone-high-surrogate", header + `[{"id":"a","source":"\uD800\\uDE00"}]}`, errJSON},
		{"escaped-nul", header + `[{"id":"a","source":"x\u0000"}]}`, errJSON},
		{"deep-source", header + `[{"id":"a","source":` + strings.Repeat("[", 500) + "0" + strings.Repeat("]", 500) + "}]}", errJSON},
		{"depth-after-span", header + `[{"id":"a","source":"x","protection":[{"start":[0],"end":1,"kind":"placeholder","ordered":false}]}]}`, errJSON},
	} {
		t.Run(test.name, func(t *testing.T) { reject(t, a, []byte(test.raw), test.want) })
	}
}

func TestImportProtectionV1(t *testing.T) {
	const open = `{"start":0,"end":3,"kind":"open_tag","ordered":true,"pair":"b"}`
	const close = `{"start":4,"end":8,"kind":"close_tag","ordered":true,"pair":"b"}`
	const tagged = header + `[{"id":"a","source":"<b>x</b>","protection":[`
	a := setup(t, config.DefaultLimits())
	s := importOK(t, a, []byte(tagged+open+","+close+"]}]}"))
	u := s.Units()[0]
	want := []unit.ProtectionSpan{
		{Start: 0, End: 3, Kind: unit.OpenTag, Ordered: true, Pair: "b"},
		{Start: 4, End: 8, Kind: unit.CloseTag, Ordered: true, Pair: "b"},
	}
	if !slices.Equal(u.ProtectionSpans(), want) {
		t.Fatal("ordered tag declarations were not retained")
	}
	if _, err := protect.Prepare(s, u.ID()); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name, source, spans string
		want                error
	}{
		{"outside-source", "a", `{"start":0,"end":2,"kind":"placeholder","ordered":false}`, unit.ErrInvalidProtectionSpan},
		{"negative", "a", `{"start":-1,"end":1,"kind":"placeholder","ordered":false}`, unit.ErrInvalidProtectionSpan},
		{"reversed", "ab", `{"start":1,"end":0,"kind":"placeholder","ordered":false}`, unit.ErrInvalidProtectionSpan},
		{"fractional-offset", "a", `{"start":0,"end":1.0,"kind":"placeholder","ordered":false}`, errJSON},
		{"inside-rune", "界", `{"start":0,"end":2,"kind":"placeholder","ordered":false}`, unit.ErrInvalidProtectionSpan},
		{"unknown-kind", "a", `{"start":0,"end":1,"kind":"private","ordered":false}`, unit.ErrInvalidProtectionSpan},
		{"missing-ordered", "a", `{"start":0,"end":1,"kind":"placeholder"}`, errRequired},
		{"pair-on-placeholder", "a", `{"start":0,"end":1,"kind":"placeholder","ordered":false,"pair":"a"}`, unit.ErrInvalidProtectionSpan},
		{"overlap", "<b>x</b>", open + "," + strings.Replace(close, "4", "2", 1), unit.ErrInvalidProtectionSpan},
		{"unsorted", "<b>x</b>", close + "," + open, unit.ErrInvalidProtectionSpan},
		{"unordered-tag", "<b>x</b>", strings.Replace(open, "true", "false", 1) + "," + close, unit.ErrInvalidProtectionSpan},
		{"unclosed-tag", "<b>x", open, protect.ErrInvalidSource},
		{"wrong-pair", "<b>x</b>", open + "," + strings.Replace(close, `"b"`, `"x"`, 1), protect.ErrInvalidSource},
		{"undeclared-literal", "aaa", `{"start":0,"end":2,"kind":"placeholder","ordered":false}`, protect.ErrInvalidSource},
		{"undeclared-prefix", "axa", `{"start":2,"end":3,"kind":"placeholder","ordered":false}`, protect.ErrInvalidSource},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := fmt.Sprintf(header+`[{"id":"a","source":%q,"protection":[%s]}]}`, test.source, test.spans)
			reject(t, a, []byte(raw), test.want)
		})
	}
}

func TestImportV1Limits(t *testing.T) {
	const compact = header + `[{"id":"x","source":""}]}`
	source := func(n int) string { return "界" + strings.Repeat("x", n-3) }
	units := func(n int) string {
		var entries []string
		for i := range n {
			entries = append(entries, fmt.Sprintf(`{"id":"%d","source":""}`, i))
		}
		return header + "[" + strings.Join(entries, ",") + "]}"
	}
	spans := func(n int) string {
		var spans []string
		for i := range n {
			spans = append(spans, fmt.Sprintf(`{"start":%d,"end":%d,"kind":"placeholder","ordered":false}`, i, i+1))
		}
		return fmt.Sprintf(header+`[{"id":"x","source":%q,"protection":[%s]}]}`, strings.Repeat("x", n), strings.Join(spans, ","))
	}
	defaults := config.DefaultLimits()
	for _, test := range []struct {
		name  string
		limit int
		input func(int) string
	}{
		{"artifact bytes", defaults.ArtifactBytes, func(n int) string { return compact + strings.Repeat(" ", n-len(compact)) }},
		{"units", defaults.Units, units},
		{"unit text bytes", defaults.UnitTextBytes, func(n int) string {
			return fmt.Sprintf(header+`[{"id":"x","source":%q}]}`, source(n))
		}},
		{"total text bytes", defaults.TotalTextBytes, func(n int) string {
			return fmt.Sprintf(header+`[{"id":"x","source":%q},{"id":"y","source":%q}]}`, source(n/2), source(n-n/2))
		}},
		{"protection spans", maxSpans, spans},
	} {
		t.Run(test.name, func(t *testing.T) {
			limits := defaults
			if test.name == "units" || test.name == "protection spans" {
				limits.ArtifactBytes = len(test.input(test.limit + 1))
			}
			a := setup(t, limits)
			importOK(t, a, []byte(test.input(test.limit)))
			s, err := a.Import([]byte(test.input(test.limit+1)), validate.Profile{})
			var bound *config.LimitError
			if !errors.As(err, &bound) || bound.Limit != test.name ||
				bound.Actual != test.limit+1 || bound.Maximum != test.limit || s.Check() == nil {
				t.Fatalf("wrong limit error or session retained: %v", err)
			}
		})
	}

	t.Run("metadata and optional strings use decoded byte limits", func(t *testing.T) {
		limits := defaults
		limits.UnitTextBytes = 11
		a := setup(t, limits)
		const exact = `\u754c12345678`
		const large = exact + "9"
		valid := header + `[{"id":"` + exact + `","source":"","translation":"` + exact + `","context":{"speaker":"` + exact + `"}}]}`
		importOK(t, a, []byte(valid))
		for _, field := range []string{"id", "translation", "speaker"} {
			raw := strings.Replace(valid, `"`+field+`":"`+exact+`"`, `"`+field+`":"`+large+`"`, 1)
			reject(t, a, []byte(raw), config.ErrLimitExceeded)
		}
	})
}

func TestNewRequiresExplicitConfiguration(t *testing.T) {
	limits := config.DefaultLimits()
	for _, missing := range []Metadata{
		{TargetLanguage: "ja", GameNamespace: "g", ArtifactNamespace: "a"},
		{SourceLanguage: "en", GameNamespace: "g", ArtifactNamespace: "a"},
		{SourceLanguage: "en", TargetLanguage: "ja", ArtifactNamespace: "a"},
		{SourceLanguage: "en", TargetLanguage: "ja", GameNamespace: "g"},
	} {
		if a, err := New(missing, limits); err == nil || a != nil {
			t.Fatal("constructor accepted incomplete metadata")
		}
	}
	if a, err := New(expected, config.Limits{}); !errors.Is(err, config.ErrInvalidLimits) || a != nil {
		t.Fatalf("constructor accepted invalid limits: %v", err)
	}
}
