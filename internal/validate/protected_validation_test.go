package validate

import (
	"errors"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/unit"
)

var nestedSpans = []unit.ProtectionSpan{
	{Start: 0, End: 3, Kind: unit.OpenTag, Pair: "b", Ordered: true},
	{Start: 3, End: 6, Kind: unit.OpenTag, Pair: "i", Ordered: true},
	{Start: 12, End: 15, Kind: unit.Placeholder},
	{Start: 15, End: 19, Kind: unit.CloseTag, Pair: "i", Ordered: true},
	{Start: 19, End: 23, Kind: unit.CloseTag, Pair: "b", Ordered: true},
}

func protectedFixture(t *testing.T, source string, spans []unit.ProtectionSpan) (unit.Session, unit.UnitID, Profile) {
	t.Helper()
	id, err := unit.NewUnitID("fixture", "v1", "protected")
	if err != nil {
		t.Fatal(err)
	}
	u, err := unit.NewTranslationUnitWithProtection(id, []byte(source), "en", "ja", nil, spans)
	if err != nil {
		t.Fatal(err)
	}
	session, err := unit.NewSession([]byte("artifact"), []unit.TranslationUnit{u})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewProfile([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	return session, id, profile
}

func TestValidateGeneratedAndRestored(t *testing.T) {
	for _, tt := range []struct {
		name, source, tokenized, restored string
		spans                             []unit.ProtectionSpan
		generate                          bool
	}{
		{
			name: "nested tags and exact Unicode bytes", source: "<b><i>Hello {p}</i></b>", spans: nestedSpans, generate: true,
			tokenized: " \r\n[[YAKUORI_0_0]][[YAKUORI_0_1]]訳 e\u0301 [[YAKUORI_0_2]][[YAKUORI_0_3]][[YAKUORI_0_4]]\r\n ",
			restored:  " \r\n<b><i>訳 e\u0301 {p}</i></b>\r\n ",
		},
		{name: "empty source", source: "", tokenized: "", restored: ""},
		{
			name: "protected only exact source", source: " {p}\r\n", spans: []unit.ProtectionSpan{{Start: 1, End: 4, Kind: unit.Placeholder}},
			tokenized: " [[YAKUORI_0_0]]\r\n", restored: " {p}\r\n",
		},
		{name: "no spans", source: "Hello", tokenized: "こんにちは", restored: "こんにちは", generate: true},
		{name: "whitespace-only no spans", source: " \r\n", tokenized: "\t", restored: "\t", generate: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			session, id, profile := protectedFixture(t, tt.source, tt.spans)
			prepared, err := protect.Prepare(session, id)
			if err != nil || prepared.GenerationRequired() != tt.generate {
				t.Fatalf("generation required = %v, error = %v", prepared.GenerationRequired(), err)
			}
			restored, err := prepared.Restore(session, id, tt.tokenized)
			if err != nil || restored != tt.restored {
				t.Fatalf("restored = %q, error = %v", restored, err)
			}
			generated, err := Validate(session, id, profile, restored)
			if err != nil {
				t.Fatal(err)
			}
			tm, err := Validate(session, id, profile, tt.restored)
			if err != nil || generated != tm || tm.Text() != tt.restored || tm.UnitID() != id {
				t.Fatalf("generated = %#v, TM = %#v, error = %v", generated, tm, err)
			}
			if err := CheckBinding(session, id, profile, tm); err != nil {
				t.Fatal(err)
			}
			if !tt.generate {
				unchanged, err := Validate(session, id, profile, tt.source)
				if err != nil || unchanged != tm {
					t.Fatalf("generation-free accepted = %#v, error = %v", unchanged, err)
				}
			}
		})
	}
}

func TestValidateRejectsRestoredStructure(t *testing.T) {
	for _, tt := range []struct{ name, candidate string }{
		{"missing tag", "<b>訳 {p}</i></b>"},
		{"extra tag", "<b><i>訳 {p}</i></b><b>"},
		{"crossed tags", "<b><i>訳 {p}</b></i>"},
		{"changed nesting", "<i><b>訳 {p}</b></i>"},
		{"missing placeholder", "<b><i>訳 </i></b>"},
		{"duplicate placeholder", "<b><i>訳 {p}{p}</i></b>"},
		{"protected payload loss", "<b><i> \r\n{p}</i></b>"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			session, id, profile := protectedFixture(t, "<b><i>Hello {p}</i></b>", nestedSpans)
			t.Run("TM direct", func(t *testing.T) {
				accepted, err := Validate(session, id, profile, tt.candidate)
				if !errors.Is(err, ErrInvalidCandidate) || accepted != (AcceptedTranslation{}) {
					t.Fatalf("accepted = %#v, error = %v", accepted, err)
				}
			})
			t.Run("generated", func(t *testing.T) {
				prepared, err := protect.Prepare(session, id)
				if err != nil {
					t.Fatal(err)
				}
				tokenized := strings.NewReplacer(
					"<b>", "[[YAKUORI_0_0]]", "<i>", "[[YAKUORI_0_1]]", "{p}", "[[YAKUORI_0_2]]",
					"</i>", "[[YAKUORI_0_3]]", "</b>", "[[YAKUORI_0_4]]",
				).Replace(tt.candidate)
				restored, err := prepared.Restore(session, id, tokenized)
				if !errors.Is(err, protect.ErrInvalidCandidate) || restored != "" {
					t.Fatalf("restored = %q, error = %v", restored, err)
				}
				accepted, err := Validate(session, id, profile, restored)
				if !errors.Is(err, ErrInvalidCandidate) || accepted != (AcceptedTranslation{}) {
					t.Fatalf("accepted = %#v, error = %v", accepted, err)
				}
			})
		})
	}
}

func TestValidateRequiresCurrentProtection(t *testing.T) {
	for _, tt := range []struct {
		name, source, candidate string
		spans                   []unit.ProtectionSpan
		want                    error
	}{
		{"unrestored tokens", "<b><i>Hello {p}</i></b>", "[[YAKUORI_0_0]]訳", nestedSpans, ErrInvalidCandidate},
		{"malformed token prefix", "<b><i>Hello {p}</i></b>", "<b><i>訳 {p}</i></b> [[YAKUORI_0_bad", nestedSpans, ErrInvalidCandidate},
		{"unknown namespace", "<b><i>Hello {p}</i></b>", "<b><i>訳 {p}</i></b> [[YAKUORI_9_0]]", nestedSpans, ErrInvalidCandidate},
		{"protected-only changed bytes", " {p}\r\n", "{p}", []unit.ProtectionSpan{{Start: 1, End: 4, Kind: unit.Placeholder}}, ErrInvalidCandidate},
		{"source undeclared occurrence", "{p} {p}", "訳 {p}", []unit.ProtectionSpan{{Start: 0, End: 3, Kind: unit.Placeholder}}, ErrInvalidSource},
		{"source unclosed tag", "<b>Hello", "訳", []unit.ProtectionSpan{{Start: 0, End: 3, Kind: unit.OpenTag, Pair: "b", Ordered: true}}, ErrInvalidSource},
	} {
		t.Run(tt.name, func(t *testing.T) {
			session, id, profile := protectedFixture(t, tt.source, tt.spans)
			accepted, err := Validate(session, id, profile, tt.candidate)
			if !errors.Is(err, tt.want) || accepted != (AcceptedTranslation{}) {
				t.Fatalf("accepted = %#v, error = %v", accepted, err)
			}
		})
	}
}

func TestValidateRejectsMissingUnprotectedPayload(t *testing.T) {
	session, ids, profile := fixture(t, "source")
	prepared, err := protect.Prepare(session, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	restored, err := prepared.Restore(session, ids[0], " \r\n")
	if !errors.Is(err, protect.ErrInvalidCandidate) || restored != "" {
		t.Fatalf("restored = %q, error = %v", restored, err)
	}
	for _, tt := range []struct{ name, candidate string }{
		{"generated", restored}, {"TM direct", " \r\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			accepted, err := Validate(session, ids[0], profile, tt.candidate)
			if !errors.Is(err, ErrInvalidCandidate) || accepted != (AcceptedTranslation{}) {
				t.Fatalf("accepted = %#v, error = %v", accepted, err)
			}
		})
	}
}

func TestValidatePreservesErrorOrder(t *testing.T) {
	session, ids, _ := fixture(t, "\xff")
	for _, tt := range []struct {
		name    string
		session unit.Session
		id      unit.UnitID
		want    error
	}{
		{"session before profile", unit.Session{}, ids[0], unit.ErrInvalidSession},
		{"unit before profile", session, unit.UnitID{}, unit.ErrUnknownUnit},
		{"profile before source", session, ids[0], ErrInvalidProfile},
	} {
		t.Run(tt.name, func(t *testing.T) {
			accepted, err := Validate(tt.session, tt.id, Profile{}, "\x00")
			if !errors.Is(err, tt.want) || accepted != (AcceptedTranslation{}) {
				t.Fatalf("accepted = %#v, error = %v", accepted, err)
			}
		})
	}
}
