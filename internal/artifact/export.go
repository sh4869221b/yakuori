package artifact

import (
	"errors"
	"fmt"

	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

var (
	ErrIncompleteExport     = errors.New("incomplete export")
	ErrDuplicateTranslation = errors.New("duplicate accepted translation")
	ErrInvalidExpected      = errors.New("invalid expected translations")
)

type ExpectedTranslations struct {
	session unit.Session
	profile validate.Profile
	texts   map[unit.UnitID]string
}

func PrepareExport(session unit.Session, profile validate.Profile, accepted []validate.AcceptedTranslation) (ExpectedTranslations, error) {
	if err := session.Check(); err != nil {
		return ExpectedTranslations{}, err
	}
	if profile.Digest() == ([32]byte{}) {
		return ExpectedTranslations{}, validate.ErrInvalidProfile
	}
	texts := make(map[unit.UnitID]string, len(accepted))
	for _, translation := range accepted {
		id := translation.UnitID()
		if err := validate.CheckBinding(session, id, profile, translation); err != nil {
			return ExpectedTranslations{}, err
		}
		if _, exists := texts[id]; exists {
			return ExpectedTranslations{}, fmt.Errorf("%w: %s", ErrDuplicateTranslation, id.StableID())
		}
		texts[id] = translation.Text()
	}
	if len(texts) != len(session.Units()) {
		return ExpectedTranslations{}, ErrIncompleteExport
	}
	return ExpectedTranslations{session: session, profile: profile, texts: texts}, nil
}

func (e ExpectedTranslations) Text(id unit.UnitID) (string, bool) {
	text, exists := e.texts[id]
	return text, exists
}

func (e ExpectedTranslations) Len() int { return len(e.texts) }
