//go:build linux

package publication

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPrepareRecoveryRejectsAmbiguity(t *testing.T) {
	for _, name := range []string{"multiple", "broken-other", "missing-record", "run-symlink", "source-mismatch", "parent-mismatch", "busy"} {
		t.Run(name, func(t *testing.T) {
			mode := InPlace
			if name == "source-mismatch" || name == "parent-mismatch" {
				mode = Replace
			}
			old := publishFixture(t, mode, mode == Replace)
			record := pendingRecoveryRecord(t, old)
			if mode == InPlace {
				moveRecoveryFile(t, old.source.path, old.backupPath())
			}
			options := Options{old.source.path, old.output.path, mode}
			id := strings.Repeat("f", 32)
			candidate := filepath.Join(old.output.parent.path, ".yakuori-run-"+id)
			switch name {
			case "multiple", "broken-other", "missing-record":
				if err := os.Mkdir(candidate, 0700); err != nil {
					t.Fatal(err)
				}
				putFile(t, filepath.Join(candidate, "stage"), "translated")
				if name == "multiple" {
					stage, err := openParent(filepath.Join(candidate, "stage"))
					if err != nil {
						t.Fatal(err)
					}
					if _, err = observe(&stage); err != nil {
						t.Fatal(err)
					}
					record.RunID, record.Stage = id, recordedFile(stage)
					record.Backup.Path = filepath.Join(old.output.parent.path, ".yakuori-backup-"+id)
					record.Backup.Basename = filepath.Base(record.Backup.Path)
					if err = stage.parent.file.Close(); err != nil {
						t.Fatal(err)
					}
					body, err := json.Marshal(record)
					if err != nil {
						t.Fatal(err)
					}
					putFile(t, filepath.Join(candidate, "record.json"), string(body))
				} else if name == "broken-other" {
					putFile(t, filepath.Join(candidate, "record.json"), "private record contents")
				}
			case "run-symlink":
				if err := os.Symlink(old.runDir.path, candidate); err != nil {
					t.Fatal(err)
				}
			case "source-mismatch":
				options.Source = filepath.Join(old.source.parent.path, "new-source")
				putFile(t, options.Source, "new original")
			case "parent-mismatch":
				record.OutputParent.Ino++
				body, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				putFile(t, filepath.Join(old.runDir.path, "record.json"), string(body))
			}
			beforeRecord, err := os.ReadFile(filepath.Join(old.runDir.path, "record.json"))
			if err != nil {
				t.Fatal(err)
			}
			before := recoveryDirectoryNames(t, old.output.parent.path)
			if name != "busy" {
				closeRun(t, old)
			}
			r, err := Prepare(context.Background(), options)
			if r != nil || err == nil {
				t.Fatalf("ambiguous recovery continued: %v %v", r, err)
			}
			if name == "busy" {
				if !errors.Is(err, ErrBusy) {
					t.Fatal(err)
				}
			} else {
				var diagnostic *RecoveryError
				if !errors.As(err, &diagnostic) || diagnostic.Reason == "" || diagnostic.Result.RunID == "" || diagnostic.Result.RecordPath == "" || diagnostic.Result.OutputPath != old.output.path {
					t.Fatalf("missing recovery diagnostic: %v", err)
				}
				if name == "multiple" || name == "source-mismatch" || name == "parent-mismatch" {
					if !errors.Is(err, ErrRecoveryConflict) {
						t.Fatal(err)
					}
				}
				if strings.Contains(err.Error(), "private record contents") {
					t.Fatal("record body leaked")
				}
			}
			if !reflect.DeepEqual(before, recoveryDirectoryNames(t, old.output.parent.path)) {
				t.Fatal("rejected recovery changed parent entries")
			}
			wantBytes(t, filepath.Join(old.runDir.path, "record.json"), string(beforeRecord))
			wantBytes(t, old.stage.path, "translated")
			if mode == InPlace {
				wantAbsent(t, old.source.path)
				wantBytes(t, old.backupPath(), "original")
			} else {
				wantBytes(t, old.source.path, "original")
				wantBytes(t, old.output.path, "existing")
			}
		})
	}
}

func TestPrepareRejectsRecordlessFailedPreparation(t *testing.T) {
	dir := publicationFixture(t)
	path := filepath.Join(dir, "source")
	putFile(t, path, "original")
	options := Options{path, path, InPlace}
	r, err := prepare(context.Background(), options, func(int, string, int, string, uint) error { return unix.EIO })
	if r != nil || !errors.Is(err, ErrCapability) {
		t.Fatalf("failed capability preparation: %v %v", r, err)
	}
	before := recoveryDirectoryNames(t, dir)
	r, err = Prepare(context.Background(), options)
	var diagnostic *RecoveryError
	if r != nil || !errors.As(err, &diagnostic) || !errors.Is(err, ErrInvalidRecord) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recordless history was ignored: %v %v", r, err)
	}
	if diagnostic.Result.RecordPath == "" || diagnostic.Reason != "invalid or missing operation record" {
		t.Fatalf("recordless diagnostic: %+v", diagnostic)
	}
	wantAbsent(t, diagnostic.Result.RecordPath)
	wantBytes(t, path, "original")
	if !reflect.DeepEqual(before, recoveryDirectoryNames(t, dir)) {
		t.Fatal("recordless rejection created a run")
	}
}

func TestRecoveryErrorDetails(t *testing.T) {
	for _, state := range []State{Restored, Published} {
		t.Run(string(state), func(t *testing.T) {
			dir := publicationFixture(t)
			path := filepath.Join(dir, "source")
			putFile(t, path, "secret-source-body")
			options := Options{path, path, InPlace}
			old, err := Prepare(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			stageBytes(t, old, "secret-stage-body")
			pendingRecoveryRecord(t, old)
			moveRecoveryFile(t, old.source.path, old.backupPath())
			if state == Published {
				moveRecoveryFile(t, old.stage.path, old.output.path)
			}
			closeRun(t, old)
			r, err := prepare(context.Background(), options, func(a int, from string, b int, to string, flags uint) error {
				if to == "record.json" && strings.HasPrefix(from, ".record-recovery-") {
					return unix.EIO
				}
				return unix.Renameat2(a, from, b, to, flags)
			})
			var diagnostic *RecoveryError
			if r != nil || !errors.As(err, &diagnostic) || !errors.Is(err, unix.EIO) {
				t.Fatalf("completion failure: %v %v", r, err)
			}
			want := Result{State: state, RunID: old.runID, RecordPath: filepath.Join(old.runDir.path, "record.json"), OutputPath: path, BackupPath: old.backupPath()}
			if diagnostic.Result != want || diagnostic.Reason == "" {
				t.Fatalf("result lost: %+v; want %+v", diagnostic, want)
			}
			for _, value := range []string{want.RunID, want.RecordPath, want.OutputPath, want.BackupPath, diagnostic.Reason} {
				if !strings.Contains(err.Error(), value) {
					t.Fatalf("diagnostic omitted %q: %v", value, err)
				}
			}
			if strings.Contains(err.Error(), "secret-source-body") || strings.Contains(err.Error(), "secret-stage-body") {
				t.Fatal("file body leaked")
			}
			if state == Restored {
				wantBytes(t, path, "secret-source-body")
				wantAbsent(t, old.backupPath())
				wantBytes(t, old.stage.path, "secret-stage-body")
			} else {
				wantBytes(t, path, "secret-stage-body")
				wantBytes(t, old.backupPath(), "secret-source-body")
				wantAbsent(t, old.stage.path)
			}
			if readOperationRecord(t, want.RecordPath).Phase != preparedPhase {
				t.Fatal("failed record update changed old record")
			}
			for _, name := range recoveryDirectoryNames(t, dir) {
				if strings.HasPrefix(name, ".yakuori-run-") && name != filepath.Base(old.runDir.path) {
					t.Fatal("recovery failure created a new run")
				}
			}
		})
	}
}

func recoveryDirectoryNames(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}
