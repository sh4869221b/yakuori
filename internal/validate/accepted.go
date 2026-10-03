package validate

import (
	"errors"

	"github.com/sh4869221b/yakuori/internal/unit"
)

var (
	ErrInvalidProfile  = errors.New("invalid profile hash")
	ErrInvalidAccepted = errors.New("invalid accepted translation")
	ErrBindingMismatch = errors.New("translation binding mismatch")
)

type Profile struct {
	digest [32]byte
}

func NewProfile(digest [32]byte) (Profile, error) {
	if digest == ([32]byte{}) {
		return Profile{}, ErrInvalidProfile
	}
	return Profile{digest: digest}, nil
}

func (p Profile) Digest() [32]byte { return p.digest }

type binding struct {
	session unit.Session
	unitID  unit.UnitID
	source  [32]byte
	profile [32]byte
}

type AcceptedTranslation struct {
	text      string
	binding   binding
	validated bool
}

func (a AcceptedTranslation) Text() string        { return a.text }
func (a AcceptedTranslation) UnitID() unit.UnitID { return a.binding.unitID }

func CheckBinding(session unit.Session, id unit.UnitID, profile Profile, accepted AcceptedTranslation) error {
	u, err := session.Unit(id)
	if err != nil {
		return err
	}
	if profile.digest == ([32]byte{}) {
		return ErrInvalidProfile
	}
	if !accepted.validated {
		return ErrInvalidAccepted
	}
	b := accepted.binding
	if !session.SameImport(b.session) || id != b.unitID || u.SourceDigest() != b.source || profile.digest != b.profile {
		return ErrBindingMismatch
	}
	return nil
}
