//go:build linux && pipelinefaults

package tm

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestPipelineTMCommitHooks(t *testing.T) {
	for _, mode := range []string{
		"success", "isolation", "next-run-isolation", "before-error",
		"before-cancel", "after-cancel", "private-callback", "ineligible-run", "dml-error",
	} {
		t.Run(mode, func(t *testing.T) {
			// Given: an invalid candidate to UPDATE, 23 misses to INSERT, and
			// an unrelated sentinel. Expected rows come from authored inputs.
			path, s := storeFixture(t)
			r, d1 := checkpointBatch(t, s)
			d0 := []checkpointRow{d1[0], d1[1]}
			d0[1].Text = ""
			for _, row := range d0 {
				storeExec(t, s.db, "INSERT INTO tm VALUES(?,?,?,?)",
					row.Schema, []byte(row.Source), []byte(row.Profile), row.Text)
			}
			observer, err := connect(path, "ro")
			checkpointOK(t, err)
			defer func() { checkpointOK(t, observer.Close()) }()
			checkpointState(t, observer, d0)
			checkpointCapture(t, "original.sqlite", readBytes(t, path))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			injected := errors.New("before SQL COMMIT")
			var order []string
			s.SetPipelineCommitHooks(PipelineCommitHooks{
				Before: func(_ context.Context, c *sql.Conn) error {
					order = append(order, "before")
					checkpointState(t, c, d1)
					checkpointState(t, observer, d0)
					t.Log("before: transaction D1, separate connection D0")
					switch mode {
					case "before-error":
						return injected
					case "before-cancel":
						cancel()
					}
					return nil
				},
				After: func() {
					order = append(order, "after")
					checkpointState(t, observer, d1)
					t.Log("after: separate connection D1 before Commit returned")
					if mode == "after-cancel" {
						cancel()
					}
				},
			})
			switch mode {
			case "isolation":
				otherPath, other := storeFixture(t)
				otherRun, otherD1 := checkpointBatch(t, other)
				storeExec(t, other.db, "INSERT INTO tm VALUES(?,?,?,?)",
					otherD1[0].Schema, []byte(otherD1[0].Source), []byte(otherD1[0].Profile), otherD1[0].Text)
				commitRun(t, otherRun, checkpointAccepted(t, otherRun))
				otherObserver, err := connect(otherPath, "ro")
				checkpointOK(t, err)
				checkpointState(t, otherObserver, otherD1)
				checkpointOK(t, otherObserver.Close())
				checkpointCapture(t, "other.sqlite", readBytes(t, otherPath))
				if len(order) != 0 {
					t.Fatalf("hooks leaked to another Store: %v", order)
				}
			case "ineligible-run":
				if err := r.Commit(ctx, r.session, r.profile, nil); !errors.Is(err, ErrRunState) {
					t.Fatalf("unstaged run: %v", err)
				}
			case "dml-error":
				// Fail the second INSERT, after the repair and first INSERT.
				storeExec(t, s.db, `CREATE TRIGGER hook_second_insert BEFORE INSERT ON tm
					WHEN (SELECT count(*) FROM tm)=3
					BEGIN SELECT RAISE(ABORT,'hook DML failure'); END`)
			}
			values := checkpointAccepted(t, r)
			var private func(context.Context, *sql.Conn) error
			if mode == "private-callback" {
				private = func(_ context.Context, c *sql.Conn) error {
					order = append(order, "private")
					checkpointState(t, c, d1)
					return nil
				}
			}

			// When: the public Commit uses the real transaction. Only the
			// legacy-callback coexistence case needs the existing private seam.
			if private == nil {
				err = r.Commit(ctx, r.session, r.profile, values)
			} else {
				err = r.commit(ctx, r.session, r.profile, values, private)
			}
			checkpointCapture(t, "returned.sqlite", readBytes(t, path))

			// Then: exact callback order, error identity, and full independent
			// readback distinguish rollback from a successful cancelled commit.
			wantOrder := []string{"before", "after"}
			wantRows := d1
			var wantErr error
			switch mode {
			case "before-error":
				wantErr, wantRows, wantOrder = injected, d0, []string{"before"}
			case "before-cancel":
				wantErr, wantRows, wantOrder = context.Canceled, d0, []string{"before"}
			case "private-callback":
				wantOrder = []string{"private", "before", "after"}
			case "dml-error":
				if err == nil || !strings.Contains(err.Error(), "hook DML failure") {
					t.Fatalf("real DML failure was not reached: %v", err)
				}
				wantRows, wantOrder = d0, nil
			}
			if mode != "dml-error" && !errors.Is(err, wantErr) {
				t.Fatalf("Commit error=%v want=%v", err, wantErr)
			}
			if !slices.Equal(order, wantOrder) {
				t.Fatalf("checkpoint order/calls=%v want=%v", order, wantOrder)
			}
			if mode == "after-cancel" && !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("after callback did not cancel: %v", ctx.Err())
			}
			checkpointState(t, observer, wantRows)
			t.Logf("returned: order=%v error=%v context=%v", order, err, ctx.Err())
			if mode == "dml-error" {
				storeExec(t, s.db, "DROP TRIGGER hook_second_insert")
			}
			// An explicit fresh import is the next-run control, also after a
			// failed commit. It must neither cancel nor invoke stale hooks.
			fresh, freshD1 := checkpointBatch(t, s)
			commitRun(t, fresh, checkpointAccepted(t, fresh))
			if !slices.Equal(order, wantOrder) {
				t.Fatalf("hooks leaked to fresh Run: %v want=%v", order, wantOrder)
			}
			checkpointState(t, observer, freshD1)
			checkpointCapture(t, "fresh.sqlite", readBytes(t, path))
		})
	}
}
