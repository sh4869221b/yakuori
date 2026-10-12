package glossary_test

import (
	"errors"
	"testing"

	"github.com/sh4869221b/yakuori/internal/glossary"
)

func TestParseRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct{ name, schema, data string }{
		{"unknown schema", "v99", "[]"},
		{"null entries", glossary.Schema, "null"},
		{"object entries", glossary.Schema, "{}"},
		{"trailing value", glossary.Schema, "[] []"},
		{"invalid UTF8", glossary.Schema, "[\xff]"},
		{"unknown field", glossary.Schema, `[{"source":"cat","mode":"protect","match":"word","unused":true}]`},
		{"empty protect target", glossary.Schema, `[{"source":"cat","target":"","mode":"protect","match":"word"}]`},
		{"null protect target", glossary.Schema, `[{"source":"cat","target":null,"mode":"protect","match":"word"}]`},
		{"null preferred target", glossary.Schema, `[{"source":"cat","target":null,"mode":"prefer","match":"word"}]`},
		{"missing target", glossary.Schema, `[{"source":"cat","mode":"mandatory","match":"word"}]`},
		{"reserved mandatory target", glossary.Schema, `[{"source":"cat","target":"[[YAKUORI_0_0]]","mode":"mandatory","match":"word"}]`},
		{"selected prefix mandatory target", glossary.Schema, `[{"source":"cat","target":"[[YAKUORI_0_x]]","mode":"mandatory","match":"word"}]`},
		{"wrong target type", glossary.Schema, `[{"source":"cat","target":1,"mode":"mandatory","match":"word"}]`},
		{"NUL escape", glossary.Schema, `[{"source":"cat\u0000","mode":"protect","match":"word"}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			_, err := glossary.Parse(tt.schema, []byte(tt.data))
			// Then
			if !errors.Is(err, glossary.ErrInvalidGlossary) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestMatchRejectsInvalidSource(t *testing.T) {
	for _, source := range []string{"\xff", "cat\x00"} {
		// Given
		g := newGlossary(t, []glossary.Entry{{"cat", "猫", glossary.Mandatory, glossary.Literal}})
		// When
		_, err := g.Match(source)
		// Then
		if !errors.Is(err, glossary.ErrInvalidGlossary) {
			t.Fatalf("source %q error = %v", source, err)
		}
	}
}
