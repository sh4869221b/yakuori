//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/sh4869221b/yakuori/internal/tm"
)

type stdoutBoundaryWriter struct {
	bytes.Buffer
	calls int
	after *bool
	t     *testing.T
}

func (w *stdoutBoundaryWriter) Write(p []byte) (int, error) {
	w.calls++
	if !*w.after {
		w.t.Error("write before commit completed")
	}
	return w.Buffer.Write(p)
}

func TestPipelineStdoutBoundaries(t *testing.T) {
	for _, scenario := range []string{"normal", "cancel-before", "cancel-after"} {
		t.Run(scenario, func(t *testing.T) {
			spec := pipelineHarnessCase{name: "stdout/text", kind: "text", surface: "stdout"}
			h := newPipelineHarness(t, spec, true)
			expected := pipelineHarnessExpectationForFixture(t, spec, h.fixture)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			before, after, observe := false, false, false
			w := &stdoutBoundaryWriter{after: &after, t: t}
			h.adapter.(*pipelineHarnessAdapter).observeEntry = func() {
				observe = true
				if w.calls != 0 {
					t.Error("write reached during Observe")
				}
			}
			h.fixture.store.SetPipelineCommitHooks(tm.PipelineCommitHooks{
				Before: func(context.Context, *sql.Conn) error {
					before = true
					if w.calls != 0 {
						t.Error("write reached before commit")
					}
					if scenario == "cancel-before" {
						cancel()
					}
					return nil
				},
				After: func() {
					after = true
					if scenario == "cancel-after" {
						cancel()
					}
				},
			})
			result, err := h.core.TextWithTM(ctx, h.fixture.input.source, h.adapter, h.fixture.profile, h.fixture.store, h.fixture.scope, w)
			if !observe || h.adapterObserves() != 1 || h.trace.reached[pipelineBoundaryObserveAfter] != 1 || !before || after != (scenario != "cancel-before") {
				t.Fatalf("boundaries observe=%v before=%v after=%v", observe, before, after)
			}
			rows := expected.D1
			if scenario == "cancel-before" {
				rows = expected.D0
			}
			if diff := pipelineFixtureDiff(rows, pipelineFixtureReadRows(t, h.fixture.db)); len(diff) != 0 {
				t.Fatalf("rows: %v", diff)
			}
			if scenario == "normal" {
				if err != nil || !result.TMCommitted || w.calls != 1 || !bytes.Equal(w.Bytes(), expected.TargetBytes) || result.BytesWritten != w.Len() {
					t.Fatalf("result=%+v err=%v calls=%d bytes=%q", result, err, w.calls, w.Bytes())
				}
			} else {
				var failure *Error
				if !errors.As(err, &failure) || failure.Phase != "commit" || result.TMCommitted != after || w.calls != 0 || w.Len() != 0 || result.BytesWritten != 0 {
					t.Fatalf("result=%+v err=%v calls=%d", result, err, w.calls)
				}
			}
			if result.Publication.State != "" {
				t.Fatalf("state=%q", result.Publication.State)
			}
		})
	}
}
