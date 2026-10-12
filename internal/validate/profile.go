package validate

import (
	"bytes"
	"fmt"

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
