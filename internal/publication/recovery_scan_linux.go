//go:build linux

package publication

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func (r *Run) recoverPendingRecords(ctx context.Context) (err error) {
	entries, err := r.output.parent.file.ReadDir(-1)
	if err != nil {
		return &RecoveryError{Result: Result{State: NotPublished, OutputPath: r.output.path}, Reason: "read interrupted run candidates", Cause: err}
	}
	var pending *directory
	var pendingRecord operationRecord
	var pendingResult *Result
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".yakuori-run-") {
			continue
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(r.output.parent.path, entry.Name())
		result := Result{State: NotPublished, RunID: strings.TrimPrefix(entry.Name(), ".yakuori-run-"), RecordPath: filepath.Join(path, "record.json"), OutputPath: r.output.path}
		fd, e := unix.Openat(int(r.output.parent.file.Fd()), entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if e != nil {
			return &RecoveryError{Result: result, Reason: "open interrupted run", Cause: e}
		}
		dir := &directory{file: os.NewFile(uintptr(fd), entry.Name()), path: path}
		closeCandidate := func() error {
			if e := dir.file.Close(); e != nil {
				return &RecoveryError{Result: result, Reason: "close interrupted run", Cause: e}
			}
			return nil
		}
		dir.id, e = identify(dir.file)
		if e != nil {
			return errors.Join(&RecoveryError{Result: result, Reason: "identify interrupted run", Cause: e}, closeCandidate())
		}
		record, e := readRecord(dir)
		if e != nil {
			return errors.Join(&RecoveryError{Result: result, Reason: "invalid or missing operation record", Cause: e}, closeCandidate())
		}
		result.RunID, result.OutputPath = record.RunID, record.Output.Path
		if record.Backup != nil {
			result.BackupPath = record.Backup.Path
		}
		terminal := false
		switch record.Phase {
		case publishedPhase:
			terminal, result.State = true, Published
		case restoredPhase:
			terminal, result.State = true, Restored
		case abandonedPhase:
			terminal = true
		}
		if terminal || record.Output.Basename != r.output.name {
			if e = closeCandidate(); e != nil {
				return e
			}
			continue
		}
		if !sameDirectory(record.OutputParent, r.output.parent.id) {
			return errors.Join(&RecoveryError{Result: result, Reason: "recorded output parent differs", Cause: ErrRecoveryConflict}, closeCandidate())
		}
		if record.Source.Path != r.source.path || record.Source.Basename != r.source.name || !sameDirectory(record.SourceParent, r.source.parent.id) {
			return errors.Join(&RecoveryError{Result: result, Reason: "recorded source or parent differs", Cause: ErrRecoveryConflict}, closeCandidate())
		}
		if pending != nil {
			return errors.Join(&RecoveryError{Result: result, Reason: "multiple pending records for target",
				Cause: fmt.Errorf("other run=%s record=%s: %w", pendingRecord.RunID, filepath.Join(pending.path, "record.json"), ErrRecoveryConflict)}, closeCandidate())
		}
		pending, pendingRecord = dir, record
		pendingResult = &result
		defer func() { err = errors.Join(err, closeCandidate()) }()
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if pending == nil {
		return nil
	}
	result, err := r.reconcileRecord(pendingRecord, pending)
	*pendingResult = result
	if err != nil {
		return &RecoveryError{Result: result, Reason: "reconcile interrupted publication", Cause: err}
	}
	return nil
}
