//go:build linux

package publication

import (
	"context"
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestBackupFailure(t *testing.T) {
	for _, name := range []string{"collision", "racing-collision", "backup-rename", "publish-rename", "backed-record", "stage-drift-after-backup"} {
		t.Run(name, func(t *testing.T) {
			r := publishFixture(t, InPlace, false)
			wantState, wantErr := NotPublished, error(unix.EIO)
			stageWant := "translated"
			switch name {
			case "collision":
				putFile(t, r.backupPath(), "prior")
				wantErr = unix.EEXIST
			case "racing-collision":
				wantErr = unix.EEXIST
				r.recordSync = func(f *os.File) error {
					if f == r.runDir.file {
						putFile(t, r.backupPath(), "prior")
					}
					return f.Sync()
				}
			case "backup-rename", "publish-rename":
				if name == "publish-rename" {
					wantState = Restored
				}
				r.rename = func(a int, b string, c int, d string, e uint) error {
					if (name == "backup-rename" && d == r.backupName()) || (name == "publish-rename" && b == r.stage.name) {
						return unix.EIO
					}
					return unix.Renameat2(a, b, c, d, e)
				}
			case "backed-record":
				wantState = Restored
				r.recordWrite = func(f *os.File, b []byte) (int, error) {
					if f.Name() == ".record-source-backed-up" {
						return 0, unix.EIO
					}
					return f.Write(b)
				}
			case "stage-drift-after-backup":
				wantState, wantErr, stageWant = SourceBackedUp, ErrChanged, "modified!!"
				n := 0
				r.recordSync = func(f *os.File) error {
					if f == r.runDir.file {
						n++
						if n == 2 {
							putFile(t, r.stage.path, stageWant)
						}
					}
					return f.Sync()
				}
			}
			result, err := r.Publish(context.Background())
			if !errors.Is(err, wantErr) || result.State != wantState {
				t.Fatalf("backup failure: %+v %v", result, err)
			}
			wantBytes(t, r.stage.path, stageWant)
			if wantState == SourceBackedUp {
				wantAbsent(t, r.source.path)
				wantBytes(t, result.BackupPath, "original")
			} else {
				wantBytes(t, r.source.path, "original")
				if name == "collision" || name == "racing-collision" {
					wantBytes(t, r.backupPath(), "prior")
				} else {
					wantAbsent(t, r.backupPath())
				}
			}
		})
	}
}

func TestCancellationState(t *testing.T) {
	for _, name := range []string{"before", "prepared", "backup", "backed-record", "publish"} {
		t.Run(name, func(t *testing.T) {
			r := publishFixture(t, InPlace, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantState := NotPublished
			switch name {
			case "before":
				cancel()
			case "backup", "backed-record":
				wantState = Restored
			case "publish":
				wantState = Published
			}
			r.rename = func(a int, b string, c int, d string, e uint) error {
				err := unix.Renameat2(a, b, c, d, e)
				if err == nil && ((name == "prepared" && b == ".record-prepared") || (name == "backup" && d == r.backupName()) || (name == "backed-record" && b == ".record-source-backed-up") || (name == "publish" && b == r.stage.name)) {
					cancel()
				}
				return err
			}
			result, err := r.Publish(ctx)
			if !errors.Is(err, context.Canceled) || result.State != wantState {
				t.Fatalf("cancellation: %+v %v", result, err)
			}
			switch wantState {
			case NotPublished, Restored:
				wantBytes(t, r.source.path, "original")
				wantBytes(t, r.stage.path, "translated")
				wantAbsent(t, r.backupPath())
			case Published:
				wantBytes(t, r.output.path, "translated")
				wantAbsent(t, r.stage.path)
				wantBytes(t, result.BackupPath, "original")
			}
			if name == "before" {
				wantAbsent(t, result.RecordPath)
			} else {
				record := readOperationRecord(t, result.RecordPath)
				phase := preparedPhase
				if wantState == Restored {
					phase = restoredPhase
				}
				if wantState == Published {
					phase = publishedPhase
				}
				if record.Phase != phase {
					t.Fatalf("untruthful phase: %q", record.Phase)
				}
			}
		})
	}
}
