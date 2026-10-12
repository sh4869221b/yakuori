package validate

import (
	"errors"
	"testing"

	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/identity"
)

func glossaryProfile(t *testing.T, entries ...glossary.Entry) Profile {
	t.Helper()
	g, err := glossary.New(glossary.Schema, entries)
	if err != nil {
		t.Fatal(err)
	}
	input := canonicalInput()
	input.Glossary = GlossaryInput(g)
	p, err := NewCanonicalProfile(input)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestValidateMandatoryUsesRawParentAndExactTarget(t *testing.T) {
	for _, tt := range []struct {
		name, source, target, candidate string
		mode                            glossary.Mode
		match                           glossary.MatchKind
		invalid                         bool
	}{
		{"matching target", "cat", "猫", "猫です", glossary.Mandatory, glossary.Literal, false},
		{"two sources one target", "cat cat", "猫", "猫です", glossary.Mandatory, glossary.Word, false},
		{"missing target", "cat", "猫", "犬です", glossary.Mandatory, glossary.Word, true},
		{"target case", "cat", "Cat", "cat", glossary.Mandatory, glossary.Word, true},
		{"target unicode normalization", "cat", "é", "e\u0301", glossary.Mandatory, glossary.Word, true},
		{"source case differs", "Cat", "猫", "犬です", glossary.Mandatory, glossary.Word, false},
		{"source absent", "dog", "猫", "犬です", glossary.Mandatory, glossary.Word, false},
		{"word inside another word", "scatter", "猫", "犬です", glossary.Mandatory, glossary.Word, false},
		{"literal inside another word", "scatter", "猫", "犬です", glossary.Mandatory, glossary.Literal, true},
		{"prefer not mandatory", "cat", "猫", "犬です", glossary.Prefer, glossary.Word, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			session, ids, _ := fixture(t, tt.source)
			profile := glossaryProfile(t, glossary.Entry{Source: "cat", Target: tt.target, Mode: tt.mode, Match: tt.match})
			// When
			accepted, err := Validate(session, ids[0], profile, tt.candidate)
			// Then
			if tt.invalid {
				if !errors.Is(err, ErrInvalidCandidate) || accepted != (AcceptedTranslation{}) {
					t.Fatalf("accepted=%v err=%v", accepted, err)
				}
			} else if err != nil || accepted.Text() != tt.candidate || CheckBinding(session, ids[0], profile, accepted) != nil {
				t.Fatalf("accepted=%v err=%v", accepted, err)
			}
		})
	}
}

func TestValidateGlossaryConflictIsInvalidSource(t *testing.T) {
	// Given
	session, ids, _ := fixture(t, "cat")
	profile := glossaryProfile(t,
		glossary.Entry{Source: "cat", Target: "猫", Mode: glossary.Mandatory, Match: glossary.Word},
		glossary.Entry{Source: "cat", Target: "犬", Mode: glossary.Prefer, Match: glossary.Word},
	)
	// When
	accepted, err := Validate(session, ids[0], profile, "猫と犬")
	// Then
	if !errors.Is(err, ErrInvalidSource) || !errors.Is(err, glossary.ErrConflict) || errors.Is(err, ErrInvalidCandidate) || accepted != (AcceptedTranslation{}) {
		t.Fatalf("accepted=%v err=%v", accepted, err)
	}
}

func TestValidateRejectsUnsupportedOrNoncanonicalGlossary(t *testing.T) {
	for _, tt := range []struct {
		name, schema string
		entries      []byte
	}{
		{"unsupported schema", "future", []byte(`[]`)},
		{"opaque entries", "v1", []byte{0, 255}},
		{"noncanonical entries", glossary.Schema, []byte(`[ {"source":"cat","target":"猫","mode":"mandatory","match":"word"} ]`)},
		{"provided empty entries", glossary.Schema, []byte(`[]`)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Given: opaque identity remains constructible, but cannot execute.
			session, ids, _ := fixture(t, "cat")
			input := canonicalInput()
			input.Glossary = identity.GlossaryInput{Mode: "provided", Schema: tt.schema, Entries: tt.entries}
			profile, err := NewCanonicalProfile(input)
			if err != nil {
				t.Fatal(err)
			}
			// When
			accepted, err := Validate(session, ids[0], profile, "猫")
			// Then
			if !errors.Is(err, ErrInvalidProfile) || !errors.Is(err, glossary.ErrInvalidGlossary) || errors.Is(err, ErrInvalidCandidate) || accepted != (AcceptedTranslation{}) {
				t.Fatalf("accepted=%v err=%v", accepted, err)
			}
		})
	}
}

func TestValidateGlossaryProtectUsesMatchingBoundaries(t *testing.T) {
	for _, tt := range []struct {
		candidate string
		invalid   bool
	}{
		{"cat scatter", false}, {"cat 散らす", false}, {"scatter", true}, {"cat cat 散らす", true}, {"scat 散らす", true},
	} {
		t.Run(tt.candidate, func(t *testing.T) {
			// Given
			session, ids, _ := fixture(t, "cat scatter")
			profile := glossaryProfile(t, glossary.Entry{Source: "cat", Mode: glossary.Protect, Match: glossary.Word})
			// When
			accepted, err := Validate(session, ids[0], profile, tt.candidate)
			// Then
			if tt.invalid {
				if !errors.Is(err, ErrInvalidCandidate) || accepted != (AcceptedTranslation{}) {
					t.Fatalf("accepted=%v err=%v", accepted, err)
				}
			} else if err != nil || accepted.Text() != tt.candidate {
				t.Fatalf("accepted=%v err=%v", accepted, err)
			}
		})
	}
}
