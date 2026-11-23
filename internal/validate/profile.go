package validate

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/identity"
)

// NewCanonicalProfile derives a profile from complete effective conditions.
func NewCanonicalProfile(input identity.ProfileInput) (Profile, error) {
	canonical, err := identity.NewProfileIdentity(input)
	if err != nil {
		return Profile{}, err
	}
	schema, fixtures := canonical.Segmentation()
	return Profile{digest: canonical.Digest(), segmentation: SegmentationInput{Schema: schema, BoundaryFixtures: fixtures}, canonical: canonical}, nil
}

func (p Profile) CanonicalIdentity() identity.ProfileIdentity { return p.canonical }
func (p Profile) CanonicalBytes() []byte                      { return p.canonical.CanonicalBytes() }
func (p Profile) ExactKey(source identity.SourceIdentity) (identity.ExactKey, error) {
	return identity.NewExactKey(source, p.canonical)
}

// GlossaryInput snapshots executable terms for the canonical profile input.
func GlossaryInput(g glossary.Glossary) identity.GlossaryInput {
	if g.Empty() {
		return identity.GlossaryInput{Mode: "none"}
	}
	return identity.GlossaryInput{Mode: "provided", Schema: g.Schema(), Entries: g.CanonicalBytes()}
}

// Glossary parses the exact profile definition at its execution boundary.
// Legacy profiles have no glossary; canonical profiles must use canonical terms.
func (p Profile) Glossary() (glossary.Glossary, error) {
	input := p.canonical.Glossary()
	if input.Mode == "" || input.Mode == "none" {
		return glossary.Glossary{}, nil
	}
	g, err := glossary.Parse(input.Schema, input.Entries)
	if err != nil {
		return glossary.Glossary{}, err
	}
	if g.Empty() || !bytes.Equal(input.Entries, g.CanonicalBytes()) {
		return glossary.Glossary{}, fmt.Errorf("%w: profile entries are not canonical", glossary.ErrInvalidGlossary)
	}
	return g, nil
}

// UnitValidation extends a profile's glossary for one unit while binding
// accepted translations to the original run profile.
type UnitValidation struct {
	profile    Profile
	terms      glossary.Glossary
	additional glossary.Glossary
}

func (p Profile) WithAdditionalTerms(additions []glossary.Entry) (UnitValidation, error) {
	base, err := p.Glossary()
	if err != nil {
		return UnitValidation{}, err
	}
	validation := UnitValidation{profile: p, terms: base}
	if len(additions) == 0 {
		return validation, nil
	}
	entries, err := base.Entries()
	if err != nil {
		return UnitValidation{}, err
	}
	var unique []glossary.Entry
	for _, entry := range additions {
		if !slices.Contains(entries, entry) {
			unique = append(unique, entry)
		}
	}
	if len(unique) == 0 {
		return validation, nil
	}
	validation.additional, err = glossary.New(glossary.Schema, unique)
	if err != nil {
		return UnitValidation{}, err
	}
	validation.terms, err = glossary.New(glossary.Schema, append(entries, unique...))
	if err != nil {
		return UnitValidation{}, err
	}
	return validation, nil
}

func (v UnitValidation) Glossary() glossary.Glossary        { return v.terms }
func (v UnitValidation) AdditionalTerms() glossary.Glossary { return v.additional }
