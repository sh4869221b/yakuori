//go:build linux

package localize

import (
	"context"
	"errors"
	"io"

	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/validate"
)

type Result struct {
	TMCommitted  bool
	BytesWritten int
	Publication  publication.Result
}

type publicationRun interface {
	SourceSnapshot() []byte
	Stage(context.Context, func(io.Writer) error, func(io.Reader) error) error
	Publish(context.Context) (publication.Result, error)
	Close() error
}

type preparePublication func(context.Context, publication.Options) (publicationRun, error)

func (c Core) File(ctx context.Context, options publication.Options, adapter Adapter, profile validate.Profile, tm TM) (Result, error) {
	return c.file(ctx, options, adapter, profile, tm, func(ctx context.Context, options publication.Options) (publicationRun, error) {
		return publication.PrepareWithLimits(ctx, options, c.limits)
	})
}

func (c Core) FileWithTM(ctx context.Context, options publication.Options, adapter Adapter, profile validate.Profile, store *tm.Store, scope tm.Scope) (Result, error) {
	if store == nil {
		return Result{}, &Error{Phase: "tm_begin", Err: ErrNilTMStore}
	}
	return c.fileWithTM(ctx, options, adapter, profile, nil, &tmExecution{store: store, scope: scope}, func(ctx context.Context, options publication.Options) (publicationRun, error) {
		return publication.PrepareWithLimits(ctx, options, c.limits)
	})
}

func (c Core) file(ctx context.Context, options publication.Options, adapter Adapter, profile validate.Profile, tm TM, prepare preparePublication) (result Result, err error) {
	return c.fileWithTM(ctx, options, adapter, profile, tm, nil, prepare)
}

func (c Core) fileWithTM(ctx context.Context, options publication.Options, adapter Adapter, profile validate.Profile, tm TM, memory *tmExecution, prepare preparePublication) (result Result, err error) {
	result.Publication.State = publication.NotPublished
	run, err := prepare(ctx, options)
	if err != nil {
		return result, &Error{Phase: "prepare", Err: err}
	}
	defer func() {
		if closeErr := run.Close(); closeErr != nil {
			err = errors.Join(err, &Error{Phase: "close", Err: closeErr})
		}
	}()
	session, err := adapter.Import(run.SourceSnapshot(), profile)
	if err != nil {
		return result, &Error{Phase: "import", Err: err}
	}
	accepted, err := c.generate(ctx, session, profile, memory)
	if err != nil {
		return result, err
	}
	final, err := prepareFinalization(session, profile, accepted)
	if err != nil {
		return result, err
	}
	if memory != nil {
		if err := memory.run.Stage(accepted); err != nil {
			return result, &Error{Phase: "tm_stage", Err: err}
		}
		final.run, tm = memory.run, memory.run
	}
	if err := run.Stage(ctx, func(writer io.Writer) error {
		return final.export(writer, adapter)
	}, func(reader io.Reader) error {
		return final.check(reader, adapter)
	}); err != nil {
		return result, &Error{Phase: "stage", Err: err}
	}
	result.TMCommitted, err = final.commit(ctx, tm, c.limits.DBOperationTimeout)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, &Error{Phase: "publish", Err: err}
	}
	result.Publication, err = run.Publish(ctx)
	if err != nil {
		return result, &Error{Phase: "publish", Err: err}
	}
	return result, nil
}
