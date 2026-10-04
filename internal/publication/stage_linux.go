//go:build linux

package publication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var ErrInvalidStage = errors.New("publication stage unavailable")

type stageWriter struct {
	file *os.File
	err  error
}

func (w *stageWriter) Write(b []byte) (int, error) {
	if w.file == nil {
		return 0, os.ErrClosed
	}
	n, err := w.file.Write(b)
	if err != nil {
		w.err = errors.Join(w.err, err)
	}
	return n, err
}

// Stage calls callbacks synchronously; they must not retain or share the
// writer/reader. The write descriptor is closed before check sees the stage.
func (r *Run) Stage(ctx context.Context, produce func(io.Writer) error, check func(io.Reader) error) (err error) {
	if r == nil || r.closed || r.runDir == nil || r.stageAttempted || r.publishAttempted {
		return ErrInvalidStage
	}
	r.stageAttempted = true
	if produce == nil || check == nil {
		return ErrInvalidStage
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.stage = leaf{parent: r.runDir, name: "stage", path: filepath.Join(r.runDir.path, "stage")}
	fd, err := unix.Openat(int(r.runDir.file.Fd()), r.stage.name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return fmt.Errorf("create stage: %w", err)
	}
	f := os.NewFile(uintptr(fd), r.stage.name)
	w := &stageWriter{file: f}
	defer func() {
		w.file = nil
		if f != nil {
			err = errors.Join(err, r.closeStage(f))
		}
	}()
	err = produce(w)
	w.file = nil
	err = errors.Join(err, w.err, ctx.Err())
	if err == nil {
		err = r.syncStage(f)
	}
	if err == nil {
		r.stage.id, err = identify(f)
	}
	err = errors.Join(err, r.closeStage(f))
	f = nil
	if err != nil {
		return fmt.Errorf("write/close stage: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = r.validateStage(ctx, check); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	r.validated = true
	return nil
}

func (r *Run) validateStage(ctx context.Context, check func(io.Reader) error) (err error) {
	fd, err := unix.Openat(int(r.runDir.file.Fd()), r.stage.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open closed stage: %w", err)
	}
	f := os.NewFile(uintptr(fd), r.stage.name)
	defer func() {
		if f != nil {
			err = errors.Join(err, f.Close())
		}
	}()
	id, err := identify(f)
	if err != nil {
		return err
	}
	if id != r.stage.id || id.Mode&unix.S_IFMT != unix.S_IFREG || id.Nlink != 1 || id.MountID != r.output.parent.id.MountID {
		return fmt.Errorf("stage identity changed: %w", ErrInvalidStage)
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return fmt.Errorf("read closed stage: %w", err)
	}
	r.stage.digest = hex.EncodeToString(h.Sum(nil))
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind closed stage: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = check(struct{ io.Reader }{f}); err != nil {
		return fmt.Errorf("validate stage: %w", err)
	}
	after, err := identify(f)
	if err != nil {
		return err
	}
	if after != id {
		return fmt.Errorf("stage changed during validation: %w", ErrInvalidStage)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	err = f.Close()
	f = nil
	if err != nil {
		return fmt.Errorf("close stage reader: %w", err)
	}
	r.stage.present = true
	return nil
}
