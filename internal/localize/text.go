package localize

import (
	"bytes"
	"context"
	"io"

	"github.com/sh4869221b/yakuori/internal/validate"
)

func (c Core) Text(ctx context.Context, input []byte, adapter Adapter, profile validate.Profile, tm TM, writer io.Writer) (result Result, err error) {
	session, err := adapter.Import(input, profile)
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
	var buffer bytes.Buffer
	if err := final.export(&buffer, adapter); err != nil {
		return result, err
	}
	finalBytes := bytes.Clone(buffer.Bytes())
	if err := final.check(bytes.NewReader(finalBytes), adapter); err != nil {
		return result, err
	}
	if err := final.commit(ctx, tm); err != nil {
		return result, err
	}
	result.TMCommitted = true
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
