package localize

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/contextpack"
	"github.com/sh4869221b/yakuori/internal/contextresolve"
	"github.com/sh4869221b/yakuori/internal/dictionary"
	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

var ErrNilTMStore = errors.New("translation memory store is nil")

// ContextPackOptions opts into a confirmed context pack and an approved
// dictionary. Both inputs must remain unchanged while the run is active.
type ContextPackOptions struct {
	Pack                   *contextpack.Document
	Categories             contextresolve.Categories
	Budget                 contextresolve.Budget
	CountTokens            contextresolve.CountTokens
	Dictionary             *dictionary.Catalog
	OnDictionaryDiagnostic func(unit.UnitID, dictionary.Diagnostic)
}

type tmExecution struct {
	store        *tm.Store
	scope        tm.Scope
	run          *tm.Run
	observation  *AcceptanceObservation
	contextPack  ContextPackOptions
	unitContexts map[unit.UnitID]identity.UnitContext
}

func (m *tmExecution) resolveInputs(ctx context.Context, session unit.Session, units []unit.TranslationUnit) (map[unit.UnitID]contextresolve.ResolvedContext, map[unit.UnitID]dictionary.Selection, error) {
	if m.scope.GameNamespace == "" || m.scope.ArtifactNamespace == "" {
		return nil, nil, &Error{Phase: "tm_begin", Err: tm.ErrInvalidScope}
	}
	resolved := make(map[unit.UnitID]contextresolve.ResolvedContext)
	dictionaries := make(map[unit.UnitID]dictionary.Selection)
	request := contextresolve.Request{
		Pack: m.contextPack.Pack, Session: session, GameNamespace: m.scope.GameNamespace, ArtifactNamespace: m.scope.ArtifactNamespace,
		Categories: m.contextPack.Categories, Budget: m.contextPack.Budget,
	}
	for _, u := range units {
		id := u.ID()
		var scene, speaker string
		if m.contextPack.Pack != nil {
			if err := ctx.Err(); err != nil {
				return nil, nil, &Error{Phase: "context", UnitID: id, Err: err}
			}
			request.UnitID = id
			selection, diagnostics, err := contextresolve.Resolve(ctx, request, m.contextPack.CountTokens)
			if err != nil {
				return nil, nil, &Error{Phase: "context", UnitID: id, Err: err}
			}
			if !selection.Empty() {
				resolved[id] = selection
			}
			if m.contextPack.Dictionary != nil {
				for i, item := range selection.Items() {
					switch item.Category {
					case contextresolve.Scene:
						scene = diagnostics.Matched[i].ID
					case contextresolve.Speaker:
						speaker = diagnostics.Matched[i].ID
					}
				}
			}
		}
		if m.contextPack.Dictionary != nil {
			if err := ctx.Err(); err != nil {
				return nil, nil, &Error{Phase: "dictionary", UnitID: id, Err: err}
			}
			selected, err := m.contextPack.Dictionary.Select(dictionary.Query{
				Game: m.scope.GameNamespace, Artifact: m.scope.ArtifactNamespace,
				SourceLanguage: u.SourceLanguage(), TargetLanguage: u.TargetLanguage(),
				Adapter: id.Adapter(), FormatSchema: id.FormatSchema(), StableID: id.StableID(),
				Source: string(u.Source()), Scene: scene, Speaker: speaker,
			})
			if err != nil {
				return nil, nil, &Error{Phase: "dictionary", UnitID: id, Err: err}
			}
			if report := m.contextPack.OnDictionaryDiagnostic; report != nil {
				for _, diagnostic := range selected.Diagnostics {
					report(id, diagnostic)
				}
			}
			if selected.Target != "" || len(selected.Terms) != 0 {
				dictionaries[id] = dictionary.Selection{Target: selected.Target, Terms: selected.Terms}
			}
		}
	}
	return resolved, dictionaries, nil
}

// Segments of the same parent share the same checked selection and prompt.
func (m *tmExecution) recordContextRequest(id unit.UnitID, request inference.GenerationRequest) {
	if m == nil || request.ResolvedContext().Empty() {
		return
	}
	if m.unitContexts == nil {
		m.unitContexts = make(map[unit.UnitID]identity.UnitContext)
	}
	conditions := m.unitContexts[id]
	if len(conditions.Selection) == 0 {
		requestID := request.Identity()
		conditions.Prompt = identityPrompt(requestID.Prompt, requestID.DisabledThinkingPrefix)
		conditions.Selection = request.ResolvedContext().Canonical()
		m.unitContexts[id] = conditions
	}
}

func (m *tmExecution) recordDictionary(id unit.UnitID, target string, terms glossary.Glossary) {
	if target == "" && terms.Empty() {
		return
	}
	if m.unitContexts == nil {
		m.unitContexts = make(map[unit.UnitID]identity.UnitContext)
	}
	conditions := m.unitContexts[id]
	conditions.Dictionary.Translation = target
	if !terms.Empty() {
		conditions.Dictionary.Terms = terms.CanonicalBytes()
	}
	m.unitContexts[id] = conditions
}

// keyRelationProvider is implemented by adapters that can report the keys of
// the import they just returned. Core never infers relations from source bytes.
type keyRelationProvider interface {
	KeyRelations(unit.Session) ([]artifact.KeyRelation, error)
}

// bindKeys freezes the current import's key relations into the run scope before
// planning. A nil caller Keys is automatic; a nonnil Keys, including empty, is
// an explicit multiset assertion against the provider's authoritative result.
func (m *tmExecution) bindKeys(session unit.Session, adapter Adapter) error {
	provider, ok := adapter.(keyRelationProvider)
	if !ok {
		return nil
	}
	relations, err := provider.KeyRelations(session)
	if err != nil {
		return err
	}
	relations = slices.Clone(relations)
	if m.scope.Keys == nil {
		m.scope.Keys = relations
		return nil
	}
	expected := make(map[artifact.KeyRelation]int, len(m.scope.Keys))
	for _, relation := range m.scope.Keys {
		expected[relation]++
	}
	actual := make(map[artifact.KeyRelation]int, len(relations))
	for _, relation := range relations {
		actual[relation]++
	}
	if !maps.Equal(expected, actual) {
		return fmt.Errorf("%w: explicit key relations", tm.ErrInvalidScope)
	}
	m.scope.Keys = relations
	return nil
}

func (m *tmExecution) begin(session unit.Session, profile validate.Profile) error {
	var err error
	m.run, err = m.store.BeginRunWithContext(session, profile, m.scope, m.unitContexts)
	return err
}

func (m *tmExecution) lookup(ctx context.Context, session unit.Session, id unit.UnitID, validation validate.UnitValidation, dictionaryTarget string) (validate.AcceptedTranslation, bool, error) {
	text, found, err := m.run.Lookup(ctx, id)
	if err != nil || !found {
		return validate.AcceptedTranslation{}, false, err
	}
	accepted, err := validation.Validate(session, id, text)
	if err == nil && dictionaryTarget != "" && text != dictionaryTarget {
		err = validate.ErrInvalidCandidate
	}
	if m.observation != nil {
		m.observation.TMValidation.record(err)
	}
	if err != nil && !errors.Is(err, validate.ErrInvalidCandidate) {
		return validate.AcceptedTranslation{}, false, err
	}
	if recordErr := m.run.RecordValidation(id, err == nil); recordErr != nil {
		return validate.AcceptedTranslation{}, false, recordErr
	}
	return accepted, err == nil, nil
}
