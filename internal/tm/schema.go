//go:build linux

package tm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
)

const applicationID = 1497451343
const tableSQL = `CREATE TABLE tm (
 key_schema INTEGER NOT NULL CHECK(key_schema = 1),
 source_identity BLOB NOT NULL CHECK(length(source_identity) > 0),
 translation_profile BLOB NOT NULL CHECK(length(translation_profile) > 0),
 translated_text TEXT NOT NULL,
 PRIMARY KEY(key_schema, source_identity, translation_profile)
) STRICT, WITHOUT ROWID`

func initialize(ctx context.Context, s *Store) (err error) {
	c, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	if _, err = c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), s.limits.DBCleanupTimeout)
		defer cancel()
		if _, rollbackErr := c.ExecContext(cleanup, "ROLLBACK"); rollbackErr != nil {
			discardErr := c.Raw(func(any) error { return driver.ErrBadConn })
			err = errors.Join(err, fmt.Errorf("initialization rollback: %w", rollbackErr), discardErr)
		}
	}()
	for _, statement := range []string{tableSQL, "PRAGMA application_id=1497451343", "PRAGMA user_version=1"} {
		if _, err = c.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err = c.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func inspectReadOnly(ctx context.Context, path string) (err error) {
	if err := rollbackHeader(path); err != nil {
		return err
	}
	db, err := connect(path, "ro")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	return inspect(ctx, db)
}

// Even a read-only SQLite WAL connection can create -wal/-shm. Reject its
// file-format journal-mode flags before asking SQLite to inspect the database.
func rollbackHeader(path string) (err error) {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	var header [20]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return err
	}
	if header[18] != 1 || header[19] != 1 {
		return fmt.Errorf("%w: file journal mode", ErrUnsupportedStore)
	}
	return nil
}

// Inspect through the same reserved connection that will return to the Store's one-connection pool.
func inspect(ctx context.Context, db *sql.DB) (err error) {
	c, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	var app, version, objects int
	if err := c.QueryRowContext(ctx, "PRAGMA application_id").Scan(&app); err != nil {
		return err
	}
	if err := c.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if app != applicationID || version != 1 {
		return fmt.Errorf("%w: application/schema %d/%d", ErrUnsupportedStore, app, version)
	}
	var schema, mode string
	if err := c.QueryRowContext(ctx, "SELECT sql FROM sqlite_schema WHERE type='table' AND name='tm'").Scan(&schema); err != nil {
		return err
	}
	if schema != tableSQL {
		return fmt.Errorf("%w: schema layout", ErrUnsupportedStore)
	}
	if err := c.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT GLOB 'sqlite_*'").Scan(&objects); err != nil {
		return err
	}
	if objects != 1 {
		return fmt.Errorf("%w: additional schema objects", ErrUnsupportedStore)
	}
	if err := c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return err
	}
	if mode != "delete" {
		return fmt.Errorf("%w: journal mode %s", ErrUnsupportedStore, mode)
	}
	rows, err := c.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	checks := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return err
		}
		if result != "ok" {
			return fmt.Errorf("store integrity: %s", result)
		}
		checks++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if checks != 1 {
		return errors.New("missing store integrity result")
	}
	return ctx.Err()
}
