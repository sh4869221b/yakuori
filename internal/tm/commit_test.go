//go:build linux

package tm

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func commitFixture(t *testing.T, r *Run, texts ...string) []validate.AcceptedTranslation {
	t.Helper()
	accepted := stageFixture(t, r, texts...)
	for _, u := range r.session.Units() {
		observed := r.observed[u.ID()]
		if observed.found {
			_, err := validate.Validate(r.session, u.ID(), r.profile, observed.text)
			if err := r.RecordValidation(u.ID(), err == nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := r.Stage(accepted); err != nil {
		t.Fatal(err)
	}
	manifest, err := artifact.NewManifest(r.session, r.profile, "fixture", "v1", r.scope.Keys, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CheckManifest(manifest); err != nil {
		t.Fatal(err)
	}
	return accepted
}

func commitRun(t *testing.T, r *Run, accepted []validate.AcceptedTranslation) {
	t.Helper()
	if err := r.Commit(context.Background(), r.session, r.profile, accepted); err != nil {
		t.Fatal(err)
	}
}

func candidateText(t *testing.T, s *Store, r *Run, id unit.UnitID) string {
	t.Helper()
	key := r.keys[id]
	var text string
	if err := s.db.QueryRow(`SELECT translated_text FROM tm WHERE key_schema=? AND source_identity=? AND translation_profile=?`, key.Schema(), key.SourceBytes(), key.ProfileBytes()).Scan(&text); err != nil {
		t.Fatal(err)
	}
	return text
}

func reopenStore(t *testing.T, path string, s *Store) *Store {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(context.Background(), path, config.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := opened.Close(); err != nil {
			t.Error(err)
		}
	})
	return opened
}

func TestCommitAcceptedBatch(t *testing.T) {
	path, s := storeFixture(t)
	session, profile := runSession(t, "source", "other"), runProfile(t, "model")
	run := beginFixture(t, s, session, profile, testScope())
	accepted := commitFixture(t, run, "訳文\nexact", "別訳")
	commitRun(t, run, accepted)
	if err := run.Commit(context.Background(), session, profile, accepted); !errors.Is(err, ErrRunState) {
		t.Fatalf("run reused: %v", err)
	}
	reopened := reopenStore(t, path, s)
	if count := rowCount(t, reopened); count != 2 {
		t.Fatalf("reopened rows = %d", count)
	}
	for i, u := range session.Units() {
		if got := candidateText(t, reopened, run, u.ID()); got != accepted[i].Text() {
			t.Fatalf("text = %q", got)
		}
	}
	t.Run("binding", func(t *testing.T) {
		fresh := runSession(t, "source", "other")
		r := beginFixture(t, reopened, fresh, profile, testScope())
		values := commitFixture(t, r, "訳文", "別訳")
		values[0] = accepted[0]
		if err := r.Commit(context.Background(), fresh, profile, values); !errors.Is(err, validate.ErrBindingMismatch) {
			t.Fatalf("binding = %v", err)
		}
	})
	t.Run("stage mismatch", func(t *testing.T) {
		r := beginFixture(t, reopened, session, profile, testScope())
		values := commitFixture(t, r, "訳文", "別訳")
		changed, err := validate.Validate(session, session.Units()[0].ID(), profile, "変更訳")
		if err != nil {
			t.Fatal(err)
		}
		values[0] = changed
		if err := r.Commit(context.Background(), session, profile, values); !errors.Is(err, validate.ErrBindingMismatch) {
			t.Fatalf("staging = %v", err)
		}
	})
	t.Run("manifest gate", func(t *testing.T) {
		r := beginFixture(t, reopened, session, profile, testScope())
		values := stageFixture(t, r, "訳文", "別訳")
		for _, u := range session.Units() {
			if err := r.RecordValidation(u.ID(), true); err != nil {
				t.Fatal(err)
			}
		}
		if err := r.Stage(values); err != nil {
			t.Fatal(err)
		}
		if err := r.Commit(context.Background(), session, profile, values); !errors.Is(err, ErrRunState) {
			t.Fatalf("manifest gate = %v", err)
		}
	})
}

func TestCommitIdempotencyAndRepair(t *testing.T) {
	path, s := storeFixture(t)
	session, profile := runSession(t, "source", "other"), runProfile(t, "model")
	run := beginFixture(t, s, session, profile, testScope())
	ids := session.Units()
	seedCandidate(t, run, ids[0].ID(), "先行訳")
	seedCandidate(t, run, ids[1].ID(), "\x00invalid")
	accepted := commitFixture(t, run, "異なる合格訳", "修復訳")
	if count := rowCount(t, s); count != 2 || candidateText(t, s, run, ids[1].ID()) != "\x00invalid" {
		t.Fatal("repair written before Commit")
	}
	commitRun(t, run, accepted)
	s = reopenStore(t, path, s)
	if count := rowCount(t, s); count != 2 {
		t.Fatalf("rows = %d", count)
	}
	if got := candidateText(t, s, run, ids[0].ID()); got != "先行訳" {
		t.Fatalf("valid first row replaced: %q", got)
	}
	if got := candidateText(t, s, run, ids[1].ID()); got != "修復訳" {
		t.Fatalf("invalid row not repaired: %q", got)
	}
	rerun := beginFixture(t, s, runSession(t, "source", "other"), profile, testScope())
	commitRun(t, rerun, commitFixture(t, rerun, "先行訳", "修復訳"))
	if count := rowCount(t, s); count != 2 {
		t.Fatalf("idempotent rows = %d", count)
	}
}

func TestCommitConcurrentKey(t *testing.T) {
	for _, mode := range []string{"miss", "valid changed", "invalid changed", "invalid removed", "valid removed"} {
		t.Run(mode, func(t *testing.T) {
			path, s := storeFixture(t)
			session, profile := runSession(t, "source", "other"), runProfile(t, "model")
			run := beginFixture(t, s, session, profile, testScope())
			id := session.Units()[1].ID()
			switch mode {
			case "valid changed", "valid removed":
				seedCandidate(t, run, id, "先行訳")
			case "invalid changed", "invalid removed":
				seedCandidate(t, run, id, "")
			}
			accepted := commitFixture(t, run, "途中訳", "予定訳")
			switch mode {
			case "miss":
				winner := beginFixture(t, s, session, profile, testScope())
				// The second run commits only its own complete accepted parent set.
				commitRun(t, winner, commitFixture(t, winner, "先行1", "先行2"))
			case "valid changed", "invalid changed":
				storeExec(t, s.db, "UPDATE tm SET translated_text=?", "変更された先行訳")
			case "valid removed", "invalid removed":
				storeExec(t, s.db, "DELETE FROM tm")
			}
			before := rowCount(t, s)
			if err := run.Commit(context.Background(), session, profile, accepted); !errors.Is(err, ErrConflict) {
				t.Fatalf("conflict = %v", err)
			}
			s = reopenStore(t, path, s)
			if count := rowCount(t, s); count != before {
				t.Fatalf("partial batch persisted: before=%d after=%d", before, count)
			}
			if before > 0 {
				expected := "変更された先行訳"
				if mode == "miss" {
					expected = "先行2"
				}
				if got := candidateText(t, s, run, id); got != expected {
					t.Fatalf("conflicting row overwritten: %q", got)
				}
			}
			rerun := beginFixture(t, s, session, profile, testScope())
			commitRun(t, rerun, commitFixture(t, rerun, "次回1", "次回2"))
		})
	}
}

func holdTransaction(t *testing.T, db *sql.DB, mode string) *sql.Conn {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := conn.ExecContext(context.Background(), mode); err != nil {
		t.Fatal(err)
	}
	return conn
}

func releaseTransaction(t *testing.T, conn *sql.Conn) {
	t.Helper()
	if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
}

func TestCommitBusyAndDeadline(t *testing.T) {
	for _, mode := range []string{"writer", "reader", "expired", "pool wait"} {
		t.Run(mode, func(t *testing.T) {
			path, s := storeFixture(t)
			session, profile := runSession(t, "source"), runProfile(t, "model")
			run := beginFixture(t, s, session, profile, testScope())
			accepted := commitFixture(t, run, "訳文")
			var holder *sql.Conn
			if mode == "writer" || mode == "reader" {
				other, err := connect(path, "rw")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := other.Close(); err != nil {
						t.Error(err)
					}
				})
				begin := "BEGIN IMMEDIATE"
				if mode == "reader" {
					begin = "BEGIN"
				}
				holder = holdTransaction(t, other, begin)
				if mode == "reader" {
					var count int
					if err := holder.QueryRowContext(context.Background(), "SELECT count(*) FROM tm").Scan(&count); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "pool wait" {
				var err error
				holder, err = s.db.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if mode == "expired" {
				cancel()
			}
			start := time.Now()
			err := run.Commit(ctx, session, profile, accepted)
			elapsed := time.Since(start)
			if err == nil || ctx.Err() == nil || !errors.Is(err, ctx.Err()) {
				t.Fatalf("deadline ignored: %v", err)
			}
			if elapsed > time.Second {
				t.Fatalf("cooperative budget exceeded: %s", elapsed)
			}
			t.Logf("%s commit elapsed=%s error=%v", mode, elapsed, err)
			if mode == "pool wait" {
				if err := holder.Close(); err != nil {
					t.Fatal(err)
				}
			} else if holder != nil {
				releaseTransaction(t, holder)
			}
			if count := rowCount(t, s); count != 0 {
				t.Fatalf("failed commit persisted %d rows", count)
			}
			next := beginFixture(t, s, session, profile, testScope())
			commitRun(t, next, commitFixture(t, next, "次回訳"))
			reopened := reopenStore(t, path, s)
			if count := rowCount(t, reopened); count != 1 {
				t.Fatalf("next explicit run rows = %d", count)
			}
		})
	}
}

func TestCommitCleanupFailure(t *testing.T) {
	path, s := storeFixture(t)
	session, profile := runSession(t, "source", "other"), runProfile(t, "model")
	run := beginFixture(t, s, session, profile, testScope())
	accepted := commitFixture(t, run, "途中訳", "拒否訳")
	// SQLite rolls back the entire transaction itself, so explicit cleanup must
	// report its failure and discard the connection rather than reuse it.
	storeExec(t, s.db, `CREATE TRIGGER fail_commit BEFORE INSERT ON tm WHEN NEW.translated_text='拒否訳' BEGIN SELECT RAISE(ROLLBACK,'fixture rollback'); END`)
	err := run.Commit(context.Background(), session, profile, accepted)
	if err == nil || !strings.Contains(err.Error(), "fixture rollback") || !strings.Contains(err.Error(), "commit rollback") {
		t.Fatalf("primary/cleanup errors = %v", err)
	}
	if stats := s.db.Stats(); stats.OpenConnections != 0 {
		t.Fatalf("failed cleanup connection returned to pool: %+v", stats)
	}
	if count := rowCount(t, s); count != 0 {
		t.Fatalf("native rollback persisted %d rows", count)
	}
	storeExec(t, s.db, "DROP TRIGGER fail_commit")
	s = reopenStore(t, path, s)
	next := beginFixture(t, s, session, profile, testScope())
	commitRun(t, next, commitFixture(t, next, "次回1", "次回2"))
	if count := rowCount(t, s); count != 2 {
		t.Fatalf("next explicit run rows = %d", count)
	}
}
