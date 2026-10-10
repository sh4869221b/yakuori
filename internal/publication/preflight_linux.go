//go:build linux

package publication

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/sh4869221b/yakuori/internal/config"
	"golang.org/x/sys/unix"
)

// Run owns its opened paths and locks. Methods must be called sequentially.
type Run struct {
	mode             Mode
	source           leaf
	output           leaf
	snapshot         []byte
	locks            []*directory
	runDir           *directory
	runID            string
	closed           bool
	stage            leaf
	stageAttempted   bool
	validated        bool
	syncStage        func(*os.File) error
	closeStage       func(*os.File) error
	publishAttempted bool
	result           Result
	rename           renameFunc
	recordWrite      func(*os.File, []byte) (int, error)
	recordSync       func(*os.File) error
	recordClose      func(*os.File) error
}

// Prepare recovers interrupted publication before taking the source snapshot.
// It must complete before generation/import.
func Prepare(ctx context.Context, options Options) (*Run, error) {
	return PrepareWithLimits(ctx, options, config.DefaultLimits())
}

// PrepareWithLimits bounds the source snapshot; existing output observations are unchanged.
func PrepareWithLimits(ctx context.Context, options Options, limits config.Limits) (*Run, error) {
	if err := limits.Check(); err != nil {
		return nil, err
	}
	return prepareWithLimits(ctx, options, unix.Renameat2, limits)
}

func prepare(ctx context.Context, options Options, rename renameFunc) (*Run, error) {
	return prepareWithLimits(ctx, options, rename, config.DefaultLimits())
}

func prepareWithLimits(ctx context.Context, options Options, rename renameFunc, limits config.Limits) (_ *Run, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch options.Mode {
	case Create, Replace, InPlace:
	default:
		return nil, ErrInvalidMode
	}
	source, err := openParent(options.Source)
	if err != nil {
		return nil, fmt.Errorf("source parent: %w", err)
	}
	r := &Run{mode: options.Mode, source: source, syncStage: (*os.File).Sync, closeStage: (*os.File).Close}
	r.rename, r.recordWrite, r.recordSync, r.recordClose = rename, (*os.File).Write, (*os.File).Sync, (*os.File).Close
	defer func() {
		if err != nil {
			err = errors.Join(err, r.Close())
		}
	}()
	output, err := openParent(options.Output)
	if err != nil {
		return nil, fmt.Errorf("output parent: %w", err)
	}
	r.output = output
	if err = r.lockDirectories(); err != nil {
		return nil, err
	}
	if _, err = filesystem(r.output.parent); err != nil {
		return nil, err
	}
	sameParent := source.parent.id.Dev == output.parent.id.Dev && source.parent.id.Ino == output.parent.id.Ino && source.parent.id.MountID == output.parent.id.MountID
	samePath := sameParent && source.name == output.name
	if r.mode == InPlace && !samePath {
		return nil, ErrAlias
	}
	if r.mode != InPlace && samePath {
		return nil, ErrAlias
	}
	if err = r.recoverPendingRecords(ctx); err != nil {
		return nil, err
	}
	r.snapshot, err = observeLimit(&r.source, limits.ArtifactBytes)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	if !r.source.present {
		return nil, fmt.Errorf("source absent: %w", os.ErrNotExist)
	}
	switch r.mode {
	case InPlace:
		r.output.id, r.output.digest, r.output.present = r.source.id, r.source.digest, true
	case Create, Replace:
		if _, err = observe(&r.output); err != nil {
			return nil, fmt.Errorf("output: %w", err)
		}
		if r.output.present && r.source.id.Dev == r.output.id.Dev && r.source.id.Ino == r.output.id.Ino {
			return nil, ErrAlias
		}
		if r.mode == Create && r.output.present {
			return nil, fmt.Errorf("output exists: %w", os.ErrExist)
		}
	}
	if r.output.present && r.output.id.MountID != r.output.parent.id.MountID {
		return nil, fmt.Errorf("output leaf crosses mount: %w", unix.EXDEV)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = r.createRunDirectory(); err != nil {
		return nil, err
	}
	if err = checkCapability(r.runDir.file, rename); err != nil {
		return nil, fmt.Errorf("destination capability: %w: %w", ErrCapability, err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Run) lockDirectories() error {
	dirs := []*directory{r.source.parent, r.output.parent}
	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i].id.Dev != dirs[j].id.Dev {
			return dirs[i].id.Dev < dirs[j].id.Dev
		}
		return dirs[i].id.Ino < dirs[j].id.Ino
	})
	for i, d := range dirs {
		if i > 0 && d.id.Dev == dirs[i-1].id.Dev && d.id.Ino == dirs[i-1].id.Ino {
			continue
		}
		if err := unix.Flock(int(d.file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			if errors.Is(err, unix.EWOULDBLOCK) {
				return fmt.Errorf("lock directory: %w: %w", ErrBusy, err)
			}
			return fmt.Errorf("lock directory: %w", err)
		}
		r.locks = append(r.locks, d)
	}
	return nil
}

func (r *Run) createRunDirectory() error {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return fmt.Errorf("run ID: %w", err)
	}
	r.runID = hex.EncodeToString(id[:])
	name := ".yakuori-run-" + r.runID
	parent := r.output.parent
	if err := unix.Mkdirat(int(parent.file.Fd()), name, 0700); err != nil {
		return fmt.Errorf("create private run: %w", err)
	}
	fd, err := unix.Openat(int(parent.file.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open private run: %w", err)
	}
	f := os.NewFile(uintptr(fd), name)
	r.runDir = &directory{file: f, path: filepath.Join(parent.path, name)}
	r.runDir.id, err = identify(f)
	if err != nil {
		return err
	}
	if r.runDir.id.MountID != parent.id.MountID {
		return fmt.Errorf("private run crosses mount: %w", unix.EXDEV)
	}
	return nil
}

// SourceSnapshot returns a copy of the bytes retained by Prepare.
func (r *Run) SourceSnapshot() []byte { return append([]byte(nil), r.snapshot...) }

// Close releases descriptors/locks. It never deletes run, stage, record or backup
// evidence, nor source/output. Only capability's own disposable files are removed.
func (r *Run) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	var err error
	if r.runDir != nil {
		err = errors.Join(err, r.runDir.file.Close())
	}
	// Close each opened description, including an unlocked duplicate parent.
	if r.output.parent != nil {
		err = errors.Join(err, r.output.parent.file.Close())
	}
	if r.source.parent != nil {
		err = errors.Join(err, r.source.parent.file.Close())
	}
	return err
}
