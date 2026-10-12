package glossary_test

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/sh4869221b/yakuori/internal/glossary"
)

func TestCanonicalOrdering(t *testing.T) {
	// Given
	entries := []glossary.Entry{
		{"cat", "猫", glossary.Prefer, glossary.Word},
		{"cat", "猫", glossary.Mandatory, glossary.Word},
		{"cat", "猫", glossary.Prefer, glossary.Literal},
		{"cat", "cat", glossary.Prefer, glossary.Word},
		{"Cat", "", glossary.Protect, glossary.Literal},
		{"cat", "猫", glossary.Prefer, glossary.Word},
	}
	want, err := os.ReadFile("testdata/canonical-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	// When
	g := newGlossary(t, entries)
	// Then
	if !bytes.Equal(g.CanonicalBytes(), bytes.TrimSpace(want)) {
		t.Fatalf("canonical = %s; want %s", g.CanonicalBytes(), want)
	}
	reverse := append([]glossary.Entry(nil), entries...)
	for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
		reverse[i], reverse[j] = reverse[j], reverse[i]
	}
	if other := newGlossary(t, reverse); g != other {
		t.Fatal("reordering changed glossary equality")
	}
	parsed, err := glossary.Parse(glossary.Schema, g.CanonicalBytes())
	if err != nil || parsed != g {
		t.Fatalf("parsed = %v, error = %v; want %v", parsed, err, g)
	}
}

func TestGlossaryOwnsItsData(t *testing.T) {
	// Given
	input := []glossary.Entry{{"cat", "猫", glossary.Prefer, glossary.Word}}
	g := newGlossary(t, input)
	before := g.CanonicalBytes()
	// When
	input[0].Target = "changed"
	output, err := g.Entries()
	if err != nil {
		t.Fatal(err)
	}
	output[0].Target = "changed"
	data := g.CanonicalBytes()
	data[0] = '!'
	// Then
	if !bytes.Equal(before, g.CanonicalBytes()) {
		t.Fatal("caller changed canonical glossary")
	}
}

func TestInvalidEntries(t *testing.T) {
	tests := []struct {
		name  string
		entry glossary.Entry
	}{
		{"empty source", glossary.Entry{"", "猫", glossary.Prefer, glossary.Word}},
		{"empty preferred target", glossary.Entry{"cat", "", glossary.Prefer, glossary.Word}},
		{"empty mandatory target", glossary.Entry{"cat", "", glossary.Mandatory, glossary.Word}},
		{"protect target", glossary.Entry{"cat", "cat", glossary.Protect, glossary.Word}},
		{"unknown mode", glossary.Entry{"cat", "猫", "other", glossary.Word}},
		{"unknown match", glossary.Entry{"cat", "猫", glossary.Prefer, "other"}},
		{"invalid source UTF8", glossary.Entry{"\xff", "猫", glossary.Prefer, glossary.Word}},
		{"invalid target UTF8", glossary.Entry{"cat", "\xff", glossary.Prefer, glossary.Word}},
		{"source NUL", glossary.Entry{"cat\x00", "猫", glossary.Prefer, glossary.Word}},
		{"target NUL", glossary.Entry{"cat", "猫\x00", glossary.Prefer, glossary.Word}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			_, err := glossary.New(glossary.Schema, []glossary.Entry{tt.entry})
			// Then
			if !errors.Is(err, glossary.ErrInvalidGlossary) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestEmptyGlossary(t *testing.T) {
	// When
	g, err := glossary.Parse(glossary.Schema, []byte("[]"))
	// Then
	if err != nil || !g.Empty() || g != (glossary.Glossary{}) || g.Schema() != "" {
		t.Fatalf("empty = %v, error = %v", g, err)
	}
	matches, err := g.Match("cat")
	if err != nil || len(matches) != 0 {
		t.Fatalf("empty matches = %v, error = %v", matches, err)
	}
}
