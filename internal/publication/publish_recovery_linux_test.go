//go:build linux

package publication

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPublishRecoveryFailure(t *testing.T) {
	for _, name := range []string{"restore-rename", "record-write", "record-rename", "final-race", "invalid-record", "parent-drift"} {
		t.Run(name, func(t *testing.T) {
			r := publishFixture(t, InPlace, false)
			evidenceParent := r.output.parent.path
			publishErr := errors.New("injected publish failure")
			wantState, wantCause := SourceBackedUp, error(unix.EACCES)
			if name == "record-write" || name == "record-rename" {
				wantState, wantCause = Restored, unix.EIO
			}
			if name == "final-race" {
				wantCause = unix.EEXIST
			}
			if name == "invalid-record" {
				wantCause = ErrInvalidRecord
			}
			if name == "parent-drift" {
				wantCause = ErrRecoveryConflict
			}
			attempts := 0
			r.recordWrite = func(f *os.File, body []byte) (int, error) {
				if name == "record-write" && strings.HasPrefix(f.Name(), ".record-recovery-") {
					return 0, unix.EIO
				}
				return f.Write(body)
			}
			r.rename = func(a int, from string, b int, to string, flags uint) error {
				if from == r.stage.name {
					if name == "invalid-record" {
						putFile(t, filepath.Join(r.runDir.path, "record.json"), "untrusted record body")
					}
					if name == "parent-drift" {
						evidenceParent += "-moved"
						if err := os.Rename(r.output.parent.path, evidenceParent); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(r.output.parent.path, 0700); err != nil {
							t.Fatal(err)
						}
					}
					return publishErr
				}
				if from == r.backupName() {
					attempts++
					if flags != unix.RENAME_NOREPLACE {
						t.Fatalf("restore flags: %d", flags)
					}
					if name == "final-race" {
						putFile(t, r.output.path, "new writer")
					}
					if name == "restore-rename" {
						if os.Geteuid() == 0 {
							t.Fatal("restore EACCES requires non-root")
						}
						if err := unix.Fchmod(b, 0500); err != nil {
							t.Fatal(err)
						}
						err := unix.Renameat2(a, from, b, to, flags)
						return errors.Join(err, unix.Fchmod(b, 0700))
					}
				}
				if name == "record-rename" && strings.HasPrefix(from, ".record-recovery-") {
					return unix.EIO
				}
				return unix.Renameat2(a, from, b, to, flags)
			}
			result, err := r.Publish(context.Background())
			var diagnostic *RecoveryError
			if !errors.Is(err, publishErr) || !errors.Is(err, wantCause) || !errors.As(err, &diagnostic) || result.State != wantState || diagnostic.Result != result {
				t.Fatalf("recovery failure lost cause/result: %+v %v", result, err)
			}
			wantAttempts := 1
			if name == "invalid-record" || name == "parent-drift" {
				wantAttempts = 0
			}
			if attempts != wantAttempts {
				t.Fatalf("restore attempts %d; want %d", attempts, wantAttempts)
			}
			if strings.Contains(err.Error(), "untrusted record body") {
				t.Fatal("record body leaked")
			}
			repeated, err := r.Publish(context.Background())
			if !errors.Is(err, ErrInvalidPublish) || repeated != result || attempts != wantAttempts {
				t.Fatalf("repeated Publish: %+v %v attempts=%d", repeated, err, attempts)
			}
			closeRun(t, r)
			stagePath := filepath.Join(evidenceParent, filepath.Base(r.runDir.path), r.stage.name)
			recordPath := filepath.Join(filepath.Dir(stagePath), "record.json")
			backupPath := filepath.Join(evidenceParent, r.backupName())
			outputPath := filepath.Join(evidenceParent, r.output.name)
			wantBytes(t, stagePath, "translated")
			if wantState == Restored {
				wantBytes(t, outputPath, "original")
				wantAbsent(t, backupPath)
			} else {
				wantBytes(t, backupPath, "original")
				if name == "final-race" {
					wantBytes(t, outputPath, "new writer")
				} else {
					wantAbsent(t, outputPath)
				}
			}
			if name == "invalid-record" {
				wantBytes(t, recordPath, "untrusted record body")
			} else if readOperationRecord(t, recordPath).Phase != backedUpPhase {
				t.Fatal("failed recovery rewrote record")
			}
		})
	}
}
