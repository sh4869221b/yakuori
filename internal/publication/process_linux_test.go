//go:build linux

package publication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type processNotice struct {
	RunID, Stage, Record, Backup string
	State                        State
	Error                        string
	Snapshot                     string
}

func TestPublicationProcessHelper(t *testing.T) {
	d := os.Getenv("YAKUORI_PUBLICATION_TEST_DIR")
	if d == "" {
		return
	}
	boundary := os.Getenv("YAKUORI_PUBLICATION_TEST_BOUNDARY")
	source, output := filepath.Join(d, "source"), filepath.Join(d, "source")
	mode := InPlace
	if boundary == "race" {
		mode = Create
		output = filepath.Join(d, "output")
	}
	notice := os.NewFile(3, "notice")
	defer notice.Close()
	release := os.NewFile(4, "release")
	defer release.Close()
	encoder := json.NewEncoder(notice)
	r, err := Prepare(context.Background(), Options{source, output, mode})
	if err != nil {
		if boundary == "recover-conflict" {
			var diagnostic *RecoveryError
			if !errors.As(err, &diagnostic) || !errors.Is(err, ErrRecoveryConflict) {
				t.Fatal(err)
			}
			result := diagnostic.Result
			if err := encoder.Encode(processNotice{RunID: result.RunID, Record: result.RecordPath, Backup: result.BackupPath, State: result.State, Error: err.Error()}); err != nil {
				t.Fatal(err)
			}
			return
		}
		t.Fatal(err)
	}
	defer closeRun(t, r)
	if boundary == "recover-conflict" {
		t.Fatal("conflicting recovery continued")
	}
	if boundary == "recover" {
		if err := encoder.Encode(processNotice{RunID: r.runID, Snapshot: string(r.SourceSnapshot())}); err != nil {
			t.Fatal(err)
		}
		return
	}
	stageBytes(t, r, "translated")
	pause := func() {
		t.Helper()
		if err := encoder.Encode(processNotice{RunID: r.runID, Stage: r.stage.path, Record: filepath.Join(r.runDir.path, "record.json"), Backup: r.backupPath()}); err != nil {
			t.Fatal(err)
		}
		var b [1]byte
		if _, err := release.Read(b[:]); err != nil {
			t.Fatal(err)
		}
	}
	r.recordSync = func(f *os.File) error {
		err := f.Sync()
		if err == nil && f == r.runDir.file {
			record := readOperationRecord(t, filepath.Join(r.runDir.path, "record.json"))
			if (record.Phase == preparedPhase && (boundary == "prepared" || boundary == "changed-source")) || (record.Phase == publishedPhase && boundary == "published") {
				pause()
			}
		}
		return err
	}
	r.rename = func(a int, from string, b int, to string, flags uint) error {
		backup, publish := to == r.backupName(), from == r.stage.name
		if (backup && boundary == "before-backup") || (publish && (boundary == "before-publish" || boundary == "race")) {
			pause()
		}
		err := unix.Renameat2(a, from, b, to, flags)
		if err == nil && ((backup && boundary == "after-backup") || (publish && boundary == "after-publish")) {
			pause()
		}
		return err
	}
	result, err := r.Publish(context.Background())
	final := processNotice{State: result.State}
	if err != nil {
		final.Error = err.Error()
	}
	if boundary == "race" && !errors.Is(err, unix.EEXIST) {
		t.Fatalf("race error: %v", err)
	}
	if boundary == "changed-source" && !errors.Is(err, ErrChanged) {
		t.Fatalf("changed source: %v", err)
	}
	if err := encoder.Encode(final); err != nil {
		t.Fatal(err)
	}
}

func startPublicationProcess(t *testing.T, d, boundary string) (*exec.Cmd, *json.Decoder, *os.File) {
	t.Helper()
	notifyRead, notifyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		notifyRead.Close()
		notifyWrite.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPublicationProcessHelper$")
	cmd.Env = append(os.Environ(), "YAKUORI_PUBLICATION_TEST_DIR="+d, "YAKUORI_PUBLICATION_TEST_BOUNDARY="+boundary)
	cmd.ExtraFiles = []*os.File{notifyWrite, releaseRead}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		cancel()
		notifyRead.Close()
		notifyWrite.Close()
		releaseRead.Close()
		releaseWrite.Close()
		t.Fatal(err)
	}
	notifyWrite.Close()
	releaseRead.Close()
	t.Cleanup(func() {
		cancel()
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
		notifyRead.Close()
		releaseWrite.Close()
		if t.Failed() {
			t.Logf("child output: %s", output.String())
		}
	})
	return cmd, json.NewDecoder(notifyRead), releaseWrite
}

func assertProcessLockReleased(t *testing.T, d string) {
	t.Helper()
	f, err := os.Open(d)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("process lock survived exit: %v", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationConcurrentProcess(t *testing.T) {
	for _, boundary := range []string{"changed-source", "race"} {
		t.Run(boundary, func(t *testing.T) {
			d := publicationFixture(t)
			source, output := filepath.Join(d, "source"), filepath.Join(d, "output")
			putFile(t, source, "original")
			cmd, decoder, release := startPublicationProcess(t, d, boundary)
			var notice processNotice
			if err := decoder.Decode(&notice); err != nil {
				t.Fatal(err)
			}
			competing, err := Prepare(context.Background(), Options{source, output, Create})
			if competing != nil {
				closeRun(t, competing)
			}
			if !errors.Is(err, ErrBusy) {
				t.Fatalf("concurrent process not busy: %v", err)
			}
			if boundary == "race" {
				putFile(t, output, "competing")
			} else {
				putFile(t, source, "mutated!")
			}
			if _, err := release.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			var final processNotice
			if err := decoder.Decode(&final); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			if final.State != NotPublished || final.Error == "" {
				t.Fatalf("concurrent change published: %+v", final)
			}
			wantBytes(t, notice.Stage, "translated")
			wantAbsent(t, notice.Backup)
			if boundary == "race" {
				wantBytes(t, source, "original")
				wantBytes(t, output, "competing")
			} else {
				wantBytes(t, source, "mutated!")
				wantAbsent(t, output)
			}
			assertProcessLockReleased(t, d)
		})
	}
}
