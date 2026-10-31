//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
)

func pipelineCommitProbe(t *testing.T) {
	root := t.TempDir()
	t.Setenv(pipelineTask5RootEnv, root)
	plan := pipelineBarrierPlan{Case: "w3-commit-probe", Boundary: "sql-commit-reader-lock", Occurrence: 1}
	p := startPipelineBarrier(t, plan, pipelineBarrierModeCommitProbe)
	if _, err := p.awaitReady(p.ctx); err != nil {
		state, _ := os.ReadFile(filepath.Join(root, "child-state.json"))
		t.Fatalf("%v: state=%s child=%s", err, state, p.output.String())
	}
	statusBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", p.cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	tracerPID := 0
	for _, line := range strings.Split(string(statusBytes), "\n") {
		if value, ok := strings.CutPrefix(line, "TracerPid:"); ok {
			tracerPID, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if tracerPID == 0 {
		t.Fatal("strace tracer not attached")
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "tm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reader, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.ExecContext(t.Context(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	d0 := pipelineTMFaultConnRows(t, reader)
	p.release(t)
	deadline := time.Now().Add(10 * time.Second)
	var matched string
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(filepath.Join(root, "commit.trace"))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, filepath.Join(root, "tm.db")) && strings.Contains(line, "F_SETLK") && strings.Contains(line, "EAGAIN") {
				matched = line
				break
			}
		}
		if matched != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if matched == "" {
		raw, _ := os.ReadFile(filepath.Join(root, "commit.trace"))
		t.Fatalf("no contested COMMIT lock: trace bytes=%d; child=%s", len(raw), p.output.String())
	}
	t.Logf("reader-lock COMMIT syscall pid=%d: %s", p.cmd.Process.Pid, matched)
	if err := p.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	p.finish(t, pipelineBarrierExitKilled)
	tracerDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(tracerDeadline) {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", tracerPID)); errors.Is(err, os.ErrNotExist) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", tracerPID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("strace tracer pid=%d remains after target Wait", tracerPID)
	}
	if _, err := reader.ExecContext(t.Context(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "tm.db")
	beforeDB, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeJournal, err := os.ReadFile(path + "-journal")
	if err != nil {
		t.Fatal(err)
	}
	rawD0, err := json.Marshal(d0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPipelineCommitProbeReader$", "-test.v")
	child.Env = append(os.Environ(), "YAKUORI_COMMIT_PROBE_DB="+path, "YAKUORI_COMMIT_PROBE_D0="+string(rawD0))
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("recovery child: %v %s", err, output)
	}
	t.Logf("recovery pid=%d: %s", child.Process.Pid, output)
	afterDB, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterJournal, err := os.ReadFile(path + "-journal")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeDB, afterDB) || !bytes.Equal(beforeJournal, afterJournal) {
		t.Fatal("refusal changed DB or journal")
	}
	t.Logf("non-hot journal refused; DB and %d-byte journal retained", len(beforeJournal))
}

func TestPipelineCommitProbeReader(t *testing.T) {
	path := os.Getenv("YAKUORI_COMMIT_PROBE_DB")
	if path == "" {
		return
	}
	var d0 []pipelineFixtureRow
	if err := json.Unmarshal([]byte(os.Getenv("YAKUORI_COMMIT_PROBE_D0")), &d0); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	if diff := pipelineFixtureDiff(d0, pipelineFixtureReadRows(t, db)); len(diff) != 0 {
		t.Fatalf("D0 mismatch: %v", diff)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := tm.Open(t.Context(), path, config.DefaultLimits()); !errors.Is(err, tm.ErrRecoveryRequired) {
		t.Fatalf("Open=%v", err)
	}
	if store, err := tm.Recover(t.Context(), path, config.DefaultLimits()); err == nil {
		store.Close()
		t.Fatal("unexpected recovery of non-hot journal")
	}
	t.Logf("D0 all-column rows=%d; Open/Recover refused non-hot journal", len(d0))
}

func TestPipelineProcessRecovery(t *testing.T) {
	for _, tc := range []struct {
		boundary  string
		committed bool
	}{
		{pipelineBoundaryGenerateBefore, false}, {pipelineBoundaryGenerateAfter, false},
		{pipelineBoundaryStageWriteBefore, false}, {pipelineBoundaryStageWriteAfter, false},
		{pipelineBoundaryStageSyncBefore, false}, {pipelineBoundaryStageSyncAfter, false},
		{pipelineBoundaryStageCloseBefore, false}, {pipelineBoundaryObserveEntryWriterClosed, false},
		{pipelineBoundaryObserveAfter, false}, {pipelineBoundaryCompareFinalAfter, false},
		{pipelineBoundaryTMCommitBefore, false}, {"sql-commit.before", false},
		{"sql-commit.after", true}, {pipelineBoundaryTMCommitAfter, true},
		{"w3/sql-commit.before", false}, {"w3/sql-commit.after", true},
	} {
		t.Run(tc.boundary, func(t *testing.T) {
			kind, boundary := "text", tc.boundary
			if strings.HasPrefix(boundary, "w3/") {
				kind, boundary = "w3", strings.TrimPrefix(boundary, "w3/")
			}
			root := t.TempDir()
			t.Setenv(pipelineTask5RootEnv, root)
			input, _ := pipelineHarnessInput(t, kind)
			fixture := newPipelineFixture(t, input)
			want := fixture.d0
			if tc.committed {
				want = fixture.expectedRows(t, input.units)
			}
			plan := pipelineBarrierPlan{Case: kind + "-" + boundary, Boundary: boundary, Occurrence: 1}
			p := startPipelineBarrier(t, plan, pipelineBarrierModeRecoveryProbe)
			if _, err := p.awaitReady(p.ctx); err != nil {
				t.Fatalf("%v: %s", err, p.output.String())
			}
			if err := p.cmd.Process.Signal(syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			p.finish(t, pipelineBarrierExitKilled)
			if !pipelineLockAvailable(t, root) {
				t.Fatal("publication lock remains held")
			}
			output, err := os.ReadFile(filepath.Join(root, "output"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(output, pipelineHarnessOldOutput) {
				t.Fatal("output published before boundary")
			}
			actual := pipelineFaultReadRowsAfterWait(t, root, "stopped")
			if diff := pipelineFixtureDiff(want, actual); len(diff) != 0 {
				t.Fatalf("all-column rows: %v", diff)
			}
			runDirs := pipelineFaultRunDirs(t, root)
			if len(runDirs) != 1 {
				t.Fatalf("run dirs=%v", runDirs)
			}
			evidencePaths := []string{filepath.Join(runDirs[0], "stage"), filepath.Join(runDirs[0], "record.json")}
			evidenceBefore := pipelineImages(t, evidencePaths)
			if boundary == "sql-commit.before" {
				path := filepath.Join(root, "tm.db")
				beforeDB, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				beforeJournal, err := os.ReadFile(path + "-journal")
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(want)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPipelineCommitProbeReader$", "-test.v")
				child.Env = append(os.Environ(), "YAKUORI_COMMIT_PROBE_DB="+path, "YAKUORI_COMMIT_PROBE_D0="+string(raw))
				if out, err := child.CombinedOutput(); err != nil {
					t.Fatalf("recovery child: %v %s", err, out)
				}
				afterDB, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				afterJournal, err := os.ReadFile(path + "-journal")
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(beforeDB, afterDB) || !bytes.Equal(beforeJournal, afterJournal) {
					t.Fatal("recovery changed evidence")
				}
				t.Logf("recovery PID=%d non-hot journal refused; DB/journal retained", child.Process.Pid)
			} else {
				rerunPlan := pipelineBarrierPlan{Case: kind + "-rerun", Boundary: pipelineBoundaryStageCloseBefore, Occurrence: 1}
				rerun := startPipelineBarrier(t, rerunPlan, pipelineBarrierModePipelineRerun)
				if _, err := rerun.awaitEvent(rerun.ctx, "returned"); err != nil {
					t.Fatalf("rerun: %v", err)
				}
				rerun.finish(t, pipelineBarrierExitZero)
				state := pipelineFaultChildStateAt(t, root)
				if state.ErrorPhase != "prepare" || !state.InvalidRecord || !state.NotExist || state.GenerateCalls != 0 {
					t.Fatalf("rerun state: %+v", state)
				}
				if diff := pipelineFixtureDiff(want, pipelineFaultReadRowsAfterWait(t, root, "rerun")); len(diff) != 0 {
					t.Fatalf("rerun rows: %v", diff)
				}
				afterOutput, err := os.ReadFile(filepath.Join(root, "output"))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(output, afterOutput) {
					t.Fatal("rerun changed old output")
				}
				t.Logf("rerun PID=%d manual missing-record refusal, output and rows retained", rerun.cmd.Process.Pid)
			}
			if evidenceAfter := pipelineImages(t, evidencePaths); !reflect.DeepEqual(evidenceBefore, evidenceAfter) {
				t.Fatal("refusal changed stage or record")
			}
			t.Logf("boundary=%s PID=%d SIGKILL rows=%d committed=%t output preserved", tc.boundary, p.cmd.Process.Pid, len(actual), tc.committed)
		})
	}
	t.Run("commit-reader-lock", pipelineCommitProbe)
	t.Run("publication", pipelinePublicationRecoveryProbe)
	t.Run("publication-conflict", pipelinePublicationRecoveryConflict)
	t.Run("publication-invalid", pipelinePublicationRecoveryInvalidEvidence)
}

func pipelinePublicationRecoveryProbe(t *testing.T) {
	for _, tc := range []struct{ mode, boundary string }{
		{"create", pipelineBoundaryPublishRenameBefore}, {"create", pipelineBoundaryPublishRenameAfter},
		{"replace-absent", pipelineBoundaryPublishRenameBefore}, {"replace-absent", pipelineBoundaryPublishRenameAfter},
		{"replace-existing", pipelineBoundaryPublishRenameBefore}, {"replace-existing", pipelineBoundaryPublishRenameAfter},
		{"in-place", pipelineBoundaryBackupRenameBefore}, {"in-place", pipelineBoundaryBackupRenameAfter},
		{"in-place", pipelineBoundaryPublishRenameBefore}, {"in-place", pipelineBoundaryPublishRenameAfter},
		{"in-place-same", pipelineBoundaryPublishRenameAfter},
	} {
		t.Run(tc.mode+"/"+tc.boundary, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv(pipelineTask5RootEnv, root)
			input, _ := pipelineHarnessInput(t, "text")
			if tc.mode == "in-place-same" {
				input.target = slices.Clone(input.source)
				input.units[0].translation = input.units[0].source
			}
			fixture := newPipelineFixture(t, input)
			want := fixture.expectedRows(t, input.units)
			plan := pipelineBarrierPlan{Case: "text-" + tc.mode + "-" + tc.boundary, Boundary: tc.boundary, Occurrence: 1}
			p := startPipelineBarrier(t, plan, pipelineBarrierModeRecoveryProbe)
			if _, err := p.awaitReady(p.ctx); err != nil {
				p.finish(t, pipelineBarrierExitNonzero)
				t.Fatalf("%v child=%s", err, p.output.String())
			}
			if err := p.cmd.Process.Signal(syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			p.finish(t, pipelineBarrierExitKilled)
			if !pipelineLockAvailable(t, root) {
				t.Fatal("publication lock held")
			}
			if diff := pipelineFixtureDiff(want, pipelineFaultReadRowsAfterWait(t, root, "published-stop")); len(diff) > 0 {
				t.Fatalf("D1: %v", diff)
			}
			source := filepath.Join(root, "source")
			output := filepath.Join(root, "output")
			if strings.HasPrefix(tc.mode, "in-place") {
				output = source
			}
			got, err := os.ReadFile(output)
			switch {
			case tc.boundary == pipelineBoundaryPublishRenameAfter:
				if err != nil || !bytes.Equal(got, input.target) {
					t.Fatalf("published output=%q err=%v", got, err)
				}
			case strings.HasPrefix(tc.mode, "in-place") && (tc.boundary == pipelineBoundaryBackupRenameAfter || tc.boundary == pipelineBoundaryPublishRenameBefore):
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("source after backup: %v", err)
				}
			case strings.HasPrefix(tc.mode, "in-place"):
				if err != nil || !bytes.Equal(got, input.source) {
					t.Fatal("in-place original changed")
				}
			case tc.mode == "replace-existing":
				if err != nil || !bytes.Equal(got, pipelineHarnessOldOutput) {
					t.Fatal("old output changed")
				}
			default:
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("output exists: %v", err)
				}
			}
			runs := pipelineFaultRunDirs(t, root)
			if len(runs) != 1 {
				t.Fatalf("run dirs=%v", runs)
			}
			oldStage := pipelineImage(t, filepath.Join(runs[0], "stage"))
			recordPath := filepath.Join(runs[0], "record.json")
			if tc.boundary == pipelineBoundaryPublishRenameAfter {
				wantPhase := "prepared"
				if strings.HasPrefix(tc.mode, "in-place") {
					wantPhase = "source-backed-up"
				}
				if phase := pipelineRecoveryRecordPhase(t, recordPath); phase != wantPhase {
					t.Fatalf("stale record phase=%s want=%s", phase, wantPhase)
				}
			}
			rerunPlan := pipelineBarrierPlan{Case: "text-" + tc.mode + "-rerun", Boundary: pipelineBoundaryStageCloseBefore, Occurrence: 1}
			rerunMode := pipelineBarrierModePipelineRerun
			publishedImage := pipelineImage(t, output)
			if tc.boundary == pipelineBoundaryPublishRenameAfter && tc.mode != "create" {
				rerunMode = pipelineBarrierModePreparedRerun
				rerunPlan.Boundary = "recovery.prepare.after"
			}
			rerun := startPipelineBarrier(t, rerunPlan, rerunMode)
			if rerunMode == pipelineBarrierModePreparedRerun {
				if _, err := rerun.awaitReady(rerun.ctx); err != nil {
					t.Fatalf("prepare boundary: %v", err)
				}
				if !reflect.DeepEqual(publishedImage, pipelineImage(t, output)) {
					t.Fatal("Prepare changed published output identity or bytes")
				}
				if pipelineImage(t, filepath.Join(runs[0], "stage")).Present {
					t.Fatal("published stage reappeared at Prepare")
				}
				if phase := pipelineRecoveryRecordPhase(t, recordPath); phase != "published" {
					t.Fatalf("recovered record phase=%s", phase)
				}
				rerun.release(t)
			}
			if _, err := rerun.awaitEvent(rerun.ctx, "returned"); err != nil {
				t.Fatalf("rerun: %v", err)
			}
			rerun.finish(t, pipelineBarrierExitZero)
			state := pipelineFaultChildStateAt(t, root)
			if tc.mode == "create" && tc.boundary == pipelineBoundaryPublishRenameAfter {
				if state.ErrorPhase != "prepare" {
					t.Fatalf("create existing refusal: %+v", state)
				}
				if !reflect.DeepEqual(publishedImage, pipelineImage(t, output)) || pipelineImage(t, filepath.Join(runs[0], "stage")).Present {
					t.Fatal("create refusal changed published output or stage")
				}
				if phase := pipelineRecoveryRecordPhase(t, recordPath); phase != "published" {
					t.Fatalf("recovered record phase=%s", phase)
				}
			} else {
				if state.ErrorPhase != "" || state.Snapshot.Result.Publication.State != publication.Published {
					t.Fatalf("rerun: %+v", state)
				}
			}
			final, err := os.ReadFile(output)
			if err != nil || !bytes.Equal(final, input.target) {
				t.Fatalf("rerun output=%q err=%v", final, err)
			}
			if oldStage.Present && pipelineImage(t, output).ID.Inode == oldStage.ID.Inode {
				t.Fatal("old stage was published")
			}
			if tc.mode == "in-place" && tc.boundary == pipelineBoundaryPublishRenameAfter {
				nextInput := input
				nextInput.source = slices.Clone(input.target)
				nextInput.units = slices.Clone(input.units)
				nextInput.units[0].source = string(input.target)
				nextFixture := newPipelineFixture(t, nextInput)
				want = pipelineFixtureSortRows(append(want, nextFixture.authoredRow(t, nextInput.units[0])))
			}
			if diff := pipelineFixtureDiff(want, pipelineFaultReadRowsAfterWait(t, root, "published-rerun")); len(diff) > 0 {
				t.Fatalf("rerun D1: %v", diff)
			}
			t.Logf("rerun PID=%d result=%s output retained", rerun.cmd.Process.Pid, state.Snapshot.Result.Publication.State)
			t.Logf("mode=%s boundary=%s pid=%d SIGKILL D1 rows=%d", tc.mode, tc.boundary, p.cmd.Process.Pid, len(want))
		})
	}
}

func pipelineRecoveryRecordPhase(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := pipelineHarnessDecodeRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	return record.Phase
}

func pipelinePublicationRecoveryConflict(t *testing.T) {
	root := t.TempDir()
	t.Setenv(pipelineTask5RootEnv, root)
	plan := pipelineBarrierPlan{Case: "text-in-place-conflict", Boundary: pipelineBoundaryBackupRenameAfter, Occurrence: 1}
	p := startPipelineBarrier(t, plan, pipelineBarrierModeRecoveryProbe)
	if _, err := p.awaitReady(p.ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	p.finish(t, pipelineBarrierExitKilled)
	dirs := pipelineFaultRunDirs(t, root)
	if len(dirs) != 1 {
		t.Fatalf("run dirs=%v", dirs)
	}
	backup, _ := filepath.Glob(filepath.Join(root, ".yakuori-backup-*"))
	if len(backup) != 1 {
		t.Fatalf("backups=%v", backup)
	}
	paths := []string{filepath.Join(dirs[0], "stage"), filepath.Join(dirs[0], "record.json"), backup[0]}
	before := pipelineImages(t, paths)
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("external file"), 0600); err != nil {
		t.Fatal(err)
	}
	rerun := startPipelineBarrier(t, pipelineBarrierPlan{Case: "text-in-place-conflict-rerun", Boundary: pipelineBoundaryStageCloseBefore, Occurrence: 1}, pipelineBarrierModePipelineRerun)
	if _, err := rerun.awaitEvent(rerun.ctx, "returned"); err != nil {
		t.Fatal(err)
	}
	rerun.finish(t, pipelineBarrierExitNonzero)
	state := pipelineFaultChildStateAt(t, root)
	if state.ErrorPhase != "prepare" || state.RecoveryReason == "" || state.GenerateCalls != 0 {
		t.Fatalf("no conflict refusal: phase=%s reason=%s generated=%d", state.ErrorPhase, state.RecoveryReason, state.GenerateCalls)
	}
	if got, err := os.ReadFile(source); err != nil || string(got) != "external file" {
		t.Fatalf("conflict file=%q err=%v", got, err)
	}
	after := pipelineImages(t, paths)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("conflict changed stage/record/backup evidence")
	}
	t.Logf("SIGKILL pid=%d recovery pid=%d conflict refused; external source and evidence preserved", p.cmd.Process.Pid, rerun.cmd.Process.Pid)
}

func pipelinePublicationRecoveryInvalidEvidence(t *testing.T) {
	for _, corruption := range []string{"missing-record", "corrupt-record", "stage-mismatch"} {
		t.Run(corruption, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv(pipelineTask5RootEnv, root)
			p := startPipelineBarrier(t, pipelineBarrierPlan{Case: "text-in-place-invalid", Boundary: pipelineBoundaryBackupRenameAfter, Occurrence: 1}, pipelineBarrierModeRecoveryProbe)
			if _, err := p.awaitReady(p.ctx); err != nil {
				t.Fatal(err)
			}
			if err := p.cmd.Process.Signal(syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			p.finish(t, pipelineBarrierExitKilled)
			dirs := pipelineFaultRunDirs(t, root)
			if len(dirs) != 1 {
				t.Fatal(dirs)
			}
			stage, record := filepath.Join(dirs[0], "stage"), filepath.Join(dirs[0], "record.json")
			switch corruption {
			case "missing-record":
				if err := os.Remove(record); err != nil {
					t.Fatal(err)
				}
			case "corrupt-record":
				if err := os.WriteFile(record, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "stage-mismatch":
				if err := os.WriteFile(stage, []byte("changed stage"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			backup, _ := filepath.Glob(filepath.Join(root, ".yakuori-backup-*"))
			if len(backup) != 1 {
				t.Fatal(backup)
			}
			paths := []string{stage, record, backup[0], filepath.Join(root, "source")}
			before := pipelineImages(t, paths)
			rerun := startPipelineBarrier(t, pipelineBarrierPlan{Case: "text-in-place-rerun", Boundary: pipelineBoundaryStageCloseBefore, Occurrence: 1}, pipelineBarrierModeInvalidRerun)
			if _, err := rerun.awaitEvent(rerun.ctx, "returned"); err != nil {
				t.Fatal(err)
			}
			rerun.finish(t, pipelineBarrierExitZero)
			if after := pipelineImages(t, paths); !reflect.DeepEqual(before, after) {
				t.Fatal("manual refusal changed evidence")
			}
			t.Logf("%s SIGKILL pid=%d recovery pid=%d manual refusal retains evidence", corruption, p.cmd.Process.Pid, rerun.cmd.Process.Pid)
		})
	}
}
