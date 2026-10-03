package protect_test

import (
	"errors"
	"testing"

	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func TestProtectionRejectsInvalidStructure(t *testing.T) {
	tests := []struct {
		name, source, tokenized, restored string
		spans                             []unit.ProtectionSpan
	}{
		{
			name: "known literal overlap", source: "ab bc words", spans: []unit.ProtectionSpan{{Start: 0, End: 2, Kind: unit.Placeholder}, {Start: 3, End: 5, Kind: unit.Placeholder}},
			tokenized: "[[YAKUORI_0_0]] [[YAKUORI_0_1]] abc", restored: "abc",
		},
		{
			name: "ordered placeholders", source: "{a} hello {b}", spans: []unit.ProtectionSpan{{Start: 0, End: 3, Kind: unit.Placeholder, Ordered: true}, {Start: 10, End: 13, Kind: unit.Placeholder, Ordered: true}},
			tokenized: "[[YAKUORI_0_1]]こんにちは[[YAKUORI_0_0]]", restored: "{b}こんにちは{a}",
		},
		{
			name: "crossed tags", source: "<b><i>x</i></b>", spans: nestedTagSpans,
			tokenized: "[[YAKUORI_0_0]][[YAKUORI_0_1]]訳[[YAKUORI_0_3]][[YAKUORI_0_2]]", restored: "<b><i>訳</b></i>",
		},
		{
			name: "balanced changed nesting", source: "<b><i>x</i></b>", spans: nestedTagSpans,
			tokenized: "[[YAKUORI_0_1]][[YAKUORI_0_0]]訳[[YAKUORI_0_3]][[YAKUORI_0_2]]", restored: "<i><b>訳</b></i>",
		},
		{
			name: "same-label occurrence order", source: "<b><b>x</b></b>", spans: []unit.ProtectionSpan{
				{Start: 0, End: 3, Kind: unit.OpenTag, Pair: "b", Ordered: true}, {Start: 3, End: 6, Kind: unit.OpenTag, Pair: "b", Ordered: true},
				{Start: 7, End: 11, Kind: unit.CloseTag, Pair: "b", Ordered: true}, {Start: 11, End: 15, Kind: unit.CloseTag, Pair: "b", Ordered: true},
			}, tokenized: "[[YAKUORI_0_1]][[YAKUORI_0_0]]訳[[YAKUORI_0_2]][[YAKUORI_0_3]]", restored: "<b>訳</b><b></b>",
		},
		{name: "empty source gains text", source: "", tokenized: "unexpected", restored: "unexpected"},
		{name: "nonempty source empty candidate", source: "hello", tokenized: "", restored: ""},
		{name: "no-span payload whitespace", source: "hello", tokenized: " \t\r\n", restored: " \t\r\n"},
		{name: "whitespace source byte emptiness", source: " ", tokenized: "", restored: ""},
		{
			name: "protected-only exact source", source: " {p}\r\n", spans: []unit.ProtectionSpan{{Start: 1, End: 4, Kind: unit.Placeholder}},
			tokenized: "[[YAKUORI_0_0]]", restored: "{p}",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session, id := fixtureSession(t, tt.source, tt.spans)
			prepared, err := protect.Prepare(session, id)
			if err != nil {
				t.Fatal(err)
			}
			got, err := prepared.Restore(session, id, tt.tokenized)
			if !errors.Is(err, protect.ErrInvalidCandidate) || got != "" {
				t.Fatalf("restored = %q, error = %v", got, err)
			}
			if err := prepared.CheckRestored(session, id, tt.restored); !errors.Is(err, protect.ErrInvalidCandidate) {
				t.Fatalf("check error = %v", err)
			}
		})
	}
}

var nestedTagSpans = []unit.ProtectionSpan{
	{Start: 0, End: 3, Kind: unit.OpenTag, Pair: "b", Ordered: true}, {Start: 3, End: 6, Kind: unit.OpenTag, Pair: "i", Ordered: true},
	{Start: 7, End: 11, Kind: unit.CloseTag, Pair: "i", Ordered: true}, {Start: 11, End: 15, Kind: unit.CloseTag, Pair: "b", Ordered: true},
}

func TestProtectionRejectsInvalidOriginalTokenCounts(t *testing.T) {
	session, id := fixtureSession(t, "Hello [[YAKUORI_0_7]] [[YAKUORI_0_7]]", nil)
	prepared, err := protect.Prepare(session, id)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ name, candidate string }{
		{"missing original", "訳 [[YAKUORI_0_7]]"}, {"duplicate original", "訳 [[YAKUORI_0_7]][[YAKUORI_0_7]][[YAKUORI_0_7]]"},
		{"unknown original namespace", "訳 [[YAKUORI_0_7]][[YAKUORI_0_7]][[YAKUORI_0_8]]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := prepared.Restore(session, id, tt.candidate)
			if !errors.Is(err, protect.ErrInvalidCandidate) || got != "" {
				t.Fatalf("restored = %q, error = %v", got, err)
			}
			if err := prepared.CheckRestored(session, id, tt.candidate); !errors.Is(err, protect.ErrInvalidCandidate) {
				t.Fatalf("check error = %v", err)
			}
		})
	}
}
