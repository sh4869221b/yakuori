package unit_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/sh4869221b/yakuori/internal/unit"
)

func TestProtectionSpanSnapshot(t *testing.T) {
	// Given
	id, err := unit.NewUnitID("fixture", "v1", "protected")
	if err != nil {
		t.Fatal(err)
	}
	source := []byte("<b>{player}</b><b>{player}</b>")
	spans := []unit.ProtectionSpan{
		{Start: 0, End: 3, Kind: unit.OpenTag, Pair: "b", Ordered: true},
		{Start: 3, End: 11, Kind: unit.Placeholder},
		{Start: 11, End: 15, Kind: unit.CloseTag, Pair: "b", Ordered: true},
		{Start: 15, End: 18, Kind: unit.OpenTag, Pair: "b", Ordered: true},
		{Start: 18, End: 26, Kind: unit.Placeholder},
		{Start: 26, End: 30, Kind: unit.CloseTag, Pair: "b", Ordered: true},
	}
	wantSource := bytes.Clone(source)
	wantSpans := append([]unit.ProtectionSpan(nil), spans...)

	// When
	u, err := unit.NewTranslationUnitWithProtection(id, source, "en", "ja", nil, spans)
	if err != nil {
		t.Fatal(err)
	}
	source[0] = 'x'
	spans[0].Start = 1
	returnedSpans := u.ProtectionSpans()
	returnedSpans[0].End = 2

	// Then
	if !bytes.Equal(u.Source(), wantSource) || !reflect.DeepEqual(u.ProtectionSpans(), wantSpans) {
		t.Fatalf("unit snapshot changed: source %q, spans %#v", u.Source(), u.ProtectionSpans())
	}
}

func TestProtectionSpanRejectsInvalid(t *testing.T) {
	id, err := unit.NewUnitID("fixture", "v1", "invalid-protected")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		source []byte
		spans  []unit.ProtectionSpan
	}{
		{name: "negative start", source: []byte("abc"), spans: []unit.ProtectionSpan{{Start: -1, End: 1, Kind: unit.Placeholder}}},
		{name: "empty range", source: []byte("abc"), spans: []unit.ProtectionSpan{{Start: 1, End: 1, Kind: unit.Placeholder}}},
		{name: "end past source", source: []byte("abc"), spans: []unit.ProtectionSpan{{Start: 0, End: 4, Kind: unit.Placeholder}}},
		{name: "start inside utf8 rune", source: []byte("éx"), spans: []unit.ProtectionSpan{{Start: 1, End: 2, Kind: unit.Placeholder}}},
		{name: "end inside utf8 rune", source: []byte("é"), spans: []unit.ProtectionSpan{{Start: 0, End: 1, Kind: unit.Placeholder}}},
		{name: "overlap", source: []byte("abcd"), spans: []unit.ProtectionSpan{{Start: 0, End: 3, Kind: unit.Placeholder}, {Start: 2, End: 4, Kind: unit.Placeholder}}},
		{name: "out of order", source: []byte("abcd"), spans: []unit.ProtectionSpan{{Start: 2, End: 3, Kind: unit.Placeholder}, {Start: 0, End: 1, Kind: unit.Placeholder}}},
		{name: "unknown kind", source: []byte("abc"), spans: []unit.ProtectionSpan{{Start: 0, End: 1, Kind: unit.ProtectionKind("future")}}},
		{name: "placeholder pair", source: []byte("abc"), spans: []unit.ProtectionSpan{{Start: 0, End: 1, Kind: unit.Placeholder, Pair: "b"}}},
		{name: "tag pair missing", source: []byte("abc"), spans: []unit.ProtectionSpan{{Start: 0, End: 1, Kind: unit.OpenTag, Ordered: true}}},
		{name: "unordered opening tag", source: []byte("abc"), spans: []unit.ProtectionSpan{{Start: 0, End: 1, Kind: unit.OpenTag, Pair: "b"}}},
		{name: "unordered closing tag", source: []byte("abc"), spans: []unit.ProtectionSpan{{Start: 0, End: 1, Kind: unit.CloseTag, Pair: "b"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			// When
			_, err := unit.NewTranslationUnitWithProtection(id, tt.source, "en", "ja", nil, tt.spans)

			// Then
			if !errors.Is(err, unit.ErrInvalidProtectionSpan) {
				t.Fatalf("error = %v, want %v", err, unit.ErrInvalidProtectionSpan)
			}
		})
	}
}

func TestProtectionSpanNoSpansPreserveInvalidUTF8(t *testing.T) {
	// Given
	id, err := unit.NewUnitID("fixture", "v1", "raw-bytes")
	if err != nil {
		t.Fatal(err)
	}
	source := []byte{0xff, 'x'}

	// When
	u, err := unit.NewTranslationUnitWithProtection(id, source, "en", "ja", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Then
	if !bytes.Equal(u.Source(), source) {
		t.Fatalf("source bytes were changed: %v", u.Source())
	}
}
