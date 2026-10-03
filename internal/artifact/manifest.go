package artifact

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

var (
	ErrInvalidManifest = errors.New("invalid artifact manifest")
	ErrInputMismatch   = errors.New("input artifact mismatch")
)

type KeyRelation struct {
	UnitID unit.UnitID
	Key    string
}

type Manifest struct {
	session       unit.Session
	profile       [32]byte
	adapter       string
	formatSchema  string
	artifact      [32]byte
	units         map[unit.UnitID]unit.TranslationUnit
	keys          map[KeyRelation]int
	finalMetadata map[string][]byte
}

func NewManifest(session unit.Session, profile validate.Profile, adapter, formatSchema string, keys []KeyRelation, preservedMetadata, expectedMetadata map[string][]byte) (Manifest, error) {
	if err := session.Check(); err != nil {
		return Manifest{}, err
	}
	if profile.Digest() == ([32]byte{}) {
		return Manifest{}, validate.ErrInvalidProfile
	}
	if adapter == "" || formatSchema == "" {
		return Manifest{}, ErrInvalidManifest
	}
	units := make(map[unit.UnitID]unit.TranslationUnit)
	for _, u := range session.Units() {
		if u.ID().Adapter() != adapter || u.ID().FormatSchema() != formatSchema {
			return Manifest{}, fmt.Errorf("%w: unit schema", ErrInvalidManifest)
		}
		units[u.ID()] = u
	}
	relations := make(map[KeyRelation]int, len(keys))
	for _, key := range keys {
		if _, exists := units[key.UnitID]; !exists {
			return Manifest{}, fmt.Errorf("%w: key references unknown unit", ErrInvalidManifest)
		}
		relations[key]++
	}
	metadata := make(map[string][]byte, len(preservedMetadata)+len(expectedMetadata))
	for name, value := range preservedMetadata {
		metadata[name] = bytes.Clone(value)
	}
	for name, value := range expectedMetadata {
		if _, exists := metadata[name]; exists {
			return Manifest{}, fmt.Errorf("%w: preserved metadata cannot change", ErrInvalidManifest)
		}
		metadata[name] = bytes.Clone(value)
	}
	return Manifest{
		session: session, profile: profile.Digest(), adapter: adapter, formatSchema: formatSchema,
		artifact: session.ArtifactDigest(), units: units, keys: relations, finalMetadata: metadata,
	}, nil
}

func (m Manifest) CheckInput(session unit.Session) error {
	if m.units == nil {
		return ErrInvalidManifest
	}
	if err := session.Check(); err != nil {
		return err
	}
	if session.ArtifactDigest() != m.artifact || len(session.Units()) != len(m.units) {
		return ErrInputMismatch
	}
	for _, u := range session.Units() {
		original, exists := m.units[u.ID()]
		if !exists || u.SourceDigest() != original.SourceDigest() || u.SourceLanguage() != original.SourceLanguage() || u.TargetLanguage() != original.TargetLanguage() {
			return ErrInputMismatch
		}
	}
	return nil
}
