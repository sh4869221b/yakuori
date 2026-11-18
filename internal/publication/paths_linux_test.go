//go:build linux

package publication

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

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
