package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/publication"
)

func TestTranslationSignalCancellation(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			// Given: subscribe to dedicated ready pipe before starting the child.
			root := t.TempDir()
			supported := translationPublicationSupported(t, root)
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := reader.Close(); err != nil {
					t.Error(err)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, binary, "-test.run", "^TestTranslationSignalHelper$")
			child.Env = append(os.Environ(), "YAKUORI_SIGNAL_ROOT="+root)
			child.ExtraFiles = []*os.File{writer}
			var out, diag bytes.Buffer
			child.Stdout, child.Stderr = &out, &diag
			ready := make(chan error, 1)
			go func() {
				line, err := bufio.NewReader(reader).ReadString('\n')
				if line != "READY\n" && (err == nil || line != "") {
					err = errors.New("unexpected ready event")
				}
				ready <- err
			}()
			if err := child.Start(); err != nil {
				if closeErr := writer.Close(); closeErr != nil {
					t.Error(closeErr)
				}
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Error(err)
			}
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			reaped := false
			defer func() {
				if !reaped {
					cancel()
					<-done
				}
			}()

			// When: no signal can be sent until Generate has announced readiness.
			select {
			case err := <-ready:
				if supported && err != nil || !supported && !errors.Is(err, io.EOF) {
					t.Fatalf("Generate ready: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("Generate ready timed out")
			}
			if supported {
				if err := child.Process.Signal(signal); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-done:
				reaped = true
			case <-ctx.Done():
				t.Fatal("signal cancellation timed out")
			}

			// Then: real child exit, actual stdout and read-only persistent TM.
			data, err := os.ReadFile(filepath.Join(root, "result.json"))
			if err != nil {
				t.Fatalf("helper result: %v stdout=%q stderr=%q", err, out.String(), diag.String())
			}
			var record translationSignalRecord
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			rows := readTranslationTM(t, record.Paths)
			source, sourceErr := os.ReadFile(filepath.Join(root, "source.txt"))
			if sourceErr != nil || string(source) != "private signal source\n" {
				t.Fatalf("source=%q err=%v", source, sourceErr)
			}
			_, outputErr := os.Lstat(filepath.Join(root, "output.txt"))
			passed := record.Ready && record.Canceled && record.Drained && record.CloseAfterDrain &&
				record.Closed == 1 && record.Generated == 1 && record.Code == ExitFailure &&
				child.ProcessState.ExitCode() == ExitFailure && out.Len() == 0 && len(rows) == 0 &&
				!record.Result.TMCommitted && record.Result.BytesWritten == 0 &&
				record.Result.Publication.State == publication.NotPublished && errors.Is(outputErr, os.ErrNotExist) &&
				!record.UnsupportedFilesystem && record.Phase == "generate" &&
				strings.Contains(diag.String(), "localize generate failed") && strings.Contains(diag.String(), "TMCommitted=false")
			if !supported {
				passed = record.UnsupportedFilesystem && record.Phase == "prepare" &&
					!record.Ready && !record.Canceled && !record.Drained && !record.CloseAfterDrain &&
					record.Closed == 1 && record.Generated == 0 && record.Code == ExitFailure &&
					child.ProcessState.ExitCode() == ExitFailure && out.Len() == 0 && len(rows) == 0 &&
					!record.Result.TMCommitted && record.Result.BytesWritten == 0 &&
					record.Result.Publication.State == publication.NotPublished && errors.Is(outputErr, os.ErrNotExist) &&
					strings.Contains(diag.String(), "TMCommitted=false")
			}
			evidence := struct {
				translationSignalRecord
				Signal     string             `json:"signal"`
				SignalSent bool               `json:"signal_sent"`
				PID        int                `json:"pid"`
				Stdout     string             `json:"stdout"`
				Stderr     string             `json:"stderr"`
				Rows       []translationTMRow `json:"tm"`
				Pass       bool               `json:"pass"`
			}{record, signal.String(), supported, child.Process.Pid, out.String(), diag.String(), rows, passed}
			if dir := os.Getenv("YAKUORI_SIGNAL_EVIDENCE"); dir != "" {
				data, err := json.MarshalIndent(evidence, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "signal-"+signal.String()+".json"), append(data, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if !passed {
				t.Fatalf("signal=%s evidence=%+v", signal, evidence)
			}
			if !supported {
				t.Logf("requested_signal=%s signal_sent=false typed ErrUnsupportedFilesystem refusal; generated=0 closed=1 tm_rows=0 reaped=true; signal cancellation not exercised", signal)
				return
			}
			t.Logf("signal=%s pid=%d ready_before_signal=true canceled=true drained=true closed_after_drain=true exit=1 stdout_bytes=0 tm_rows=0 reaped=true", signal, child.Process.Pid)
		})
	}
}
