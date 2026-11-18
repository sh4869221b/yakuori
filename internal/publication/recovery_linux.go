//go:build linux

package publication

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

var ErrRecoveryConflict = errors.New("publication recovery requires manual reconciliation")

const legacyPathRecoveryReason = "ambiguous legacy path bytes, manual reconciliation required"

var errAmbiguousLegacyPath = fmt.Errorf("schema 1 cannot prove the original path: %w", ErrRecoveryConflict)

// RecoveryError separates an observed publication/restoration result from the
// failure that prevents Prepare or its caller from continuing.
type RecoveryError struct {
	Result Result
	Reason string
	Cause  error
}

func (e *RecoveryError) Error() string {
	return fmt.Sprintf("publication recovery run=%s reason=%s record=%s final=%s backup=%s: %v",
		e.Result.RunID, e.Reason, e.Result.RecordPath, e.Result.OutputPath, e.Result.BackupPath, e.Cause)
}

func (e *RecoveryError) Unwrap() error { return e.Cause }

// The caller reads the strict record first and retains both parent locks.
func (r *Run) reconcileRecord(record operationRecord, dir *directory) (result Result, err error) {
	result = Result{State: NotPublished, RunID: record.RunID, RecordPath: filepath.Join(dir.path, "record.json"), OutputPath: record.Output.Path}
	if record.Backup != nil {
		result.BackupPath = record.Backup.Path
	}
	if r.closed {
		return result, fmt.Errorf("recovery run closed: %w", ErrRecoveryConflict)
	}
	switch record.Phase {
	case publishedPhase:
		result.State = Published
		return result, nil
	case restoredPhase:
		result.State = Restored
		return result, nil
	case abandonedPhase:
		return result, nil
	}
	if record.hasLegacyPathCollision() {
		return result, errAmbiguousLegacyPath
	}
	legacyOutputMayBeLossy := record.SchemaVersion == recordUTF8Schema && strings.ContainsRune(record.Output.Basename, utf8.RuneError)
	backup, err := r.observeRecoveryBackup(record, &result)
	if err != nil {
		if legacyOutputMayBeLossy && errors.Is(err, ErrInvalidPath) {
			err = errors.Join(errAmbiguousLegacyPath, err)
		}
		return result, err
	}
	source, err := r.recoveryLeaf(recordPath{record.Source.Path, record.Source.Basename}, record.SourceParent)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, source.parent.file.Close()) }()
	final, err := r.recoveryLeaf(record.Output, record.OutputParent)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, final.parent.file.Close()) }()
	stage := leaf{parent: dir, path: record.Stage.Path, lookupPath: record.Stage.Path, name: record.Stage.Basename}
	leaves := []*leaf{&final}
	if record.Mode != InPlace {
		leaves = append(leaves, &source)
	}
	for _, l := range leaves {
		if err = observe(l); err != nil {
			return result, fmt.Errorf("observe recovery file: %w: %w", ErrRecoveryConflict, err)
		}
	}
	if record.Backup != nil {
		leaves = append(leaves, &backup)
	}
	// Schema 1 cannot distinguish a literal replacement rune from an invalid
	// byte. Require a known file at that spelling before reconciling the record.
	if legacyOutputMayBeLossy {
		existing := record.ExistingOutput.File
		if !matchesRecord(final, record.Stage) && (existing == nil || !matchesRecord(final, *existing)) {
			return result, errAmbiguousLegacyPath
		}
	}
	if err = observe(&stage); err != nil {
		return result, fmt.Errorf("observe recovery stage: %w: %w", ErrRecoveryConflict, err)
	}
	leaves = append(leaves, &stage)
	if stage.present && !matchesRecord(stage, record.Stage) {
		return result, fmt.Errorf("recovery stage differs: %w", ErrRecoveryConflict)
	}
	for i, l := range leaves {
		for _, other := range leaves[:i] {
			if l.present && other.present && l.id.Dev == other.id.Dev && l.id.Ino == other.id.Ino {
				return result, fmt.Errorf("recovery file alias: %w", ErrRecoveryConflict)
			}
		}
	}
	if record.Mode == InPlace {
		switch {
		case matchesRecord(final, record.Source) && !backup.present:
			// A failed completion write after restore has the same evidence as
			// an unmoved original. An absent stage is also safe to abandon.
			result.State = NotPublished
		case !final.present && matchesRecord(backup, record.Source):
			if _, err = filesystem(final.parent); err != nil {
				return result, fmt.Errorf("recovery filesystem: %w: %w", ErrRecoveryConflict, err)
			}
			for _, l := range leaves {
				if err = verifyPath(*l); err != nil {
					return result, fmt.Errorf("restore file recheck: %w: %w", ErrRecoveryConflict, err)
				}
			}
			if err = r.rename(int(backup.parent.file.Fd()), backup.name, int(final.parent.file.Fd()), final.name, unix.RENAME_NOREPLACE); err != nil {
				return result, fmt.Errorf("restore original: %w: %w", ErrRecoveryConflict, err)
			}
			result.State = Restored
			return result, r.completeRecoveryRecord(record, dir, result.State)
		case matchesRecord(final, record.Stage) && !stage.present && matchesRecord(backup, record.Source):
			result.State = Published
		default:
			return result, fmt.Errorf("in-place evidence conflicts: %w", ErrRecoveryConflict)
		}
	} else {
		if !matchesRecord(source, record.Source) {
			return result, fmt.Errorf("recovery source differs: %w", ErrRecoveryConflict)
		}
		switch {
		case matchesRecord(final, record.Stage) && !stage.present:
			result.State = Published
		case matchesRecord(stage, record.Stage) && ((!final.present && record.ExistingOutput.Absent) ||
			(record.ExistingOutput.File != nil && matchesRecord(final, *record.ExistingOutput.File))):
		default:
			return result, fmt.Errorf("ordinary output evidence conflicts: %w", ErrRecoveryConflict)
		}
	}
	for _, l := range leaves {
		if err = verifyPath(*l); err != nil {
			return result, fmt.Errorf("recovery file recheck: %w: %w", ErrRecoveryConflict, err)
		}
	}
	return result, r.completeRecoveryRecord(record, dir, result.State)
}

func (r *Run) lockedRecoveryParent(parent identity) (*directory, error) {
	for _, d := range r.locks {
		if sameDirectory(d.id, parent) {
			id, err := identify(d.file)
			if err != nil {
				return nil, fmt.Errorf("identify locked recovery parent: %w: %w", ErrRecoveryConflict, err)
			}
			if sameDirectory(id, parent) {
				return d, nil
			}
			break
		}
	}
	return nil, fmt.Errorf("recovery parent identity/lock differs: %w", ErrRecoveryConflict)
}

func (r *Run) observeRecoveryBackup(record operationRecord, result *Result) (leaf, error) {
	if record.Backup == nil {
		return leaf{}, nil
	}
	parent, err := r.lockedRecoveryParent(record.SourceParent)
	if err != nil {
		return leaf{}, err
	}
	// Use the held directory and the validated run ID, even when the JSON path is lossy.
	name := backupBasename(record.RunID)
	path := filepath.Join(parent.path, name)
	result.BackupPath = path
	backup := leaf{parent: parent, name: name, path: path, lookupPath: path}
	if err := observe(&backup); err != nil {
		return leaf{}, fmt.Errorf("observe recovery backup: %w: %w", ErrRecoveryConflict, err)
	}
	if matchesRecord(backup, record.Source) {
		result.State = SourceBackedUp
	}
	return backup, nil
}

func (r *Run) recoveryLeaf(path recordPath, parent identity) (leaf, error) {
	l, err := openParent(path.Path)
	if err != nil {
		return leaf{}, fmt.Errorf("recovery parent path: %w: %w", ErrRecoveryConflict, err)
	}
	if _, err = r.lockedRecoveryParent(parent); err == nil && !sameDirectory(l.parent.id, parent) {
		err = fmt.Errorf("recovery parent path identity differs: %w", ErrRecoveryConflict)
	}
	if err != nil {
		return leaf{}, errors.Join(err, l.parent.file.Close())
	}
	return l, nil
}

func matchesRecord(l leaf, f recordFile) bool {
	return l.present && l.id == f.Identity && l.digest == f.SHA256
}

func (r *Run) completeRecoveryRecord(record operationRecord, dir *directory, state State) error {
	switch state {
	case Restored:
		record.Phase, record.LastCompletedOperation = restoredPhase, "restore-rename"
	case Published:
		record.Phase, record.LastCompletedOperation = publishedPhase, "publish-rename"
	case NotPublished:
		record.Phase, record.LastCompletedOperation = abandonedPhase, "reconcile-unpublished"
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return fmt.Errorf("recovery snapshot ID: %w", err)
	}
	w := recordWriter{dir, r.recordWrite, r.recordSync, r.recordClose, r.rename}
	return w.write(record, ".record-recovery-"+hex.EncodeToString(id[:]))
}
