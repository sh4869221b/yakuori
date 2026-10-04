//go:build linux

package publication

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPublicationStoppedProcess(t *testing.T) {
	for _, boundary := range []string{"prepared", "before-backup", "after-backup", "before-publish", "after-publish", "published"} {
		for _, original := range []string{"original", "translated"} {
			t.Run(boundary+"/"+original, func(t *testing.T) {
				d := publicationFixture(t)
				source := filepath.Join(d, "source")
				putFile(t, source, original)
				cmd, decoder, _ := startPublicationProcess(t, d, boundary)
				var notice processNotice
				if err := decoder.Decode(&notice); err != nil {
					t.Fatal(err)
				}
				killPublicationProcess(t, cmd)
				phase, resolvedPhase, wantSnapshot := preparedPhase, abandonedPhase, original
				switch boundary {
				case "prepared", "before-backup":
					wantBytes(t, source, original)
					wantBytes(t, notice.Stage, "translated")
					wantAbsent(t, notice.Backup)
				case "after-backup", "before-publish":
					wantAbsent(t, source)
					wantBytes(t, notice.Backup, original)
					wantBytes(t, notice.Stage, "translated")
					resolvedPhase = restoredPhase
					if boundary == "before-publish" {
						phase = backedUpPhase
					}
				case "after-publish", "published":
					wantBytes(t, source, "translated")
					wantBytes(t, notice.Backup, original)
					wantAbsent(t, notice.Stage)
					phase, resolvedPhase, wantSnapshot = backedUpPhase, publishedPhase, "translated"
					if boundary == "published" {
						phase = publishedPhase
					}
				}
				record := readOperationRecord(t, notice.Record)
				if record.Phase != phase || record.RunID != notice.RunID {
					t.Fatalf("wrong phase at stop: %+v", record)
				}
				assertProcessLockReleased(t, d)
				next, decoder, _ := startPublicationProcess(t, d, "recover")
				var recovered processNotice
				if err := decoder.Decode(&recovered); err != nil {
					t.Fatal(err)
				}
				if err := next.Wait(); err != nil {
					t.Fatal(err)
				}
				if next.Process.Pid == cmd.Process.Pid || recovered.RunID == notice.RunID || recovered.RunID == "" || recovered.Snapshot != wantSnapshot || recovered.Error != "" {
					t.Fatalf("new-process snapshot: %+v; want %q", recovered, wantSnapshot)
				}
				resolved := readOperationRecord(t, notice.Record)
				if resolved.Phase != resolvedPhase || resolved.RunID != notice.RunID {
					t.Fatalf("new-process record: %+v; want %s", resolved, resolvedPhase)
				}
				wantBytes(t, source, wantSnapshot)
				if resolvedPhase == publishedPhase {
					wantBytes(t, notice.Backup, original)
					wantAbsent(t, notice.Stage)
				} else {
					wantAbsent(t, notice.Backup)
					wantBytes(t, notice.Stage, "translated")
				}
				assertProcessLockReleased(t, d)
				t.Logf("SIGKILL pid=%d; recovery pid=%d snapshot=%q old phase=%s", cmd.Process.Pid, next.Process.Pid, recovered.Snapshot, resolved.Phase)
			})
		}
	}
}

func TestPublicationRecoveryConflictProcess(t *testing.T) {
	for _, boundary := range []string{"after-backup", "before-publish"} {
		t.Run(boundary, func(t *testing.T) {
			d := publicationFixture(t)
			source := filepath.Join(d, "source")
			putFile(t, source, "original")
			cmd, decoder, _ := startPublicationProcess(t, d, boundary)
			var notice processNotice
			if err := decoder.Decode(&notice); err != nil {
				t.Fatal(err)
			}
			killPublicationProcess(t, cmd)
			before, err := os.ReadFile(notice.Record)
			if err != nil {
				t.Fatal(err)
			}
			putFile(t, source, "external final")
			next, decoder, _ := startPublicationProcess(t, d, "recover-conflict")
			var recovered processNotice
			if err := decoder.Decode(&recovered); err != nil {
				t.Fatal(err)
			}
			if err := next.Wait(); err != nil {
				t.Fatal(err)
			}
			if next.Process.Pid == cmd.Process.Pid || recovered.RunID != notice.RunID || recovered.Record != notice.Record || recovered.Backup != notice.Backup || recovered.State != SourceBackedUp || recovered.Error == "" || recovered.Snapshot != "" {
				t.Fatalf("conflict diagnostic: %+v", recovered)
			}
			wantBytes(t, source, "external final")
			wantBytes(t, notice.Backup, "original")
			wantBytes(t, notice.Stage, "translated")
			wantBytes(t, notice.Record, string(before))
			assertProcessLockReleased(t, d)
			t.Logf("SIGKILL pid=%d; conflict recovery pid=%d state=%s; final/backup/stage/record unchanged", cmd.Process.Pid, next.Process.Pid, recovered.State)
		})
	}
}

func killPublicationProcess(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	var exit *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exit) {
		t.Fatalf("child was not killed: %v", err)
	}
	status, ok := exit.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child termination was not SIGKILL: %v", exit.ProcessState)
	}
}
