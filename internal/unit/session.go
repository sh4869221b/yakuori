package unit

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
)

var (
	ErrInvalidSession = errors.New("invalid import session")
	ErrDuplicateUnit  = errors.New("duplicate unit ID")
	ErrUnknownUnit    = errors.New("unknown unit ID")
)

// Session copies share one import; NewSession always creates a distinct import.
type Session struct {
	data *sessionData
}

// This allocation has nonzero size, so pointer identity distinguishes imports
// even when all their artifact and source bytes are equal.
type sessionData struct {
	artifact       []byte
	artifactDigest [32]byte
	units          []TranslationUnit
	index          map[UnitID]int
}

func NewSession(artifact []byte, units []TranslationUnit) (Session, error) {
	index := make(map[UnitID]int, len(units))
	sessionUnits := slices.Clone(units)
	for i, u := range units {
		if u.ID() == (UnitID{}) {
			return Session{}, ErrInvalidUnitID
		}
		if _, exists := index[u.ID()]; exists {
			return Session{}, fmt.Errorf("%w: %s", ErrDuplicateUnit, u.ID().StableID())
		}
		index[u.ID()] = i
		sessionUnits[i].protectionSpans = slices.Clone(u.protectionSpans)
	}
	return Session{data: &sessionData{
		artifact:       bytes.Clone(artifact),
		artifactDigest: sha256.Sum256(artifact),
		units:          sessionUnits,
		index:          index,
	}}, nil
}

func (s Session) Check() error {
	if s.data == nil {
		return ErrInvalidSession
	}
	return nil
}

func (s Session) SameImport(other Session) bool {
	return s.data != nil && s.data == other.data
}

func (s Session) Unit(id UnitID) (TranslationUnit, error) {
	if err := s.Check(); err != nil {
		return TranslationUnit{}, err
	}
	i, exists := s.data.index[id]
	if !exists {
		return TranslationUnit{}, ErrUnknownUnit
	}
	return s.data.units[i], nil
}

func (s Session) Units() []TranslationUnit {
	if s.data == nil {
		return nil
	}
	return slices.Clone(s.data.units)
}

func (s Session) Artifact() []byte {
	if s.data == nil {
		return nil
	}
	return bytes.Clone(s.data.artifact)
}

func (s Session) ArtifactDigest() [32]byte {
	if s.data == nil {
		return [32]byte{}
	}
	return s.data.artifactDigest
}

// ArtifactBytes returns the retained byte count without copying the artifact.
func (s Session) ArtifactBytes() int {
	if s.data == nil {
		return 0
	}
	return len(s.data.artifact)
}

// UnitCount returns the retained unit count without copying the unit slice.
func (s Session) UnitCount() int {
	if s.data == nil {
		return 0
	}
	return len(s.data.units)
}
