package localize

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

var ErrNilTMStore = errors.New("translation memory store is nil")

type tmExecution struct {
	store *tm.Store
	scope tm.Scope
	run   *tm.Run
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
	m.run, err = m.store.BeginRun(session, profile, m.scope)
	return err
}

func (m *tmExecution) lookup(ctx context.Context, session unit.Session, id unit.UnitID, profile validate.Profile) (validate.AcceptedTranslation, bool, error) {
	text, found, err := m.run.Lookup(ctx, id)
	if err != nil || !found {
		return validate.AcceptedTranslation{}, false, err
	}
	accepted, err := validate.Validate(session, id, profile, text)
	if err != nil && !errors.Is(err, validate.ErrInvalidCandidate) {
		return validate.AcceptedTranslation{}, false, err
	}
	if recordErr := m.run.RecordValidation(id, err == nil); recordErr != nil {
		return validate.AcceptedTranslation{}, false, recordErr
	}
	return accepted, err == nil, nil
}
