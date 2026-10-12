package validate

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/unit"
)

var (
	ErrInvalidSource    = errors.New("invalid source text")
	ErrInvalidCandidate = errors.New("invalid candidate text")
)

// Validate checks restored parent text and binds the result to this import.
func Validate(session unit.Session, id unit.UnitID, profile Profile, candidate string) (AcceptedTranslation, error) {
	u, err := session.Unit(id)
	if err != nil {
		return AcceptedTranslation{}, err
	}
	if profile.digest == ([32]byte{}) {
		return AcceptedTranslation{}, ErrInvalidProfile
	}
	g, err := profile.Glossary()
	if err != nil {
		return AcceptedTranslation{}, fmt.Errorf("%w: %w", ErrInvalidProfile, err)
	}
	source := string(u.Source())
	matches, err := g.Match(source)
	if err != nil {
		return AcceptedTranslation{}, fmt.Errorf("%w: %w", ErrInvalidSource, err)
	}
	prepared, err := protect.PrepareWithGlossary(session, id, g)
	if err != nil {
		return AcceptedTranslation{}, fmt.Errorf("%w: %w", ErrInvalidSource, err)
	}
	if err := prepared.CheckRestored(session, id, candidate); err != nil {
		return AcceptedTranslation{}, fmt.Errorf("%w: %w", ErrInvalidCandidate, err)
	}
	if err := checkContent(source, candidate); err != nil {
		return AcceptedTranslation{}, err
	}
	for _, match := range matches {
		if match.Entry.Mode == glossary.Mandatory && !strings.Contains(candidate, match.Entry.Target) {
			return AcceptedTranslation{}, fmt.Errorf("%w: missing mandatory target %q", ErrInvalidCandidate, match.Entry.Target)
		}
	}
	return AcceptedTranslation{
		text: candidate,
		binding: binding{
			session: session,
			unitID:  id,
			source:  u.SourceDigest(),
			profile: profile,
		},
		validated: true,
		review:    reviewContent(u, source, candidate),
	}, nil
}
