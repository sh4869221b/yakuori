package localize

import (
	"context"
	"errors"
	"io"
	"slices"
	"time"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

// Import implementations must enforce their limits before additional retention.
// Core can reject a generic Adapter's returned session before protection/planning,
// but cannot bound allocations already performed inside Import.
type Adapter interface {
	Import([]byte, validate.Profile) (unit.Session, error)
	Export(io.Writer, unit.Session, validate.Profile, []validate.AcceptedTranslation) (artifact.Manifest, error)
	Observe(io.Reader) (artifact.FinalObservation, error)
}

type TM interface {
	Commit(context.Context, unit.Session, validate.Profile, []validate.AcceptedTranslation) error
}

var ErrFinalUnvalidated = errors.New("final artifact has not passed validation")

type finalization struct {
	session  unit.Session
	profile  validate.Profile
	accepted []validate.AcceptedTranslation
	expected artifact.ExpectedTranslations
	manifest artifact.Manifest
	valid    bool
}

func prepareFinalization(session unit.Session, profile validate.Profile, accepted []validate.AcceptedTranslation) (*finalization, error) {
	expected, err := artifact.PrepareExport(session, profile, accepted)
	if err != nil {
		return nil, &Error{Phase: "prepare_export", Err: err}
	}
	return &finalization{session: session, profile: profile, accepted: slices.Clone(accepted), expected: expected}, nil
}

func (f *finalization) export(writer io.Writer, adapter Adapter) error {
	f.valid = false
	manifest, err := adapter.Export(writer, f.session, f.profile, f.accepted)
	if err != nil {
		return &Error{Phase: "export", Err: err}
	}
	if err := manifest.CheckInput(f.session); err != nil {
		return &Error{Phase: "export", Err: err}
	}
	f.manifest = manifest
	return nil
}

func (f *finalization) check(reader io.Reader, adapter Adapter) error {
	f.valid = false
	observed, err := adapter.Observe(reader)
	if err != nil {
		return &Error{Phase: "observe", Err: err}
	}
	if err := artifact.CompareFinal(f.manifest, f.expected, observed); err != nil {
		return &Error{Phase: "compare_final", Err: err}
	}
	f.valid = true
	return nil
}

func (f *finalization) commit(ctx context.Context, tm TM, timeout time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if !f.valid {
		return false, &Error{Phase: "commit", Err: ErrFinalUnvalidated}
	}
	if err := ctx.Err(); err != nil {
		return false, &Error{Phase: "commit", Err: err}
	}
	if err := tm.Commit(ctx, f.session, f.profile, f.accepted); err != nil {
		return false, &Error{Phase: "commit", Err: err}
	}
	if err := ctx.Err(); err != nil {
		return true, &Error{Phase: "commit", Err: err}
	}
	return true, nil
}
