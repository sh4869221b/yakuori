package unit

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"maps"
)

var ErrInvalidUnitID = errors.New("invalid unit ID")

// UnitID identifies a stable record within an adapter's format schema.
// Key relations belong to the artifact manifest, separately from this ID.
type UnitID struct {
	adapter      string
	formatSchema string
	stableID     string
}

func NewUnitID(adapter, formatSchema, stableID string) (UnitID, error) {
	if adapter == "" || formatSchema == "" || stableID == "" {
		return UnitID{}, ErrInvalidUnitID
	}
	return UnitID{adapter: adapter, formatSchema: formatSchema, stableID: stableID}, nil
}

func (id UnitID) Adapter() string      { return id.adapter }
func (id UnitID) FormatSchema() string { return id.formatSchema }
func (id UnitID) StableID() string     { return id.stableID }

type TranslationUnit struct {
	id             UnitID
	source         []byte
	sourceLanguage string
	targetLanguage string
	origin         map[string]string
	sourceDigest   [32]byte
}

func NewTranslationUnit(id UnitID, source []byte, sourceLanguage, targetLanguage string, origin map[string]string) (TranslationUnit, error) {
	if id == (UnitID{}) {
		return TranslationUnit{}, ErrInvalidUnitID
	}
	return TranslationUnit{
		id:             id,
		source:         bytes.Clone(source),
		sourceLanguage: sourceLanguage,
		targetLanguage: targetLanguage,
		origin:         maps.Clone(origin),
		sourceDigest:   sha256.Sum256(source),
	}, nil
}

func (u TranslationUnit) ID() UnitID                { return u.id }
func (u TranslationUnit) Source() []byte            { return bytes.Clone(u.source) }
func (u TranslationUnit) SourceLanguage() string    { return u.sourceLanguage }
func (u TranslationUnit) TargetLanguage() string    { return u.targetLanguage }
func (u TranslationUnit) Origin() map[string]string { return maps.Clone(u.origin) }
func (u TranslationUnit) SourceDigest() [32]byte    { return u.sourceDigest }
