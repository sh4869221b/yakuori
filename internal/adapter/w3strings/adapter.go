package w3strings

import (
	"errors"
	"slices"
	"strconv"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

const (
	adapterName  = "witcher3-w3strings"
	formatSchema = "w3strings-v162-en-jp-v1"
)

type Adapter struct {
	limits         config.Limits
	expectedSource string
	session        unit.Session
	source         *parsedFile
	keys           []artifact.KeyRelation
}

func New() *Adapter {
	return &Adapter{limits: config.DefaultLimits()}
}

// NewWithLimits applies explicit finite import limits.
func NewWithLimits(limits config.Limits) (*Adapter, error) {
	if err := limits.Check(); err != nil {
		return nil, err
	}
	return &Adapter{limits: limits}, nil
}

// NewForSource requires the imported header to declare canonical en or ja.
// Binary jp still satisfies ja; other declarations are rejected here.
func NewForSource(sourceLanguage string, limits config.Limits) (*Adapter, error) {
	a, err := NewWithLimits(limits)
	if err != nil {
		return nil, err
	}
	if sourceLanguage != "en" && sourceLanguage != "ja" {
		return nil, errors.New("unsupported w3strings source language")
	}
	a.expectedSource = sourceLanguage
	return a, nil
}

func (a *Adapter) Import(raw []byte, _ validate.Profile) (unit.Session, error) {
	source, err := parse(raw, a.limits.ArtifactBytes, &a.limits)
	if err != nil {
		return unit.Session{}, err
	}
	sourceLanguage := source.language
	if sourceLanguage == "jp" {
		sourceLanguage = "ja"
	}
	if a.expectedSource != "" && sourceLanguage != a.expectedSource {
		return unit.Session{}, errors.New("w3strings source language does not match header")
	}
	units := make([]unit.TranslationUnit, len(source.entries))
	ids := make(map[uint32]unit.UnitID, len(source.entries))
	for i, entry := range source.entries {
		id, err := unit.NewUnitID(adapterName, formatSchema, strconv.FormatUint(uint64(entry.id), 10))
		if err != nil {
			return unit.Session{}, err
		}
		u, err := unit.NewTranslationUnit(id, []byte(entry.text), sourceLanguage, "ja", nil)
		if err != nil {
			return unit.Session{}, err
		}
		units[i], ids[entry.id] = u, id
	}
	keys := make([]artifact.KeyRelation, len(source.keys))
	for i, key := range source.keys {
		var rawKey [4]byte
		le.PutUint32(rawKey[:], key.hash)
		keys[i] = artifact.KeyRelation{UnitID: ids[key.id], Key: string(rawKey[:])}
	}
	session, err := unit.NewSession(source.raw, units)
	if err != nil {
		return unit.Session{}, err
	}
	// Publish the import state only after every unit and relation is complete.
	a.session, a.source, a.keys = session, source, keys
	return session, nil
}

// KeyRelations returns opaque four-byte LE hashes in the original table order.
func (a *Adapter) KeyRelations(session unit.Session) ([]artifact.KeyRelation, error) {
	if !a.session.SameImport(session) {
		return nil, unit.ErrInvalidSession
	}
	return slices.Clone(a.keys), nil
}
