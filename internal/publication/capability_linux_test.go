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

func TestCapability(t *testing.T) {
	for _, name := range []string{"success", "unsupported", "permission-denied", "replace-failure"} {
		t.Run(name, func(t *testing.T) {
			d := publicationFixture(t)
			source, output := filepath.Join(d, "source"), filepath.Join(d, "output")
			putFile(t, source, "original")
			putFile(t, output, "existing")
			rename := renameFunc(unix.Renameat2)
			switch name {
			case "unsupported":
				rename = func(a int, b string, c int, d string, flags uint) error {
					return unix.Renameat2(a, b, c, d, 0x80000000)
				}
			case "permission-denied":
				if os.Geteuid() == 0 {
					t.Fatal("permission scenario requires non-root")
				}
				rename = func(a int, b string, c int, d string, flags uint) error {
					if err := unix.Fchmod(a, 0500); err != nil {
						return err
					}
					err := unix.Renameat2(a, b, c, d, flags)
					return errors.Join(err, unix.Fchmod(a, 0700))
				}
			case "replace-failure":
				rename = func(a int, b string, c int, d string, flags uint) error {
					if flags == 0 {
						flags = 0x80000000
					}
					return unix.Renameat2(a, b, c, d, flags)
				}
			}
			r, err := prepare(context.Background(), Options{source, output, Replace}, rename)
			if r != nil {
				closeRun(t, r)
			}
			if name == "success" && err != nil {
				t.Fatal(err)
			}
			if name != "success" && err == nil {
				t.Fatal("capability failure accepted")
			}
			if name == "permission-denied" && !errors.Is(err, unix.EACCES) {
				t.Fatalf("want EACCES: %v", err)
			}
			if name == "unsupported" && !errors.Is(err, unix.EINVAL) {
				t.Fatalf("want EINVAL: %v", err)
			}
			wantBytes(t, source, "original")
			wantBytes(t, output, "existing")
			entries, err := os.ReadDir(d)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				files, err := os.ReadDir(filepath.Join(d, entry.Name()))
				if err != nil || len(files) != 0 {
					t.Fatalf("capability cleanup: %v %v", files, err)
				}
			}
			reopened, err := Prepare(context.Background(), Options{source, output, Replace})
			if err != nil {
				t.Fatalf("capability failure leaked lock: %v", err)
			}
			closeRun(t, reopened)
		})
	}
}
