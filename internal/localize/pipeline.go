// Package localize connects generation to shared translation validation.
package localize

import (
	"context"
	"errors"
	"fmt"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/segment"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

type Finish string

const (
	Stop          Finish = "stop"
	MaxTokens     Finish = "max_tokens"
	ContextLimit  Finish = "context_limit"
	Timeout       Finish = "timeout"
	Canceled      Finish = "canceled"
	DecodeError   Finish = "decode_error"
	InvalidOutput Finish = "invalid_output"
)

var ErrIncompleteGeneration = errors.New("generation did not finish with stop")

type Generation struct {
	Text   string
	Finish Finish
}

type Engine interface {
	Generate(context.Context, unit.UnitID, string) (Generation, error)
}

// Error identifies the failed phase without including source or backend text.
type Error struct {
	Phase  string
	UnitID unit.UnitID
	Err    error
}

func (e *Error) Error() string {
	if e.UnitID == (unit.UnitID{}) {
		return fmt.Sprintf("localize %s failed", e.Phase)
	}
	return fmt.Sprintf("localize %s failed for unit %s", e.Phase, e.UnitID.StableID())
}

func (e *Error) Unwrap() error { return e.Err }

type Core struct {
	limits    config.Limits
	engine    Engine
	segmented *segmentedGenerator
}

func NewCore(engine Engine) Core { return Core{engine: engine, limits: config.DefaultLimits()} }

func NewCoreWithLimits(engine Engine, limits config.Limits) (Core, error) {
	if err := limits.Check(); err != nil {
		return Core{}, err
	}
	return Core{engine: engine, limits: limits}, nil
}

// Generate returns no usable subset when any unit fails.
func (c Core) Generate(ctx context.Context, session unit.Session, profile validate.Profile) ([]validate.AcceptedTranslation, error) {
	if err := session.Check(); err != nil {
		return nil, &Error{Phase: "input", Err: err}
	}
	if profile.Digest() == ([32]byte{}) {
		return nil, &Error{Phase: "input", Err: validate.ErrInvalidProfile}
	}
	expectedSegmentation := validate.SegmentationInput{}
	if c.segmented != nil {
		expectedSegmentation = segment.ProfileInput()
	}
	if profile.Segmentation() != expectedSegmentation {
		return nil, &Error{Phase: "input", Err: validate.ErrInvalidProfile}
	}
	units, err := c.inputUnits(session)
	if err != nil {
		return nil, &Error{Phase: "input", Err: err}
	}
	// Finish every unit's protection and plan before any generation starts.
	preparedUnits := make([]protect.Prepared, 0, len(units))
	plans := make([]segment.Plan, 0, len(units))
	segments := 0
	for _, u := range units {
		id := u.ID()
		if err := ctx.Err(); err != nil {
			return nil, &Error{Phase: "generate", UnitID: id, Err: err}
		}
		prepared, err := protect.Prepare(session, id)
		if err != nil {
			return nil, &Error{Phase: "protect", UnitID: id, Err: err}
		}
		var plan segment.Plan
		if c.segmented != nil {
			limits := c.limits
			limits.Segments -= segments
			if limits.Segments <= 0 {
				return nil, &Error{Phase: "generate", UnitID: id, Err: c.limits.CheckSegments(segments + 1)}
			}
			plan, err = c.segmented.plan(ctx, id, prepared, limits)
			if err != nil {
				return nil, &Error{Phase: "generate", UnitID: id, Err: err}
			}
			segments += len(plan.Segments())
		} else {
			segments++
			if err := c.limits.CheckSegments(segments); err != nil {
				return nil, &Error{Phase: "generate", UnitID: id, Err: err}
			}
		}
		preparedUnits = append(preparedUnits, prepared)
		plans = append(plans, plan)
	}
	accepted := make([]validate.AcceptedTranslation, 0, len(units))
	for i, u := range units {
		id, prepared := u.ID(), preparedUnits[i]
		if err := ctx.Err(); err != nil {
			return nil, &Error{Phase: "generate", UnitID: id, Err: err}
		}
		candidate := prepared.Text()
		if prepared.GenerationRequired() {
			var generated Generation
			var err error
			if c.segmented != nil {
				generated, err = c.segmented.generate(ctx, plans[i])
			} else {
				generated, err = c.engine.Generate(ctx, id, candidate)
			}
			if err != nil {
				return nil, &Error{Phase: "generate", UnitID: id, Err: err}
			}
			if err := ctx.Err(); err != nil {
				return nil, &Error{Phase: "generate", UnitID: id, Err: err}
			}
			if generated.Finish != Stop {
				return nil, &Error{Phase: "generate", UnitID: id, Err: ErrIncompleteGeneration}
			}
			candidate = generated.Text
		}
		restored, err := prepared.Restore(session, id, candidate)
		if err != nil {
			return nil, &Error{Phase: "restore", UnitID: id, Err: err}
		}
		translation, err := validate.Validate(session, id, profile, restored)
		if err != nil {
			return nil, &Error{Phase: "validate", UnitID: id, Err: err}
		}
		accepted = append(accepted, translation)
	}
	if err := ctx.Err(); err != nil {
		return nil, &Error{Phase: "prepare_export", Err: err}
	}
	if _, err := artifact.PrepareExport(session, profile, accepted); err != nil {
		return nil, &Error{Phase: "prepare_export", Err: err}
	}
	return accepted, nil
}

func (c Core) inputUnits(session unit.Session) ([]unit.TranslationUnit, error) {
	if err := c.limits.CheckArtifactBytes(session.ArtifactBytes()); err != nil {
		return nil, err
	}
	if err := c.limits.CheckUnits(session.UnitCount()); err != nil {
		return nil, err
	}
	units := session.Units()
	total := 0
	for _, u := range units {
		n := u.SourceBytes()
		if err := c.limits.CheckUnitTextBytes(n); err != nil {
			return nil, err
		}
		// Subtract first so the cumulative count cannot wrap before rejection.
		if n > c.limits.TotalTextBytes-total {
			return nil, &config.LimitError{Limit: "total text bytes", Actual: total + n, Maximum: c.limits.TotalTextBytes}
		}
		total += n
	}
	return units, nil
}
