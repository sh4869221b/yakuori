//go:build linux && (amd64 || arm64)

package publication

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func fixture(t *testing.T) (string, *os.File) {
	t.Helper()
	d := t.TempDir()
	f, e := os.Open(d)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.Close() })
	return d, f
}
func put(t *testing.T, d, n, s string) {
	t.Helper()
	if e := os.WriteFile(filepath.Join(d, n), []byte(s), 0600); e != nil {
		t.Fatal(e)
	}
}
func content(t *testing.T, d, n, want string) {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(d, n))
	if e != nil || string(b) != want {
		t.Fatalf("%s: %q %v want %q", n, b, e, want)
	}
}
func absent(t *testing.T, d, n string) {
	t.Helper()
	_, e := os.Lstat(filepath.Join(d, n))
	if !os.IsNotExist(e) {
		t.Fatalf("%s must be absent: %v", n, e)
	}
}

func TestNoReplaceAndBackupCollision(t *testing.T) {
	d, f := fixture(t)
	put(t, d, "stage", "translated")
	put(t, d, "final", "existing")
	if e := rename(f, "stage", "final", 1); !errors.Is(e, syscall.EEXIST) {
		t.Fatalf("got %v", e)
	}
	content(t, d, "final", "existing")
	content(t, d, "stage", "translated")
	put(t, d, "original", "source")
	put(t, d, "backup", "previous-backup")
	if e := rename(f, "original", "backup", 1); !errors.Is(e, syscall.EEXIST) {
		t.Fatalf("got %v", e)
	}
	content(t, d, "original", "source")
	content(t, d, "backup", "previous-backup")
	if e := rename(f, "stage", "new", 1); e != nil {
		t.Fatal(e)
	}
	content(t, d, "new", "translated")
	absent(t, d, "stage")
}
func TestAtomicReplaceOpenReader(t *testing.T) {
	d, f := fixture(t)
	put(t, d, "stage", "new")
	put(t, d, "final", "old")
	old, e := os.Open(filepath.Join(d, "final"))
	if e != nil {
		t.Fatal(e)
	}
	defer old.Close()
	if e = rename(f, "stage", "final", 0); e != nil {
		t.Fatal(e)
	}
	content(t, d, "final", "new")
	b := make([]byte, 3)
	if _, e = old.Read(b); e != nil || string(b) != "old" {
		t.Fatalf("old reader %q %v", b, e)
	}
}
func TestConcurrentNoReplace(t *testing.T) {
	d, f := fixture(t)
	const n = 16
	for i := 0; i < n; i++ {
		put(t, d, fmt.Sprint(i), fmt.Sprint(i))
	}
	start := make(chan struct{})
	results := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); <-start; results <- rename(f, fmt.Sprint(i), "final", 1) }(i)
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for e := range results {
		if e == nil {
			wins++
		} else if !errors.Is(e, syscall.EEXIST) {
			t.Fatal(e)
		}
	}
	if wins != 1 {
		t.Fatalf("winners %d", wins)
	}
}
func TestAliasAndChangedSource(t *testing.T) {
	d, _ := fixture(t)
	put(t, d, "source", "original")
	p := filepath.Join(d, "source")
	before, e := observe(p)
	if e != nil {
		t.Fatal(e)
	}
	put(t, d, "source", "modified")
	after, e := observe(p)
	if e != nil || before == after {
		t.Fatalf("mutation not detected: %v", e)
	}
	if e = os.Symlink(p, filepath.Join(d, "sym")); e != nil {
		t.Fatal(e)
	}
	if _, e = observe(filepath.Join(d, "sym")); e == nil {
		t.Fatal("symlink accepted")
	}
	if e = os.Link(p, filepath.Join(d, "hard")); e != nil {
		t.Fatal(e)
	}
	a, _ := os.Stat(p)
	b, _ := os.Stat(filepath.Join(d, "hard"))
	if !os.SameFile(a, b) {
		t.Fatal("same inode not detected")
	}
	if _, e = observe(p); e == nil {
		t.Fatal("hardlinked source accepted")
	}
}
func TestRecoveryTable(t *testing.T) {
	for _, tc := range []struct{ name, original, stage, final, backup, left, want string }{
		{"before-backup", "o", "s", "o", "", "s", "unchanged"},
		{"between-renames", "o", "s", "", "o", "s", "restore-no-replace"},
		{"after-publish", "o", "s", "s", "o", "", "published-keep-backup"},
		{"other-writer", "o", "s", "other", "o", "s", "manual-no-mutation"},
		{"bad-backup", "o", "s", "", "bad", "s", "manual-no-mutation"},
		{"equal-before", "same", "same", "same", "", "same", "unchanged"},
		{"equal-gap", "same", "same", "", "same", "same", "restore-no-replace"},
		{"equal-published", "same", "same", "same", "same", "", "published-keep-backup"},
		{"stage-still-present", "o", "s", "s", "o", "s", "manual-no-mutation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.original, tc.stage, tc.final, tc.backup, tc.left); got != tc.want {
				t.Fatalf("%s != %s", got, tc.want)
			}
		})
	}
}
func TestRecoveryNeverClobbersNewWriter(t *testing.T) {
	d, f := fixture(t)
	put(t, d, "backup", "original")
	absent(t, d, "final")
	put(t, d, "final", "arrived-after-check")
	if e := rename(f, "backup", "final", 1); !errors.Is(e, syscall.EEXIST) {
		t.Fatal(e)
	}
	content(t, d, "backup", "original")
	content(t, d, "final", "arrived-after-check")
}
func TestPermissionFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory DAC; must run non-root")
	}
	d, f := fixture(t)
	put(t, d, "stage", "new")
	put(t, d, "final", "old")
	if e := os.Chmod(d, 0500); e != nil {
		t.Fatal(e)
	}
	defer os.Chmod(d, 0700)
	if e := rename(f, "stage", "final", 0); !errors.Is(e, syscall.EACCES) {
		t.Fatalf("want EACCES: %v", e)
	}
	content(t, d, "stage", "new")
	content(t, d, "final", "old")
}

// A helper really exits without defers at each rename boundary. Its parent
// checks filesystem evidence, not a trusting in-memory state flag.
func TestStoppedProcess(t *testing.T) {
	if d := os.Getenv("YAKUORI_PROBE_DIR"); d != "" {
		f, e := os.Open(d)
		if e != nil {
			panic(e)
		}
		// Lock the stable directory inode: no lockfile unlink/recreation split lock.
		if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
			panic(e)
		}
		stop := os.Getenv("YAKUORI_PROBE_STOP")
		if stop == "prepared" {
			os.Exit(73)
		}
		if e = rename(f, "final", "backup", 1); e != nil {
			panic(e)
		}
		if stop == "backed-up" {
			os.Exit(73)
		}
		if e = rename(f, "stage", "final", 1); e != nil {
			panic(e)
		}
		os.Exit(73)
	}
	for _, stop := range []string{"prepared", "backed-up", "published"} {
		t.Run(stop, func(t *testing.T) {
			d, f := fixture(t)
			put(t, d, "final", "original")
			put(t, d, "stage", "translated")
			// A minimal closed record is in place before any rename.
			put(t, d, "run.json", `{"schema":1,"mode":"same-path","run_id":"fixture","source":"final","output":"final","stage":"stage","backup":"backup"}`)
			c := exec.Command(os.Args[0], "-test.run=^TestStoppedProcess$")
			c.Env = append(os.Environ(), "YAKUORI_PROBE_DIR="+d, "YAKUORI_PROBE_STOP="+stop)
			e := c.Run()
			var ee *exec.ExitError
			if !errors.As(e, &ee) || ee.ExitCode() != 73 {
				t.Fatalf("helper: %v", e)
			}
			if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
				t.Fatalf("lock survived exit: %v", e)
			}
			defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			switch stop {
			case "prepared":
				content(t, d, "final", "original")
				absent(t, d, "backup")
			case "backed-up":
				absent(t, d, "final")
				content(t, d, "backup", "original")
				if e = rename(f, "backup", "final", 1); e != nil {
					t.Fatal(e)
				}
				content(t, d, "final", "original")
			case "published":
				content(t, d, "final", "translated")
				content(t, d, "backup", "original")
				absent(t, d, "stage")
			}
		})
	}
}
func TestDirectoryLock(t *testing.T) {
	d, f := fixture(t)
	g, e := os.Open(d)
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		t.Fatal(e)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if e = syscall.Flock(int(g.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(e, syscall.EWOULDBLOCK) {
		t.Fatalf("second lock: %v", e)
	}
}
func TestFilesystemIdentity(t *testing.T) {
	d, _ := fixture(t)
	var st syscall.Statfs_t
	if e := syscall.Statfs(d, &st); e != nil {
		t.Fatal(e)
	}
	t.Logf("filesystem magic=0x%x; Go=%s", st.Type, strings.TrimSpace(os.Getenv("GOVERSION")))
}

func TestUnsupportedFlagsPreserveBytes(t *testing.T) {
	d, f := fixture(t)
	put(t, d, "stage", "new")
	put(t, d, "final", "old")
	if e := rename(f, "stage", "final", 0x80000000); !errors.Is(e, syscall.EINVAL) {
		t.Fatalf("invalid flags: %v", e)
	}
	content(t, d, "stage", "new")
	content(t, d, "final", "old")
}

func TestCrossMountRejected(t *testing.T) {
	d, f := fixture(t)
	put(t, d, "stage", "new")
	// Second fixture in the checkout; never touch an existing user path.
	other, e := os.MkdirTemp(".", ".cross-mount-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(other)
	g, e := os.Open(other)
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	var a, b syscall.Stat_t
	if e = syscall.Fstat(int(f.Fd()), &a); e != nil {
		t.Fatal(e)
	}
	if e = syscall.Fstat(int(g.Fd()), &b); e != nil {
		t.Fatal(e)
	}
	if a.Dev == b.Dev {
		t.Skip("TMPDIR and checkout are same mount; run with separate mounts to exercise EXDEV")
	}
	put(t, other, "final", "old")
	// renameat also has the same cross-mount refusal as renameat2.
	if e = syscall.Renameat(int(f.Fd()), "stage", int(g.Fd()), "final"); !errors.Is(e, syscall.EXDEV) {
		t.Fatalf("cross mount: %v", e)
	}
	content(t, d, "stage", "new")
	content(t, other, "final", "old")
}
