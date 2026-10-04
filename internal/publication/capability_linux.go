//go:build linux

package publication

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

type renameFunc func(int, string, int, string, uint) error

// All names are disposable files exclusively owned inside the private run.
func checkCapability(dir *os.File, rename renameFunc) (err error) {
	fd := int(dir.Fd())
	names := []string{"cap-source", "cap-target", "cap-new"}
	defer func() {
		for _, name := range names {
			e := unix.Unlinkat(fd, name, 0)
			if e != nil && !errors.Is(e, unix.ENOENT) {
				err = errors.Join(err, fmt.Errorf("cleanup capability: %w", e))
			}
		}
	}()
	for _, item := range []struct{ name, content string }{{names[0], "new"}, {names[1], "old"}} {
		f, e := openCapability(dir, item.name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
		if e != nil {
			return e
		}
		_, writeErr := io.WriteString(f, item.content)
		if e = errors.Join(writeErr, f.Close()); e != nil {
			return fmt.Errorf("write capability: %w", e)
		}
	}
	if e := rename(fd, names[0], fd, names[1], unix.RENAME_NOREPLACE); !errors.Is(e, unix.EEXIST) {
		return fmt.Errorf("no-replace collision: %w", errors.Join(ErrCapability, e))
	}
	for _, item := range []struct{ name, content string }{{names[0], "new"}, {names[1], "old"}} {
		if err = capabilityBytes(dir, item.name, item.content); err != nil {
			return err
		}
	}
	if err = rename(fd, names[0], fd, names[2], unix.RENAME_NOREPLACE); err != nil {
		return fmt.Errorf("no-replace creation: %w", err)
	}
	if err = capabilityBytes(dir, names[2], "new"); err != nil {
		return err
	}
	old, err := openCapability(dir, names[1], unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, old.Close()) }()
	if err = rename(fd, names[2], fd, names[1], 0); err != nil {
		return fmt.Errorf("atomic replace: %w", err)
	}
	b, err := io.ReadAll(old)
	if err != nil {
		return fmt.Errorf("old capability reader: %w", err)
	}
	if !bytes.Equal(b, []byte("old")) {
		return fmt.Errorf("old reader changed: %w", ErrCapability)
	}
	return capabilityBytes(dir, names[1], "new")
}

func openCapability(dir *os.File, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open capability: %w", err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

func capabilityBytes(dir *os.File, name, want string) (err error) {
	f, err := openCapability(dir, name, unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	b, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("read capability: %w", err)
	}
	if !bytes.Equal(b, []byte(want)) {
		return fmt.Errorf("capability bytes changed: %w", ErrCapability)
	}
	return nil
}
