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
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

var ErrNilTMStore = errors.New("translation memory store is nil")

// ContextPackOptions opts into confirmed context from a document read by
// contextpack.Read. Categories remain explicit and a nil Pack disables context.
// The document must remain unchanged while the run is active.
type ContextPackOptions struct {
	Pack        *contextpack.Document
	Categories  contextresolve.Categories
	Budget      contextresolve.Budget
	CountTokens contextresolve.CountTokens
}

type tmExecution struct {
	store        *tm.Store
	scope        tm.Scope
	run          *tm.Run
	observation  *AcceptanceObservation
	contextPack  ContextPackOptions
	unitContexts map[unit.UnitID]identity.UnitContext
}

func (m *tmExecution) resolveContext(ctx context.Context, session unit.Session, units []unit.TranslationUnit) (map[unit.UnitID]contextresolve.ResolvedContext, error) {
	if m.scope.GameNamespace == "" || m.scope.ArtifactNamespace == "" {
		return nil, &Error{Phase: "tm_begin", Err: tm.ErrInvalidScope}
	}
	resolved := make(map[unit.UnitID]contextresolve.ResolvedContext)
	request := contextresolve.Request{
		Pack: m.contextPack.Pack, Session: session, GameNamespace: m.scope.GameNamespace, ArtifactNamespace: m.scope.ArtifactNamespace,
		Categories: m.contextPack.Categories, Budget: m.contextPack.Budget,
	}
	for _, u := range units {
		request.UnitID = u.ID()
		if err := ctx.Err(); err != nil {
			return nil, &Error{Phase: "context", UnitID: u.ID(), Err: err}
		}
		selection, _, err := contextresolve.Resolve(ctx, request, m.contextPack.CountTokens)
		if err != nil {
			return nil, &Error{Phase: "context", UnitID: u.ID(), Err: err}
		}
		if !selection.Empty() {
			resolved[u.ID()] = selection
		}
	}
	return resolved, nil
}

// Segments of the same parent share the same checked selection and prompt.
func (m *tmExecution) recordContextRequest(id unit.UnitID, request inference.GenerationRequest) {
	if m == nil || request.ResolvedContext().Empty() {
		return
	}
	if m.unitContexts == nil {
		m.unitContexts = make(map[unit.UnitID]identity.UnitContext)
	}
	if _, exists := m.unitContexts[id]; !exists {
		requestID := request.Identity()
		m.unitContexts[id] = identity.UnitContext{
			Prompt:    identityPrompt(requestID.Prompt, requestID.DisabledThinkingPrefix),
			Selection: request.ResolvedContext().Canonical(),
		}
	}
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

func (m *tmExecution) lookup(ctx context.Context, session unit.Session, id unit.UnitID, profile validate.Profile) (validate.AcceptedTranslation, bool, error) {
	text, found, err := m.run.Lookup(ctx, id)
	if err != nil || !found {
		return validate.AcceptedTranslation{}, false, err
	}
	accepted, err := validate.Validate(session, id, profile, text)
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
