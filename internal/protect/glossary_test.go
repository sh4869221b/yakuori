package protect_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func protectedGlossary(t *testing.T, source string, match glossary.MatchKind) glossary.Glossary {
	t.Helper()
	g, err := glossary.New(glossary.Schema, []glossary.Entry{{Source: source, Mode: glossary.Protect, Match: match}})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGlossaryProtectionRoundTrip(t *testing.T) {
	for _, tt := range []struct {
		name, source, term, candidate, want string
		spans                               []unit.ProtectionSpan
		generate                            bool
	}{
		{name: "word boundary", source: "cat scatter", term: "cat", candidate: "[[YAKUORI_0_0]] 散らす", want: "cat 散らす", generate: true},
		{name: "repeated", source: "cat and cat", term: "cat", candidate: "[[YAKUORI_0_1]] と [[YAKUORI_0_0]]", want: "cat と cat", generate: true},
		{name: "only protected", source: "cat", term: "cat", candidate: "[[YAKUORI_0_0]]", want: "cat"},
		{name: "tag coexistence", source: "<b>cat</b> runs", term: "cat", spans: []unit.ProtectionSpan{{Start: 0, End: 3, Kind: unit.OpenTag, Pair: "b", Ordered: true}, {Start: 6, End: 10, Kind: unit.CloseTag, Pair: "b", Ordered: true}}, candidate: "[[YAKUORI_0_0]][[YAKUORI_0_1]][[YAKUORI_0_2]] 走る", want: "<b>cat</b> 走る", generate: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			session, id := fixtureSession(t, tt.source, tt.spans)
			prepared, err := protect.PrepareWithGlossary(session, id, protectedGlossary(t, tt.term, glossary.Word))
			if err != nil {
				t.Fatal(err)
			}
			// When
			got, err := prepared.Restore(session, id, tt.candidate)
			// Then
			if err != nil || got != tt.want || prepared.GenerationRequired() != tt.generate {
				t.Fatalf("text=%q generation=%v err=%v", got, prepared.GenerationRequired(), err)
			}
		})
	}
}

func TestGlossaryWordProtectionKeepsUnmatchedSubstringAcrossPiece(t *testing.T) {
	// Given
	session, id := fixtureSession(t, "cat scatter", nil)
	prepared, err := protect.PrepareWithGlossary(session, id, protectedGlossary(t, "cat", glossary.Word))
	if err != nil {
		t.Fatal(err)
	}
	piece, err := prepared.Slice(0, len(prepared.Text()))
	if err != nil {
		t.Fatal(err)
	}
	// When
	err = prepared.CheckCandidate(piece, prepared.Text())
	// Then
	if err != nil {
		t.Fatal(err)
	}
	restored, err := prepared.Restore(session, id, prepared.Text())
	if err != nil || restored != "cat scatter" {
		t.Fatalf("restored=%q err=%v", restored, err)
	}
}

func TestGlossaryProtectionRejectsInvalidTokenStreams(t *testing.T) {
	for _, candidate := range []string{"散らす", "[[YAKUORI_0_9]] 散らす", "[[YAKUORI_0_0]][[YAKUORI_0_0]] 散らす", "[[YAKUORI_0_0]] cat 散らす", "s[[YAKUORI_0_0]] 散らす"} {
		t.Run(candidate, func(t *testing.T) {
			// Given
			session, id := fixtureSession(t, "cat scatter", nil)
			prepared, err := protect.PrepareWithGlossary(session, id, protectedGlossary(t, "cat", glossary.Word))
			if err != nil {
				t.Fatal(err)
			}
			// When
			_, err = prepared.Restore(session, id, candidate)
			// Then
			if !errors.Is(err, protect.ErrInvalidCandidate) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestGlossaryProtectionChecksRestoredCountsAndBoundaries(t *testing.T) {
	for _, tt := range []struct {
		text    string
		invalid bool
	}{
		{"cat scatter", false}, {"cat 走る", false}, {"scatter", true}, {"Cat scatter", true}, {"scat scatter", true}, {"cat cat scatter", true}, {"cat_ scatter", true}, {"cat\u0301 scatter", true},
	} {
		t.Run(tt.text, func(t *testing.T) {
			// Given
			session, id := fixtureSession(t, "cat scatter", nil)
			prepared, err := protect.PrepareWithGlossary(session, id, protectedGlossary(t, "cat", glossary.Word))
			if err != nil {
				t.Fatal(err)
			}
			// When
			err = prepared.CheckRestored(session, id, tt.text)
			// Then
			if errors.Is(err, protect.ErrInvalidCandidate) != tt.invalid {
				t.Fatalf("err=%v invalid=%v", err, tt.invalid)
			}
		})
	}
}

func TestGlossaryProtectionPreservesAdapterMetadata(t *testing.T) {
	// Given
	source := "<b>cat</b> runs"
	spans := []unit.ProtectionSpan{{Start: 0, End: 3, Kind: unit.OpenTag, Pair: "b", Ordered: true}, {Start: 6, End: 10, Kind: unit.CloseTag, Pair: "b", Ordered: true}}
	session, id := fixtureSession(t, source, spans)
	prepared, err := protect.PrepareWithGlossary(session, id, protectedGlossary(t, "<b>", glossary.Literal))
	if err != nil {
		t.Fatal(err)
	}
	// When: moving the reused opening tag after its closing tag remains invalid.
	_, err = prepared.Restore(session, id, "[[YAKUORI_0_1]]cat[[YAKUORI_0_0]] 走る")
	// Then
	if !errors.Is(err, protect.ErrInvalidCandidate) {
		t.Fatalf("err=%v", err)
	}
	if string(session.Units()[0].Source()) != source {
		t.Fatal("source changed")
	}
}

func TestGlossaryProtectionRejectsAdapterConflicts(t *testing.T) {
	for _, tt := range []struct {
		source, term, target string
		mode                 glossary.Mode
		span                 unit.ProtectionSpan
	}{
		{"cat runs", "at", "", glossary.Protect, unit.ProtectionSpan{Start: 0, End: 3, Kind: unit.Placeholder}},
		{"cat runs", "cat runs", "", glossary.Protect, unit.ProtectionSpan{Start: 0, End: 3, Kind: unit.Placeholder}},
		{"cat runs", "cat", "猫", glossary.Mandatory, unit.ProtectionSpan{Start: 0, End: 3, Kind: unit.Placeholder}},
		{"cat runs", "cat", "猫", glossary.Prefer, unit.ProtectionSpan{Start: 0, End: 3, Kind: unit.Placeholder}},
	} {
		t.Run(tt.term+string(tt.mode), func(t *testing.T) {
			// Given
			session, id := fixtureSession(t, tt.source, []unit.ProtectionSpan{tt.span})
			g, err := glossary.New(glossary.Schema, []glossary.Entry{{Source: tt.term, Target: tt.target, Mode: tt.mode, Match: glossary.Literal}})
			if err != nil {
				t.Fatal(err)
			}
			// When
			_, err = protect.PrepareWithGlossary(session, id, g)
			// Then
			if !errors.Is(err, protect.ErrInvalidSource) || !errors.Is(err, glossary.ErrConflict) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestGlossaryProtectionRetainsAdapterUndeclaredOccurrenceCheck(t *testing.T) {
	// Given
	session, id := fixtureSession(t, "cat cat", []unit.ProtectionSpan{{Start: 0, End: 3, Kind: unit.Placeholder}})
	// When
	_, err := protect.PrepareWithGlossary(session, id, protectedGlossary(t, "cat", glossary.Word))
	// Then
	if !errors.Is(err, protect.ErrInvalidSource) {
		t.Fatalf("err=%v", err)
	}
}

func TestGlossaryProtectionRejectsForeignImport(t *testing.T) {
	// Given
	session, id := fixtureSession(t, "cat", nil)
	foreign, _ := fixtureSession(t, "cat", nil)
	prepared, err := protect.PrepareWithGlossary(session, id, protectedGlossary(t, "cat", glossary.Word))
	if err != nil {
		t.Fatal(err)
	}
	// When
	_, err = prepared.Restore(foreign, id, prepared.Text())
	// Then
	if !errors.Is(err, protect.ErrInvalidPrepared) {
		t.Fatalf("err=%v", err)
	}
}

func TestGlossaryTermsAreIndivisibleWithoutTokens(t *testing.T) {
	// Given
	session, id := fixtureSession(t, "a red cat. b", nil)
	g, err := glossary.New(glossary.Schema, []glossary.Entry{{Source: "red cat.", Target: "赤い猫", Mode: glossary.Prefer, Match: glossary.Literal}})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := protect.PrepareWithGlossary(session, id, g)
	if err != nil {
		t.Fatal(err)
	}
	// When
	_, err = prepared.Slice(0, strings.Index(prepared.Text(), "cat"))
	// Then
	if !errors.Is(err, protect.ErrInvalidPrepared) {
		t.Fatalf("err=%v", err)
	}
	for _, cut := range prepared.Cuts() {
		if cut.End > 2 && cut.End < 10 {
			t.Fatalf("cut inside term: %+v", cut)
		}
	}
}

func TestGlossaryTermCannotEscapeIntoSeparator(t *testing.T) {
	// Given
	session, id := fixtureSession(t, "a b", nil)
	g, err := glossary.New(glossary.Schema, []glossary.Entry{{Source: " ", Target: "・", Mode: glossary.Mandatory, Match: glossary.Literal}})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := protect.PrepareWithGlossary(session, id, g)
	if err != nil {
		t.Fatal(err)
	}
	// When
	cuts := prepared.Cuts()
	// Then
	if len(cuts) != 0 {
		t.Fatalf("term escaped into separator: %+v", cuts)
	}
}

func TestGlossaryAdapterSameOutputCanCoexist(t *testing.T) {
	for _, mode := range []glossary.Mode{glossary.Prefer, glossary.Mandatory} {
		t.Run(string(mode), func(t *testing.T) {
			// Given
			session, id := fixtureSession(t, "cat runs", []unit.ProtectionSpan{{Start: 0, End: 3, Kind: unit.Placeholder}})
			g, err := glossary.New(glossary.Schema, []glossary.Entry{{Source: "cat", Target: "cat", Mode: mode, Match: glossary.Word}})
			if err != nil {
				t.Fatal(err)
			}
			// When
			prepared, err := protect.PrepareWithGlossary(session, id, g)
			// Then
			if err != nil || prepared.Text() != "[[YAKUORI_0_0]] runs" {
				t.Fatalf("text=%q err=%v", prepared.Text(), err)
			}
		})
	}
}

func TestCompatibleProtectEntriesKeepEachBoundary(t *testing.T) {
	session, id := fixtureSession(t, "cat runs", nil)
	g, err := glossary.New(glossary.Schema, []glossary.Entry{
		{Source: "cat", Mode: glossary.Protect, Match: glossary.Literal},
		{Source: "cat", Mode: glossary.Protect, Match: glossary.Word},
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := protect.PrepareWithGlossary(session, id, g)
	if err != nil {
		t.Fatal(err)
	}
	piece, err := prepared.Slice(0, len(prepared.Text()))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, tokenized, restored string
		invalid                   bool
	}{
		{"compatible", "[[YAKUORI_0_0]] walks", "cat walks", false},
		{"word boundary lost", "s[[YAKUORI_0_0]]ter runs", "scatter runs", true},
	} {
		for _, method := range []string{"restore", "restored", "piece"} {
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				var err error
				switch method {
				case "restore":
					_, err = prepared.Restore(session, id, tc.tokenized)
				case "restored":
					err = prepared.CheckRestored(session, id, tc.restored)
				case "piece":
					err = prepared.CheckCandidate(piece, tc.tokenized)
				}
				if tc.invalid {
					if !errors.Is(err, protect.ErrInvalidCandidate) {
						t.Fatalf("error=%v; want lost word-boundary rejection", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
