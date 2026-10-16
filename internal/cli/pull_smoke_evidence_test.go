//go:build modelsmoke

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

const pullSmokeTimeout = 5 * time.Minute

type pullSmokeProcess struct {
	cmd     *exec.Cmd
	stdout  bytes.Buffer
	stderr  bytes.Buffer
	args    []string
	started time.Time
	timeout time.Duration
}

func (l *pullSmokeLayout) start(t *testing.T, bin, caFile string, args ...string) *pullSmokeProcess {
	t.Helper()
	process := &pullSmokeProcess{args: args, timeout: pullSmokeTimeout}
	process.cmd = exec.Command(bin, args...)
	process.cmd.Dir = l.root
	process.cmd.Env = l.childEnv(caFile)
	process.cmd.Stdout, process.cmd.Stderr = &process.stdout, &process.stderr
	if err := process.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	process.started = time.Now()
	t.Cleanup(func() {
		if process.cmd.ProcessState == nil {
			_ = process.cmd.Process.Kill()
			_, _ = process.cmd.Process.Wait()
		}
	})
	return process
}

func (l *pullSmokeLayout) run(t *testing.T, bin, caFile string, args ...string) pullSmokeRun {
	t.Helper()
	return l.start(t, bin, caFile, args...).finish(t)
}

// finish bounds the child's exit so a product that ignores a signal still ends.
func (p *pullSmokeProcess) finish(t *testing.T) pullSmokeRun {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(p.timeout):
		_ = p.cmd.Process.Kill()
		<-done
		t.Fatalf("product CLI exceeded %s without exiting: yakuori %s", p.timeout, strings.Join(p.args, " "))
	}
	run := pullSmokeRun{
		Args:       p.args,
		ExitCode:   p.cmd.ProcessState.ExitCode(),
		DurationMS: time.Since(p.started).Milliseconds(),
		StartedAt:  p.started.UTC().Format(time.RFC3339Nano),
		Stdout:     p.stdout.String(),
		Stderr:     p.stderr.String(),
	}
	if status, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		run.Signal = status.Signal().String()
	}
	return run
}

func pullSmokeAwait(t *testing.T, event <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(60 * time.Second):
		t.Fatalf("timed out after 60s waiting for %s", what)
	}
}

type pullSmokeCheck struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

type pullSmokeRun struct {
	Args       []string `json:"args"`
	ExitCode   int      `json:"exit_code"`
	Signal     string   `json:"signal,omitempty"`
	DurationMS int64    `json:"duration_ms"`
	StartedAt  string   `json:"started_at"`
	Stdout     string   `json:"stdout"`
	Stderr     string   `json:"stderr"`
}

type pullSmokeEvidence struct {
	Case     string                    `json:"case"`
	Task     string                    `json:"task"`
	Binary   string                    `json:"binary"`
	Model    string                    `json:"model,omitempty"`
	Fixture  map[string]any            `json:"fixture,omitempty"`
	Env      []string                  `json:"child_env"`
	Commands []pullSmokeRun            `json:"commands"`
	States   map[string]pullSmokeState `json:"states"`
	Extra    map[string]any            `json:"extra,omitempty"`
	Checks   []pullSmokeCheck          `json:"checks"`
	Cleanup  map[string]string         `json:"cleanup,omitempty"`
	Layout   map[string]string         `json:"layout"`
}

// recordPullSmokeEvidence writes the artifact before reporting failures, so a
// failing assertion still leaves the raw observation behind.
func recordPullSmokeEvidence(t *testing.T, name string, evidence *pullSmokeEvidence) {
	t.Helper()
	dir := os.Getenv("YAKUORI_PULL_EVIDENCE")
	if dir == "" {
		t.Logf("YAKUORI_PULL_EVIDENCE is unset; the %s evidence artifact was not written", name)
	} else {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		data, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".json"), append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".transcript.txt"), []byte(pullSmokeTranscript(evidence)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, check := range evidence.Checks {
		if !check.Pass {
			t.Errorf("check %s failed: %s", check.Name, check.Detail)
		}
	}
}

func pullSmokeTranscript(evidence *pullSmokeEvidence) string {
	var text strings.Builder
	for _, command := range evidence.Commands {
		fmt.Fprintf(&text, "$ yakuori %s\n[exit=%d signal=%q duration_ms=%d]\n--- stdout · %d bytes ---\n%s--- stderr · %d bytes ---\n%s\n",
			strings.Join(command.Args, " "), command.ExitCode, command.Signal, command.DurationMS,
			len(command.Stdout), command.Stdout, len(command.Stderr), command.Stderr)
	}
	names := make([]string, 0, len(evidence.States))
	for point := range evidence.States {
		names = append(names, point)
	}
	sort.Strings(names)
	for _, point := range names {
		data, err := json.MarshalIndent(evidence.States[point], "", "  ")
		if err != nil {
			continue
		}
		fmt.Fprintf(&text, "--- state %s ---\n%s\n", point, data)
	}
	for _, check := range evidence.Checks {
		fmt.Fprintf(&text, "check %s pass=%t %s\n", check.Name, check.Pass, check.Detail)
	}
	return text.String()
}
