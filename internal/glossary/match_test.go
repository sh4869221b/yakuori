package glossary_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/sh4869221b/yakuori/internal/glossary"
)

func TestMatchBoundaries(t *testing.T) {
	tests := []struct {
		name, term, source string
		kind               glossary.MatchKind
		spans              [][2]int
	}{
		{"literal cat catch scatter", "cat", "cat catch scatter", glossary.Literal, [][2]int{{0, 3}, {4, 7}, {11, 14}}},
		{"word cat catch scatter", "cat", "cat catch scatter", glossary.Word, [][2]int{{0, 3}}},
		{"underscore and digits", "cat", "_cat cat_ 1cat cat2 cat", glossary.Word, [][2]int{{20, 23}}},
		{"marks on both sides", "cat", "\u0301cat cat\u0301 cat", glossary.Word, [][2]int{{12, 15}}},
		{"CJK letters on both sides", "cat", "猫cat cat犬 cat", glossary.Word, [][2]int{{14, 17}}},
		{"Unicode number", "cat", "²cat catⅢ cat", glossary.Word, [][2]int{{13, 16}}},
		{"Unicode punctuation", "cat", "「cat」", glossary.Word, [][2]int{{3, 6}}},
		{"whole CJK word", "猫", "猫 猫犬", glossary.Word, [][2]int{{0, 3}}},
		{"case stays exact", "cat", "Cat CAT", glossary.Literal, nil},
		{"composed stays exact", "é", "e\u0301 é", glossary.Literal, [][2]int{{4, 6}}},
		{"decomposed stays exact", "e\u0301", "é e\u0301", glossary.Literal, [][2]int{{3, 6}}},
		{"adjacent occurrences", "猫", "猫猫", glossary.Literal, [][2]int{{0, 3}, {3, 6}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			g := newGlossary(t, []glossary.Entry{{Source: tt.term, Target: "訳", Mode: glossary.Mandatory, Match: tt.kind}})
			// When
			matches, err := g.Match(tt.source)
			// Then
			if err != nil {
				t.Fatal(err)
			}
			var spans [][2]int
			for _, m := range matches {
				spans = append(spans, [2]int{m.Start, m.End})
			}
			if !reflect.DeepEqual(spans, tt.spans) {
				t.Fatalf("spans = %v; want %v", spans, tt.spans)
			}
		})
	}
}

func TestMatchConflicts(t *testing.T) {
	tests := []struct {
		name, source string
		entries      []glossary.Entry
		conflict     bool
		count        int
	}{
		{"same demand retains modes", "cat", []glossary.Entry{{"cat", "猫", glossary.Prefer, glossary.Literal}, {"cat", "猫", glossary.Mandatory, glossary.Word}}, false, 2},
		{"different target", "cat", []glossary.Entry{{"cat", "猫", glossary.Prefer, glossary.Literal}, {"cat", "ネコ", glossary.Mandatory, glossary.Word}}, true, 0},
		{"protect different target", "cat", []glossary.Entry{{"cat", "", glossary.Protect, glossary.Word}, {"cat", "猫", glossary.Mandatory, glossary.Word}}, true, 0},
		{"protect same target", "cat", []glossary.Entry{{"cat", "", glossary.Protect, glossary.Word}, {"cat", "cat", glossary.Mandatory, glossary.Word}}, false, 2},
		{"contained", "catch", []glossary.Entry{{"cat", "猫", glossary.Prefer, glossary.Literal}, {"catch", "捕", glossary.Mandatory, glossary.Literal}}, true, 0},
		{"partial", "abcd", []glossary.Entry{{"abc", "同", glossary.Prefer, glossary.Literal}, {"bcd", "同", glossary.Prefer, glossary.Literal}}, true, 0},
		{"self overlap", "banana", []glossary.Entry{{"ana", "訳", glossary.Prefer, glossary.Literal}}, true, 0},
		{"duplicate removed", "cat cat", []glossary.Entry{{"cat", "猫", glossary.Prefer, glossary.Literal}, {"cat", "猫", glossary.Prefer, glossary.Literal}}, false, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			g := newGlossary(t, tt.entries)
			// When
			matches, err := g.Match(tt.source)
			// Then
			if errors.Is(err, glossary.ErrConflict) != tt.conflict {
				t.Fatalf("error = %v; conflict want %v", err, tt.conflict)
			}
			if !tt.conflict && (err != nil || len(matches) != tt.count) {
				t.Fatalf("matches = %v, error = %v; count want %d", matches, err, tt.count)
			}
		})
	}
}

func newGlossary(t *testing.T, entries []glossary.Entry) glossary.Glossary {
	t.Helper()
	g, err := glossary.New(glossary.Schema, entries)
	if err != nil {
		t.Fatal(err)
	}
	return g
}
