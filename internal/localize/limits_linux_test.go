//go:build linux

package localize

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/unit"
)

type limitRun struct{ staged, published int }

func (*limitRun) SourceSnapshot() []byte { return []byte("a") }
func (r *limitRun) Stage(context.Context, func(io.Writer) error, func(io.Reader) error) error {
	r.staged++
	return nil
}
func (r *limitRun) Publish(context.Context) (publication.Result, error) {
	r.published++
	return publication.Result{}, nil
}
func (*limitRun) Close() error { return nil }

func TestOverLimitNeverFinalizes(t *testing.T) {
	for _, bound := range []string{"artifact", "units", "unit bytes", "total bytes", "segments", "later segments"} {
		t.Run(bound, func(t *testing.T) {
			session, profile := generationFixture(t, []string{"one", "two three four"}, nil)
			calls := 0
			core := NewCore(fakeEngine(func(context.Context, unit.UnitID, string) (Generation, error) {
				calls++
				return Generation{Finish: Stop}, nil
			}))
			switch bound {
			case "artifact":
				core.limits.ArtifactBytes = session.ArtifactBytes() - 1
			case "units":
				core.limits.Units = 1
			case "unit bytes":
				core.limits.UnitTextBytes = 12
			case "total bytes":
				core.limits.TotalTextBytes = 15
			case "segments":
				core.limits.Segments = 1
			case "later segments":
				core = segmentedFixture(t, byteLength, func(context.Context, inference.GenerationRequest) (inference.GenerationResult, error) {
					calls++
					return inference.GenerationResult{}, nil
				})
				core.limits.Segments = 3
				profile = segmentedProfile(t)
			}
			run, tm := &limitRun{}, &fakeTM{}
			result, err := core.file(context.Background(), publication.Options{}, sessionAdapter{session: session}, profile, tm, func(context.Context, publication.Options) (publicationRun, error) { return run, nil })
			if !errors.Is(err, config.ErrLimitExceeded) || calls != 0 || tm.calls != 0 || run.staged != 0 || run.published != 0 || result.TMCommitted || result.Publication.State != publication.NotPublished {
				t.Fatalf("result=%+v err=%v generate=%d commit=%d stage=%d publish=%d", result, err, calls, tm.calls, run.staged, run.published)
			}
		})
	}
}
