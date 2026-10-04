//go:build linux

package publication

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRecordBeforeMutation(t *testing.T) {
	r := publishFixture(t, InPlace, false)
	var closed *os.File
	var directorySynced bool
	r.recordClose = func(f *os.File) error { closed = f; return f.Close() }
	r.recordSync = func(f *os.File) error {
		if f == r.runDir.file {
			directorySynced = true
		}
		return f.Sync()
	}
	operations := 0
	r.rename = func(a int, from string, b int, to string, flags uint) error {
		if to == "record.json" {
			directorySynced = false
		}
		if to == r.backupName() || from == "stage" {
			if closed == nil || !directorySynced {
				t.Fatal("real rename preceded closed/synced record")
			}
			if _, err := closed.Write([]byte("late")); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("record descriptor still writable: %v", err)
			}
			record := readOperationRecord(t, filepath.Join(r.runDir.path, "record.json"))
			if to == r.backupName() {
				if record.Phase != preparedPhase || record.LastCompletedOperation != "none" || record.Backup.Path != r.backupPath() {
					t.Fatal("backup lacks prepared record")
				}
				wantBytes(t, r.source.path, "original")
			} else {
				if record.Phase != backedUpPhase || record.LastCompletedOperation != "backup-rename" {
					t.Fatal("publication lacks backed-up record")
				}
				wantBytes(t, r.backupPath(), "original")
			}
			operations++
		}
		return unix.Renameat2(a, from, b, to, flags)
	}
	result, err := r.Publish(context.Background())
	if err != nil || result.State != Published || operations != 2 {
		t.Fatalf("record ordering failed: %+v %v operations=%d", result, err, operations)
	}
	b, err := os.ReadFile(result.RecordPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("original")) || bytes.Contains(b, []byte("translated")) {
		t.Fatal("record included content")
	}
	wantBytes(t, result.BackupPath, "original")
	wantBytes(t, result.OutputPath, "translated")
}

func TestRecordFailure(t *testing.T) {
	for _, name := range []string{"write", "short-write", "sync", "close", "rename", "directory-sync", "size-limit", "temp-collision"} {
		t.Run(name, func(t *testing.T) {
			r := publishFixture(t, InPlace, false)
			injected := unix.EIO
			var file *os.File
			wantErr := error(injected)
			switch name {
			case "write":
				r.recordWrite = func(f *os.File, b []byte) (int, error) {
					file = f
					n, err := f.Write(b[:1])
					return n, errors.Join(err, injected)
				}
			case "short-write":
				wantErr = io.ErrShortWrite
				r.recordWrite = func(f *os.File, b []byte) (int, error) { file = f; return f.Write(b[:1]) }
			case "sync":
				r.recordSync = func(f *os.File) error { file = f; return injected }
			case "close":
				r.recordClose = func(f *os.File) error { file = f; return errors.Join(f.Close(), injected) }
			case "rename":
				r.rename = func(a int, from string, b int, to string, flags uint) error {
					if to == "record.json" {
						return injected
					}
					return unix.Renameat2(a, from, b, to, flags)
				}
			case "directory-sync":
				r.recordSync = func(f *os.File) error {
					if f == r.runDir.file {
						return injected
					}
					return f.Sync()
				}
			case "size-limit":
				wantErr = ErrRecordTooLarge
				r.source.path = "/" + strings.Repeat("x", 64*1024)
			case "temp-collision":
				wantErr = os.ErrExist
				putFile(t, filepath.Join(r.runDir.path, ".record-prepared"), "prior")
			}
			source := filepath.Join(r.source.parent.path, r.source.name)
			result, err := r.Publish(context.Background())
			if !errors.Is(err, wantErr) || result.State != NotPublished {
				t.Fatalf("record failure moved source: %+v %v", result, err)
			}
			if file != nil {
				if _, err := file.Write([]byte("late")); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("failed record retained writer: %v", err)
				}
			}
			wantBytes(t, source, "original")
			wantBytes(t, r.stage.path, "translated")
			wantAbsent(t, result.BackupPath)
			if name == "temp-collision" {
				wantBytes(t, filepath.Join(r.runDir.path, ".record-prepared"), "prior")
			}
			if name != "directory-sync" {
				wantAbsent(t, result.RecordPath)
			}
		})
	}
}

func TestPublishedAfterRecordFailure(t *testing.T) {
	for _, name := range []string{"write", "sync", "close", "rename", "directory-sync", "in-place-write"} {
		t.Run(name, func(t *testing.T) {
			mode := Replace
			if name == "in-place-write" {
				mode = InPlace
			}
			r := publishFixture(t, mode, true)
			injected := unix.EIO
			var publishingRecord bool
			r.recordWrite = func(f *os.File, b []byte) (int, error) {
				publishingRecord = f.Name() == ".record-published"
				if publishingRecord && (name == "write" || name == "in-place-write") {
					return 0, injected
				}
				return f.Write(b)
			}
			if name == "sync" {
				r.recordSync = func(f *os.File) error {
					if publishingRecord {
						return injected
					}
					return f.Sync()
				}
			}
			if name == "close" {
				r.recordClose = func(f *os.File) error {
					err := f.Close()
					if publishingRecord {
						return errors.Join(err, injected)
					}
					return err
				}
			}
			if name == "rename" {
				r.rename = func(a int, from string, b int, to string, flags uint) error {
					if from == ".record-published" {
						return injected
					}
					return unix.Renameat2(a, from, b, to, flags)
				}
			}
			if name == "directory-sync" {
				r.recordSync = func(f *os.File) error {
					if publishingRecord && f == r.runDir.file {
						return injected
					}
					return f.Sync()
				}
			}
			result, err := r.Publish(context.Background())
			if !errors.Is(err, injected) || result.State != Published {
				t.Fatalf("publication was misreported: %+v %v", result, err)
			}
			wantBytes(t, r.output.path, "translated")
			wantAbsent(t, r.stage.path)
			if mode == InPlace {
				wantBytes(t, result.BackupPath, "original")
			} else {
				wantBytes(t, r.source.path, "original")
			}
			phase := preparedPhase
			if name == "directory-sync" {
				phase = publishedPhase
			} else if mode == InPlace {
				phase = backedUpPhase
			}
			if readOperationRecord(t, result.RecordPath).Phase != phase {
				t.Fatal("unexpected retained record phase")
			}
		})
	}
}
