//go:build linux

package publication

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRecoveryRecordUpdateFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stop    string
		failure string
		state   State
	}{
		{"restored-write", "backup", "write", Restored},
		{"restored-rename", "backup", "rename", Restored},
		{"restored-directory-sync", "backup", "directory-sync", Restored},
		{"restored-missing-stage", "missing-stage", "write", Restored},
		{"published-write", "published", "write", Published},
		{"abandoned-write", "prepared", "write", NotPublished},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := publishFixture(t, InPlace, false)
			record := pendingRecoveryRecord(t, r)
			if tc.stop != "prepared" {
				moveRecoveryFile(t, r.source.path, r.backupPath())
			}
			if tc.stop == "published" {
				moveRecoveryFile(t, r.stage.path, r.output.path)
			}
			if tc.stop == "missing-stage" {
				moveRecoveryFile(t, r.stage.path, filepath.Join(r.runDir.path, "retained-stage"))
			}
			var temp string
			r.recordWrite = func(f *os.File, b []byte) (int, error) {
				temp = filepath.Join(r.runDir.path, f.Name())
				if tc.failure == "write" {
					return 0, unix.EIO
				}
				return f.Write(b)
			}
			r.rename = func(a int, from string, b int, to string, flags uint) error {
				if tc.failure == "rename" && to == "record.json" {
					return unix.EIO
				}
				return unix.Renameat2(a, from, b, to, flags)
			}
			r.recordSync = func(f *os.File) error {
				if tc.failure == "directory-sync" && f == r.runDir.file {
					return unix.EIO
				}
				return f.Sync()
			}
			result, err := r.reconcileRecord(record, r.runDir)
			if !errors.Is(err, unix.EIO) || result.State != tc.state {
				t.Fatalf("record failure lost result: %+v %v", result, err)
			}
			wantFinal := "original"
			if tc.state == Published {
				wantFinal = "translated"
				wantBytes(t, r.backupPath(), "original")
			} else {
				wantAbsent(t, r.backupPath())
			}
			wantBytes(t, r.output.path, wantFinal)
			if tc.stop == "published" || tc.stop == "missing-stage" {
				wantAbsent(t, r.stage.path)
			} else {
				wantBytes(t, r.stage.path, "translated")
			}
			var retained []byte
			if tc.failure != "directory-sync" {
				retained, err = os.ReadFile(temp)
				if err != nil {
					t.Fatal(err)
				}
			}
			r.recordWrite, r.recordSync, r.rename = (*os.File).Write, (*os.File).Sync, unix.Renameat2
			record, err = readRecord(r.runDir)
			if err != nil {
				t.Fatal(err)
			}
			retried, err := r.reconcileRecord(record, r.runDir)
			wantState := tc.state
			if tc.state == Restored && tc.failure != "directory-sync" {
				wantState = NotPublished
			}
			if err != nil || retried.State != wantState {
				t.Fatalf("record retry: %+v %v; want %s", retried, err, wantState)
			}
			wantBytes(t, r.output.path, wantFinal)
			if tc.failure != "directory-sync" {
				wantBytes(t, temp, string(retained))
			}
			resolved, err := readRecord(r.runDir)
			if err != nil || (resolved.Phase != publishedPhase && resolved.Phase != restoredPhase && resolved.Phase != abandonedPhase) {
				t.Fatalf("retry did not resolve record: %+v %v", resolved, err)
			}
			if tc.stop == "missing-stage" {
				wantBytes(t, filepath.Join(r.runDir.path, "retained-stage"), "translated")
			}
		})
	}
}

func TestRecoveryTerminalHistory(t *testing.T) {
	for _, stop := range []string{"prepared", "backup", "published"} {
		t.Run(stop, func(t *testing.T) {
			r := publishFixture(t, InPlace, false)
			record := pendingRecoveryRecord(t, r)
			if stop != "prepared" {
				moveRecoveryFile(t, r.source.path, r.backupPath())
			}
			if stop == "published" {
				moveRecoveryFile(t, r.stage.path, r.output.path)
			}
			resolved, err := r.reconcileRecord(record, r.runDir)
			if err != nil {
				t.Fatal(err)
			}
			putFile(t, r.output.path, "later legitimate output")
			if stop != "published" {
				putFile(t, r.stage.path, "later stage change")
			}
			record, err = readRecord(r.runDir)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(resolved.RecordPath)
			if err != nil {
				t.Fatal(err)
			}
			result, err := r.reconcileRecord(record, r.runDir)
			if err != nil || result.State != resolved.State {
				t.Fatalf("terminal history rechecked current output: %+v %v", result, err)
			}
			wantBytes(t, r.output.path, "later legitimate output")
			wantBytes(t, resolved.RecordPath, string(before))
			if stop == "published" {
				wantBytes(t, r.backupPath(), "original")
			} else {
				wantBytes(t, r.stage.path, "later stage change")
				wantAbsent(t, r.backupPath())
			}
		})
	}
}

func TestRecoveryRenameFailure(t *testing.T) {
	for _, name := range []string{"permission", "cross-mount", "unsupported"} {
		t.Run(name, func(t *testing.T) {
			r := publishFixture(t, InPlace, false)
			record := pendingRecoveryRecord(t, r)
			moveRecoveryFile(t, r.source.path, r.backupPath())
			wantErr := error(unix.EOPNOTSUPP)
			other := ""
			r.rename = func(a int, from string, b int, to string, flags uint) error {
				if from != r.backupName() {
					return unix.Renameat2(a, from, b, to, flags)
				}
				switch name {
				case "permission":
					if os.Geteuid() == 0 {
						t.Fatal("permission test requires non-root")
					}
					wantErr = unix.EACCES
					if err := unix.Fchmod(b, 0500); err != nil {
						return err
					}
					return errors.Join(unix.Renameat2(a, from, b, to, flags), unix.Fchmod(b, 0700))
				case "cross-mount":
					wantErr = unix.EXDEV
					dir, f, _ := externalPublicationFixture(t, r.source.parent.id.MountID)
					other = filepath.Join(dir, "output")
					putFile(t, other, "other writer")
					return unix.Renameat2(a, from, int(f.Fd()), "output", flags)
				default:
					return unix.EOPNOTSUPP
				}
			}
			result, err := r.reconcileRecord(record, r.runDir)
			if !errors.Is(err, wantErr) || !errors.Is(err, ErrRecoveryConflict) || result.State != SourceBackedUp {
				t.Fatalf("rename failure: %+v %v", result, err)
			}
			wantAbsent(t, r.output.path)
			wantBytes(t, r.backupPath(), "original")
			wantBytes(t, r.stage.path, "translated")
			if other != "" {
				wantBytes(t, other, "other writer")
			}
			if readOperationRecord(t, result.RecordPath).Phase != preparedPhase {
				t.Fatal("failed rename changed record")
			}
		})
	}
}
