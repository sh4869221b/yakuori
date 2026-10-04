//go:build linux

package publication

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareRecoveryBeforeSnapshot(t *testing.T) {
	for _, name := range []string{"restore", "published", "different-target", "changed-mode"} {
		t.Run(name, func(t *testing.T) {
			oldMode := InPlace
			if name == "changed-mode" {
				oldMode = Create
			}
			old := publishFixture(t, oldMode, false)
			pendingRecoveryRecord(t, old)
			if oldMode == InPlace {
				moveRecoveryFile(t, old.source.path, old.backupPath())
			}
			if name == "published" {
				moveRecoveryFile(t, old.stage.path, old.output.path)
			}
			closeRun(t, old)
			var other *Run
			if name == "different-target" {
				path := filepath.Join(old.source.parent.path, "other-source")
				putFile(t, path, "other original")
				var err error
				other, err = Prepare(context.Background(), Options{path, path, InPlace})
				if err != nil {
					t.Fatal(err)
				}
				stageBytes(t, other, "other translated")
				pendingRecoveryRecord(t, other)
				closeRun(t, other)
			}
			mode := oldMode
			if name == "changed-mode" {
				mode = Replace
			}
			r, err := Prepare(context.Background(), Options{old.source.path, old.output.path, mode})
			if err != nil {
				t.Fatal(err)
			}
			defer closeRun(t, r)
			wantSnapshot, wantPhase := "original", restoredPhase
			if name == "published" {
				wantSnapshot, wantPhase = "translated", publishedPhase
			}
			if name == "changed-mode" {
				wantPhase = abandonedPhase
			}
			if string(r.SourceSnapshot()) != wantSnapshot || r.runID == old.runID {
				t.Fatalf("snapshot=%q run=%s", r.SourceSnapshot(), r.runID)
			}
			if readOperationRecord(t, filepath.Join(old.runDir.path, "record.json")).Phase != wantPhase {
				t.Fatal("old record was not resolved")
			}
			if name == "published" {
				wantBytes(t, old.backupPath(), "original")
				wantAbsent(t, old.stage.path)
			} else {
				wantAbsent(t, old.backupPath())
				wantBytes(t, old.stage.path, "translated")
			}
			if name == "changed-mode" {
				wantAbsent(t, old.output.path)
			} else {
				wantBytes(t, old.output.path, wantSnapshot)
			}
			if other != nil {
				wantBytes(t, other.source.path, "other original")
				wantBytes(t, other.stage.path, "other translated")
				if readOperationRecord(t, filepath.Join(other.runDir.path, "record.json")).Phase != preparedPhase {
					t.Fatal("different target record changed")
				}
			}
		})
	}
}

func TestPrepareAfterResolvedHistory(t *testing.T) {
	for _, stop := range []string{"prepared", "backup", "published"} {
		t.Run(stop, func(t *testing.T) {
			old := publishFixture(t, InPlace, false)
			record := pendingRecoveryRecord(t, old)
			if stop != "prepared" {
				moveRecoveryFile(t, old.source.path, old.backupPath())
			}
			if stop == "published" {
				moveRecoveryFile(t, old.stage.path, old.output.path)
			}
			resolved, err := old.reconcileRecord(record, old.runDir)
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(resolved.RecordPath)
			if err != nil {
				t.Fatal(err)
			}
			closeRun(t, old)
			wantSnapshot := "original"
			if stop == "published" {
				wantSnapshot = "translated"
			}
			for _, content := range []string{"next-one", "next-two"} {
				r, err := Prepare(context.Background(), Options{old.source.path, old.output.path, InPlace})
				if err != nil {
					t.Fatal(err)
				}
				if string(r.SourceSnapshot()) != wantSnapshot {
					t.Fatalf("later snapshot %q; want %q", r.SourceSnapshot(), wantSnapshot)
				}
				stageBytes(t, r, content)
				result, err := r.Publish(context.Background())
				if err != nil || result.State != Published {
					t.Fatalf("later publication: %+v %v", result, err)
				}
				wantBytes(t, result.BackupPath, wantSnapshot)
				closeRun(t, r)
				wantSnapshot = content
			}
			wantBytes(t, old.output.path, "next-two")
			wantBytes(t, resolved.RecordPath, string(before))
			if stop == "published" {
				wantBytes(t, old.backupPath(), "original")
				wantAbsent(t, old.stage.path)
			} else {
				wantAbsent(t, old.backupPath())
				wantBytes(t, old.stage.path, "translated")
			}
		})
	}
}
