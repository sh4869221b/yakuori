package validate

import (
	"bytes"
	"errors"
	"testing"

	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func canonicalInput() identity.ProfileInput {
	return identity.ProfileInput{
		Version: 1, ModelID: "model", ModelDigest: [32]byte{1}, Backend: "cpu", BackendPin: "goinfer-v1", ComputeQuant: "q4", KVPrecision: "not-applicable",
		Tokenizer: identity.TokenizerInput{ModelDigest: [32]byte{1}, BackendPin: "tokenizer-v1"}, Template: identity.TemplateInput{Family: "chat", Source: "embedded", RendererVersion: "renderer-v1"},
		Prompt:       identity.PromptInput{Schema: 1, Variant: "plain", SystemTemplate: []byte("system"), UserTemplate: []byte("user"), LanguageAliases: "aliases-v1"},
		Generation:   identity.GenerationInput{Schema: 1, MaxOutputTokens: 32, RequestTimeoutNS: 1, SeedFixed: true, Thinking: "disabled", Prefill: identity.PrefillInput{Path: "cpu"}},
		Segmentation: identity.SegmentationInput{Mode: "enabled", Schema: "segments-v1", BoundaryFixtures: "fixtures-v1", UnitTextBytes: 980, Segments: 76},
		Glossary:     identity.GlossaryInput{Mode: "none"}, Protection: identity.SchemaInput{Schema: "protection-v1", FixtureSet: "fixtures-v1"}, Validation: identity.SchemaInput{Schema: "validation-v1", FixtureSet: "fixtures-v1"}, Context: identity.ContextInput{Tokens: 4096, Mode: "none"},
	}
}

func TestCanonicalProfileBindingRequiresCompleteProfileAndCurrentImport(t *testing.T) {
	session, ids, _ := fixture(t, "source", "other source")
	profile, err := NewCanonicalProfile(canonicalInput())
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := Validate(session, ids[0], profile, "訳文")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckBinding(session, ids[0], profile, accepted); err != nil {
		t.Fatal(err)
	}
	legacy, err := NewProfileWithSegmentation(profile.Digest(), profile.Segmentation())
	if err != nil {
		t.Fatal(err)
	}
	otherSession, _, _ := fixture(t, "source", "other source")
	for _, tt := range []struct {
		name    string
		session unit.Session
		id      unit.UnitID
		profile Profile
	}{
		{"same digest legacy", session, ids[0], legacy},
		{"different session", otherSession, ids[0], profile},
		{"different unit", session, ids[1], profile},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := CheckBinding(tt.session, tt.id, tt.profile, accepted); !errors.Is(err, ErrBindingMismatch) {
				t.Fatalf("binding error=%v", err)
			}
		})
	}
	legacyAccepted, err := Validate(session, ids[0], legacy, "訳文")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckBinding(session, ids[0], profile, legacyAccepted); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("legacy acceptance binding error=%v", err)
	}
}

func TestCanonicalProfileExactCandidateStillRequiresValidation(t *testing.T) {
	profile, err := NewCanonicalProfile(canonicalInput())
	if err != nil {
		t.Fatal(err)
	}
	id, err := unit.NewUnitID("fixture", "v1", "a")
	if err != nil {
		t.Fatal(err)
	}
	u, err := unit.NewTranslationUnitWithProtection(id, []byte("Hello {p}"), "en", "ja", nil, []unit.ProtectionSpan{{Start: 6, End: 9, Kind: unit.Placeholder}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := unit.NewSession([]byte("artifact"), []unit.TranslationUnit{u})
	if err != nil {
		t.Fatal(err)
	}
	current, err := unit.NewSession([]byte("artifact"), []unit.TranslationUnit{u})
	if err != nil {
		t.Fatal(err)
	}
	firstSource, err := unit.NewSourceIdentity(first, id, "game", "artifact", nil)
	if err != nil {
		t.Fatal(err)
	}
	currentSource, err := unit.NewSourceIdentity(current, id, "game", "artifact", nil)
	if err != nil {
		t.Fatal(err)
	}
	firstKey, err := profile.ExactKey(firstSource)
	if err != nil {
		t.Fatal(err)
	}
	currentKey, err := profile.ExactKey(currentSource)
	if err != nil || firstKey != currentKey {
		t.Fatalf("same candidate key differs: %v", err)
	}
	accepted, err := Validate(first, id, profile, "こんにちは {p}")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckBinding(current, id, profile, accepted); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("previous import acceptance reused: %v", err)
	}
	accepted, err = Validate(current, id, profile, "こんにちは [[YAKUORI_0_9]]")
	if !errors.Is(err, ErrInvalidCandidate) || accepted != (AcceptedTranslation{}) {
		t.Fatalf("invalid protected candidate accepted=%v error=%v", accepted, err)
	}
	accepted, err = Validate(current, id, profile, "こんにちは {p}")
	if err != nil || CheckBinding(current, id, profile, accepted) != nil {
		t.Fatalf("current validation failed: %v", err)
	}
}
func TestCanonicalProfileDerivedComparableIdentity(t *testing.T) {
	input := canonicalInput()
	canonical, err := identity.NewProfileIdentity(input)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewCanonicalProfile(input)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Digest() != canonical.Digest() || profile.CanonicalIdentity() != canonical || !bytes.Equal(profile.CanonicalBytes(), canonical.CanonicalBytes()) || profile.Segmentation() != (SegmentationInput{Schema: "segments-v1", BoundaryFixtures: "fixtures-v1"}) {
		t.Fatal("profile lost canonical identity or segmentation")
	}
	equal, err := NewCanonicalProfile(canonicalInput())
	if err != nil {
		t.Fatal(err)
	}
	if profile != equal {
		t.Fatal("same effective conditions differ")
	}
	legacy, err := NewProfileWithSegmentation(profile.Digest(), profile.Segmentation())
	if err != nil {
		t.Fatal(err)
	}
	if profile == legacy {
		t.Fatal("canonical and legacy profiles compare equal")
	}
	input.Prompt.SystemTemplate[0] = '!'
	returned := profile.CanonicalBytes()
	returned[0] = '!'
	if profile != equal {
		t.Fatal("profile snapshot mutated")
	}
}
func TestCanonicalProfileExactKeyEligibility(t *testing.T) {
	source, err := identity.NewSourceIdentity(identity.SourceInput{Version: 1, GameNamespace: "game", ArtifactNamespace: "artifact", OriginalArtifactDigest: [32]byte{1}, Adapter: "text", FormatSchema: "v1", StableID: "record", RawSourceDigest: [32]byte{2}, SourceLanguage: "en", TargetLanguage: "ja"})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := NewCanonicalProfile(canonicalInput())
	if err != nil {
		t.Fatal(err)
	}
	key, err := canonical.ExactKey(source)
	if err != nil {
		t.Fatal(err)
	}
	if key.Schema() != 1 || !bytes.Equal(key.ProfileBytes(), canonical.CanonicalBytes()) {
		t.Fatal("canonical profile lost key bytes")
	}
	legacy, err := NewProfile(canonical.Digest())
	if err != nil {
		t.Fatal(err)
	}
	segmentedLegacy, err := NewProfileWithSegmentation(canonical.Digest(), canonical.Segmentation())
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []Profile{{}, legacy, segmentedLegacy} {
		key, err := profile.ExactKey(source)
		if !errors.Is(err, identity.ErrInvalidKey) || key != (identity.ExactKey{}) {
			t.Fatalf("legacy/zero key=%#v, err=%v", key, err)
		}
	}
}
func TestCanonicalProfileRejectsInvalidInput(t *testing.T) {
	input := canonicalInput()
	input.Version = 2
	profile, err := NewCanonicalProfile(input)
	if !errors.Is(err, identity.ErrInvalidProfile) || profile != (Profile{}) {
		t.Fatalf("profile=%#v, err=%v", profile, err)
	}
}
