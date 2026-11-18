//go:build linux

package publication

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyPathRejectsDataPastStatSize(t *testing.T) {
	// This procfs file exposes public data even though stat reports size zero.
	const path = "/proc/sys/kernel/ostype"
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("procfs test file unavailable: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	id, err := identify(f)
	if err != nil {
		t.Fatal(err)
	}
	if id.Size != 0 || id.Nlink != 1 {
		t.Skipf("procfs fixture does not report a zero-sized single-link file: %+v", id)
	}
	var data [1]byte
	if _, err = io.ReadFull(f, data[:]); err != nil {
		t.Fatalf("procfs fixture is missing data: %v", err)
	}
	l, err := openParent(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.parent.file.Close() })
	empty := sha256.Sum256(nil)
	l.id, l.digest, l.present = id, hex.EncodeToString(empty[:]), true
	if err := verifyPath(l); !errors.Is(err, ErrChanged) || !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("zero-sized snapshot recheck: %v", err)
	}
}

func TestExistingOutputIdentityUsesBoundedMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing-output")
	putFile(t, path, "initial")
	if err := os.Truncate(path, 8<<20); err != nil {
		t.Fatal(err)
	}
	l, err := openParent(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.parent.file.Close() })
	if err := observe(&l); err != nil {
		t.Fatal(err)
	}
	result := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			if err := verifyPath(l); err != nil {
				b.Fatal(err)
			}
		}
	})
	if allocated := result.AllocedBytesPerOp(); result.N == 0 || allocated > 256<<10 {
		t.Errorf("rechecking an 8 MiB output allocated %d bytes per operation, iterations=%d", allocated, result.N)
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteAt([]byte("x"), l.id.Size-1)
	if err := errors.Join(err, f.Close()); err != nil {
		t.Fatal(err)
	}
	if err := verifyPath(l); !errors.Is(err, ErrChanged) {
		t.Fatalf("same-size output mutation: %v", err)
	}
}
