// Package sqliteprobe is an executable feasibility specification, not a TM API.
// All databases and synthetic translations here are test-owned fixtures.
package sqliteprobe

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"modernc.org/sqlite"
)

const busyMS = 100
const operationLimit = time.Second
const applicationID = 1497451343 // ASCII YAKO
const tableSQL = `CREATE TABLE tm (
 key_schema INTEGER NOT NULL CHECK(key_schema = 1),
 source_identity BLOB NOT NULL CHECK(length(source_identity) > 0),
 translation_profile BLOB NOT NULL CHECK(length(translation_profile) > 0),
 translated_text TEXT NOT NULL,
 PRIMARY KEY(key_schema, source_identity, translation_profile)
) STRICT, WITHOUT ROWID`
const insertSQL = `INSERT INTO tm VALUES(1, ?, ?, ?)
ON CONFLICT(key_schema, source_identity, translation_profile) DO NOTHING`

func connect(t *testing.T, path, mode string) *sql.DB {
	t.Helper()
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{"mode": {mode}, "_pragma": {fmt.Sprintf("busy_timeout(%d)", busyMS)}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}
func exec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), operationLimit)
	defer cancel()
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}
func fixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tm # fixture?.sqlite")
	// Creation is explicit and exclusively for a new test fixture; never an Open fallback.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	db := connect(t, path, "rw")
	exec(t, db, "PRAGMA journal_mode=DELETE")
	exec(t, db, fmt.Sprintf("PRAGMA application_id=%d", applicationID))
	exec(t, db, tableSQL)
	exec(t, db, "PRAGMA user_version=1")
	return path, db
}
func count(t *testing.T, db *sql.DB) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), operationLimit)
	defer cancel()
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM tm").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Dedicated sql.Conn allows COMMIT itself to receive the finite context; sql.Tx.Commit
// has no context parameter, and the pinned driver's Commit uses Background.
// No connection with an uncertain transaction is returned for reuse.
func transaction(ctx context.Context, db *sql.DB, work func(*sql.Conn) error) (err error) {
	c, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), operationLimit)
		defer cancel()
		if _, rollbackErr := c.ExecContext(cleanup, "ROLLBACK"); rollbackErr != nil {
			// An interrupted SQLite statement may already have rolled the transaction back.
			// Discard regardless, rather than deciding a failed rollback was harmless.
			_ = c.Raw(func(any) error { return driver.ErrBadConn })
			err = errors.Join(err, fmt.Errorf("rollback: %w", rollbackErr))
		}
	}()
	if err = work(c); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if _, err = c.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

// probeExisting is read-only and rejects missing, empty, foreign, corrupt and unknown
// schemas. It is a quiescent-fixture proof, not a concurrent production Open API.
func probeExisting(t *testing.T, path string) error {
	// This probe intentionally refuses recovery and never ignores WAL state with immutable=1.
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if _, err := os.Lstat(path + suffix); err == nil {
			return errors.New("recovery sidecar requires separate inspection")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	db := connect(t, path, "ro")
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), operationLimit)
	defer cancel()
	var check string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&check); err != nil {
		return err
	}
	if check != "ok" {
		return fmt.Errorf("integrity: %s", check)
	}
	var app, version int
	if err := db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&app); err != nil {
		return err
	}
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if app != applicationID || version != 1 {
		return fmt.Errorf("unsupported application/schema: %d/%d", app, version)
	}
	var schema string
	if err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_schema WHERE type='table' AND name='tm'").Scan(&schema); err != nil {
		return err
	}
	if schema != tableSQL {
		return errors.New("unknown schema layout")
	}
	var objects int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&objects); err != nil {
		return err
	}
	if objects != 1 {
		return errors.New("unexpected schema objects")
	}
	return nil
}

func TestDriverAndRollback(t *testing.T) {
	_, db := fixture(t)
	var version string
	if err := db.QueryRow("SELECT sqlite_version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("SQLite %s; driver modernc.org/sqlite v1.60.1; CGO=0 gate", version)
	sentinel := errors.New("injected failure")
	ctx, cancel := context.WithTimeout(context.Background(), operationLimit)
	defer cancel()
	err := transaction(ctx, db, func(c *sql.Conn) error {
		if _, e := c.ExecContext(ctx, insertSQL, []byte("source"), []byte("profile"), "合格訳"); e != nil {
			return e
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if n := count(t, db); n != 0 {
		t.Fatalf("rollback left %d rows", n)
	}
}

func TestExactKeyFirstValidWriterWins(t *testing.T) {
	path, db := fixture(t)
	other := connect(t, path, "rw")
	for _, candidate := range []struct {
		db   *sql.DB
		text string
	}{{db, "最初の合格訳"}, {other, "別の合格訳"}, {db, "最初の合格訳"}} {
		ctx, cancel := context.WithTimeout(context.Background(), operationLimit)
		err := transaction(ctx, candidate.db, func(c *sql.Conn) error {
			_, e := c.ExecContext(ctx, insertSQL, []byte("source"), []byte("profile"), candidate.text)
			return e
		})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, db); n != 1 {
		t.Fatalf("got %d rows", n)
	}
	var got string
	if err := db.QueryRow("SELECT translated_text FROM tm").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "最初の合格訳" {
		t.Fatal(got)
	}
	// Full binary identities are distinct, including trailing whitespace, NUL and Unicode.
	for _, s := range []string{"source ", "source\x00", "é", "e\u0301"} {
		exec(t, db, insertSQL, []byte(s), []byte("profile"), "fixture")
	}
	exec(t, db, insertSQL, []byte("source"), []byte("profile-v2"), "fixture")
	if n := count(t, db); n != 6 {
		t.Fatalf("exact bytes collapsed: %d", n)
	}
	// Only targeted UNIQUE conflict is ignored, not schema/check failures.
	if _, err := db.Exec("INSERT INTO tm VALUES(2,?,?,?)", []byte("source"), []byte("profile"), "bad"); err == nil {
		t.Fatal("accepted unknown key schema")
	}
}

func TestRejectPreservesExistingBytes(t *testing.T) {
	for _, kind := range []string{"garbage", "truncated", "empty", "foreign", "future", "changed-layout", "extra-trigger"} {
		t.Run(kind, func(t *testing.T) {
			path, db := fixture(t)
			switch kind {
			case "foreign":
				exec(t, db, "PRAGMA application_id=0")
			case "future":
				exec(t, db, "PRAGMA user_version=99")
			case "changed-layout":
				exec(t, db, "ALTER TABLE tm ADD COLUMN surprise TEXT")
			case "extra-trigger":
				exec(t, db, "CREATE TRIGGER surprise AFTER INSERT ON tm BEGIN DELETE FROM tm; END")
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "garbage":
				if err := os.WriteFile(path, []byte("not a database\x00existing asset"), 0600); err != nil {
					t.Fatal(err)
				}
			case "truncated":
				if err := os.Truncate(path, 150); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if err := os.Truncate(path, 0); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = probeExisting(t, path); err == nil {
				t.Fatal("accepted unsupported database")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("modified rejected database")
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatal("created sidecar for rejected fixture")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "missing.sqlite")
	if err := probeExisting(t, path); err == nil {
		t.Fatal("accepted missing DB")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created missing DB", err)
	}
	path, db := fixture(t)
	db.Close()
	if err := probeExisting(t, path); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationAtomic(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			path, db := fixture(t)
			exec(t, db, insertSQL, []byte("source"), []byte("profile"), "preserved")
			ctx, cancel := context.WithTimeout(context.Background(), operationLimit)
			defer cancel()
			err := transaction(ctx, db, func(c *sql.Conn) error {
				for _, stmt := range []string{"ALTER TABLE tm ADD COLUMN fixture_migration INTEGER NOT NULL DEFAULT 0", "PRAGMA user_version=2"} {
					if _, e := c.ExecContext(ctx, stmt); e != nil {
						return e
					}
				}
				if fail {
					_, e := c.ExecContext(ctx, "INSERT INTO nonexistent VALUES(1)")
					return e
				}
				return nil
			})
			if fail != (err != nil) {
				t.Fatalf("migration failure=%v err=%v", fail, err)
			}
			db.Close()
			reopened := connect(t, path, "rw")
			var version int
			if err := reopened.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
				t.Fatal(err)
			}
			var schema string
			if err := reopened.QueryRow("SELECT sql FROM sqlite_schema WHERE name='tm'").Scan(&schema); err != nil {
				t.Fatal(err)
			}
			if fail && (version != 1 || strings.Contains(schema, "fixture_migration")) {
				t.Fatal("partial migration")
			}
			if !fail && (version != 2 || !strings.Contains(schema, "fixture_migration")) {
				t.Fatal("incomplete migration")
			}
			if count(t, reopened) != 1 {
				t.Fatal("migration lost row")
			}
		})
	}
}

func requireBusy(t *testing.T, err error, elapsed time.Duration) {
	t.Helper()
	var se *sqlite.Error
	if !errors.As(err, &se) || se.Code()&255 != 5 {
		t.Fatalf("want SQLITE_BUSY, got %v", err)
	}
	// This is a generous regression ceiling, not a hard real-time scheduling guarantee.
	if elapsed > operationLimit+500*time.Millisecond {
		t.Fatalf("unbounded wait %s", elapsed)
	}
	t.Logf("busy_timeout=%dms elapsed=%s", busyMS, elapsed)
}
func TestWriterBusyDeadline(t *testing.T) {
	path, db := fixture(t)
	other := connect(t, path, "rw")
	holder, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err = holder.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer holder.ExecContext(context.Background(), "ROLLBACK")
	ctx, cancel := context.WithTimeout(context.Background(), operationLimit)
	defer cancel()
	start := time.Now()
	err = transaction(ctx, other, func(*sql.Conn) error { t.Fatal("work ran despite lock"); return nil })
	requireBusy(t, err, time.Since(start))
}
func TestCommitBusyDeadlineRollback(t *testing.T) {
	path, db := fixture(t)
	reader := connect(t, path, "ro")
	r, err := reader.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err = r.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = r.QueryRowContext(context.Background(), "SELECT count(*) FROM tm").Scan(&n); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationLimit)
	defer cancel()
	start := time.Now()
	err = transaction(ctx, db, func(c *sql.Conn) error {
		_, e := c.ExecContext(ctx, insertSQL, []byte("source"), []byte("profile"), "uncommitted")
		return e
	})
	requireBusy(t, err, time.Since(start))
	if _, err = r.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if count(t, db) != 0 {
		t.Fatal("failed commit persisted rows")
	}
	// A failed commit neither retries nor leaves a transaction active on the pool.
	exec(t, db, insertSQL, []byte("source"), []byte("profile"), "next explicit operation")
	if count(t, db) != 1 {
		t.Fatal("connection unusable after rollback")
	}
}
func TestOperationDeadlineAndCancelledCommit(t *testing.T) {
	_, db := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := db.ExecContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x < 1000000000) SELECT sum(x) FROM n`)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("deadline not observed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > operationLimit {
		t.Fatal("deadline delayed", elapsed)
	} else {
		t.Logf("query cancellation elapsed=%s", elapsed)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), operationLimit)
	defer cancel2()
	err = transaction(ctx2, db, func(c *sql.Conn) error {
		_, e := c.ExecContext(ctx2, insertSQL, []byte("source"), []byte("profile"), "not committed")
		cancel2()
		return e
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if count(t, db) != 0 {
		t.Fatal("cancelled operation committed")
	}
}

func TestSidecarPreserved(t *testing.T) {
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		t.Run(suffix, func(t *testing.T) {
			path, db := fixture(t)
			db.Close()
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			sidecar := []byte("unresolved recovery asset")
			if err := os.WriteFile(path+suffix, sidecar, 0600); err != nil {
				t.Fatal(err)
			}
			if err := probeExisting(t, path); err == nil {
				t.Fatal("accepted unresolved sidecar")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("changed database")
			}
			after, err = os.ReadFile(path + suffix)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(sidecar, after) {
				t.Fatal("changed sidecar")
			}
		})
	}
}

func TestCancellationDuringBusyCommit(t *testing.T) {
	path, db := fixture(t)
	reader := connect(t, path, "ro")
	r, err := reader.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err = r.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = r.QueryRowContext(context.Background(), "SELECT count(*) FROM tm").Scan(&n); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = transaction(ctx, db, func(c *sql.Conn) error {
		_, e := c.ExecContext(ctx, insertSQL, []byte("source"), []byte("profile"), "uncommitted")
		return e
	})
	elapsed := time.Since(start)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("commit ignored cancellation: %v", err)
	}
	if elapsed > operationLimit {
		t.Fatal("commit cancellation delayed", elapsed)
	}
	t.Logf("20ms context during busy COMMIT: elapsed=%s error=%v", elapsed, err)
	if _, err = r.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if count(t, db) != 0 {
		t.Fatal("cancelled commit persisted rows")
	}
}

func TestConcurrentExactKey(t *testing.T) {
	path, first := fixture(t)
	second := connect(t, path, "rw")
	ctx, cancel := context.WithTimeout(context.Background(), operationLimit)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- transaction(ctx, first, func(c *sql.Conn) error {
			_, err := c.ExecContext(ctx, insertSQL, []byte("source"), []byte("profile"), "winner")
			close(entered)
			<-release
			return err
		})
	}()
	<-entered
	started := make(chan struct{})
	otherDone := make(chan error, 1)
	go func() {
		close(started)
		otherDone <- transaction(ctx, second, func(c *sql.Conn) error {
			_, err := c.ExecContext(ctx, insertSQL, []byte("source"), []byte("profile"), "different valid translation")
			return err
		})
	}()
	<-started
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-otherDone; err != nil {
		t.Fatal(err)
	}
	var got string
	if err := first.QueryRow("SELECT translated_text FROM tm").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "winner" || count(t, first) != 1 {
		t.Fatal("lost first accepted row", got)
	}
}

func TestSmallBatchBudget(t *testing.T) {
	_, db := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), operationLimit)
	defer cancel()
	start := time.Now()
	err := transaction(ctx, db, func(c *sql.Conn) error {
		for i := 0; i < 1000; i++ {
			if _, err := c.ExecContext(ctx, insertSQL, []byte(fmt.Sprintf("fixture-unit-%04d", i)), []byte("synthetic-profile"), "検証用の訳文"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count(t, db) != 1000 {
		t.Fatal("incomplete batch")
	}
	t.Logf("1000 synthetic rows, transaction including commit elapsed=%s", time.Since(start))
}

func TestAdoptedOperationLimit(t *testing.T) {
	_, db := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), operationLimit)
	defer cancel()
	start := time.Now()
	err := transaction(ctx, db, func(c *sql.Conn) error {
		if _, err := c.ExecContext(ctx, insertSQL, []byte("source"), []byte("profile"), "must rollback"); err != nil {
			return err
		}
		_, err := c.ExecContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x < 1000000000) SELECT sum(x) FROM n`)
		return err
	})
	elapsed := time.Since(start)
	if err == nil || ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("limit not enforced: %v", err)
	}
	if elapsed > operationLimit+500*time.Millisecond {
		t.Fatal("deadline delayed", elapsed)
	}
	if count(t, db) != 0 {
		t.Fatal("expired operation left rows")
	}
	t.Logf("adopted 1s operation context: elapsed=%s", elapsed)
}
