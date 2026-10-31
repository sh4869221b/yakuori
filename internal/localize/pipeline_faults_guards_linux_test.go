//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// pipelineBarrierGuardRunnerWindow bounds only the monitoring of the guard
// runner process. The stimulus is always an explicit cancel or a real child
// exit, never elapsed time.
const pipelineBarrierGuardRunnerWindow = 120 * time.Second

type pipelineBarrierGuardObserved struct {
	Case           string
	ExpectedClass  string
	Slug           string
	RunnerExit     int
	RunnerSignaled bool
	Cleanup        pipelineBarrierCleanup
	Record         pipelineBarrierRecord
	Note           string `json:",omitempty"`
}

type pipelineBarrierGuardCaseSpec struct {
	Slug          string
	ExpectedClass string
	Spec          pipelineBarrierGuardCase
}

func pipelineBarrierRequireClean(t *testing.T, cleanup pipelineBarrierCleanup) {
	t.Helper()
	if cleanup.WaitCalls != 1 || cleanup.CleanupRescueKills != 0 || !cleanup.Waited ||
		!cleanup.PIDAbsent || !cleanup.GroupAbsent || !cleanup.ProcAbsent ||
		!cleanup.ReadEOF || cleanup.ResidualNotices != 0 {
		t.Fatalf("cleanup mismatch: %+v", cleanup)
	}
}

func pipelineBarrierRequireRescued(t *testing.T, cleanup pipelineBarrierCleanup) {
	t.Helper()
	if cleanup.WaitCalls != 1 || cleanup.CleanupRescueKills != 1 || !cleanup.Waited ||
		!cleanup.Signaled || cleanup.Signal != syscall.SIGKILL ||
		!cleanup.PIDAbsent || !cleanup.GroupAbsent || !cleanup.ProcAbsent ||
		!cleanup.ReadEOF || cleanup.ResidualNotices != 0 {
		t.Fatalf("cleanup-rescue record mismatch: %+v", cleanup)
	}
}

// pipelineBarrierRequireAbsent re-proves from the guard test process that the
// child which actually performed the wire exchange is gone.
func pipelineBarrierRequireAbsent(t *testing.T, pid int) {
	t.Helper()
	if pid <= 0 {
		t.Fatalf("missing child pid %d", pid)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child pid=%d pid probe = %v, want ESRCH", pid, err)
	}
	if err := syscall.Kill(-pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child group pid=%d probe = %v, want ESRCH", pid, err)
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child /proc/%d = %v, want ENOENT", pid, err)
	}
}

func pipelineBarrierEnvWithout(keys ...string) []string {
	env := os.Environ()
	for _, key := range keys {
		prefix := key + "="
		for i := 0; i < len(env); i++ {
			if strings.HasPrefix(env[i], prefix) {
				env = append(env[:i], env[i+1:]...)
				i--
			}
		}
	}
	return env
}

func pipelineBarrierGuardEvidenceDir(t *testing.T, slug string) string {
	t.Helper()
	root := os.Getenv("YAKUORI_PIPELINE_EVIDENCE")
	if root == "" {
		root = t.TempDir()
	}
	dir := filepath.Join(root, "guards", slug)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func pipelineBarrierLoadJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("guard evidence %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, value); err != nil {
		t.Fatalf("guard evidence %s: %v", path, err)
	}
}

// pipelineBarrierRunGuardRunner executes the scheduled case in a real child
// process of this test binary. A genuine scheduled-test failure is a nonzero
// exit; a monitor kill is reported separately so it can never be mistaken for
// guard detection.
func pipelineBarrierRunGuardRunner(t *testing.T, item pipelineBarrierGuardCaseSpec, dir string) (string, int, bool) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(item.Spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), pipelineBarrierGuardRunnerWindow)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestPipelineHarnessScheduledCase$", "-test.v")
	cmd.Env = append(pipelineBarrierEnvWithout(pipelineBarrierGuardCaseEnv, "YAKUORI_PIPELINE_EVIDENCE"),
		pipelineBarrierGuardCaseEnv+"="+string(payload), "YAKUORI_PIPELINE_EVIDENCE="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		t.Fatalf("guard runner %s monitor ended (%v) before the child exited: wait=%v\n%s", item.Slug, ctx.Err(), waitErr, output.String())
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("guard runner %s has no Linux WaitStatus: %v", item.Slug, cmd.ProcessState)
	}
	exit := -1
	if status.Exited() {
		exit = status.ExitStatus()
	}
	if err := os.WriteFile(filepath.Join(dir, "runner.log"), output.Bytes(), 0600); err != nil {
		t.Fatalf("guard runner log for %s: %v", item.Slug, err)
	}
	t.Logf("guard runner %s pid=%d exit=%d signaled=%t signal=%s wait=%v", item.Slug, cmd.Process.Pid, exit, status.Signaled(), status.Signal(), waitErr)
	return output.String(), exit, status.Signaled()
}

func pipelineBarrierRequireGuardOutcome(t *testing.T, item pipelineBarrierGuardCaseSpec, output string, exit int, signaled bool) {
	t.Helper()
	caseName := item.Spec.Plan.Case
	if signaled {
		t.Fatalf("guard runner %s was killed instead of failing as a scheduled test\n%s", item.Slug, output)
	}
	if item.ExpectedClass == "" {
		if exit != 0 {
			t.Fatalf("positive control %s exited %d, want 0\n%s", caseName, exit, output)
		}
		if !strings.Contains(output, "scheduled case "+caseName+" accepted") {
			t.Fatalf("positive control %s reported no acceptance\n%s", caseName, output)
		}
		return
	}
	if exit == 0 {
		t.Fatalf("guard runner %s exited 0 for scheduled case %s, want nonzero with typed reason %s\n%s", item.Slug, caseName, item.ExpectedClass, output)
	}
	want := "scheduled case " + caseName + " rejected: " + item.ExpectedClass + ":"
	if item.ExpectedClass == pipelineBarrierCleanupRescue {
		want = pipelineBarrierCleanupRescue + ": scheduled case " + caseName + " relied on registered cleanup"
		if strings.Contains(output, "accepted") {
			t.Fatalf("cleanup-rescue runner %s reported acceptance although it never reaped its child\n%s", item.Slug, output)
		}
	}
	if !strings.Contains(output, want) {
		t.Fatalf("guard runner %s output missing %q\n%s", item.Slug, want, output)
	}
}

func pipelineBarrierReadGuardEvidence(t *testing.T, item pipelineBarrierGuardCaseSpec, dir string) (pipelineBarrierCleanup, pipelineBarrierRecord) {
	t.Helper()
	if item.ExpectedClass == pipelineBarrierCleanupRescue {
		var result pipelineBarrierRescueResult
		pipelineBarrierLoadJSON(t, filepath.Join(dir, "cleanup-rescue.json"), &result)
		if result.Case != item.Spec.Plan.Case {
			t.Fatalf("cleanup-rescue evidence case = %q, want %q", result.Case, item.Spec.Plan.Case)
		}
		return result.Cleanup, pipelineBarrierRecord{}
	}
	var result pipelineBarrierScheduledResult
	pipelineBarrierLoadJSON(t, filepath.Join(dir, "case-result.json"), &result)
	if result.Spec.Plan.Case != item.Spec.Plan.Case || result.Spec.Mode != item.Spec.Mode || result.Spec.PreWait != item.Spec.PreWait {
		t.Fatalf("case evidence spec = %+v, want %+v", result.Spec, item.Spec)
	}
	return result.Cleanup, result.Record
}

// TestPipelineHarnessGuards executes every scheduled case in a real child
// process. The child applies the same acceptance decision a product case uses,
// so a guard failure ends in a genuine nonzero exit carrying the typed class.
// The parent checks that exit and class, then proves from its own process that
// the child's real Wait left no pid, group or /proc entry behind. A synthetic
// record is never accepted as proof for a mandatory negative.
func TestPipelineHarnessGuards(t *testing.T) {
	cases := []pipelineBarrierGuardCaseSpec{
		{
			Slug: "healthy-positive-control",
			Spec: pipelineBarrierGuardCase{Plan: pipelineBarrierPlan{Case: "guard-healthy", Boundary: "tm-commit.before", Occurrence: 1, Inject: true}, Mode: pipelineBarrierModeHappy},
		},
		{
			Slug: "wrong-boundary", ExpectedClass: pipelineBarrierWrongBoundary,
			Spec: pipelineBarrierGuardCase{Plan: pipelineBarrierPlan{Case: "guard-wrong-boundary", Boundary: "stage.close.after", Occurrence: 2, Inject: true}, Mode: pipelineBarrierModeWrong},
		},
		{
			Slug: "unreached", ExpectedClass: pipelineBarrierUnreached,
			Spec: pipelineBarrierGuardCase{Plan: pipelineBarrierPlan{Case: "guard-unreached", Boundary: "stage.write.after", Occurrence: 1, Inject: true}, Mode: pipelineBarrierModeNoReady},
		},
		{
			Slug: "uninjected", ExpectedClass: pipelineBarrierUninjected,
			Spec: pipelineBarrierGuardCase{Plan: pipelineBarrierPlan{Case: "guard-uninjected", Boundary: "stage.sync.after", Occurrence: 3, Inject: true}, Mode: pipelineBarrierModeNoInject},
		},
		{
			Slug: "early-return", ExpectedClass: pipelineBarrierEarlyReturn,
			Spec: pipelineBarrierGuardCase{Plan: pipelineBarrierPlan{Case: "guard-early-return", Boundary: "record.close.after", Occurrence: 1, Inject: true}, Mode: pipelineBarrierModeEarlyExit},
		},
		{
			Slug: "timeout-explicit-cancel", ExpectedClass: pipelineBarrierTimeout,
			Spec: pipelineBarrierGuardCase{Plan: pipelineBarrierPlan{Case: "guard-timeout", Boundary: "observe.before", Occurrence: 1, Inject: true}, Mode: pipelineBarrierModeHang},
		},
		{
			Slug: "unreaped", ExpectedClass: pipelineBarrierUnreaped,
			Spec: pipelineBarrierGuardCase{Plan: pipelineBarrierPlan{Case: "guard-unreaped", Boundary: "backup-rename.after", Occurrence: 1, Inject: true}, Mode: pipelineBarrierModeHappy, PreWait: true},
		},
		{
			Slug: "malformed-input", ExpectedClass: pipelineBarrierEarlyReturn,
			Spec: pipelineBarrierGuardCase{Plan: pipelineBarrierPlan{Case: "guard-malformed", Boundary: "tm-commit.after", Occurrence: 1, Inject: true}, Mode: pipelineBarrierModeMalformed},
		},
		{
			Slug: "cleanup-rescue", ExpectedClass: pipelineBarrierCleanupRescue,
			Spec: pipelineBarrierGuardCase{Plan: pipelineBarrierPlan{Case: "guard-cleanup-rescue", Boundary: "publish-rename.after", Occurrence: 1, Inject: true}, Mode: pipelineBarrierModeRescue},
		},
	}
	if len(cases) != 9 {
		t.Fatalf("scheduled guard cases = %d, want 9", len(cases))
	}
	observed := make([]pipelineBarrierGuardObserved, 0, len(cases))
	for _, item := range cases {
		item := item
		t.Run(item.Slug, func(t *testing.T) {
			dir := pipelineBarrierGuardEvidenceDir(t, item.Slug)
			output, exit, signaled := pipelineBarrierRunGuardRunner(t, item, dir)
			pipelineBarrierRequireGuardOutcome(t, item, output, exit, signaled)
			cleanup, record := pipelineBarrierReadGuardEvidence(t, item, dir)
			if item.ExpectedClass == pipelineBarrierCleanupRescue {
				pipelineBarrierRequireRescued(t, cleanup)
			} else {
				pipelineBarrierRequireClean(t, cleanup)
			}
			pipelineBarrierRequireAbsent(t, cleanup.PID)
			if item.ExpectedClass == "" {
				if failure := record.failure(); failure != nil {
					t.Fatalf("%s recorded lifecycle = %v, want nil after the real Wait", item.Spec.Plan.Case, failure)
				}
			}
			observed = append(observed, pipelineBarrierGuardObserved{
				Case: item.Spec.Plan.Case, ExpectedClass: item.ExpectedClass, Slug: item.Slug,
				RunnerExit: exit, RunnerSignaled: signaled, Cleanup: cleanup, Record: record,
				Note: "scheduled test executed in a real child process; detection is its nonzero exit plus the printed typed class",
			})
		})
	}
	if len(observed) != 9 {
		t.Fatalf("guard cases executed = %d, want 9", len(observed))
	}
	pids := make(map[int]struct{}, len(observed))
	for _, item := range observed {
		if _, duplicate := pids[item.Cleanup.PID]; duplicate {
			t.Fatalf("reused child pid=%d across guard cases", item.Cleanup.PID)
		}
		pids[item.Cleanup.PID] = struct{}{}
	}
	pipelineBarrierSaveJSON(t, "guards-child-events.json", observed)
	cleanups := make([]pipelineBarrierCleanup, 0, len(observed))
	for _, item := range observed {
		cleanups = append(cleanups, item.Cleanup)
	}
	pipelineBarrierSaveJSON(t, "guards-cleanup.json", cleanups)
	t.Logf("guards: %d scheduled cases ran in real child runners, %d distinct reaped children; mandatory unreached/uninjected/timeout/unreaped/wrong-boundary classes came from genuine nonzero child exits", len(observed), len(pids))
}
