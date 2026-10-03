package protect_test

import (
	"errors"
	"testing"

	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func TestProtectionRejectsInvalid(t *testing.T) {
	t.Run("source", func(t *testing.T) {
		tests := []struct {
			name, source string
			spans        []unit.ProtectionSpan
		}{
			{name: "UTF-8", source: "\xff"}, {name: "NUL", source: "hello\x00"},
			{name: "undeclared literal", source: "{p} {p}", spans: []unit.ProtectionSpan{{Start: 0, End: 3, Kind: unit.Placeholder}}},
			{name: "inconsistent metadata", source: "{p} {p}", spans: []unit.ProtectionSpan{{Start: 0, End: 3, Kind: unit.Placeholder}, {Start: 4, End: 7, Kind: unit.Placeholder, Ordered: true}}},
			{name: "different literal overlap", source: "ab bc abc", spans: []unit.ProtectionSpan{{Start: 0, End: 2, Kind: unit.Placeholder}, {Start: 3, End: 5, Kind: unit.Placeholder}}},
			{name: "self overlap", source: "aaa", spans: []unit.ProtectionSpan{{Start: 0, End: 2, Kind: unit.Placeholder}}},
			{name: "unclosed tag", source: "<b>hello", spans: []unit.ProtectionSpan{{Start: 0, End: 3, Kind: unit.OpenTag, Pair: "b", Ordered: true}}},
			{name: "closing before opening", source: "</b>x<b>", spans: []unit.ProtectionSpan{{Start: 0, End: 4, Kind: unit.CloseTag, Pair: "b", Ordered: true}, {Start: 5, End: 8, Kind: unit.OpenTag, Pair: "b", Ordered: true}}},
			{name: "crossed tags", source: "<b><i>x</b></i>", spans: crossTagSpans},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				session, id := fixtureSession(t, tt.source, tt.spans)
				prepared, err := protect.Prepare(session, id)
				if !errors.Is(err, protect.ErrInvalidSource) || prepared != (protect.Prepared{}) {
					t.Fatalf("prepared = %#v, error = %v", prepared, err)
				}
			})
		}
	})
	t.Run("tokenized", func(t *testing.T) {
		session, id := fixtureSession(t, "<b>Hello {player}</b> %s", dialogueSpans)
		prepared, err := protect.Prepare(session, id)
		if err != nil {
			t.Fatal(err)
		}
		tests := []struct{ name, candidate string }{
			{"UTF-8", "\xff"}, {"NUL", "\x00"},
			{"missing", "[[YAKUORI_0_0]]訳[[YAKUORI_0_2]][[YAKUORI_0_3]]"},
			{"duplicate", "[[YAKUORI_0_0]]訳[[YAKUORI_0_1]][[YAKUORI_0_1]][[YAKUORI_0_2]][[YAKUORI_0_3]]"},
			{"unknown", "[[YAKUORI_0_0]]訳[[YAKUORI_0_1]][[YAKUORI_0_2]][[YAKUORI_0_3]][[YAKUORI_0_9]]"},
			{"foreign namespace", "[[YAKUORI_0_0]]訳[[YAKUORI_0_1]][[YAKUORI_0_2]][[YAKUORI_0_3]][[YAKUORI_9_0]]"},
			{"malformed digits", "[[YAKUORI_0_0]]訳[[YAKUORI_0_bad]][[YAKUORI_0_1]][[YAKUORI_0_2]][[YAKUORI_0_3]]"},
			{"noncanonical digits", "[[YAKUORI_0_00]]訳[[YAKUORI_0_1]][[YAKUORI_0_2]][[YAKUORI_0_3]]"},
			{"missing terminator", "[[YAKUORI_0_0]]訳[[YAKUORI_0_1]][[YAKUORI_0_2]][[YAKUORI_0_3]] [[YAKUORI_0_"},
			{"ordered tag swap", "[[YAKUORI_0_2]]訳[[YAKUORI_0_1]][[YAKUORI_0_0]][[YAKUORI_0_3]]"},
			{"payload loss", "[[YAKUORI_0_0]] [[YAKUORI_0_1]][[YAKUORI_0_2]][[YAKUORI_0_3]]"},
			{"restoration exposes extra literal", "[[YAKUORI_0_0]]訳{player}[[YAKUORI_0_1]][[YAKUORI_0_2]][[YAKUORI_0_3]]"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := prepared.Restore(session, id, tt.candidate)
				if !errors.Is(err, protect.ErrInvalidCandidate) || got != "" {
					t.Fatalf("restored = %q, error = %v", got, err)
				}
			})
		}
	})
	t.Run("restored", func(t *testing.T) {
		session, id := fixtureSession(t, "<b>Hello {player}</b> %s", dialogueSpans)
		prepared, err := protect.Prepare(session, id)
		if err != nil {
			t.Fatal(err)
		}
		tests := []struct{ name, candidate string }{
			{"UTF-8", "\xff"}, {"NUL", "\x00"}, {"missing", "<b>こんにちは</b> %s"},
			{"duplicate", "<b>こんにちは {player}{player}</b> %s"}, {"tag order", "</b>こんにちは {player}<b> %s"},
			{"payload loss", "<b> {player}</b> %s"}, {"selected prefix", "<b>こんにちは {player}</b> %s [[YAKUORI_0_bad"},
			{"foreign namespace", "<b>こんにちは {player}</b> %s [[YAKUORI_1_0]]"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if err := prepared.CheckRestored(session, id, tt.candidate); !errors.Is(err, protect.ErrInvalidCandidate) {
					t.Fatalf("error = %v", err)
				}
			})
		}
	})
}

var crossTagSpans = []unit.ProtectionSpan{
	{Start: 0, End: 3, Kind: unit.OpenTag, Pair: "b", Ordered: true}, {Start: 3, End: 6, Kind: unit.OpenTag, Pair: "i", Ordered: true},
	{Start: 7, End: 11, Kind: unit.CloseTag, Pair: "b", Ordered: true}, {Start: 11, End: 15, Kind: unit.CloseTag, Pair: "i", Ordered: true},
}
