// Package genericjson reads and writes v1 JSON exchange documents.
package genericjson

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

const (
	adapterName  = "generic-json"
	formatSchema = "generic-json-v1"
)

// Metadata is the caller's expected language pair and artifact scope.
type Metadata struct {
	SourceLanguage    string
	TargetLanguage    string
	GameNamespace     string
	ArtifactNamespace string
}

var errMetadataMismatch = errors.New("generic JSON language or scope mismatch")

type Adapter struct {
	expected Metadata
	limits   config.Limits
	session  unit.Session
	source   document
}

// New requires explicit language and scope values and finite operational limits.
func New(expected Metadata, limits config.Limits) (*Adapter, error) {
	if err := limits.Check(); err != nil {
		return nil, err
	}
	for _, value := range []string{expected.SourceLanguage, expected.TargetLanguage, expected.GameNamespace, expected.ArtifactNamespace} {
		if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return nil, errors.New("invalid expected generic JSON metadata")
		}
		if err := limits.CheckUnitTextBytes(len(value)); err != nil {
			return nil, err
		}
	}
	return &Adapter{expected: expected, limits: limits}, nil
}

// Import retains the source document and binds context references to each unit's
// Origin under "speaker" and "scene".
func (a *Adapter) Import(raw []byte, _ validate.Profile) (unit.Session, error) {
	doc, err := parse(raw, a.limits)
	if err != nil {
		return unit.Session{}, err
	}
	if doc.metadata != a.expected {
		return unit.Session{}, errMetadataMismatch
	}
	units, err := doc.translationUnits()
	if err != nil {
		return unit.Session{}, err
	}
	session, err := unit.NewSession(raw, units)
	if err != nil {
		return unit.Session{}, err
	}
	a.session, a.source = session, doc
	return session, nil
}

func (doc document) translationUnits() ([]unit.TranslationUnit, error) {
	units := make([]unit.TranslationUnit, len(doc.units))
	for i, entry := range doc.units {
		id, err := unit.NewUnitID(adapterName, formatSchema, entry.id)
		if err != nil {
			return nil, err
		}
		u, err := unit.NewTranslationUnitWithProtection(id, []byte(entry.source),
			doc.metadata.SourceLanguage, doc.metadata.TargetLanguage, entry.context, entry.spans)
		if err == nil && len(entry.spans) > 0 {
			err = protect.CheckDeclaredSpans(u)
		}
		if err != nil {
			return nil, fmt.Errorf("generic JSON unit %d: %w", i+1, err)
		}
		units[i] = u
	}
	return units, nil
}
