//go:build linux

package localize

import (
	"context"
	"errors"
	"io"

	"github.com/sh4869221b/yakuori/internal/publication"
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

func (c Core) file(ctx context.Context, options publication.Options, adapter Adapter, profile validate.Profile, tm TM, prepare preparePublication) (result Result, err error) {
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
	accepted, err := c.Generate(ctx, session, profile)
	if err != nil {
		return result, err
	}
	final, err := prepareFinalization(session, profile, accepted)
	if err != nil {
		return result, err
	}
	if err := run.Stage(ctx, func(writer io.Writer) error {
		return final.export(writer, adapter)
	}, func(reader io.Reader) error {
		return final.check(reader, adapter)
	}); err != nil {
		return result, &Error{Phase: "stage", Err: err}
	}
	if err := final.commit(ctx, tm); err != nil {
		return result, err
	}
	result.TMCommitted = true
	if err := ctx.Err(); err != nil {
		return result, &Error{Phase: "publish", Err: err}
	}
	result.Publication, err = run.Publish(ctx)
	if err != nil {
		return result, &Error{Phase: "publish", Err: err}
	}
	return result, nil
}
