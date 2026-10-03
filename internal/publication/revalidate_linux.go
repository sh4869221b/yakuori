//go:build linux

package publication

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var ErrChanged = errors.New("publication path or bytes changed")

func sameDirectory(a, b identity) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.MountID == b.MountID && a.Mode == b.Mode
}

func verifyLeaf(want leaf, currentParent *directory) error {
	for _, parent := range []*directory{want.parent, currentParent} {
		current := leaf{parent: parent, name: want.name}
		if _, err := observe(&current); err != nil {
			return fmt.Errorf("observe current leaf: %w: %w", ErrChanged, err)
		}
		if current.present != want.present || current.id != want.id || current.digest != want.digest {
			return ErrChanged
		}
	}
	return nil
}

func verifyPath(want leaf) (err error) {
	id, err := identify(want.parent.file)
	if err != nil {
		return fmt.Errorf("retained parent: %w", err)
	}
	if !sameDirectory(id, want.parent.id) {
		return ErrChanged
	}
	current, err := openParent(want.lookupPath)
	if err != nil {
		return fmt.Errorf("current parent path: %w: %w", ErrChanged, err)
	}
	defer func() { err = errors.Join(err, current.parent.file.Close()) }()
	if !sameDirectory(current.parent.id, want.parent.id) {
		return ErrChanged
	}
	return verifyLeaf(want, current.parent)
}

func (r *Run) revalidate(state State) (err error) {
	source, output := r.source, r.output
	if state == SourceBackedUp {
		source.name = r.backupName()
		output.present, output.id, output.digest = false, identity{}, ""
	}
	if err = verifyPath(source); err != nil {
		return fmt.Errorf("source recheck: %w", err)
	}
	if err = verifyPath(output); err != nil {
		return fmt.Errorf("output recheck: %w", err)
	}
	id, err := identify(r.runDir.file)
	if err != nil {
		return fmt.Errorf("retained run directory: %w", err)
	}
	if !sameDirectory(id, r.runDir.id) {
		return ErrChanged
	}
	fd, err := unix.Openat(int(r.output.parent.file.Fd()), filepath.Base(r.runDir.path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("current run directory: %w: %w", ErrChanged, err)
	}
	f := os.NewFile(uintptr(fd), "private run")
	defer func() { err = errors.Join(err, f.Close()) }()
	id, err = identify(f)
	if err != nil {
		return err
	}
	if !sameDirectory(id, r.runDir.id) {
		return ErrChanged
	}
	return verifyLeaf(r.stage, &directory{file: f, id: id, path: r.runDir.path})
}
