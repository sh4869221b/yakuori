package protect_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func TestSegmentProtectionMappingAndCuts(t *testing.T) {
	source := " \r\n<b><i>日本。 {p}</i></b>。\r\n{p} 次!  終\t"
	literals := []struct {
		text string
		kind unit.ProtectionKind
		pair string
	}{
		{"<b>", unit.OpenTag, "b"}, {"<i>", unit.OpenTag, "i"}, {"{p}", unit.Placeholder, ""}, {"</i>", unit.CloseTag, "i"}, {"</b>", unit.CloseTag, "b"}, {"{p}", unit.Placeholder, ""},
	}
	var spans []unit.ProtectionSpan
	cursor := 0
	for _, lit := range literals {
		start := cursor + strings.Index(source[cursor:], lit.text)
		spans = append(spans, unit.ProtectionSpan{Start: start, End: start + len(lit.text), Kind: lit.kind, Pair: lit.pair, Ordered: lit.kind != unit.Placeholder})
		cursor = start + len(lit.text)
	}
	session, id := fixtureSession(t, source, spans)
	p, err := protect.Prepare(session, id)
	if err != nil {
		t.Fatal(err)
	}
	text := p.Text()
	bodyEnd := strings.Index(text, "。\r\n") + len("。")
	placeholderEnd := bodyEnd + 2 + len("[[YAKUORI_0_5]]")
	sentenceEnd := strings.Index(text, "!  ") + 1
	want := []protect.Cut{{End: 0, Next: 3}, {End: bodyEnd, Next: bodyEnd + 2}, {End: placeholderEnd, Next: placeholderEnd + 1}, {End: sentenceEnd, Next: sentenceEnd + 2}, {End: len(text) - 1, Next: len(text)}}
	if got := p.Cuts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("cuts=%v want=%v text=%q", got, want, text)
	}
	// The cuts partition original bytes despite replacements of differing length.
	previous := 0
	var original strings.Builder
	for _, cut := range want {
		body, err := p.Slice(previous, cut.End)
		if err != nil {
			t.Fatal(err)
		}
		separator, err := p.Slice(cut.End, cut.Next)
		if err != nil {
			t.Fatal(err)
		}
		original.WriteString(body.Source())
		original.WriteString(separator.Source())
		if err := p.CheckCandidate(body, body.Text()); err != nil {
			t.Fatal(err)
		}
		if got := body.SourceRange(); source[got.Start:got.End] != body.Source() {
			t.Fatal("source mapping")
		}
		if got := body.PreparedRange(); text[got.Start:got.End] != body.Text() {
			t.Fatal("prepared mapping")
		}
		previous = cut.Next
	}
	if original.String() != source {
		t.Fatalf("original=%q", original.String())
	}
	// Returned slices are copies.
	cuts := p.Cuts()
	cuts[0].End = 999
	if !reflect.DeepEqual(p.Cuts(), want) {
		t.Fatal("mutable cut state")
	}
	protected, err := p.Slice(bodyEnd+2, placeholderEnd)
	if err != nil {
		t.Fatal(err)
	}
	if protected.GenerationRequired() {
		t.Fatal("placeholder-only piece requires generation")
	}
	if err := p.CheckCandidate(protected, protected.Text()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Slice(3, bodyEnd); err != nil {
		t.Fatal(err)
	}
	for _, bounds := range [][2]int{{-1, 0}, {0, len(text) + 1}, {4, bodyEnd}, {3, 4}, {3, strings.Index(text, "日本") + 1}, {3, strings.Index(text, "日本")}, {0, 2}} {
		if _, err := p.Slice(bounds[0], bounds[1]); !errors.Is(err, protect.ErrInvalidPrepared) {
			t.Fatalf("range %v error=%v", bounds, err)
		}
	}
}

func TestSegmentProtectionSubsetCandidate(t *testing.T) {
	source := "<b>Hi {p}</b> {p} and {q} [[YAKUORI_0_7]]"
	spans := []unit.ProtectionSpan{{Start: 0, End: 3, Kind: unit.OpenTag, Pair: "b", Ordered: true}, {Start: 6, End: 9, Kind: unit.Placeholder}, {Start: 9, End: 13, Kind: unit.CloseTag, Pair: "b", Ordered: true}, {Start: 14, End: 17, Kind: unit.Placeholder}, {Start: 22, End: 25, Kind: unit.Placeholder}}
	session, id := fixtureSession(t, source, spans)
	p, err := protect.Prepare(session, id)
	if err != nil {
		t.Fatal(err)
	}
	split := strings.Index(p.Text(), " and ") - len("[[YAKUORI_1_3]]")
	left, err := p.Slice(0, split)
	if err != nil {
		t.Fatal(err)
	}
	right, err := p.Slice(split, len(p.Text()))
	if err != nil {
		t.Fatal(err)
	}
	valid := "[[YAKUORI_1_4]] と [[YAKUORI_1_3]] [[YAKUORI_0_7]]"
	if !right.GenerationRequired() {
		t.Fatal("payload piece needs generation")
	}
	if err := p.CheckCandidate(right, valid); err != nil {
		t.Fatal(err)
	}
	tests := []struct{ name, text string }{
		{"missing", "訳 [[YAKUORI_1_3]] [[YAKUORI_0_7]]"},
		{"duplicate", valid + "[[YAKUORI_1_3]]"},
		{"unknown", valid + "[[YAKUORI_1_9]]"},
		{"other piece", valid + "[[YAKUORI_1_1]]"},
		{"foreign namespace", valid + "[[YAKUORI_9_0]]"},
		{"missing reserved", "訳 [[YAKUORI_1_3]][[YAKUORI_1_4]]"},
		{"duplicate reserved", valid + "[[YAKUORI_0_7]]"},
		{"unterminated", valid + "[[YAKUORI_1_"},
		{"UTF-8", valid + "\xff"}, {"NUL", valid + "\x00"},
		{"extra literal", valid + "{p}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := p.CheckCandidate(right, tt.text); !errors.Is(err, protect.ErrInvalidCandidate) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	leftBad := []string{"[[YAKUORI_1_0]][[YAKUORI_1_1]][[YAKUORI_1_2]] ", "[[YAKUORI_1_2]]訳[[YAKUORI_1_1]][[YAKUORI_1_0]] ", "[[YAKUORI_1_0]]訳[[YAKUORI_1_1]] "}
	for _, candidate := range leftBad {
		if err := p.CheckCandidate(left, candidate); !errors.Is(err, protect.ErrInvalidCandidate) {
			t.Fatalf("candidate=%q error=%v", candidate, err)
		}
	}
	foreign, err := protect.Prepare(session, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := foreign.CheckCandidate(right, valid); !errors.Is(err, protect.ErrInvalidPrepared) {
		t.Fatalf("binding error=%v", err)
	}
	copied := p
	if err := copied.CheckCandidate(right, valid); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckCandidate(protect.Piece{}, valid); !errors.Is(err, protect.ErrInvalidPrepared) {
		t.Fatalf("zero piece error=%v", err)
	}
	if _, err := p.Restore(session, id, valid); !errors.Is(err, protect.ErrInvalidCandidate) {
		t.Fatalf("parent no longer requires all tokens: %v", err)
	}
}

func TestSegmentProtectionReservedAndOrdered(t *testing.T) {
	source := "{a} words {b}。 [[YAKUORI_0_7]] 末"
	spans := []unit.ProtectionSpan{{Start: 0, End: 3, Kind: unit.Placeholder, Ordered: true}, {Start: 10, End: 13, Kind: unit.Placeholder, Ordered: true}}
	session, id := fixtureSession(t, source, spans)
	p, err := protect.Prepare(session, id)
	if err != nil {
		t.Fatal(err)
	}
	end := strings.Index(p.Text(), "。") + len("。")
	first, err := p.Slice(0, end)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CheckCandidate(first, "[[YAKUORI_1_1]]訳[[YAKUORI_1_0]]。"); !errors.Is(err, protect.ErrInvalidCandidate) {
		t.Fatalf("ordered error=%v", err)
	}
	last, err := p.Slice(end+1, len(p.Text()))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CheckCandidate(last, "[[YAKUORI_0_7]] 訳"); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckCandidate(last, "訳"); !errors.Is(err, protect.ErrInvalidCandidate) {
		t.Fatalf("reserved error=%v", err)
	}
	if _, err := p.Slice(end+2, len(p.Text())); !errors.Is(err, protect.ErrInvalidPrepared) {
		t.Fatalf("reserved interior error=%v", err)
	}
}
