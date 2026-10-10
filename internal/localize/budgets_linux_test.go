//go:build linux

package localize

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func TestCommitDeadlinePreventsPublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFileFixture(t, publication.Replace)
		f.core.limits.DBOperationTimeout = time.Second
		var run *testRun
		calls := 0
		tm := contextTM(func(ctx context.Context, _ unit.Session, _ validate.Profile, _ []validate.AcceptedTranslation) error {
			calls++
			<-ctx.Done()
			return ctx.Err()
		})
		result, err := f.core.file(context.Background(), f.options, f.adapter, f.profile, tm, func(ctx context.Context, options publication.Options) (publicationRun, error) {
			realRun, err := publication.Prepare(ctx, options)
			if err != nil {
				return nil, err
			}
			run = &testRun{publicationRun: realRun}
			return run, nil
		})
		if errors.Is(err, publication.ErrUnsupportedFilesystem) {
			t.Skip("requires qualified ext4/Btrfs TMPDIR")
		}
		if !errors.Is(err, context.DeadlineExceeded) || calls != 1 || result.TMCommitted || result.Publication.State != publication.NotPublished || run.published != 0 || run.closed != 1 {
			t.Fatalf("result=%+v err=%v commit=%d run=%+v", result, err, calls, run)
		}
		if !bytes.Equal(fileBytes(t, f.options.Source), f.input) || string(fileBytes(t, f.options.Output)) != "existing" {
			t.Fatal("source or output changed after commit timeout")
		}
	})
}
