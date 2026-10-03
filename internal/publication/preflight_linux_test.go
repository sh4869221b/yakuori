//go:build linux

package publication

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"golang.org/x/sys/unix"
)

func publicationFixture(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	l, err := openParent(filepath.Join(d, "source"))
	if err != nil {
		t.Fatal(err)
	}
	_, fsErr := filesystem(l.parent)
	if err := l.parent.file.Close(); err != nil {
		t.Fatal(err)
	}
	if errors.Is(fsErr, ErrUnsupportedFilesystem) {
		t.Skip("success scenarios require qualified ext4/Btrfs TMPDIR")
	}
	if fsErr != nil {
		t.Fatal(fsErr)
	}
	return d
}

func putFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func wantBytes(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil || string(b) != want {
		t.Fatalf("bytes %s = %q, %v; want %q", path, b, err, want)
	}
}

func wantAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path must be absent: %s: %v", path, err)
	}
}

func closeRun(t *testing.T, r *Run) {
	t.Helper()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPreflight(t *testing.T) {
	for _, mode := range []Mode{Create, Replace, InPlace} {
		t.Run(string(mode), func(t *testing.T) {
			d := publicationFixture(t)
			source, output := filepath.Join(d, "source"), filepath.Join(d, "output")
			putFile(t, source, "original")
			if mode == Replace {
				putFile(t, output, "existing")
			}
			if mode == InPlace {
				output = source
			}
			r, err := Prepare(context.Background(), Options{source, output, mode})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeRun(t, r) })
			if string(r.SourceSnapshot()) != "original" {
				t.Fatal("snapshot differs from source")
			}
			copy := r.SourceSnapshot()
			copy[0] = 'X'
			if string(r.SourceSnapshot()) != "original" {
				t.Fatal("caller modified retained snapshot")
			}
			if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(r.runID) {
				t.Fatalf("invalid run ID %q", r.runID)
			}
			st, err := os.Stat(r.runDir.path)
			if err != nil || st.Mode().Perm() != 0700 {
				t.Fatalf("private run mode: %v %v", st, err)
			}
			entries, err := os.ReadDir(r.runDir.path)
			if err != nil || len(entries) != 0 {
				t.Fatalf("capability left files: %v %v", entries, err)
			}
			wantBytes(t, source, "original")
			if mode == Replace {
				wantBytes(t, output, "existing")
			}
			if mode == Create {
				wantAbsent(t, output)
			}
		})
	}
	for _, name := range []string{"replace-absent", "source-on-another-mount"} {
		t.Run(name, func(t *testing.T) {
			d := publicationFixture(t)
			source, output := filepath.Join(d, "source"), filepath.Join(d, "output")
			if name == "source-on-another-mount" {
				other, err := os.MkdirTemp("/tmp", "yakuori-source-")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.RemoveAll(other); err != nil {
						t.Error(err)
					}
				})
				source = filepath.Join(other, "source")
			}
			putFile(t, source, "original")
			r, err := Prepare(context.Background(), Options{source, output, Replace})
			if err != nil {
				t.Fatal(err)
			}
			defer closeRun(t, r)
			if name == "source-on-another-mount" && r.source.id.MountID == r.output.parent.id.MountID {
				t.Skip("source and output fixtures share a mount; cross-mount source scenario unverified here")
			}
			wantBytes(t, source, "original")
			wantAbsent(t, output)
		})
	}
	t.Run("refusal", testPreflightRefusal)
}

func testPreflightRefusal(t *testing.T) {
	for _, name := range []string{"source-symlink", "output-symlink", "dangling-output", "symlink-component", "symlink-before-dotdot", "source-hardlink", "output-hardlink", "same-path", "same-inode-alias", "in-place-different", "directory-leaf", "fifo-leaf", "existing-create", "missing-source", "invalid-mode", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			d := publicationFixture(t)
			source, output := filepath.Join(d, "source"), filepath.Join(d, "output")
			putFile(t, source, "original")
			putFile(t, output, "existing")
			opts := Options{source, output, Replace}
			ctx := context.Background()
			var fixtureErr error
			switch name {
			case "source-symlink":
				opts.Source = filepath.Join(d, "link")
				fixtureErr = os.Symlink(source, opts.Source)
			case "output-symlink":
				opts.Output = filepath.Join(d, "link")
				fixtureErr = os.Symlink(output, opts.Output)
			case "dangling-output":
				opts.Output = filepath.Join(d, "link")
				fixtureErr = os.Symlink(filepath.Join(d, "absent"), opts.Output)
			case "symlink-component", "symlink-before-dotdot":
				fixtureErr = os.Symlink(d, filepath.Join(d, "link"))
				opts.Output = d + "/link/output"
				if name == "symlink-before-dotdot" {
					opts.Output = d + "/link/../output"
				}
			case "source-hardlink":
				fixtureErr = os.Link(source, filepath.Join(d, "alias"))
			case "output-hardlink":
				fixtureErr = os.Link(output, filepath.Join(d, "alias"))
			case "same-path":
				opts.Output = source
			case "same-inode-alias":
				opts.Output = d + "/./source"
			case "in-place-different":
				opts.Mode = InPlace
			case "directory-leaf":
				opts.Output = filepath.Join(d, "directory")
				fixtureErr = os.Mkdir(opts.Output, 0700)
			case "fifo-leaf":
				opts.Output = filepath.Join(d, "fifo")
				fixtureErr = unix.Mkfifo(opts.Output, 0600)
			case "existing-create":
				opts.Mode = Create
			case "missing-source":
				opts.Source = filepath.Join(d, "absent")
			case "invalid-mode":
				opts.Mode = Mode("bad")
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if fixtureErr != nil {
				t.Fatal(fixtureErr)
			}
			r, err := Prepare(ctx, opts)
			if err == nil {
				closeRun(t, r)
				t.Fatal("unsafe path accepted")
			}
			wantBytes(t, source, "original")
			wantBytes(t, output, "existing")
			entries, err := os.ReadDir(d)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if regexp.MustCompile(`^\.yakuori-run-`).MatchString(entry.Name()) {
					t.Fatal("refusal created run")
				}
			}
		})
	}
}

func TestUnsupportedFilesystem(t *testing.T) {
	d := t.TempDir()
	source, output := filepath.Join(d, "source"), filepath.Join(d, "output")
	putFile(t, source, "original")
	putFile(t, output, "existing")
	l, err := openParent(output)
	if err != nil {
		t.Fatal(err)
	}
	_, fsErr := filesystem(l.parent)
	if err := l.parent.file.Close(); err != nil {
		t.Fatal(err)
	}
	if fsErr == nil {
		t.Skip("refusal scenario requires unqualified TMPDIR")
	}
	r, err := Prepare(context.Background(), Options{source, output, Replace})
	if !errors.Is(err, ErrUnsupportedFilesystem) {
		if r != nil {
			closeRun(t, r)
		}
		t.Fatalf("want unsupported filesystem: %v", err)
	}
	wantBytes(t, source, "original")
	wantBytes(t, output, "existing")
	entries, err := os.ReadDir(d)
	if err != nil || len(entries) != 2 {
		t.Fatalf("refusal created artifacts: %v %v", entries, err)
	}
}
