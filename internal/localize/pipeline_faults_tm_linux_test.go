//go:build linux && pipelinefaults

package localize

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
)

// allow: SIZE_OK - Task4 owns this single file; the SQL matrix and its fixture
// lifecycle stay together without extending the shared harness or TM hooks.
func TestPipelineTMTransactionFailures(t *testing.T) {
	pipelineTMFaultMatrix(t, []string{
		"success", "writer-deadline", "reader-commit-deadline", "row-abort",
		"row-rollback", "rollback-cleanup", "conflict-miss",
		"conflict-valid-changed", "conflict-valid-removed",
		"conflict-invalid-changed", "conflict-invalid-removed",
	})
}

func TestPipelineTMCommitCancellation(t *testing.T) {
	pipelineTMFaultMatrix(t, []string{
		"after-stage", "before-sql-commit", "after-sql-commit", "publish-entry",
	})
}

func pipelineTMFaultMatrix(t *testing.T, rows []string) {
	t.Helper()
	for _, row := range rows {
		t.Run(row, func(t *testing.T) {
			for _, kind := range []string{"text", "w3"} {
				t.Run(kind, func(t *testing.T) {
					for _, mode := range pipelineFixtureModes() {
						t.Run(mode.name, func(t *testing.T) {
							pipelineTMFaultExercise(t, row, pipelineFixtureSpec(kind, mode))
						})
					}
				})
			}
		})
	}
}

type pipelineTMFaultSchema struct {
	Type, Name, Table string
	RootPage          int
	SQL               string
}

func pipelineTMFaultSchemaRows(t *testing.T, db *sql.DB) []pipelineTMFaultSchema {
	t.Helper()
	rows, err := db.Query("SELECT type,name,tbl_name,rootpage,coalesce(sql,'') FROM sqlite_schema ORDER BY type,name")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	var result []pipelineTMFaultSchema
	for rows.Next() {
		var row pipelineTMFaultSchema
		if err := rows.Scan(&row.Type, &row.Name, &row.Table, &row.RootPage, &row.SQL); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func pipelineTMFaultConnRows(t *testing.T, c *sql.Conn) []pipelineFixtureRow {
	t.Helper()
	rows, err := c.QueryContext(t.Context(), "SELECT key_schema,lower(hex(source_identity)),lower(hex(translation_profile)),translated_text FROM tm ORDER BY source_identity,translation_profile")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	var result []pipelineFixtureRow
	for rows.Next() {
		var row pipelineFixtureRow
		if err := rows.Scan(&row.Schema, &row.Source, &row.Profile, &row.Text); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

// Exercise owns one real Core call, its independent SQL stimulus, and the
// explicit removal of fixture schema. It never opens a second owning Store
// to manufacture a SQLite contention error.
func pipelineTMFaultExercise(t *testing.T, row string, spec pipelineHarnessCase) {
	t.Helper()
	// Given: authored D0/D1, a repair UPDATE, misses, and an unrelated sentinel.
	h := newPipelineHarnessWith(t, spec, true, func(f *pipelineFixture) {
		if row == "writer-deadline" {
			// Only the bounded transaction is timed; staging precedes its clock.
			f.limits.DBOperationTimeout = 20 * time.Millisecond
		}
		if row == "reader-commit-deadline" {
			f.limits.DBOperationTimeout = time.Second
		}
	})
	f := h.fixture
	if fs := pipelineHarnessFilesystem(t, h.sourcePath); fs != "btrfs" && fs != "ext4" {
		t.Fatalf("qualified TM matrix requires ext4/Btrfs, got %s", fs)
	}
	want := pipelineHarnessExpectationForFixture(t, spec, f)
	first := f.authoredRow(t, f.input.units[0])
	last := f.authoredRow(t, f.input.units[len(f.input.units)-1])
	seed := first
	seed.Text = ""
	seeds := []pipelineFixtureRow{seed}
	if spec.kind == "text" && row == "conflict-miss" {
		seeds = nil
	}
	if strings.HasPrefix(row, "conflict-valid-") || strings.HasPrefix(row, "conflict-invalid-") {
		seed = last
		if strings.HasPrefix(row, "conflict-invalid-") {
			seed.Text = ""
		}
		if first.Source == last.Source {
			seeds = []pipelineFixtureRow{seed}
		} else {
			seeds = append(seeds, seed)
		}
	}
	for _, seeded := range seeds {
		if _, err := f.db.Exec("INSERT INTO tm VALUES(?,?,?,?)", seeded.Schema,
			pipelineFixtureKeyBytes(t, seeded.Source), pipelineFixtureKeyBytes(t, seeded.Profile), seeded.Text); err != nil {
			t.Fatal(err)
		}
		want.D0 = append(want.D0, seeded)
	}
	want.D0 = pipelineFixtureSortRows(want.D0)
	want.StageRows = slices.Clone(want.D0)
	if diff := pipelineFixtureDiff(want.D0, pipelineFixtureReadRows(t, f.db)); len(diff) != 0 {
		t.Fatalf("authored setup differs: %v", diff)
	}
	generate := pipelineHarnessGenerateCount(f.input)
	if strings.HasPrefix(row, "conflict-valid-") {
		generate--
	}
	committed := row == "success" || row == "after-sql-commit" || row == "publish-entry"
	if row != "success" {
		want.Outcome, want.StageRetained, want.StageBytes = "not-published", true, slices.Clone(f.input.target)
		want.RecordInstalled, want.RecordFDClosed, want.BackupPresent = false, false, false
		want.RecordPhase, want.RecordOperation = "", ""
		want.OutputPresent = spec.outputPresent || spec.mode == publication.InPlace
		want.OutputPreserved, want.SourcePreserved = want.OutputPresent, true
		want.OutputBytes = nil
		if spec.outputPresent {
			want.OutputBytes = slices.Clone(pipelineHarnessOldOutput)
		}
		if spec.mode == publication.InPlace {
			want.OutputBytes = slices.Clone(f.input.source)
		}
		want.CommitCheckpoint = row == "publish-entry"
		want.CommitCertainty = pipelineHarnessNotCommitted
		want.FinalRows = slices.Clone(want.D0)
		if committed {
			want.FinalRows = slices.Clone(want.D1)
			want.CommitCertainty = pipelineHarnessUnknown
			if row == "publish-entry" {
				want.CommitCertainty = pipelineHarnessCommitted
			}
		}
		for name := range want.RequiredBoundaries {
			if strings.HasPrefix(name, "record.") || strings.HasPrefix(name, "backup-rename.") ||
				strings.HasPrefix(name, "publish-rename.") || name == pipelineBoundaryStageCloseAfter || name == pipelineBoundaryTMCommitAfter {
				want.RequiredBoundaries[name] = 0
			}
		}
		if row == "publish-entry" {
			want.RequiredBoundaries[pipelineBoundaryTMCommitAfter] = 1
		}
	}
	if strings.HasPrefix(row, "conflict-") {
		// The expected winner is authored before any external write or readback.
		winner := last
		winner.Text = "external winner.\n"
		want.FinalRows = slices.DeleteFunc(slices.Clone(want.D0), func(r pipelineFixtureRow) bool {
			return r.Schema == last.Schema && r.Source == last.Source && r.Profile == last.Profile
		})
		if !strings.HasSuffix(row, "-removed") {
			want.FinalRows = append(want.FinalRows, winner)
		}
		want.FinalRows = pipelineFixtureSortRows(want.FinalRows)
		want.CommitCertainty = pipelineHarnessUnknown
	}
	want.RequiredBoundaries[pipelineBoundaryGenerateBefore] = generate
	want.RequiredBoundaries[pipelineBoundaryGenerateAfter] = generate
	want.BoundaryOrder = nil
	for range generate {
		want.BoundaryOrder = append(want.BoundaryOrder, pipelineBoundaryGenerateBefore, pipelineBoundaryGenerateAfter)
	}
	want.BoundaryOrder = append(want.BoundaryOrder, pipelineBoundaryStageWriteBefore, pipelineBoundaryStageWriteAfter,
		pipelineBoundaryObserveBefore, pipelineBoundaryObserveAfter, pipelineBoundaryCompareFinalAfter, pipelineBoundaryTMCommitBefore)
	if row == "success" || row == "publish-entry" {
		want.BoundaryOrder = append(want.BoundaryOrder, pipelineBoundaryTMCommitAfter)
	}
	want.BoundaryOrder = append(want.BoundaryOrder, pipelineBoundaryRunCloseAfter)
	schema := pipelineTMFaultSchemaRows(t, f.db)
	pipelineFixtureSaveJSON(t, "schema-before.json", schema)
	pipelineFixtureSaveJSON(t, "expected.json", want)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var holder *sql.Conn
	t.Cleanup(func() {
		if holder != nil {
			if _, err := holder.ExecContext(context.Background(), "ROLLBACK"); err != nil {
				t.Error(err)
			}
			if err := holder.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	var stage pipelineHarnessFile
	var parents pipelineHarnessRecordIdentity
	var hooks []string
	var transactionRows, independentBefore, independentAfter []pipelineFixtureRow
	var discarded any // The actual SQLite driver connection, compared only in the cleanup case.
	trigger := ""
	lockAcquired := false
	h.afterStageHook = func() error {
		h.rowsAfterStage = pipelineFixtureReadRows(t, f.db)
		var err error
		stage, err = pipelineHarnessReadFile(h.ops.paths.Stage)
		if err != nil {
			t.Fatal(err)
		}
		parents = pipelineHarnessReadParent(t, h.options.Source)
		switch row {
		case "after-stage":
			h.injected = append(h.injected, row)
			cancel()
		case "writer-deadline", "reader-commit-deadline":
			holder, err = f.db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			begin := "BEGIN IMMEDIATE"
			if row == "reader-commit-deadline" {
				begin = "BEGIN"
			}
			if _, err := holder.ExecContext(t.Context(), begin); err != nil {
				t.Fatal(err)
			}
			// A completed SELECT holds a real SHARED lock until ROLLBACK.
			if diff := pipelineFixtureDiff(want.D0, pipelineTMFaultConnRows(t, holder)); len(diff) != 0 {
				t.Fatalf("lock holder rows: %v", diff)
			}
			lockAcquired = true
			h.injected = append(h.injected, row)
		case "row-abort", "row-rollback", "rollback-cleanup":
			action, operation, target := "ABORT", "INSERT", last
			if row != "row-abort" {
				action = "ROLLBACK"
			}
			if spec.kind == "text" {
				operation = "UPDATE"
			}
			if row == "rollback-cleanup" {
				operation, target = "UPDATE", f.d0[0]
			}
			predicate := fmt.Sprintf("NEW.key_schema=%d AND NEW.source_identity=x'%s' AND NEW.translation_profile=x'%s'", target.Schema, target.Source, target.Profile)
			if spec.kind == "w3" {
				// The fault is reachable only AFTER the real repair UPDATE and
				// an earlier INSERT are visible inside this same transaction.
				second := f.authoredRow(t, f.input.units[1])
				for _, prior := range []pipelineFixtureRow{first, second} {
					predicate += fmt.Sprintf(" AND EXISTS(SELECT 1 FROM tm WHERE key_schema=%d AND source_identity=x'%s' AND translation_profile=x'%s' AND translated_text=CAST(x'%s' AS TEXT))",
						prior.Schema, prior.Source, prior.Profile, hex.EncodeToString([]byte(prior.Text)))
				}
			}
			trigger = fmt.Sprintf("CREATE TRIGGER task4_fault BEFORE %s ON tm WHEN %s BEGIN SELECT RAISE(%s,'task4-after-prior-DML'); END", operation, predicate, action)
			if _, err := f.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			h.injected = append(h.injected, row)
		case "conflict-miss":
			if _, err := f.db.Exec("INSERT INTO tm VALUES(?,?,?,?)", last.Schema,
				pipelineFixtureKeyBytes(t, last.Source), pipelineFixtureKeyBytes(t, last.Profile), "external winner.\n"); err != nil {
				t.Fatal(err)
			}
			h.injected = append(h.injected, row)
		case "conflict-valid-changed", "conflict-invalid-changed", "conflict-valid-removed", "conflict-invalid-removed":
			query := "UPDATE tm SET translated_text='external winner.' || char(10) WHERE key_schema=? AND source_identity=? AND translation_profile=?"
			if strings.HasSuffix(row, "-removed") {
				query = "DELETE FROM tm WHERE key_schema=? AND source_identity=? AND translation_profile=?"
			}
			result, err := f.db.Exec(query, last.Schema, pipelineFixtureKeyBytes(t, last.Source), pipelineFixtureKeyBytes(t, last.Profile))
			if err != nil {
				t.Fatal(err)
			}
			if n, err := result.RowsAffected(); err != nil || n != 1 {
				t.Fatalf("external winner affected %d rows: %v", n, err)
			}
			h.injected = append(h.injected, row)
		}
		return nil
	}
	f.store.SetPipelineCommitHooks(tm.PipelineCommitHooks{
		Before: func(commitCtx context.Context, c *sql.Conn) error {
			hooks = append(hooks, "before")
			transactionRows = pipelineTMFaultConnRows(t, c)
			independentBefore = pipelineFixtureReadRows(t, f.db)
			if diff := pipelineFixtureDiff(want.D1, transactionRows); len(diff) != 0 {
				t.Fatalf("before SQL COMMIT lacks real DML: %v", diff)
			}
			if diff := pipelineFixtureDiff(want.D0, independentBefore); len(diff) != 0 {
				t.Fatalf("uncommitted DML leaked to the independent connection: %v", diff)
			}
			if row == "reader-commit-deadline" {
				// Keep the real COMMIT waiting longer than its budget, without
				// making preceding UPDATE/INSERT race a tiny wall-clock limit.
				if _, err := c.ExecContext(commitCtx, "PRAGMA busy_timeout=2000"); err != nil {
					t.Fatal(err)
				}
			}
			if err := commitCtx.Err(); err != nil {
				t.Fatalf("deadline expired before the intended COMMIT command: %v", err)
			}
			switch row {
			case "before-sql-commit":
				h.injected = append(h.injected, row)
				cancel()
			case "rollback-cleanup":
				if err := c.Raw(func(actual any) error { discarded = actual; return nil }); err != nil {
					t.Fatal(err)
				}
				// The component's RAISE(ROLLBACK) mechanism ends the SQL
				// transaction, making product cleanup discard this connection.
				_, err := c.ExecContext(commitCtx, "UPDATE tm SET translated_text=translated_text WHERE source_identity=? AND translation_profile=?",
					pipelineFixtureKeyBytes(t, f.d0[0].Source), pipelineFixtureKeyBytes(t, f.d0[0].Profile))
				return err
			}
			return nil
		},
		After: func() {
			hooks = append(hooks, "after")
			independentAfter = pipelineFixtureReadRows(t, f.db)
			if diff := pipelineFixtureDiff(want.D1, independentAfter); len(diff) != 0 {
				t.Fatalf("after SQL COMMIT does not expose D1: %v", diff)
			}
			if row == "after-sql-commit" {
				h.injected = append(h.injected, row)
				cancel()
			}
		},
	})
	// When: the same real Core/Store/publication path runs with per-Store SQL
	// checkpoints. Publish-entry remains distinct from the strict after hook.
	h.memory = &tmExecution{store: f.store, scope: f.scope}
	h.result, h.runErr = h.core.fileWithTM(ctx, h.options, h.adapter, f.profile, nil, h.memory,
		func(ctx context.Context, options publication.Options) (publicationRun, error) {
			run, err := h.prepare(ctx, options)
			if err == nil && row == "publish-entry" {
				run.(*pipelineHarnessRun).publishEntry = func() error {
					if err := h.commitEntry(); err != nil {
						return err
					}
					h.injected = append(h.injected, row)
					cancel()
					return nil
				}
			}
			return run, err
		})
	if holder != nil {
		if _, err := holder.ExecContext(t.Context(), "ROLLBACK"); err != nil {
			t.Fatal(err)
		}
		if err := holder.Close(); err != nil {
			t.Fatal(err)
		}
		holder = nil
	}
	// Then: preserve raw DB/schema before fixture cleanup; independently judge
	// complete rows, file bytes/identities, real closure and installation.
	got, err := h.snapshot(want)
	if err != nil {
		t.Fatal(err)
	}
	pipelineFinalSave(t, h, "returned", got)
	var sequence int
	var database, dbPath string
	if err := f.db.QueryRow("PRAGMA database_list").Scan(&sequence, &database, &dbPath); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	pipelineFixtureSave(t, "returned-raw.db", raw)
	pipelineFixtureSaveJSON(t, "schema-returned.json", pipelineTMFaultSchemaRows(t, f.db))
	pipelineFixtureSaveJSON(t, "before-sql-transaction.json", transactionRows)
	pipelineFixtureSaveJSON(t, "before-sql-independent.json", independentBefore)
	pipelineFixtureSaveJSON(t, "after-sql-independent.json", independentAfter)
	var failure *Error
	cause := ""
	if errors.As(h.runErr, &failure) {
		cause = failure.Err.Error()
	}
	// A false result flag is not a rollback receipt. Keep what the caller can
	// know separate from the observer's subsequent full physical DB readback.
	callerCertainty := pipelineHarnessUnknown
	if h.result.TMCommitted {
		callerCertainty = pipelineHarnessCommitted
	}
	pipelineFixtureSaveJSON(t, "sql-boundaries.json", struct {
		Hooks           []string
		LockAcquired    bool
		Trigger         string
		Cause           string
		CallerCertainty pipelineHarnessCertaintyValue
		Stage           pipelineHarnessFile
		Parents         pipelineHarnessRecordIdentity
		ParentsAfter    pipelineHarnessRecordIdentity
	}{hooks, lockAcquired, trigger, cause, callerCertainty, stage, parents, pipelineHarnessReadParent(t, h.sourcePath)})
	if problems := pipelineHarnessJudge(want, got); len(problems) != 0 {
		t.Fatalf("TM physical state: %v", problems)
	}
	wantHooks := []string{"before", "after"}
	switch row {
	case "writer-deadline", "row-abort", "row-rollback", "after-stage",
		"conflict-miss", "conflict-valid-changed", "conflict-valid-removed", "conflict-invalid-changed", "conflict-invalid-removed":
		wantHooks = nil
	case "reader-commit-deadline", "before-sql-commit", "rollback-cleanup":
		wantHooks = []string{"before"}
	}
	if !slices.Equal(hooks, wantHooks) || h.result.TMCommitted != committed || got.GenerateCalls != generate {
		t.Fatalf("SQL boundary/result mismatch: hooks=%v want=%v result=%+v generation=%d want=%d", hooks, wantHooks, h.result, got.GenerateCalls, generate)
	}
	injections := []string{row}
	if row == "success" {
		injections = nil
	}
	if !slices.Equal(h.injected, injections) {
		t.Fatalf("unhit/double injection: got=%v want=%v", h.injected, injections)
	}
	phase := "commit"
	switch row {
	case "success":
		if h.runErr != nil {
			t.Fatal(h.runErr)
		}
		phase = ""
	case "writer-deadline", "reader-commit-deadline":
		if !lockAcquired || !errors.Is(h.runErr, context.DeadlineExceeded) {
			t.Fatalf("real lock deadline missing: %v", h.runErr)
		}
	case "row-abort", "row-rollback", "rollback-cleanup":
		if !strings.Contains(cause, "task4-after-prior-DML") {
			t.Fatalf("DML trigger did not execute after its required prefix: %s", cause)
		}
		if row != "row-abort" && (!errors.Is(h.runErr, driver.ErrBadConn) || !strings.Contains(cause, "commit rollback")) {
			t.Fatalf("primary/cleanup/discard cause chain incomplete: %s", cause)
		}
	case "conflict-miss", "conflict-valid-changed", "conflict-valid-removed", "conflict-invalid-changed", "conflict-invalid-removed":
		if !errors.Is(h.runErr, tm.ErrConflict) {
			t.Fatalf("real candidate conflict missing: %v", h.runErr)
		}
	case "after-stage", "before-sql-commit", "after-sql-commit", "publish-entry":
		if !errors.Is(h.runErr, context.Canceled) {
			t.Fatalf("cancellation missing: %v", h.runErr)
		}
		if row == "publish-entry" {
			phase = "publish"
		}
	}
	if got.ErrorPhase != phase || !got.ResultPresent {
		t.Fatalf("phase/Result presence: %s/%t, want %s/true", got.ErrorPhase, got.ResultPresent, phase)
	}
	counterpart := got.Files["stage"]
	if row == "success" {
		counterpart = got.Files["output"]
	}
	if problems := pipelineHarnessCheckCaptured("validated-stage", counterpart, &stage, f.input.target); len(problems) != 0 {
		t.Fatalf("stage identity/bytes: %v", problems)
	}
	source := got.Files["source"]
	if row == "success" && spec.mode == publication.InPlace {
		source = got.Files["backup"]
	}
	if problems := pipelineHarnessCheckCaptured("preserved-source", source, &h.beforeSource, f.input.source); len(problems) != 0 {
		t.Fatalf("source identity/bytes: %v", problems)
	}
	if row != "success" && pipelineHarnessReadParent(t, h.sourcePath) != parents {
		t.Fatal("unpublished source/output parent identity changed")
	}
	if !pipelineLockAvailable(t, filepath.Dir(h.sourcePath)) {
		t.Fatal("publication lock retained after Core returned")
	}
	// Sensitivity controls alter exactly the final DB observation. Their
	// expectations remain authored, including every unrelated sentinel column.
	for _, control := range []string{"D1-as-D0", "sentinel-text"} {
		bad := got.clone()
		switch control {
		case "D1-as-D0":
			bad.Rows = slices.Clone(want.D0)
			if pipelineHarnessRowsEqual(want.FinalRows, want.D0) {
				control = "D0-as-D1"
				bad.Rows = slices.Clone(want.D1)
			}
		case "sentinel-text":
			for i := range bad.Rows {
				if bad.Rows[i].Source == f.d0[0].Source {
					bad.Rows[i].Text = "corrupt sentinel\n"
				}
			}
		}
		problems := pipelineHarnessJudge(want, bad)
		if len(problems) == 0 {
			t.Fatalf("oracle accepted %s", control)
		}
		pipelineFixtureSaveJSON(t, "reject-"+control+".json", problems)
	}
	if trigger != "" {
		if _, err := f.db.Exec("DROP TRIGGER task4_fault"); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(schema, pipelineTMFaultSchemaRows(t, f.db)) ||
		len(pipelineFixtureDiff(want.FinalRows, pipelineFixtureReadRows(t, f.db))) != 0 {
		t.Fatal("explicit fixture trigger removal changed rows or failed to restore schema")
	}
	pipelineFixtureSaveJSON(t, "schema-restored.json", pipelineTMFaultSchemaRows(t, f.db))
	if row == "rollback-cleanup" {
		freshConnections := 0
		freshDriver := ""
		f.store.SetPipelineCommitHooks(tm.PipelineCommitHooks{Before: func(_ context.Context, c *sql.Conn) error {
			freshConnections++
			return c.Raw(func(actual any) error {
				if actual == discarded {
					t.Fatal("discarded SQLite connection was reused")
				}
				freshDriver = fmt.Sprintf("%p", actual)
				return nil
			})
		}})
		next := pipelineHarnessOnFixture(t, spec, f, t.TempDir())
		if result, err := next.execute(t.Context()); err != nil || !result.TMCommitted || freshConnections != 1 {
			t.Fatalf("next fresh Run acquisition: result=%+v err=%v connections=%d", result, err, freshConnections)
		}
		output := next.outputBytes(t)
		f.assertFixtureOutput(t, f.input, output)
		if diff := pipelineFixtureDiff(want.D1, pipelineFixtureReadRows(t, f.db)); len(diff) != 0 {
			t.Fatalf("fresh Run rows: %v", diff)
		}
		pipelineFixtureSaveJSON(t, "fresh-rows.json", pipelineFixtureReadRows(t, f.db))
		pipelineFixtureSave(t, "fresh-output.bin", output)
		raw, err := os.ReadFile(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		pipelineFixtureSave(t, "fresh-raw.db", raw)
		pipelineFixtureSaveJSON(t, "discard.json", struct {
			FreshConnections             int
			DiscardedDriver, FreshDriver string
		}{freshConnections, fmt.Sprintf("%p", discarded), freshDriver})
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := tm.Open(t.Context(), dbPath, f.limits)
	if err != nil {
		t.Fatalf("explicit fresh Open after fixture schema restoration: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("TM row=%s hooks=%v injected=%v physical_rows=%d observed_certainty=%s caller_certainty=%s ResultPresent=%t TMCommitted=%t phase=%s lock=%t trigger=%t", row, hooks, h.injected, len(got.Rows), got.Certainty, callerCertainty, got.ResultPresent, got.TMCommitted, got.ErrorPhase, lockAcquired, trigger != "")
}
