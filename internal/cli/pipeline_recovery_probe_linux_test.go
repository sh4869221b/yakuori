//go:build linux && pipelinefaults

package cli

import (
	"bytes"
	"io"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPipelineCLIProcessRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, mode, event string
		committed         bool
	}{
		{"before-commit", "initial", "generate-ready", false},
		{"before-write", "before-write", "stdout-write", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := signalCase{Backend: "cpu", Surface: "text-translate-stdout", Row: "sigterm", Format: "text", Mode: "stdout"}
			f := newSignalFixture(t, t.TempDir(), c)
			run := startSignalChild(t, f.childRequest(c, tc.mode), signalWatchdog)
			if _, err := run.await(tc.event); err != nil {
				t.Fatal(err)
			}
			if err := run.cmd.Process.Signal(syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			observation, err := run.finish()
			if err != nil {
				t.Fatal(err)
			}
			if !observation.Signaled || observation.Signal != syscall.SIGKILL || observation.Rescues != 0 || observation.Waits != 1 || len(observation.Stdout) != 0 {
				t.Fatalf("kill: %+v", observation)
			}
			want := f.D0
			if tc.committed {
				want = f.D1
			}
			if actual := readTranslationTM(t, f.Paths); !signalRowsEqual(want, actual) {
				t.Fatalf("rows mismatch actual=%+v want=%+v", actual, want)
			}
			t.Logf("boundary=%s pid=%d SIGKILL stdout=0 rows=%d", tc.event, run.cmd.Process.Pid, len(want))
			rerun := startSignalChild(t, f.childRequest(c, "rerun"), signalWatchdog)
			after, err := rerun.finish()
			if err != nil {
				t.Fatal(err)
			}
			generated := 1
			if tc.committed {
				generated = 0
			}
			if rerun.cmd.Process.Pid == run.cmd.Process.Pid || after.ExitCode != ExitOK || after.Rescues != 0 || after.Waits != 1 ||
				!bytes.Equal(after.Stdout, []byte(cliAuthoredTranslation)) || len(after.Notices) != 1 ||
				after.Notices[0].Event != "final" || after.Notices[0].Generated != generated ||
				!signalRowsEqual(f.D1, readTranslationTM(t, f.Paths)) {
				t.Fatalf("rerun pid=%d generated want=%d observation=%+v", rerun.cmd.Process.Pid, generated, after)
			}
			t.Logf("rerun pid=%d generated=%d stdout=%d D1 rows=%d", rerun.cmd.Process.Pid, generated, len(after.Stdout), len(f.D1))
		})
	}
	t.Run("partial-write", testPipelineStdoutPartialWrite)
}

func testPipelineStdoutPartialWrite(t *testing.T) {
	c := signalCase{Backend: "cpu", Surface: "text-translate-stdout", Row: "sigterm", Format: "text", Mode: "stdout"}
	f := newSignalFixture(t, t.TempDir(), c)
	target := []byte(strings.Repeat("訳文。", 4096))
	// The ordering of source identities is not tied to the sentinel's index.
	for i := range f.D1 {
		if f.D1[i].Text == cliAuthoredTranslation {
			f.D1[i].Text = string(target)
		}
	}
	run := startSignalChild(t, f.childRequest(c, "stdout-probe"), signalWatchdog)
	capacity, err := unix.FcntlInt(run.stdoutPipe.Fd(), unix.F_GETPIPE_SZ, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.await("stdout-write"); err != nil {
		t.Fatal(err)
	}
	prefix := make([]byte, 1)
	if _, err := io.ReadFull(run.stdoutPipe, prefix); err != nil {
		t.Fatal(err)
	}
	if len(target) <= capacity+len(prefix) || !bytes.Equal(prefix, target[:len(prefix)]) {
		t.Fatalf("target=%d capacity=%d prefix=%x", len(target), capacity, prefix)
	}
	if err := run.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	observation, err := run.finish()
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Signaled || observation.Signal != syscall.SIGKILL || observation.Rescues != 0 || observation.Waits != 1 {
		t.Fatalf("kill: %+v", observation)
	}
	if actual := readTranslationTM(t, f.Paths); !signalRowsEqual(f.D1, actual) {
		t.Fatalf("D1 mismatch actual=%+v", actual)
	}
	t.Logf("stdout write pid=%d SIGKILL capacity=%d output=%d prefix=%x; D1 rows=%d", run.cmd.Process.Pid, capacity, len(target), prefix, len(f.D1))
	rerun := startSignalChild(t, f.childRequest(c, "rerun"), signalWatchdog)
	after, err := rerun.finish()
	if err != nil {
		t.Fatal(err)
	}
	if after.ExitCode != 0 || !bytes.Equal(after.Stdout, target) || rerun.cmd.Process.Pid == run.cmd.Process.Pid {
		t.Fatalf("rerun exit=%d output=%d stderr=%s", after.ExitCode, len(after.Stdout), after.Stderr)
	}
	if len(after.Notices) != 1 || after.Notices[0].Event != "final" || after.Notices[0].Generated != 0 || !signalRowsEqual(f.D1, readTranslationTM(t, f.Paths)) {
		t.Fatalf("rerun did not reuse and revalidate TM: notices=%+v", after.Notices)
	}
	t.Logf("rerun pid=%d output=%d; prefix retained=%x", rerun.cmd.Process.Pid, len(after.Stdout), prefix)
}
