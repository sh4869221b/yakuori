package validate

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sh4869221b/yakuori/internal/unit"
)

var (
	ErrInvalidSource    = errors.New("invalid source text")
	ErrInvalidCandidate = errors.New("invalid candidate text")
)

// Validate enforces the foundation text rules and binds the result to this import.
// Protected tokens and content rules are added here by the subsequent validation work.
func Validate(session unit.Session, id unit.UnitID, profile Profile, candidate string) (AcceptedTranslation, error) {
	u, err := session.Unit(id)
	if err != nil {
		return AcceptedTranslation{}, err
	}
	if profile.digest == ([32]byte{}) {
		return AcceptedTranslation{}, ErrInvalidProfile
	}
	source := u.Source()
	if !utf8.Valid(source) {
		return AcceptedTranslation{}, fmt.Errorf("%w: invalid UTF-8", ErrInvalidSource)
	}
	if bytes.IndexByte(source, 0) >= 0 {
		return AcceptedTranslation{}, fmt.Errorf("%w: NUL", ErrInvalidSource)
	}
	if !utf8.ValidString(candidate) {
		return AcceptedTranslation{}, fmt.Errorf("%w: invalid UTF-8", ErrInvalidCandidate)
	}
	if strings.IndexByte(candidate, 0) >= 0 {
		return AcceptedTranslation{}, fmt.Errorf("%w: NUL", ErrInvalidCandidate)
	}
	if (len(source) == 0) != (candidate == "") {
		return AcceptedTranslation{}, fmt.Errorf("%w: source and candidate emptiness differ", ErrInvalidCandidate)
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
	}, nil
}
