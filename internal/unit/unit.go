package unit

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"maps"
	"slices"
	"unicode/utf8"
)

var (
	ErrInvalidUnitID         = errors.New("invalid unit ID")
	ErrInvalidProtectionSpan = errors.New("invalid protection span")
)

// ProtectionKind identifies the protected source structure described by a span.
type ProtectionKind string

const (
	Placeholder ProtectionKind = "placeholder"
	OpenTag     ProtectionKind = "open_tag"
	CloseTag    ProtectionKind = "close_tag"
)

// ProtectionSpan marks one protected half-open byte range in the original source.
type ProtectionSpan struct {
	Start   int
	End     int
	Kind    ProtectionKind
	Pair    string
	Ordered bool
}

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
	id              UnitID
	source          []byte
	sourceLanguage  string
	targetLanguage  string
	origin          map[string]string
	sourceDigest    [32]byte
	protectionSpans []ProtectionSpan
}

func NewTranslationUnit(id UnitID, source []byte, sourceLanguage, targetLanguage string, origin map[string]string) (TranslationUnit, error) {
	return NewTranslationUnitWithProtection(id, source, sourceLanguage, targetLanguage, origin, nil)
}

// NewTranslationUnitWithProtection constructs a translation unit with explicit
// protected ranges over the original source bytes.
func NewTranslationUnitWithProtection(id UnitID, source []byte, sourceLanguage, targetLanguage string, origin map[string]string, spans []ProtectionSpan) (TranslationUnit, error) {
	if id == (UnitID{}) {
		return TranslationUnit{}, ErrInvalidUnitID
	}
	if err := validateProtectionSpans(source, spans); err != nil {
		return TranslationUnit{}, err
	}
	return TranslationUnit{
		id:              id,
		source:          bytes.Clone(source),
		sourceLanguage:  sourceLanguage,
		targetLanguage:  targetLanguage,
		origin:          maps.Clone(origin),
		sourceDigest:    sha256.Sum256(source),
		protectionSpans: slices.Clone(spans),
	}, nil
}

func validateProtectionSpans(source []byte, spans []ProtectionSpan) error {
	var previous ProtectionSpan
	for i, span := range spans {
		if span.Start < 0 || span.End <= span.Start || span.End > len(source) {
			return ErrInvalidProtectionSpan
		}
		if !isUTF8Boundary(source, span.Start) || !isUTF8Boundary(source, span.End) {
			return ErrInvalidProtectionSpan
		}
		if i > 0 && span.Start < previous.End {
			return ErrInvalidProtectionSpan
		}
		switch span.Kind {
		case Placeholder:
			if span.Pair != "" {
				return ErrInvalidProtectionSpan
			}
		case OpenTag, CloseTag:
			if span.Pair == "" || !span.Ordered {
				return ErrInvalidProtectionSpan
			}
		default:
			return ErrInvalidProtectionSpan
		}
		previous = span
	}
	return nil
}

func isUTF8Boundary(source []byte, offset int) bool {
	return offset == 0 || offset == len(source) || utf8.RuneStart(source[offset])
}

func (u TranslationUnit) ID() UnitID                { return u.id }
func (u TranslationUnit) Source() []byte            { return bytes.Clone(u.source) }
func (u TranslationUnit) SourceLanguage() string    { return u.sourceLanguage }
func (u TranslationUnit) TargetLanguage() string    { return u.targetLanguage }
func (u TranslationUnit) Origin() map[string]string { return maps.Clone(u.origin) }
func (u TranslationUnit) SourceDigest() [32]byte    { return u.sourceDigest }

// ProtectionSpans returns an independent copy of the declared protected ranges.
func (u TranslationUnit) ProtectionSpans() []ProtectionSpan {
	return slices.Clone(u.protectionSpans)
}
