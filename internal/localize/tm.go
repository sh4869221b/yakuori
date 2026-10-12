package localize

import (
	"context"
	"errors"

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
