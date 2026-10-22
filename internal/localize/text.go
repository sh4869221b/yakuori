package localize

import (
	"bytes"
	"context"
	"io"

	"github.com/sh4869221b/yakuori/internal/tm"

	"github.com/sh4869221b/yakuori/internal/validate"
)

func (c Core) Text(ctx context.Context, input []byte, adapter Adapter, profile validate.Profile, tm TM, writer io.Writer) (result Result, err error) {
	return c.text(ctx, input, adapter, profile, tm, nil, writer)
}

func (c Core) TextWithTM(ctx context.Context, input []byte, adapter Adapter, profile validate.Profile, store *tm.Store, scope tm.Scope, writer io.Writer) (Result, error) {
	if store == nil {
		return Result{}, &Error{Phase: "tm_begin", Err: ErrNilTMStore}
	}
	return c.text(ctx, input, adapter, profile, nil, &tmExecution{store: store, scope: scope}, writer)
}

func (c Core) text(ctx context.Context, input []byte, adapter Adapter, profile validate.Profile, tm TM, memory *tmExecution, writer io.Writer) (result Result, err error) {
	// The caller already owns input; reject before Import can retain additional copies.
	if err := c.limits.CheckArtifactBytes(len(input)); err != nil {
		return result, &Error{Phase: "input", Err: err}
	}

	session, err := adapter.Import(input, profile)
	if err != nil {
		return result, &Error{Phase: "import", Err: err}
	}
	if memory != nil {
		if err := memory.bindKeys(session, adapter); err != nil {
			return result, &Error{Phase: "tm_begin", Err: err}
		}
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
	var buffer bytes.Buffer
	if err := final.export(&buffer, adapter); err != nil {
		return result, err
	}
	finalBytes := bytes.Clone(buffer.Bytes())
	if err := final.check(bytes.NewReader(finalBytes), adapter); err != nil {
		return result, err
	}
	result.TMCommitted, err = final.commit(ctx, tm, c.limits.DBOperationTimeout)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, &Error{Phase: "write", Err: err}
	}
	result.BytesWritten, err = writer.Write(finalBytes)
	if err != nil {
		return result, &Error{Phase: "write", Err: err}
	}
	if result.BytesWritten != len(finalBytes) {
		return result, &Error{Phase: "write", Err: io.ErrShortWrite}
	}
	return result, nil
}
