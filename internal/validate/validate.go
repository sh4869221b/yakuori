package validate

import (
	"errors"
	"fmt"

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
	prepared, err := protect.Prepare(session, id)
	if err != nil {
		return AcceptedTranslation{}, fmt.Errorf("%w: %v", ErrInvalidSource, err)
	}
	if err := prepared.CheckRestored(session, id, candidate); err != nil {
		return AcceptedTranslation{}, fmt.Errorf("%w: %v", ErrInvalidCandidate, err)
	}
	source := string(u.Source())
	if err := checkContent(source, candidate); err != nil {
		return AcceptedTranslation{}, err
	}
	return AcceptedTranslation{
		text: candidate,
		binding: binding{
			session: session,
			unitID:  id,
			source:  u.SourceDigest(),
			profile: profile.digest,
		},
		validated: true,
		review:    reviewContent(u, source, candidate),
	}, nil
}
