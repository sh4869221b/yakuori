package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A test-only overlay changes only the opener expression. The real dispatcher
// still creates NotifyContext and calls the actual assembly/Core/Store.
func translationSignalBinary(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("signal fixture source path unavailable")
	}
	dir := filepath.Dir(filename)
	path := filepath.Join(dir, "cli.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const seam = "models.New(paths).OpenSelected"
	if strings.Count(string(raw), seam) != 1 {
		t.Fatal("dispatcher selected-engine seam changed")
	}
	root := t.TempDir()
	replacement := filepath.Join(root, "cli.go")
	if err := os.WriteFile(replacement, []byte(strings.Replace(string(raw), seam, "translationSignalOpen("+seam+")", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	overlay, err := json.Marshal(struct{ Replace map[string]string }{map[string]string{path: replacement}})
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(root, "overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	binary := filepath.Join(root, "signal.test")
	build := exec.CommandContext(ctx, "go", "test", "-c", "-overlay", overlayPath, "-o", binary, ".")
	build.Dir = dir
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("signal helper build: %v\n%s", err, output)
	}
	return binary
}

func TestTranslationSignalCancellation(t *testing.T) {
	binary := translationSignalBinary(t)
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			// Given: subscribe to dedicated ready pipe before starting the child.
			root := t.TempDir()
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
				if err == nil && line != "READY\n" {
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
				if err != nil {
					t.Fatalf("Generate ready: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("Generate ready timed out")
			}
			if err := child.Process.Signal(signal); err != nil {
				t.Fatal(err)
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
			passed := record.Ready && record.Canceled && record.Drained && record.CloseAfterDrain &&
				record.Closed == 1 && record.Generated == 1 && record.Code == ExitFailure &&
				child.ProcessState.ExitCode() == ExitFailure && out.Len() == 0 && len(rows) == 0 &&
				strings.Contains(diag.String(), "localize generate failed") && strings.Contains(diag.String(), "TMCommitted=false")
			evidence := struct {
				translationSignalRecord
				Signal string             `json:"signal"`
				PID    int                `json:"pid"`
				Stdout string             `json:"stdout"`
				Stderr string             `json:"stderr"`
				Rows   []translationTMRow `json:"tm"`
				Pass   bool               `json:"pass"`
			}{record, signal.String(), child.Process.Pid, out.String(), diag.String(), rows, passed}
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
			t.Logf("signal=%s pid=%d ready_before_signal=true canceled=true drained=true closed_after_drain=true exit=1 stdout_bytes=0 tm_rows=0 reaped=true", signal, child.Process.Pid)
		})
	}
}
