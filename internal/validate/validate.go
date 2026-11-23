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
	v, err := profile.WithAdditionalTerms(nil)
	if err != nil {
		return AcceptedTranslation{}, fmt.Errorf("%w: %w", ErrInvalidProfile, err)
	}
	return v.validate(session, u, candidate)
}

func (v UnitValidation) Validate(session unit.Session, id unit.UnitID, candidate string) (AcceptedTranslation, error) {
	u, err := session.Unit(id)
	if err != nil {
		return AcceptedTranslation{}, err
	}
	if v.profile.digest == ([32]byte{}) {
		return AcceptedTranslation{}, ErrInvalidProfile
	}
	return v.validate(session, u, candidate)
}

func (v UnitValidation) validate(session unit.Session, u unit.TranslationUnit, candidate string) (AcceptedTranslation, error) {
	id := u.ID()
	source := string(u.Source())
	matches, err := v.terms.Match(source)
	if err != nil {
		return AcceptedTranslation{}, fmt.Errorf("%w: %w", ErrInvalidSource, err)
	}
	prepared, err := protect.PrepareWithGlossary(session, id, v.terms)
	if err != nil {
		return AcceptedTranslation{}, fmt.Errorf("%w: %w", ErrInvalidSource, err)
	}
	sourcePayload, candidatePayload, err := prepared.CheckRestoredPayload(session, id, candidate)
	if err != nil {
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
			profile: v.profile,
		},
		validated: true,
		review:    reviewContent(u.TargetLanguage(), source, candidate, sourcePayload, candidatePayload),
	}, nil
}
