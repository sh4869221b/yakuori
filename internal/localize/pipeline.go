// Package localize connects generation to shared translation validation.
package localize

import (
	"context"
	"errors"
	"fmt"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/protect"
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

type Core struct{ engine Engine }

func NewCore(engine Engine) Core { return Core{engine: engine} }

// Generate returns no usable subset when any unit fails.
func (c Core) Generate(ctx context.Context, session unit.Session, profile validate.Profile) ([]validate.AcceptedTranslation, error) {
	if err := session.Check(); err != nil {
		return nil, &Error{Phase: "input", Err: err}
	}
	if profile.Digest() == ([32]byte{}) {
		return nil, &Error{Phase: "input", Err: validate.ErrInvalidProfile}
	}
	accepted := make([]validate.AcceptedTranslation, 0, len(session.Units()))
	for _, u := range session.Units() {
		id := u.ID()
		if err := ctx.Err(); err != nil {
			return nil, &Error{Phase: "generate", UnitID: id, Err: err}
		}
		prepared, err := protect.Prepare(session, id)
		if err != nil {
			return nil, &Error{Phase: "protect", UnitID: id, Err: err}
		}
		candidate := prepared.Text()
		if prepared.GenerationRequired() {
			generated, err := c.engine.Generate(ctx, id, candidate)
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
