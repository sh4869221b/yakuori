package genericjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

type jsonDocument struct {
	Schema         string     `json:"schema"`
	Version        int        `json:"version"`
	SourceLanguage string     `json:"source_language"`
	TargetLanguage string     `json:"target_language"`
	Scope          jsonScope  `json:"scope"`
	Units          []jsonUnit `json:"units"`
}

type jsonScope struct {
	Game     string `json:"game_namespace"`
	Artifact string `json:"artifact_namespace"`
}

type jsonUnit struct {
	ID          string            `json:"id"`
	Source      string            `json:"source"`
	Translation *string           `json:"translation,omitempty"`
	Protection  []jsonSpan        `json:"protection,omitzero"`
	Context     map[string]string `json:"context,omitzero"`
}

type jsonSpan struct {
	Start   int                 `json:"start"`
	End     int                 `json:"end"`
	Kind    unit.ProtectionKind `json:"kind"`
	Pair    string              `json:"pair,omitempty"`
	Ordered bool                `json:"ordered"`
}

// sourceDocument represents all preserved fields in unit order, without translations.
func (doc document) sourceDocument() jsonDocument {
	source := jsonDocument{
		Schema: schemaName, Version: version,
		SourceLanguage: doc.metadata.SourceLanguage, TargetLanguage: doc.metadata.TargetLanguage,
		Scope: jsonScope{Game: doc.metadata.GameNamespace, Artifact: doc.metadata.ArtifactNamespace},
		Units: make([]jsonUnit, len(doc.units)),
	}
	for i, entry := range doc.units {
		u := jsonUnit{ID: entry.id, Source: entry.source, Context: entry.context}
		if entry.spans != nil {
			u.Protection = make([]jsonSpan, len(entry.spans))
			for j, span := range entry.spans {
				u.Protection[j] = jsonSpan(span)
			}
		}
		source.Units[i] = u
	}
	return source
}

func (doc jsonDocument) preservedMetadata() (map[string][]byte, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{"source-document": raw}, nil
}

func (a *Adapter) Export(writer io.Writer, session unit.Session, profile validate.Profile, accepted []validate.AcceptedTranslation) (artifact.Manifest, error) {
	if !a.session.SameImport(session) {
		return artifact.Manifest{}, unit.ErrInvalidSession
	}
	expected, err := artifact.PrepareExport(session, profile, accepted)
	if err != nil {
		return artifact.Manifest{}, err
	}
	doc := a.source.sourceDocument()
	preserved, err := doc.preservedMetadata()
	if err != nil {
		return artifact.Manifest{}, err
	}
	for i, u := range session.Units() {
		text, exists := expected.Text(u.ID())
		if !exists {
			return artifact.Manifest{}, artifact.ErrIncompleteExport
		}
		if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			return artifact.Manifest{}, errJSON
		}
		if err := a.limits.CheckUnitTextBytes(len(text)); err != nil {
			return artifact.Manifest{}, err
		}
		doc.Units[i].Translation = &text
	}

	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(doc); err != nil {
		return artifact.Manifest{}, err
	}
	raw := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	if err := a.limits.CheckArtifactBytes(len(raw)); err != nil {
		return artifact.Manifest{}, err
	}
	manifest, err := artifact.NewManifest(session, profile, adapterName, formatSchema, nil, preserved, nil)
	if err != nil {
		return artifact.Manifest{}, err
	}
	n, err := writer.Write(raw)
	if err != nil {
		return artifact.Manifest{}, err
	}
	if n != len(raw) {
		return artifact.Manifest{}, io.ErrShortWrite
	}
	return manifest, nil
}

func (a *Adapter) Observe(reader io.Reader) (artifact.FinalObservation, error) {
	maxRead := int64(a.limits.ArtifactBytes)
	if maxRead < math.MaxInt64 {
		maxRead++ // Read one extra byte to detect an oversized document.
	}
	raw, err := io.ReadAll(io.LimitReader(reader, maxRead))
	if err != nil {
		return artifact.FinalObservation{}, err
	}
	doc, err := parse(raw, a.limits)
	if err != nil {
		return artifact.FinalObservation{}, err
	}
	units, err := doc.translationUnits()
	if err != nil {
		return artifact.FinalObservation{}, err
	}
	observed := artifact.FinalObservation{
		Adapter: adapterName, FormatSchema: formatSchema,
		Units: make([]artifact.ObservedUnit, len(units)),
	}
	for i, u := range units {
		text := doc.units[i].translation
		if text == nil {
			return artifact.FinalObservation{}, fmt.Errorf("generic JSON unit %d translation: %w", i+1, errRequired)
		}
		observed.Units[i] = artifact.ObservedUnit{ID: u.ID(), Text: *text, TargetLanguage: u.TargetLanguage()}
	}
	observed.Metadata, err = doc.sourceDocument().preservedMetadata()
	if err != nil {
		return artifact.FinalObservation{}, err
	}
	return observed, nil
}
