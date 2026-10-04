package text

import (
	"io"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

type Adapter struct {
	sourceLanguage string
	targetLanguage string
	session        unit.Session
	id             unit.UnitID
}

func New(sourceLanguage, targetLanguage string) *Adapter {
	return &Adapter{sourceLanguage: sourceLanguage, targetLanguage: targetLanguage}
}

func (a *Adapter) Import(raw []byte, _ validate.Profile) (unit.Session, error) {
	id, err := unit.NewUnitID("text", "text-v1", "document")
	if err != nil {
		return unit.Session{}, err
	}
	u, err := unit.NewTranslationUnit(id, raw, a.sourceLanguage, a.targetLanguage, nil)
	if err != nil {
		return unit.Session{}, err
	}
	session, err := unit.NewSession(raw, []unit.TranslationUnit{u})
	if err != nil {
		return unit.Session{}, err
	}
	a.session, a.id = session, id
	return session, nil
}

func (a *Adapter) Export(writer io.Writer, session unit.Session, profile validate.Profile, accepted []validate.AcceptedTranslation) (artifact.Manifest, error) {
	if !a.session.SameImport(session) {
		return artifact.Manifest{}, unit.ErrInvalidSession
	}
	expected, err := artifact.PrepareExport(session, profile, accepted)
	if err != nil {
		return artifact.Manifest{}, err
	}
	text, exists := expected.Text(a.id)
	if !exists {
		return artifact.Manifest{}, artifact.ErrIncompleteExport
	}
	manifest, err := artifact.NewManifest(session, profile, "text", "text-v1", nil, nil, nil)
	if err != nil {
		return artifact.Manifest{}, err
	}
	n, err := io.WriteString(writer, text)
	if err != nil {
		return artifact.Manifest{}, err
	}
	if n != len(text) {
		return artifact.Manifest{}, io.ErrShortWrite
	}
	return manifest, nil
}

func (a *Adapter) Observe(reader io.Reader) (artifact.FinalObservation, error) {
	if err := a.session.Check(); err != nil {
		return artifact.FinalObservation{}, err
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return artifact.FinalObservation{}, err
	}
	return artifact.FinalObservation{Adapter: "text", FormatSchema: "text-v1", Units: []artifact.ObservedUnit{
		{ID: a.id, Text: string(raw), TargetLanguage: a.targetLanguage},
	}}, nil
}
