//go:build linux

package tm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

var ErrConflict = errors.New("translation candidate changed since lookup")

func (r *Run) Commit(ctx context.Context, session unit.Session, profile validate.Profile, accepted []validate.AcceptedTranslation) (err error) {
	if r.finished || r.staged == nil || !r.manifestChecked {
		return ErrRunState
	}
	r.finished = true
	if !r.session.SameImport(session) || r.profile != profile {
		return validate.ErrBindingMismatch
	}
	expected, err := artifact.PrepareExport(session, profile, accepted)
	if err != nil {
		return err
	}
	for _, staged := range r.staged {
		text, exists := expected.Text(staged.UnitID())
		if !exists || text != staged.Text() {
			return validate.ErrBindingMismatch
		}
	}
	ctx, cancel := context.WithTimeout(ctx, r.store.limits.DBOperationTimeout)
	defer cancel()
	c, err := r.store.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	if _, err = c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return errors.Join(err, ctx.Err())
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), r.store.limits.DBCleanupTimeout)
		defer cancel()
		if _, rollbackErr := c.ExecContext(cleanup, "ROLLBACK"); rollbackErr != nil {
			discardErr := c.Raw(func(any) error { return driver.ErrBadConn })
			err = errors.Join(err, fmt.Errorf("commit rollback: %w", rollbackErr), discardErr)
		}
	}()
	for _, translation := range r.staged {
		if err = r.commitRow(ctx, c, translation); err != nil {
			return errors.Join(err, ctx.Err())
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if _, err = c.ExecContext(ctx, "COMMIT"); err != nil {
		return errors.Join(err, ctx.Err())
	}
	committed = true
	return nil
}

func (r *Run) commitRow(ctx context.Context, c *sql.Conn, translation validate.AcceptedTranslation) error {
	id := translation.UnitID()
	key, observed := r.keys[id], r.observed[id]
	args := []any{key.Schema(), key.SourceBytes(), key.ProfileBytes()}
	if observed.found && observed.valid {
		var current string
		err := c.QueryRowContext(ctx, `SELECT translated_text FROM tm WHERE key_schema=? AND source_identity=? AND translation_profile=?`, args...).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if current != observed.text {
			return ErrConflict
		}
		return nil
	}
	var result sql.Result
	var err error
	if observed.found {
		args = append([]any{translation.Text()}, args...)
		args = append(args, observed.text)
		result, err = c.ExecContext(ctx, `UPDATE tm SET translated_text=? WHERE key_schema=? AND source_identity=? AND translation_profile=? AND CAST(translated_text AS BLOB)=CAST(? AS BLOB)`, args...)
	} else {
		args = append(args, translation.Text())
		result, err = c.ExecContext(ctx, `INSERT INTO tm VALUES(?,?,?,?) ON CONFLICT(key_schema, source_identity, translation_profile) DO NOTHING`, args...)
	}
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}
