//go:build linux

package publication

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type State string

const (
	NotPublished   State = "not-published"
	SourceBackedUp State = "source-backed-up"
	Published      State = "published"
	Restored       State = "restored"
)

// SourceBackedUp requires the caller to reconcile/restore through recovery before
// ending its command. This primitive retains evidence and performs no recovery.
type Result struct {
	State      State
	RunID      string
	RecordPath string
	OutputPath string
	BackupPath string
}

var ErrInvalidPublish = errors.New("publication run cannot publish")

// Publish is called only after the caller's successful TM commit. It does not
// perform that commit. A successful stage rename fixes Published even on error.
func (r *Run) Publish(ctx context.Context) (result Result, err error) {
	result.State = NotPublished
	if r == nil {
		return result, ErrInvalidPublish
	}
	if r.result.State != "" {
		result = r.result
	} else if r.runDir != nil {
		result.RunID, result.RecordPath, result.OutputPath = r.runID, filepath.Join(r.runDir.path, "record.json"), r.output.path
		if r.mode == InPlace {
			result.BackupPath = r.backupPath()
		}
	}
	if r.closed || r.runDir == nil || r.publishAttempted {
		return result, ErrInvalidPublish
	}
	r.publishAttempted = true
	defer func() { r.result = result }()
	if !r.validated {
		return result, ErrInvalidPublish
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = r.revalidate(NotPublished); err != nil {
		return result, err
	}
	if r.mode == InPlace {
		if err = r.backupAvailable(); err != nil {
			return result, err
		}
	}
	if err = r.writeRecord(preparedPhase, "none"); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	// Record I/O separates checks from real renames, so recheck after it.
	if err = r.revalidate(NotPublished); err != nil {
		return result, err
	}
	if r.mode == InPlace {
		fd := int(r.source.parent.file.Fd())
		if err = r.rename(fd, r.source.name, fd, r.backupName(), unix.RENAME_NOREPLACE); err != nil {
			return result, fmt.Errorf("backup original: %w", err)
		}
		result.State = SourceBackedUp
		if err = errors.Join(r.writeRecord(backedUpPhase, "backup-rename"), ctx.Err()); err != nil {
			return result, err
		}
		if err = r.revalidate(SourceBackedUp); err != nil {
			return result, err
		}
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	flags := uint(unix.RENAME_NOREPLACE)
	if r.mode == Replace && r.output.present {
		flags = 0
	}
	if err = r.rename(int(r.runDir.file.Fd()), r.stage.name, int(r.output.parent.file.Fd()), r.output.name, flags); err != nil {
		return result, fmt.Errorf("publish stage: %w", err)
	}
	result.State = Published
	err = errors.Join(r.writeRecord(publishedPhase, "publish-rename"), ctx.Err())
	return result, err
}

func (r *Run) backupAvailable() error {
	var st unix.Stat_t
	err := unix.Fstatat(int(r.source.parent.file.Fd()), r.backupName(), &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("backup exists: %w", unix.EEXIST)
	}
	return fmt.Errorf("inspect backup: %w", err)
}
