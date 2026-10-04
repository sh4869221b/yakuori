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

func pendingRecoveryRecord(t *testing.T, r *Run) operationRecord {
	t.Helper()
	if err := r.writeRecord(preparedPhase, "none"); err != nil {
		t.Fatal(err)
	}
	record, err := readRecord(r.runDir)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func moveRecoveryFile(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryDecisionTable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     Mode
		existing bool
		stop     string
		state    State
		phase    recordPhase
	}{
		{"create-pending", Create, false, "prepared", NotPublished, abandonedPhase},
		{"create-published", Create, false, "published", Published, publishedPhase},
		{"replace-existing-pending", Replace, true, "prepared", NotPublished, abandonedPhase},
		{"replace-absent-pending", Replace, false, "prepared", NotPublished, abandonedPhase},
		{"replace-published", Replace, true, "published", Published, publishedPhase},
		{"in-place-unmoved", InPlace, false, "prepared", NotPublished, abandonedPhase},
		{"in-place-stale-prepared", InPlace, false, "backup", Restored, restoredPhase},
		{"in-place-backed-up", InPlace, false, "backed-up", Restored, restoredPhase},
		{"in-place-missing-stage", InPlace, false, "missing-stage", Restored, restoredPhase},
		{"in-place-published-stale", InPlace, false, "published", Published, publishedPhase},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := publishFixture(t, tc.mode, tc.existing)
			record := pendingRecoveryRecord(t, r)
			if tc.mode == InPlace && tc.stop != "prepared" {
				moveRecoveryFile(t, r.source.path, r.backupPath())
			}
			switch tc.stop {
			case "backed-up":
				if err := r.writeRecord(backedUpPhase, "backup-rename"); err != nil {
					t.Fatal(err)
				}
				var err error
				record, err = readRecord(r.runDir)
				if err != nil {
					t.Fatal(err)
				}
			case "published":
				moveRecoveryFile(t, r.stage.path, r.output.path)
			case "missing-stage":
				moveRecoveryFile(t, r.stage.path, filepath.Join(r.runDir.path, "retained-stage"))
			}
			result, err := r.reconcileRecord(record, r.runDir)
			if err != nil || result.State != tc.state || result.RunID != r.runID || result.RecordPath != filepath.Join(r.runDir.path, "record.json") {
				t.Fatalf("decision: %+v %v; want %s", result, err, tc.state)
			}
			if got := readOperationRecord(t, result.RecordPath); got.Phase != tc.phase {
				t.Fatalf("terminal phase %s; want %s", got.Phase, tc.phase)
			}
			if tc.state == Published {
				wantBytes(t, r.output.path, "translated")
				wantAbsent(t, r.stage.path)
			} else {
				wantBytes(t, r.source.path, "original")
				if tc.stop == "missing-stage" {
					wantBytes(t, filepath.Join(r.runDir.path, "retained-stage"), "translated")
				} else {
					wantBytes(t, r.stage.path, "translated")
				}
			}
			if tc.mode == InPlace && tc.state == Published {
				wantBytes(t, r.backupPath(), "original")
			} else {
				wantAbsent(t, r.backupPath())
			}
			if tc.mode != InPlace {
				wantBytes(t, r.source.path, "original")
				if tc.state != Published {
					if tc.existing {
						wantBytes(t, r.output.path, "existing")
					} else {
						wantAbsent(t, r.output.path)
					}
				}
			}
		})
	}
	for _, stop := range []string{"prepared", "backup", "published"} {
		t.Run("same-hash-"+stop, func(t *testing.T) {
			d := publicationFixture(t)
			source := filepath.Join(d, "source")
			putFile(t, source, "original")
			r, err := Prepare(context.Background(), Options{source, source, InPlace})
			if err != nil {
				t.Fatal(err)
			}
			defer closeRun(t, r)
			stageBytes(t, r, "original")
			record := pendingRecoveryRecord(t, r)
			if record.Source.SHA256 != record.Stage.SHA256 || record.Source.Identity.Ino == record.Stage.Identity.Ino {
				t.Fatal("fixture requires equal hashes and distinct inodes")
			}
			wantState := NotPublished
			if stop != "prepared" {
				moveRecoveryFile(t, source, r.backupPath())
				wantState = Restored
			}
			if stop == "published" {
				moveRecoveryFile(t, r.stage.path, source)
				wantState = Published
			}
			result, err := r.reconcileRecord(record, r.runDir)
			if err != nil || result.State != wantState {
				t.Fatalf("same hashes misclassified: %+v %v", result, err)
			}
			wantBytes(t, source, "original")
			if wantState == Published {
				wantBytes(t, r.backupPath(), "original")
				wantAbsent(t, r.stage.path)
			} else {
				wantAbsent(t, r.backupPath())
				wantBytes(t, r.stage.path, "original")
			}
		})
	}
}

func TestRecoveryRejectsConflicts(t *testing.T) {
	for _, name := range []string{"final-other", "backup-other", "stage-other", "stage-symlink", "stage-hardlink", "backup-hardlink", "parent-identity", "lock-absent", "ordinary-source", "ordinary-output"} {
		t.Run(name, func(t *testing.T) {
			mode := InPlace
			if name == "ordinary-source" || name == "ordinary-output" {
				mode = Replace
			}
			r := publishFixture(t, mode, true)
			record := pendingRecoveryRecord(t, r)
			if mode == InPlace {
				moveRecoveryFile(t, r.source.path, r.backupPath())
			}
			backup, stage, source, final := "original", "translated", "original", "existing"
			switch name {
			case "final-other":
				final = "foreign"
				putFile(t, r.output.path, final)
			case "backup-other":
				backup = "foreign"
				putFile(t, r.backupPath(), backup)
			case "stage-other":
				stage = "foreign"
				putFile(t, r.stage.path, stage)
			case "stage-symlink":
				other := filepath.Join(r.runDir.path, "retained-stage")
				moveRecoveryFile(t, r.stage.path, other)
				if err := os.Symlink(other, r.stage.path); err != nil {
					t.Fatal(err)
				}
			case "stage-hardlink":
				if err := os.Link(r.stage.path, filepath.Join(r.runDir.path, "stage-link")); err != nil {
					t.Fatal(err)
				}
			case "backup-hardlink":
				if err := os.Link(r.backupPath(), filepath.Join(r.source.parent.path, "backup-link")); err != nil {
					t.Fatal(err)
				}
			case "parent-identity":
				record.SourceParent.Ino++
				record.OutputParent.Ino++
			case "lock-absent":
				r.locks = nil
			case "ordinary-source":
				source = "foreign"
				putFile(t, r.source.path, source)
			case "ordinary-output":
				final = "foreign"
				putFile(t, r.output.path, final)
			}
			path := filepath.Join(r.runDir.path, "record.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			result, err := r.reconcileRecord(record, r.runDir)
			if !errors.Is(err, ErrRecoveryConflict) {
				t.Fatalf("conflict accepted: %+v %v", result, err)
			}
			wantBytes(t, path, string(before))
			wantBytes(t, r.stage.path, stage)
			if mode == InPlace {
				wantBytes(t, r.backupPath(), backup)
				if name == "final-other" {
					wantBytes(t, r.output.path, final)
				} else {
					wantAbsent(t, r.output.path)
				}
			} else {
				wantBytes(t, r.source.path, source)
				wantBytes(t, r.output.path, final)
			}
		})
	}
}

func TestRecoveryNeverClobbersNewWriter(t *testing.T) {
	r := publishFixture(t, InPlace, false)
	record := pendingRecoveryRecord(t, r)
	moveRecoveryFile(t, r.source.path, r.backupPath())
	r.rename = func(a int, from string, b int, to string, flags uint) error {
		if from == r.backupName() {
			if flags != unix.RENAME_NOREPLACE {
				t.Fatalf("restore flags %d", flags)
			}
			putFile(t, r.output.path, "new writer")
		}
		return unix.Renameat2(a, from, b, to, flags)
	}
	result, err := r.reconcileRecord(record, r.runDir)
	if !errors.Is(err, unix.EEXIST) || !errors.Is(err, ErrRecoveryConflict) || result.State != SourceBackedUp {
		t.Fatalf("racing restore: %+v %v", result, err)
	}
	wantBytes(t, r.output.path, "new writer")
	wantBytes(t, r.backupPath(), "original")
	wantBytes(t, r.stage.path, "translated")
	if readOperationRecord(t, result.RecordPath).Phase != preparedPhase {
		t.Fatal("conflict changed record")
	}
}
