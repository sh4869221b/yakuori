package artifact

import (
	"bytes"
	"errors"
	"fmt"
	"maps"

	"github.com/sh4869221b/yakuori/internal/unit"
)

var ErrFinalMismatch = errors.New("final artifact mismatch")

type ObservedUnit struct {
	ID             unit.UnitID
	Text           string
	TargetLanguage string
}

type FinalObservation struct {
	Adapter      string
	FormatSchema string
	Units        []ObservedUnit
	Keys         []KeyRelation
	Metadata     map[string][]byte
}

func CompareFinal(manifest Manifest, expected ExpectedTranslations, final FinalObservation) error {
	if manifest.units == nil {
		return ErrInvalidManifest
	}
	if expected.texts == nil {
		return ErrInvalidExpected
	}
	if !manifest.session.SameImport(expected.session) || manifest.profile != expected.profile {
		return ErrInvalidExpected
	}
	if final.Adapter != manifest.adapter || final.FormatSchema != manifest.formatSchema {
		return fmt.Errorf("%w: schema", ErrFinalMismatch)
	}
	if len(final.Units) != len(manifest.units) {
		return fmt.Errorf("%w: unit count", ErrFinalMismatch)
	}
	seen := make(map[unit.UnitID]bool, len(final.Units))
	for _, observed := range final.Units {
		if seen[observed.ID] {
			return fmt.Errorf("%w: duplicate unit", ErrFinalMismatch)
		}
		seen[observed.ID] = true
		u, exists := manifest.units[observed.ID]
		text, accepted := expected.Text(observed.ID)
		if !exists || !accepted || observed.Text != text || observed.TargetLanguage != u.TargetLanguage() {
			return fmt.Errorf("%w: unit content", ErrFinalMismatch)
		}
	}
	keys := make(map[KeyRelation]int, len(final.Keys))
	for _, key := range final.Keys {
		keys[key]++
	}
	if !maps.Equal(keys, manifest.keys) {
		return fmt.Errorf("%w: key relations", ErrFinalMismatch)
	}
	if len(final.Metadata) != len(manifest.finalMetadata) {
		return fmt.Errorf("%w: metadata fields", ErrFinalMismatch)
	}
	for name, value := range manifest.finalMetadata {
		observed, exists := final.Metadata[name]
		if !exists || !bytes.Equal(value, observed) {
			return fmt.Errorf("%w: metadata %s", ErrFinalMismatch, name)
		}
	}
	return nil
}
