//go:build linux

package publication

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDirectoryLocks(t *testing.T) {
	t.Run("stable-order-and-close", func(t *testing.T) {
		d := publicationFixture(t)
		other := filepath.Join(d, "other")
		if err := os.Mkdir(other, 0700); err != nil {
			t.Fatal(err)
		}
		source, output := filepath.Join(d, "source"), filepath.Join(other, "output")
		putFile(t, source, "original")
		putFile(t, output, "existing")
		r, err := Prepare(context.Background(), Options{source, output, Replace})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { closeRun(t, r) })
		if len(r.locks) != 2 {
			t.Fatalf("want two directory locks, got %d", len(r.locks))
		}
		a, b := r.locks[0].id, r.locks[1].id
		if a.Dev > b.Dev || (a.Dev == b.Dev && a.Ino >= b.Ino) {
			t.Fatal("directory locks not ordered by device/inode")
		}
		second, err := Prepare(context.Background(), Options{output, source, Replace})
		if second != nil {
			closeRun(t, second)
		}
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("second request must be busy: %v", err)
		}
		closeRun(t, r)
		reopened, err := Prepare(context.Background(), Options{output, source, Replace})
		if err != nil {
			t.Fatalf("close did not release locks: %v", err)
		}
		closeRun(t, reopened)
		wantBytes(t, source, "original")
		wantBytes(t, output, "existing")
	})
	t.Run("partial-acquisition-released", func(t *testing.T) {
		d := publicationFixture(t)
		other := filepath.Join(d, "other")
		if err := os.Mkdir(other, 0700); err != nil {
			t.Fatal(err)
		}
		a, err := openParent(filepath.Join(d, "file"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := a.parent.file.Close(); err != nil {
				t.Error(err)
			}
		}()
		b, err := openParent(filepath.Join(other, "file"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := b.parent.file.Close(); err != nil {
				t.Error(err)
			}
		}()
		low, high := a, b
		if low.parent.id.Ino > high.parent.id.Ino {
			low, high = high, low
		}
		putFile(t, low.path, "low")
		putFile(t, high.path, "high")
		if err := unix.Flock(int(high.parent.file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		r, err := Prepare(context.Background(), Options{low.path, high.path, Replace})
		if r != nil {
			closeRun(t, r)
		}
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("want busy: %v", err)
		}
		if err := unix.Flock(int(low.parent.file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			t.Fatalf("partial lock leaked: %v", err)
		}
		wantBytes(t, low.path, "low")
		wantBytes(t, high.path, "high")
	})
	t.Run("deduplicated-parent-and-failed-preflight", func(t *testing.T) {
		d := publicationFixture(t)
		source, output := filepath.Join(d, "source"), filepath.Join(d, "output")
		putFile(t, source, "original")
		putFile(t, output, "existing")
		if r, err := Prepare(context.Background(), Options{source, output, Create}); err == nil {
			closeRun(t, r)
			t.Fatal("create accepted existing output")
		}
		r, err := Prepare(context.Background(), Options{source, output, Replace})
		if err != nil {
			t.Fatalf("failed preflight left lock: %v", err)
		}
		defer closeRun(t, r)
		if len(r.locks) != 1 {
			t.Fatalf("duplicate lock, got %d", len(r.locks))
		}
		wantBytes(t, source, "original")
		wantBytes(t, output, "existing")
	})
}
