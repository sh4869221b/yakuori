//go:build linux

package tm

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/config"
)

func storeFixture(t *testing.T) (string, *Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tm # fixture?.sqlite")
	s, err := Create(context.Background(), path, config.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return path, s
}

func storeExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func assertPreserved(t *testing.T, path string, before []byte) {
	t.Helper()
	if after := readBytes(t, path); !bytes.Equal(before, after) {
		t.Fatalf("modified rejected asset %s", path)
	}
}

func recoveryTemp(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Cleanup(func() {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Errorf("recovery left temporary assets: %v %v", entries, err)
		}
	})
}

func TestStoreCreateOpen(t *testing.T) {
	path, s := storeFixture(t)
	storeExec(t, s.db, "INSERT INTO tm VALUES(1,?,?,?)", []byte("source\x00"), []byte("profile"), "保存訳")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("file permissions: %v %v", info, err)
	}
	reopened, err := Open(context.Background(), path, config.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	var text string
	if err := reopened.db.QueryRow("SELECT translated_text FROM tm WHERE source_identity=?", []byte("source\x00")).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "保存訳" {
		t.Fatal(text)
	}
	var timeout int
	if err := reopened.db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != 100 {
		t.Fatal("unbounded SQLite busy wait", timeout)
	}
}

func TestStoreRejectPreservesAssets(t *testing.T) {
	recoveryTemp(t)
	for _, kind := range []string{"garbage", "truncated", "empty", "foreign", "future", "layout", "trigger", "extra-table", "sqlite-like-table", "wal-mode", "-journal", "-wal", "-shm"} {
		t.Run(kind, func(t *testing.T) {
			path, s := storeFixture(t)
			switch kind {
			case "foreign":
				storeExec(t, s.db, "PRAGMA application_id=0")
			case "future":
				storeExec(t, s.db, "PRAGMA user_version=99")
			case "layout":
				storeExec(t, s.db, "ALTER TABLE tm ADD COLUMN surprise TEXT")
			case "trigger":
				storeExec(t, s.db, "CREATE TRIGGER surprise AFTER INSERT ON tm BEGIN DELETE FROM tm; END")
			case "extra-table":
				storeExec(t, s.db, "CREATE TABLE surprise(x)")
			case "sqlite-like-table":
				storeExec(t, s.db, "CREATE TABLE sqliteXsurprise(x)")
			case "wal-mode":
				storeExec(t, s.db, "PRAGMA journal_mode=WAL")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "garbage":
				if err := os.WriteFile(path, []byte("not SQLite\x00asset"), 0600); err != nil {
					t.Fatal(err)
				}
			case "truncated":
				if err := os.Truncate(path, 150); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if err := os.Truncate(path, 0); err != nil {
					t.Fatal(err)
				}
			}
			var sidecar []byte
			if strings.HasPrefix(kind, "-") {
				sidecar = []byte("unresolved sidecar")
				if err := os.WriteFile(path+kind, sidecar, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := readBytes(t, path)
			opened, err := Open(context.Background(), path, config.DefaultLimits())
			if err == nil {
				opened.Close()
				t.Fatal("accepted unsupported store")
			}
			if kind == "-journal" && !errors.Is(err, ErrRecoveryRequired) {
				t.Fatal(err)
			}
			assertPreserved(t, path, before)
			if sidecar != nil {
				assertPreserved(t, path+kind, sidecar)
			}
			for _, suffix := range []string{"-journal", "-wal", "-shm"} {
				if suffix == kind {
					continue
				}
				if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("created sidecar %s: %v", suffix, err)
				}
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.sqlite")
		if _, err := Open(context.Background(), path, config.DefaultLimits()); err == nil {
			t.Fatal("accepted missing store")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("created missing database", err)
		}
	})
	t.Run("nonregular-journal", func(t *testing.T) {
		path, s := storeFixture(t)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path+"-journal", 0700); err != nil {
			t.Fatal(err)
		}
		asset := filepath.Join(path+"-journal", "retained")
		if err := os.WriteFile(asset, []byte("existing asset"), 0600); err != nil {
			t.Fatal(err)
		}
		before := readBytes(t, path)
		if _, err := Recover(context.Background(), path, config.DefaultLimits()); err == nil {
			t.Fatal("accepted nonregular journal")
		}
		assertPreserved(t, path, before)
		assertPreserved(t, asset, []byte("existing asset"))
	})
	for _, kind := range []string{"corrupt-recovery", "unknown-recovery", "unsupported-recovery", "original-readonly"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "original-readonly" && os.Geteuid() == 0 {
				readonlyUnprivileged(t)
				return
			}
			path, s := storeFixture(t)
			if kind == "unknown-recovery" {
				storeExec(t, s.db, "PRAGMA user_version=99")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if kind == "corrupt-recovery" || kind == "unsupported-recovery" {
				if err := os.WriteFile(path+"-journal", []byte("stale journal"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				crashStore(t, path)
			}
			if kind == "corrupt-recovery" {
				if err := os.WriteFile(path, []byte("corrupt database"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "unsupported-recovery" {
				if err := os.WriteFile(path+"-wal", []byte("unresolved WAL"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "original-readonly" {
				if err := os.Chmod(path, 0400); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(path, 0600); err != nil {
						t.Error(err)
					}
				})
			}
			before, journal := readBytes(t, path), readBytes(t, path+"-journal")
			recovered, err := Recover(context.Background(), path, config.DefaultLimits())
			if err == nil {
				recovered.Close()
				t.Fatal("accepted failed recovery")
			}
			assertPreserved(t, path, before)
			assertPreserved(t, path+"-journal", journal)
			if kind == "unsupported-recovery" {
				assertPreserved(t, path+"-wal", []byte("unresolved WAL"))
			}
			t.Logf("recovery failure preserved main/journal: %v", err)
		})
	}
}

func readonlyUnprivileged(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "yakuori-tm-permission-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tm.test")
	if err := os.WriteFile(path, binary, 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-test.v", "-test.run=^TestStoreRejectPreservesAssets$/^original-readonly$")
	cmd.Dir = "/tmp"
	cmd.Env = append(os.Environ(), "TMPDIR=/tmp")
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	output, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("--- PASS: TestStoreRejectPreservesAssets/original-readonly")) {
		t.Fatalf("unprivileged permission-fault test: %v\n%s", err, output)
	}
	t.Logf("unprivileged permission-fault test:\n%s", output)
}

func TestStoreOwnership(t *testing.T) {
	path, s := storeFixture(t)
	before := readBytes(t, path)
	if _, err := Open(context.Background(), path, config.DefaultLimits()); !errors.Is(err, ErrOwnership) {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), path, config.DefaultLimits()); !errors.Is(err, ErrOwnership) {
		t.Fatal(err)
	}
	assertPreserved(t, path, before)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	lockText := []byte("retain lock contents")
	if err := os.WriteFile(path+".lock", lockText, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path, config.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	assertPreserved(t, path+".lock", lockText)
	info, err := os.Stat(path + ".lock")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("lock permissions: %v %v", info, err)
	}
}

func TestStoreCreateFailure(t *testing.T) {
	t.Run("existing", func(t *testing.T) {
		path, s := storeFixture(t)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		before := readBytes(t, path)
		if _, err := Create(context.Background(), path, config.DefaultLimits()); !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
		assertPreserved(t, path, before)
	})
	t.Run("orphan-sidecar", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "new.sqlite")
		journal := []byte("existing sidecar")
		if err := os.WriteFile(path+"-journal", journal, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Create(context.Background(), path, config.DefaultLimits()); !errors.Is(err, ErrRecoveryRequired) {
			t.Fatal(err)
		}
		assertPreserved(t, path+"-journal", journal)
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("created database beside orphan journal", err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		path := filepath.Join(t.TempDir(), "new.sqlite")
		for _, operation := range []func(context.Context, string, config.Limits) (*Store, error){Create, Open, Recover} {
			if _, err := operation(ctx, path, config.DefaultLimits()); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil || len(entries) != 0 {
			t.Fatalf("expired operation created assets: %v %v", entries, err)
		}
	})
	t.Run("cancel-streaming-copy", func(t *testing.T) {
		dir := t.TempDir()
		source, destination := filepath.Join(dir, "source"), filepath.Join(dir, "copy")
		f, err := os.Create(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(1 << 30); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			for ctx.Err() == nil {
				if _, err := os.Stat(destination); err == nil {
					cancel()
					return
				}
				runtime.Gosched()
			}
		}()
		if err := copyRecoveryFile(ctx, source, destination); !errors.Is(err, context.Canceled) {
			t.Fatal("streaming copy ignored cancellation", err)
		}
		<-done
		info, err := os.Stat(destination)
		if err != nil || info.Size() >= 1<<30 {
			t.Fatalf("copy did not stop before completion: %v %v", info, err)
		}
		info, err = os.Stat(source)
		if err != nil || info.Size() != 1<<30 {
			t.Fatalf("copy changed source: %v %v", info, err)
		}
	})
}

// crashStore creates a journal with SQLite and kills the owner only after cache spill has finished.
func crashStore(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStoreCrashProcess$")
	cmd.Env = append(os.Environ(), "YAKUORI_TM_CRASH_PATH="+path)
	release, resume, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := errors.Join(release.Close(), resume.Close()); err != nil {
			t.Error(err)
		}
	}()
	cmd.Stdin = release
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready, readErr := bufio.NewReader(output).ReadString('\n')
	killErr := cmd.Process.Kill()
	waitErr := cmd.Wait()
	if readErr != nil || ready != "ready\n" || killErr != nil || waitErr == nil {
		t.Fatalf("crash fixture: ready=%q read=%v kill=%v wait=%v stderr=%s", ready, readErr, killErr, waitErr, stderr.String())
	}
	journal := readBytes(t, path+"-journal")
	if len(journal) <= 512 || bytes.Equal(journal[:8], make([]byte, 8)) {
		t.Fatal("SQLite did not create a hot journal")
	}
	t.Logf("SIGKILL left SQLite journal: %d bytes", len(journal))
}

func TestStoreCrashProcess(t *testing.T) {
	path := os.Getenv("YAKUORI_TM_CRASH_PATH")
	if path == "" {
		return
	}
	// A small cache forces the uncommitted pages into the main file and syncs a native journal header.
	db, err := connect(path, "rw")
	if err != nil {
		t.Fatal(err)
	}
	storeExec(t, db, "PRAGMA cache_size=5")
	storeExec(t, db, "BEGIN IMMEDIATE")
	for i := range 100 {
		storeExec(t, db, "INSERT INTO tm VALUES(1,?,?,?)", fmt.Appendf(nil, "crash-%03d", i), []byte("profile"), strings.Repeat("uncommitted", 1024))
	}
	if _, err := os.Stdout.WriteString("ready\n"); err != nil {
		t.Fatal(err)
	}
	var release [1]byte
	if _, err := io.ReadFull(os.Stdin, release[:]); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRecover(t *testing.T) {
	recoveryTemp(t)
	t.Run("native-stale-journal", func(t *testing.T) {
		path, s := storeFixture(t)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		before := readBytes(t, path)
		if err := os.WriteFile(path+"-journal", []byte("stale journal"), 0600); err != nil {
			t.Fatal(err)
		}
		recovered, err := Recover(context.Background(), path, config.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		if err := recovered.Close(); err != nil {
			t.Fatal(err)
		}
		assertPreserved(t, path, before)
		if _, err := os.Stat(path + "-journal"); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("native stale journal cleanup failed", err)
		}
	})
	path, s := storeFixture(t)
	storeExec(t, s.db, "INSERT INTO tm VALUES(1,?,?,?)", []byte("source"), []byte("profile"), "確定訳")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		crashStore(t, path)
		before, journal := readBytes(t, path), readBytes(t, path+"-journal")
		if _, err := Open(context.Background(), path, config.DefaultLimits()); !errors.Is(err, ErrRecoveryRequired) {
			t.Fatal(err)
		}
		assertPreserved(t, path, before)
		assertPreserved(t, path+"-journal", journal)
		recovered, err := Recover(context.Background(), path, config.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		var count int
		var text string
		if err := recovered.db.QueryRow("SELECT count(*) FROM tm").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if err := recovered.db.QueryRow("SELECT translated_text FROM tm").Scan(&text); err != nil {
			t.Fatal(err)
		}
		if count != 1 || text != "確定訳" {
			t.Fatalf("recovery retained uncommitted rows or lost committed row: %d %q", count, text)
		}
		if _, err := os.Stat(path + "-journal"); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("journal unresolved", err)
		}
		if err := recovered.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
