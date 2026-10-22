//go:build linux && pipelinefaults

package localize

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
)

type pipelineBarrierGuardObserved struct {
	Case          string
	ExpectedClass string
	ObservedClass string
	Detail        string
	Cleanup       pipelineBarrierCleanup
	Record        pipelineBarrierRecord
	Note          string `json:",omitempty"`
}

func pipelineBarrierRequireFailure(t *testing.T, err error, class string, fragments ...string) *pipelineBarrierFailure {
	t.Helper()
	var failure *pipelineBarrierFailure
	if !errors.As(err, &failure) || failure == nil {
		t.Fatalf("guard outcome = %v, want typed failure class %q", err, class)
	}
	if failure.Class != class {
		t.Fatalf("guard class = %q (%s), want %q", failure.Class, failure.Detail, class)
	}
	for _, fragment := range fragments {
		if !strings.Contains(failure.Detail, fragment) {
			t.Fatalf("guard detail = %q, want fragment %q", failure.Detail, fragment)
		}
	}
	return failure
}

func pipelineBarrierRequireClean(t *testing.T, cleanup pipelineBarrierCleanup) {
	t.Helper()
	if cleanup.WaitCalls != 1 || cleanup.CleanupRescueKills != 0 || !cleanup.PIDAbsent ||
		!cleanup.GroupAbsent || !cleanup.ProcAbsent || !cleanup.ReadEOF || cleanup.ResidualNotices != 0 {
		t.Fatalf("cleanup mismatch: %+v", cleanup)
	}
}

// TestPipelineHarnessGuards drives real negative children through the same
// control wire as the happy barrier. Each scheduled case must produce a typed
// failure, and every spawned child is really Waited and proven absent.
func TestPipelineHarnessGuards(t *testing.T) {
	observed := make([]pipelineBarrierGuardObserved, 0, 9)
	keep := func(item pipelineBarrierGuardObserved) { observed = append(observed, item) }

	t.Run("healthy-positive-control", func(t *testing.T) {
		plan := pipelineBarrierPlan{Case: "guard-healthy", Boundary: "tm-commit.before", Occurrence: 1, Inject: true}
		p := startPipelineBarrier(t, plan, pipelineBarrierModeHappy)
		if _, err := p.awaitReady(p.ctx); err != nil {
			t.Fatalf("ready: %v", err)
		}
		p.observeAlive(t)
		p.release(t)
		for _, event := range []string{"injected", "returned"} {
			if _, err := p.awaitEvent(p.ctx, event); err != nil {
				t.Fatalf("%s: %v", event, err)
			}
		}
		cleanup := p.finish(t, pipelineBarrierExitZero)
		pipelineBarrierRequireClean(t, cleanup)
		lifecycle := p.record()
		if failure := lifecycle.failure(); failure != nil {
			t.Fatalf("healthy guard = %v, want nil", failure)
		}
		keep(pipelineBarrierGuardObserved{Case: plan.Case, ExpectedClass: "", Cleanup: cleanup, Record: lifecycle, Note: "positive control; same evaluator accepts real ready/release/returned"})
	})

	t.Run("wrong-boundary", func(t *testing.T) {
		plan := pipelineBarrierPlan{Case: "guard-wrong-boundary", Boundary: "stage.close.after", Occurrence: 2, Inject: true}
		p := startPipelineBarrier(t, plan, pipelineBarrierModeWrong)
		_, err := p.awaitReady(p.ctx)
		failure := pipelineBarrierRequireFailure(t, err, pipelineBarrierWrongBoundary, pipelineBarrierWrongBoundaryValue, "stage.close.after", "occurrence=3")
		p.release(t)
		p.drain(t)
		cleanup := p.finish(t, pipelineBarrierExitZero)
		pipelineBarrierRequireClean(t, cleanup)
		lifecycle := p.record()
		if replayed := lifecycle.failure(); replayed == nil || replayed.Class != pipelineBarrierWrongBoundary {
			t.Fatalf("recorded lifecycle = %v, want %s", replayed, pipelineBarrierWrongBoundary)
		}
		keep(pipelineBarrierGuardObserved{Case: plan.Case, ExpectedClass: failure.Class, ObservedClass: failure.Class, Detail: failure.Detail, Cleanup: cleanup, Record: lifecycle, Note: "child really reported generate.after while stage.close.after was planned"})
	})

	t.Run("unreached", func(t *testing.T) {
		plan := pipelineBarrierPlan{Case: "guard-unreached", Boundary: "stage.write.after", Occurrence: 1, Inject: true}
		p := startPipelineBarrier(t, plan, pipelineBarrierModeNoReady)
		_, err := p.awaitReady(p.ctx)
		failure := pipelineBarrierRequireFailure(t, err, pipelineBarrierUnreached, "returned", plan.Boundary)
		cleanup := p.finish(t, pipelineBarrierExitZero)
		pipelineBarrierRequireClean(t, cleanup)
		lifecycle := p.record()
		if replayed := lifecycle.failure(); replayed == nil || replayed.Class != pipelineBarrierUnreached {
			t.Fatalf("recorded lifecycle = %v, want %s", replayed, pipelineBarrierUnreached)
		}
		keep(pipelineBarrierGuardObserved{Case: plan.Case, ExpectedClass: failure.Class, ObservedClass: failure.Class, Detail: failure.Detail, Cleanup: cleanup, Record: lifecycle, Note: "child returned without ever reaching the planned boundary"})
	})

	t.Run("uninjected", func(t *testing.T) {
		plan := pipelineBarrierPlan{Case: "guard-uninjected", Boundary: "stage.sync.after", Occurrence: 3, Inject: true}
		p := startPipelineBarrier(t, plan, pipelineBarrierModeNoInject)
		if _, err := p.awaitReady(p.ctx); err != nil {
			t.Fatalf("ready: %v", err)
		}
		p.observeAlive(t)
		p.release(t)
		_, err := p.awaitEvent(p.ctx, "injected")
		failure := pipelineBarrierRequireFailure(t, err, pipelineBarrierUninjected, "returned", plan.Boundary)
		cleanup := p.finish(t, pipelineBarrierExitZero)
		pipelineBarrierRequireClean(t, cleanup)
		lifecycle := p.record()
		if replayed := lifecycle.failure(); replayed == nil || replayed.Class != pipelineBarrierUninjected {
			t.Fatalf("recorded lifecycle = %v, want %s", replayed, pipelineBarrierUninjected)
		}
		keep(pipelineBarrierGuardObserved{Case: plan.Case, ExpectedClass: failure.Class, ObservedClass: failure.Class, Detail: failure.Detail, Cleanup: cleanup, Record: lifecycle, Note: "child reached ready and returned without the planned injection"})
	})

	t.Run("early-return", func(t *testing.T) {
		plan := pipelineBarrierPlan{Case: "guard-early-return", Boundary: "record.close.after", Occurrence: 1, Inject: true}
		p := startPipelineBarrier(t, plan, pipelineBarrierModeEarlyExit)
		_, err := p.awaitReady(p.ctx)
		failure := pipelineBarrierRequireFailure(t, err, pipelineBarrierEarlyReturn, "closed")
		cleanup := p.finish(t, pipelineBarrierExitZero)
		pipelineBarrierRequireClean(t, cleanup)
		lifecycle := p.record()
		if replayed := lifecycle.failure(); replayed == nil || replayed.Class != pipelineBarrierEarlyReturn {
			t.Fatalf("recorded lifecycle = %v, want %s", replayed, pipelineBarrierEarlyReturn)
		}
		keep(pipelineBarrierGuardObserved{Case: plan.Case, ExpectedClass: failure.Class, ObservedClass: failure.Class, Detail: failure.Detail, Cleanup: cleanup, Record: lifecycle, Note: "child exited after armed without ready or returned"})
	})

	t.Run("timeout-explicit-cancel", func(t *testing.T) {
		plan := pipelineBarrierPlan{Case: "guard-timeout", Boundary: "observe.before", Occurrence: 1, Inject: true}
		p := startPipelineBarrier(t, plan, pipelineBarrierModeHang)
		p.observeAlive(t)
		p.stop()
		if !errors.Is(p.ctx.Err(), context.Canceled) {
			t.Fatalf("explicit cancel context = %v, want canceled", p.ctx.Err())
		}
		_, err := p.awaitReady(p.ctx)
		failure := pipelineBarrierRequireFailure(t, err, pipelineBarrierTimeout, "context canceled")
		cleanup := p.finish(t, pipelineBarrierExitKilled)
		pipelineBarrierRequireClean(t, cleanup)
		if !cleanup.Signaled || cleanup.Signal != syscall.SIGKILL {
			t.Fatalf("canceled child status = %+v, want SIGKILL", cleanup)
		}
		lifecycle := p.record()
		if replayed := lifecycle.failure(); replayed == nil || replayed.Class != pipelineBarrierEarlyReturn {
			t.Fatalf("post-kill lifecycle = %v, want %s", replayed, pipelineBarrierEarlyReturn)
		}
		keep(pipelineBarrierGuardObserved{Case: plan.Case, ExpectedClass: failure.Class, ObservedClass: failure.Class, Detail: failure.Detail, Cleanup: cleanup, Record: lifecycle, Note: "explicit cancel (not a timing race) killed the hung group and the real Wait reaped it"})
	})

	t.Run("unreaped", func(t *testing.T) {
		plan := pipelineBarrierPlan{Case: "guard-unreaped", Boundary: "backup-rename.after", Occurrence: 1, Inject: true}
		p := startPipelineBarrier(t, plan, pipelineBarrierModeHappy)
		if _, err := p.awaitReady(p.ctx); err != nil {
			t.Fatalf("ready: %v", err)
		}
		p.observeAlive(t)
		p.release(t)
		for _, event := range []string{"injected", "returned"} {
			if _, err := p.awaitEvent(p.ctx, event); err != nil {
				t.Fatalf("%s: %v", event, err)
			}
		}
		before := p.record()
		failure := pipelineBarrierRequireFailure(t, before.failure(), pipelineBarrierUnreaped, "Wait")
		cleanup := p.finish(t, pipelineBarrierExitZero)
		pipelineBarrierRequireClean(t, cleanup)
		reaped := p.record()
		if failure := reaped.failure(); failure != nil {
			t.Fatalf("lifecycle after real Wait = %v, want nil", failure)
		}
		keep(pipelineBarrierGuardObserved{Case: plan.Case, ExpectedClass: failure.Class, ObservedClass: failure.Class, Detail: failure.Detail, Cleanup: cleanup, Record: reaped, Note: "unreaped came from the pre-Wait lifecycle input; teardown then performed the real single Wait"})
	})

	t.Run("malformed-input", func(t *testing.T) {
		plan := pipelineBarrierPlan{Case: "guard-malformed", Boundary: "tm-commit.after", Occurrence: 1, Inject: true}
		p := startPipelineBarrierRaw(t, plan, pipelineBarrierModeMalformed, false)
		_, err := p.awaitReady(p.ctx)
		failure := pipelineBarrierRequireFailure(t, err, pipelineBarrierEarlyReturn, "closed")
		cleanup := p.finish(t, pipelineBarrierExitNonzero)
		pipelineBarrierRequireClean(t, cleanup)
		if cleanup.ExitStatus == 0 {
			t.Fatalf("malformed child exit = 0, want nonzero: %+v", cleanup)
		}
		keep(pipelineBarrierGuardObserved{Case: plan.Case, ExpectedClass: failure.Class, ObservedClass: failure.Class, Detail: failure.Detail, Cleanup: cleanup, Record: p.record(), Note: "malformed child request failed before armed and exited nonzero"})
	})

	t.Run("cleanup-rescue-lifecycle-input", func(t *testing.T) {
		plan := pipelineBarrierPlan{Case: "guard-cleanup-rescue", Boundary: "publish-rename.after", Occurrence: 1, Inject: true}
		synthetic := pipelineBarrierRecord{
			Plan: plan, WaitConfirmed: true, CleanupRescues: 1,
			Events: []pipelineBarrierNotice{
				{Event: "armed", Case: plan.Case, PID: 1, PPID: 1},
				{Event: "ready", Case: plan.Case, Boundary: plan.Boundary, Occurrence: plan.Occurrence, PID: 1, PPID: 1},
				{Event: "injected", Case: plan.Case, Boundary: plan.Boundary, Occurrence: plan.Occurrence, PID: 1, PPID: 1},
				{Event: "returned", Case: plan.Case, PID: 1, PPID: 1},
			},
		}
		failure := pipelineBarrierRequireFailure(t, synthetic.failure(), pipelineBarrierCleanupRescue, "rescues=1")
		keep(pipelineBarrierGuardObserved{Case: plan.Case, ExpectedClass: failure.Class, ObservedClass: failure.Class, Detail: failure.Detail, Record: synthetic, Note: "lifecycle-input negative only; no process is spawned or left dangling"})
	})

	if len(observed) != 9 {
		t.Fatalf("guard cases executed = %d, want 9", len(observed))
	}
	pids := make(map[int]struct{}, len(observed))
	for _, item := range observed {
		if item.Cleanup.PID == 0 {
			continue
		}
		if _, duplicate := pids[item.Cleanup.PID]; duplicate {
			t.Fatalf("reused child pid=%d across guard cases", item.Cleanup.PID)
		}
		pids[item.Cleanup.PID] = struct{}{}
	}
	if len(pids) != 8 {
		t.Fatalf("distinct real guard children = %d, want 8", len(pids))
	}
	pipelineBarrierSaveJSON(t, "guards-child-events.json", observed)
	cleanups := make([]pipelineBarrierCleanup, 0, len(observed))
	for _, item := range observed {
		if item.Cleanup.PID != 0 {
			cleanups = append(cleanups, item.Cleanup)
		}
	}
	pipelineBarrierSaveJSON(t, "guards-cleanup.json", cleanups)
	t.Logf("guards: 8 real negative/control children reaped with distinct pids, 1 lifecycle-input negative, all typed classes asserted")
}
