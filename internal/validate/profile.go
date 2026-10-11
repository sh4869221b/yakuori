package validate

import "github.com/sh4869221b/yakuori/internal/identity"

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
