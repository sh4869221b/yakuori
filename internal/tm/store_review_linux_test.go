//go:build linux

package tm

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/config"
	"golang.org/x/sys/unix"
)

func TestStorePathAliases(t *testing.T) {
	path, s := storeFixture(t)
	before := readBytes(t, path)
	aliasDir := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Dir(path), aliasDir); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.Context(), filepath.Join(aliasDir, filepath.Base(path)), config.DefaultLimits()); !errors.Is(err, ErrOwnership) {
		t.Fatalf("directory alias bypassed ownership: %v", err)
	}
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			alias := filepath.Join(t.TempDir(), "alias.sqlite")
			link := os.Symlink
			if kind == "hardlink" {
				link = os.Link
			}
			if err := link(path, alias); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(alias)
			for _, operation := range []func(context.Context, string, config.Limits) (*Store, error){Create, Open, Recover} {
				if _, err := operation(t.Context(), alias, config.DefaultLimits()); !errors.Is(err, ErrUnsupportedStore) {
					t.Fatalf("accepted aliased database: %v", err)
				}
			}
			if _, err := os.Lstat(alias + ".lock"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("created alias lock: %v", err)
			}
			assertPreserved(t, path, before)
		})
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), path, config.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRejectNonregularPaths(t *testing.T) {
	if path := os.Getenv("YAKUORI_TM_NONREGULAR_PATH"); path != "" {
		for index, operation := range []func(context.Context, string, config.Limits) (*Store, error){Create, Open, Recover} {
			want := ErrUnsupportedStore
			if os.Getenv("YAKUORI_TM_NONREGULAR_KIND") == "journal-fifo" && index < 2 {
				want = ErrRecoveryRequired
			}
			if _, err := operation(t.Context(), path, config.DefaultLimits()); !errors.Is(err, want) {
				t.Fatalf("nonregular path error: %v", err)
			}
		}
		return
	}
	for _, kind := range []string{"fifo", "directory", "lock-fifo", "journal-fifo"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "memory.sqlite")
			if kind == "directory" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				fifo := path
				if kind == "lock-fifo" {
					fifo += ".lock"
				} else if kind == "journal-fifo" {
					store, err := Create(t.Context(), path, config.DefaultLimits())
					if err != nil {
						t.Fatal(err)
					}
					if err := store.Close(); err != nil {
						t.Fatal(err)
					}
					fifo += "-journal"
				}
				if err := unix.Mkfifo(fifo, 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.v", "-test.run=^TestStoreRejectNonregularPaths$")
			cmd.Env = append(os.Environ(), "YAKUORI_TM_NONREGULAR_PATH="+path, "YAKUORI_TM_NONREGULAR_KIND="+kind)
			output, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(output), "--- PASS: TestStoreRejectNonregularPaths") {
				t.Fatalf("nonregular path blocked or was accepted: %v\n%s", err, output)
			}
		})
	}
}

func TestStoreRecoverRejectsSymlinkJournal(t *testing.T) {
	path, store := storeFixture(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before := readBytes(t, path)
	target := filepath.Join(t.TempDir(), "other-journal")
	contents := []byte("external journal contents")
	if err := os.WriteFile(target, contents, 0600); err != nil {
		t.Fatal(err)
	}
	journal := path + "-journal"
	if err := os.Symlink(target, journal); err != nil {
		t.Fatal(err)
	}
	recoveryTemp(t)
	recovered, err := Recover(t.Context(), path, config.DefaultLimits())
	if recovered != nil {
		_ = recovered.Close()
		t.Fatal("accepted symlinked recovery journal")
	}
	if !errors.Is(err, ErrUnsupportedStore) {
		t.Fatalf("symlinked journal error: %v", err)
	}
	assertPreserved(t, path, before)
	assertPreserved(t, target, contents)
	if got, err := os.Readlink(journal); err != nil || got != target {
		t.Fatalf("journal link changed: target=%q error=%v", got, err)
	}
}

func TestStoreCreateFailureCleanup(t *testing.T) {
	if os.Getenv("YAKUORI_TM_CREATE_FAILURE") == "1" {
		path := filepath.Join(t.TempDir(), "new.sqlite")
		lockText := []byte("retain ownership inode")
		if err := os.WriteFile(path+".lock", lockText, 0600); err != nil {
			t.Fatal(err)
		}
		var original unix.Rlimit
		if err := unix.Getrlimit(unix.RLIMIT_FSIZE, &original); err != nil {
			t.Fatal(err)
		}
		signal.Ignore(syscall.SIGXFSZ)
		if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &unix.Rlimit{Cur: 1, Max: original.Max}); err != nil {
			t.Fatal(err)
		}
		store, createErr := Create(t.Context(), path, config.DefaultLimits())
		if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &original); err != nil {
			t.Fatal(err)
		}
		if store != nil || createErr == nil {
			t.Fatalf("expected initialization I/O failure: store=%v error=%v", store, createErr)
		}
		t.Logf("initialization failure: %v", createErr)
		for _, suffix := range []string{"", "-journal"} {
			if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("new asset remains after failed Create: %s: %v", suffix, err)
			}
		}
		assertPreserved(t, path+".lock", lockText)
		retry, err := Create(t.Context(), path, config.DefaultLimits())
		if err != nil {
			t.Fatalf("explicit Create after failure: %v", err)
		}
		if err := retry.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.v", "-test.run=^TestStoreCreateFailureCleanup$")
	cmd.Env = append(os.Environ(), "YAKUORI_TM_CREATE_FAILURE=1")
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "--- PASS: TestStoreCreateFailureCleanup") {
		t.Fatalf("Create I/O-failure test: %v\n%s", err, output)
	}
	t.Logf("%s", output)
}
