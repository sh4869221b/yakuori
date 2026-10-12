package validate

import (
	"bytes"
	"testing"

	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func TestEquivalentGlossaryProfilesPreserveBinding(t *testing.T) {
	// Given: independent construction, reordered entries, and a duplicate.
	a := glossary.Entry{Source: "cat", Target: "猫", Mode: glossary.Mandatory, Match: glossary.Word}
	b := glossary.Entry{Source: "dog", Target: "犬", Mode: glossary.Prefer, Match: glossary.Literal}
	first := glossaryProfile(t, a, b)
	second := glossaryProfile(t, b, a, a)
	session, ids, _ := fixture(t, "cat")
	accepted, err := Validate(session, ids[0], first, "猫")
	if err != nil {
		t.Fatal(err)
	}
	// When
	err = CheckBinding(session, ids[0], second, accepted)
	// Then
	if err != nil || first != second {
		t.Fatalf("equivalent profile binding error=%v", err)
	}
}

func TestGlossarySemanticFieldsChangeExactKey(t *testing.T) {
	baseEntry := glossary.Entry{Source: "cat", Target: "猫", Mode: glossary.Mandatory, Match: glossary.Word}
	session, ids, _ := fixture(t, "cat")
	source, err := unit.NewSourceIdentity(session, ids[0], "game", "artifact", nil)
	if err != nil {
		t.Fatal(err)
	}
	base := glossaryProfile(t, baseEntry)
	baseKey, err := base.ExactKey(source)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*glossary.Entry){
		"source": func(e *glossary.Entry) { e.Source = "dog" },
		"target": func(e *glossary.Entry) { e.Target = "犬" },
		"mode":   func(e *glossary.Entry) { e.Mode = glossary.Prefer },
		"match":  func(e *glossary.Entry) { e.Match = glossary.Literal },
	} {
		t.Run(name, func(t *testing.T) {
			// Given
			entry := baseEntry
			change(&entry)
			other := glossaryProfile(t, entry)
			// When
			key, err := other.ExactKey(source)
			// Then
			if err != nil || key == baseKey || other == base {
				t.Fatalf("semantic change retained profile/key: %v", err)
			}
		})
	}
	t.Run("schema", func(t *testing.T) {
		// Given: identity permits future schemas, execution rejects them separately.
		input := canonicalInput()
		input.Glossary = base.CanonicalIdentity().Glossary()
		input.Glossary.Schema += "-future"
		other, err := NewCanonicalProfile(input)
		if err != nil {
			t.Fatal(err)
		}
		// When
		key, err := other.ExactKey(source)
		// Then
		if err != nil || key == baseKey || other == base {
			t.Fatalf("schema change retained profile/key: %v", err)
		}
	})
}

func TestProfileGlossarySnapshotsExactIdentityBytes(t *testing.T) {
	// Given
	g, err := glossary.New(glossary.Schema, []glossary.Entry{{Source: "cat", Target: "猫", Mode: glossary.Mandatory, Match: glossary.Word}})
	if err != nil {
		t.Fatal(err)
	}
	input := canonicalInput()
	input.Glossary = GlossaryInput(g)
	profile, err := NewCanonicalProfile(input)
	if err != nil {
		t.Fatal(err)
	}
	before := profile.CanonicalBytes()
	input.Glossary.Entries[0] = '!'
	returned := profile.CanonicalIdentity().Glossary()
	returned.Entries[0] = '!'
	// When
	actual, err := profile.Glossary()
	// Then
	if err != nil || actual != g || !bytes.Equal(profile.CanonicalBytes(), before) || !bytes.Equal(profile.CanonicalIdentity().Glossary().Entries, g.CanonicalBytes()) {
		t.Fatalf("glossary snapshot changed: %v", err)
	}
}

func TestEmptyGlossaryKeepsNoGlossaryProfile(t *testing.T) {
	// Given
	without, err := NewCanonicalProfile(canonicalInput())
	if err != nil {
		t.Fatal(err)
	}
	empty := glossaryProfile(t)
	// When
	actual, err := empty.Glossary()
	// Then
	if err != nil || !actual.Empty() || without != empty {
		t.Fatalf("empty glossary changed profile: %v", err)
	}
}
