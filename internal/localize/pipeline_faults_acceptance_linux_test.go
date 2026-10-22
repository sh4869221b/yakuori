//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
	"golang.org/x/sys/unix"
)

// The real fault child is selected only inside this tagged test binary. The
// root is a test-only durable directory so the parent can inspect exactly the
// files and SQLite rows the child produced after its actual Wait.
const pipelineTask5RootEnv = "YAKUORI_PIPELINE_TASK5_ROOT"

type pipelineFaultChildState struct {
	Kind           string                  `json:"kind"`
	Mode           string                  `json:"mode"`
	Root           string                  `json:"root"`
	Snapshot       pipelineHarnessSnapshot `json:"snapshot"`
	GenerateCalls  int                     `json:"generate_calls"`
	StageFDClosed  bool                    `json:"stage_fd_closed"`
	LockReleased   bool                    `json:"lock_released"`
	RunDirs        []string                `json:"run_dirs"`
	BackupPaths    []string                `json:"backup_paths"`
	ErrorPhase     string                  `json:"error_phase,omitempty"`
	RecoveryReason string                  `json:"recovery_reason,omitempty"`
	InvalidRecord  bool                    `json:"invalid_record"`
	NotExist       bool                    `json:"not_exist"`
}

func pipelineLockAvailable(t *testing.T, dir string) bool {
	t.Helper()
	file, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
			t.Fatal(err)
		}
		return true
	}
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false
	}
	t.Fatalf("flock %s: %v", dir, err)
	return false
}

// pipelineLocateRunPaths finds the retained fault run so the rerun snapshot
// reads the same stage/record locations the failed run left behind.
func pipelineLocateRunPaths(paths *publication.PipelineFaultPaths, root string) {
	if paths.Stage != "" {
		return
	}
	dirs, _ := filepath.Glob(filepath.Join(root, ".yakuori-run-*"))
	for _, dir := range dirs {
		stage := filepath.Join(dir, "stage")
		if _, err := os.Lstat(stage); err == nil {
			paths.Stage = stage
			paths.Record = filepath.Join(dir, "record.json")
			return
		}
	}
}

func pipelineFaultSaveChildState(t *testing.T, h *pipelineHarness, root string, request pipelineBarrierChildRequest) {
	t.Helper()
	snapshot, err := h.snapshot(pipelineHarnessExpectation{})
	if err != nil {
		t.Fatal(err)
	}
	state := pipelineFaultChildState{
		Kind: h.kind, Mode: request.Mode, Root: root, Snapshot: snapshot,
		GenerateCalls: len(h.fixture.backend.calls),
		LockReleased:  pipelineLockAvailable(t, filepath.Dir(h.options.Source)),
	}
	if h.ops != nil && h.ops.stageFD != nil {
		_, statErr := h.ops.stageFD.Stat()
		state.StageFDClosed = errors.Is(statErr, os.ErrClosed)
	}
	state.RunDirs, _ = filepath.Glob(filepath.Join(root, ".yakuori-run-*"))
	state.BackupPaths, _ = filepath.Glob(filepath.Join(root, ".yakuori-backup-*"))
	var typed *Error
	if errors.As(h.runErr, &typed) {
		state.ErrorPhase = typed.Phase
	}
	var recovery *publication.RecoveryError
	if errors.As(h.runErr, &recovery) {
		state.RecoveryReason = recovery.Reason
	}
	state.InvalidRecord = errors.Is(h.runErr, publication.ErrInvalidRecord)
	state.NotExist = errors.Is(h.runErr, os.ErrNotExist)
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "child-state.json"), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

// pipelineFaultChild is the real pipeline behind the Task4 ready/release wire.
// It builds the Task1 fixture, publishes through the ordinary Core, and stops
// inside the actual CloseStage callback: ready is emitted after the real stage
// Write/Sync and before the real descriptor Close, so the parent can inspect
// the live process, its open descriptor and its directory lock.
func pipelineFaultChild(t *testing.T, request pipelineBarrierChildRequest, send func(string, string, int), awaitRelease func()) {
	t.Helper()
	if request.Boundary != pipelineBoundaryStageCloseBefore || request.Occurrence != 1 {
		t.Fatalf("fault child boundary=%q occurrence=%d, want %s#1", request.Boundary, request.Occurrence, pipelineBoundaryStageCloseBefore)
	}
	root := os.Getenv(pipelineTask5RootEnv)
	if root == "" {
		t.Fatalf("%s is required for the durable fault root", pipelineTask5RootEnv)
	}
	kind := "text"
	if strings.HasPrefix(request.Case, "w3-") {
		kind = "w3"
	}
	if _, ok := pipelineHarnessInput(t, kind); !ok {
		t.Fatalf("no fixture input for kind %q", kind)
	}
	spec := pipelineHarnessCase{name: request.Case, kind: kind, surface: "file", mode: publication.Replace, outputPresent: true}
	h := newPipelineHarness(t, spec, true)
	dbPath := filepath.Join(root, "tm.db")
	_, statErr := os.Lstat(dbPath)
	existed := statErr == nil
	var store *tm.Store
	var err error
	if existed {
		store, err = tm.Open(context.Background(), dbPath, h.fixture.limits)
	} else {
		store, err = tm.Create(context.Background(), dbPath, h.fixture.limits)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	h.fixture.store, h.fixture.db = store, db
	if !existed {
		h.fixture.d0 = h.fixture.seedUnrelated(t)
	}
	h.sourcePath = filepath.Join(root, "source")
	if !existed {
		if err := os.WriteFile(h.sourcePath, h.fixture.input.source, 0600); err != nil {
			t.Fatal(err)
		}
	}
	h.options = publication.Options{Source: h.sourcePath, Output: filepath.Join(root, "output"), Mode: publication.Replace}
	if _, err := os.Lstat(h.options.Output); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(h.options.Output, pipelineHarnessOldOutput, 0600); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	if h.beforeOutput, err = pipelineHarnessReadFile(h.options.Output); err != nil {
		t.Fatal(err)
	}
	h.ops.source = h.sourcePath
	pipelineLocateRunPaths(&h.ops.paths, root)

	if request.Mode == pipelineBarrierModePipelineRerun {
		// The exact same target in a fresh child: the missing record must be a
		// typed manual refusal, never an implicit cleanup or old-stage publish.
		h.result, h.runErr = h.fixture.core.FileWithTM(t.Context(), h.options, h.fixture.adapter, h.fixture.profile, h.fixture.store, h.fixture.scope)
		pipelineFaultSaveChildState(t, h, root, request)
		send("returned", "", 0)
		return
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hooks := h.ops.hooks()
	baseClose := hooks.CloseStage
	cancelStimulus := request.Mode == pipelineBarrierModePipelineCancel
	hooks.CloseStage = func(file *os.File) error {
		if err := baseClose(file); err != nil {
			return err
		}
		send("ready", request.Boundary, request.Occurrence)
		awaitRelease()
		send("injected", request.Boundary, request.Occurrence)
		if cancelStimulus {
			h.injected = append(h.injected, "ctx-cancel")
			cancel()
			return nil
		}
		h.injected = append(h.injected, "close-stage-eio")
		return unix.EIO
	}
	stimulus := "error"
	if cancelStimulus {
		stimulus = "cancel"
	}
	h.result, h.runErr = h.executeWithHooks(ctx, stimulus, request.Boundary, func() publication.PipelineFaultHooks {
		return hooks
	})
	pipelineFaultSaveChildState(t, h, root, request)
	send("returned", "", 0)
}

func pipelineFaultChildStateAt(t *testing.T, root string) pipelineFaultChildState {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "child-state.json"))
	if err != nil {
		t.Fatalf("child state: %v", err)
	}
	var state pipelineFaultChildState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("child state: %v", err)
	}
	return state
}

// pipelineFaultReadRowsAfterWait preserves the raw SQLite database and every
// sidecar before the first read-only open, then reads all four columns after
// the child has been reaped. The child JSON never supplies these final rows.
func pipelineFaultReadRowsAfterWait(t *testing.T, root, label string) []pipelineFixtureRow {
	t.Helper()
	dbPath := filepath.Join(root, "tm.db")
	sidecars := map[string]string{}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		raw, err := os.ReadFile(dbPath + suffix)
		switch {
		case errors.Is(err, os.ErrNotExist):
			sidecars[suffix] = "ENOENT"
		case err != nil:
			t.Fatal(err)
		default:
			sidecars[suffix] = fmt.Sprintf("bytes=%d", len(raw))
			pipelineHarnessSave(t, label+"-tm.db"+suffix, raw)
		}
	}
	pipelineHarnessSaveJSON(t, label+"-sidecars.json", sidecars)
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	rows, err := db.Query("SELECT key_schema, source_identity, translation_profile, translated_text FROM tm ORDER BY source_identity, translation_profile")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	var result []pipelineFixtureRow
	for rows.Next() {
		var schema int
		var source, profile []byte
		var text string
		if err := rows.Scan(&schema, &source, &profile, &text); err != nil {
			t.Fatal(err)
		}
		result = append(result, pipelineFixtureRow{schema, hex.EncodeToString(source), hex.EncodeToString(profile), text})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("post-Wait SQLite integrity=%q error=%v", integrity, err)
	}
	pipelineHarnessSaveJSON(t, label+"-rows-postwait.json", result)
	return result
}

func pipelineFaultRunDirs(t *testing.T, root string) []string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(root, ".yakuori-run-*"))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(dirs)
	return dirs
}

// pipelineFaultCaptureAfterWait derives the final snapshot from actual files
// and SQLite only. The child JSON contributes causal facts (reached boundary,
// injection, while-alive Close/flock and Result/Error), never final files/rows.
func pipelineFaultCaptureAfterWait(t *testing.T, root string, child pipelineFaultChildState, expectation pipelineHarnessExpectation, label string) pipelineHarnessSnapshot {
	t.Helper()
	snapshot := child.Snapshot
	snapshot.Case, snapshot.Kind, snapshot.Mode, snapshot.Surface = expectation.Case, expectation.Kind, expectation.Mode, expectation.Surface
	snapshot.AuthoredD0 = slices.Clone(expectation.D0)
	snapshot.AuthoredD1 = slices.Clone(expectation.D1)
	paths := publication.PipelineFaultPaths{Stage: child.Snapshot.Files["stage"].Path, Record: child.Snapshot.Files["record"].Path}
	pipelineLocateRunPaths(&paths, root)
	locations := map[string]string{"source": filepath.Join(root, "source"), "output": filepath.Join(root, "output")}
	if paths.Stage != "" {
		locations["stage"] = paths.Stage
	}
	if paths.Record != "" {
		locations["record"] = paths.Record
	}
	if path := child.Snapshot.Files["backup"].Path; path != "" {
		locations["backup"] = path
	}
	snapshot.Files = map[string]pipelineHarnessFile{}
	manifest := map[string]any{}
	for _, key := range []string{"source", "output", "stage", "record", "backup"} {
		path := locations[key]
		if path == "" {
			snapshot.Files[key] = pipelineHarnessFile{}
			manifest[key] = map[string]any{"path": "", "exists": false}
			continue
		}
		file, err := pipelineHarnessReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Files[key] = file
		manifest[key] = map[string]any{"path": path, "exists": file.Exists, "size": file.Size, "dev": file.Dev, "inode": file.Inode, "mount_id": file.MountID, "mode": file.Mode, "nlink": file.Nlink}
		if file.Exists {
			pipelineHarnessSave(t, label+"-"+key+".bin", file.Bytes)
		}
	}
	pipelineHarnessSaveJSON(t, label+"-files.json", manifest)
	snapshot.Record = nil
	if record := snapshot.Files["record"]; record.Exists {
		if err := json.Unmarshal(record.Bytes, &snapshot.Record); err != nil {
			t.Fatalf("actual installed record: %v", err)
		}
	}
	snapshot.RecordInstalled = snapshot.Files["record"].Exists && snapshot.Record != nil &&
		snapshot.Record.Phase == "published" && snapshot.Result.Publication.State == publication.Published
	if snapshot.RecordInstalled {
		snapshot.InstallBasis = "postwait physical record and Published result; install callback unobserved"
	}
	snapshot.Rows = pipelineFaultReadRowsAfterWait(t, root, label)
	snapshot.Filesystem = pipelineHarnessFilesystem(t, filepath.Join(root, "source"))
	snapshot.Certainty, snapshot.CertaintyBasis = pipelineHarnessObserveCertainty(snapshot)
	snapshot.Outcome = pipelineHarnessObserveOutcome(snapshot)
	pipelineHarnessSaveJSON(t, label+"-postwait-state.json", snapshot)
	return snapshot
}

func pipelineFaultExpectation(t *testing.T, spec pipelineHarnessCase, f *pipelineFixture) pipelineHarnessExpectation {
	t.Helper()
	expectation := pipelineHarnessExpectationForFixture(t, spec, f)
	expectation.Status = pipelineHarnessPass
	expectation.Outcome = "not-published"
	expectation.StageRetained = true
	expectation.StageBytes = slices.Clone(f.input.target)
	expectation.OutputBytes = slices.Clone(pipelineHarnessOldOutput)
	expectation.RecordInstalled = false
	expectation.RecordPhase = ""
	expectation.RecordOperation = ""
	expectation.CommitCertainty = pipelineHarnessNotCommitted
	expectation.StageCheckpoint = false
	expectation.CommitCheckpoint = false
	expectation.StageFDClosed = true
	expectation.RecordFDClosed = false
	expectation.BackupPresent = false
	expectation.Cause = "the stage close reports an injected error/cancel after the mandatory real descriptor Close; no afterStage checkpoint can exist"
	expectation.FinalRows = slices.Clone(f.d0)
	generate := pipelineHarnessGenerateCount(f.input)
	expectation.BoundaryOrder = nil
	for range generate {
		expectation.BoundaryOrder = append(expectation.BoundaryOrder, pipelineBoundaryGenerateBefore, pipelineBoundaryGenerateAfter)
	}
	expectation.BoundaryOrder = append(expectation.BoundaryOrder,
		pipelineBoundaryStageWriteBefore, pipelineBoundaryStageWriteAfter,
		pipelineBoundaryRunCloseAfter)
	expectation.RequiredBoundaries = map[string]int{
		pipelineBoundaryGenerateBefore: generate, pipelineBoundaryGenerateAfter: generate,
		pipelineBoundaryStageWriteBefore: 1, pipelineBoundaryStageWriteAfter: 1,
		pipelineBoundaryStageSyncBefore: 1, pipelineBoundaryStageSyncAfter: 1,
		pipelineBoundaryStageCloseBefore: 1,
		pipelineBoundaryRunCloseAfter:    1,
	}
	return expectation
}

// pipelineFaultLaterBoundaries are the real stages that must never be reached
// after a stage-close fault. The normal counterpart below reaches each of them,
// which is what makes a zero count evidence rather than a disconnected counter.
func pipelineFaultLaterBoundaries() []string {
	return []string{
		pipelineBoundaryObserveBefore, pipelineBoundaryObserveAfter,
		pipelineBoundaryCompareFinalAfter,
		pipelineBoundaryTMCommitBefore, pipelineBoundaryTMCommitAfter,
		pipelineBoundaryRecordWriteBefore, pipelineBoundaryRecordWriteAfter,
		pipelineBoundaryRecordSyncBefore, pipelineBoundaryRecordSyncAfter,
		pipelineBoundaryRecordCloseBefore, pipelineBoundaryRecordCloseAfter,
		pipelineBoundaryRecordInstallBefore, pipelineBoundaryRecordInstallAfter,
		pipelineBoundaryPublishRenameBefore, pipelineBoundaryPublishRenameAfter,
	}
}

func testPipelineHarnessFaultStageClose(t *testing.T, kind string) {
	spec := pipelineFixtureSpec(kind, pipelineFixtureModes()[2])
	h := newPipelineHarness(t, spec, true)
	expectation := pipelineFaultExpectation(t, spec, h.fixture)
	result, err := h.executeStageCloseFault(t.Context(), unix.EIO)
	var failure *Error
	if !errors.Is(err, unix.EIO) || !errors.As(err, &failure) || failure.Phase != "stage" ||
		result.TMCommitted || result.Publication.State != publication.NotPublished {
		t.Fatalf("stage-close fault result=%+v error=%v", result, err)
	}
	snapshot, snapshotErr := h.snapshot(expectation)
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	if problems := pipelineHarnessJudge(expectation, snapshot); len(problems) != 0 {
		t.Fatalf("stage-close fault state rejected: %v", problems)
	}
	if len(snapshot.Injected) != 1 || snapshot.Injected[0] != "close-stage-eio" {
		t.Fatalf("injected=%v, want exactly one close-stage-eio", snapshot.Injected)
	}
	if snapshot.Reached[pipelineBoundaryStageWriteAfter] != 1 || snapshot.Reached[pipelineBoundaryObserveBefore] != 0 {
		t.Fatalf("fault position write.after=%d observe.before=%d, want 1/0",
			snapshot.Reached[pipelineBoundaryStageWriteAfter], snapshot.Reached[pipelineBoundaryObserveBefore])
	}
	if !snapshot.StageFDClosed {
		t.Fatal("injected close error did not perform the mandatory real Close")
	}
	for _, boundary := range pipelineFaultLaterBoundaries() {
		if snapshot.Reached[boundary] != 0 {
			t.Fatalf("stage-close fault reached later boundary %s %d time(s)", boundary, snapshot.Reached[boundary])
		}
	}
	if snapshot.Files["record"].Exists || snapshot.Files["backup"].Exists {
		t.Fatal("stage-close fault installed a record or backup")
	}
	if diff := pipelineFixtureDiff(expectation.D0, snapshot.Rows); len(diff) != 0 {
		t.Fatalf("stage-close fault changed D0: %v", diff)
	}
	pipelineFixtureSaveState(t, "fault-stage-close-"+kind, expectation, snapshot, snapshot.Rows)
	pipelineHarnessSaveJSON(t, "fault-stage-close-"+kind+"-later.json", snapshot.Reached)
	t.Logf("kind=%s stage-close EIO: injected=1, write.after=1 observe.before=0, real stage Close proved, S/O/T retained, no record/backup, D0=%d not-committed, stage_fd_closed=%t run.close=%d",
		kind, len(snapshot.Rows), snapshot.StageFDClosed, snapshot.Reached[pipelineBoundaryRunCloseAfter])
}

func testPipelineHarnessFaultCounterpart(t *testing.T, kind string) {
	spec := pipelineFixtureSpec(kind, pipelineFixtureModes()[2])
	h := newPipelineHarness(t, spec, true)
	expectation := pipelineHarnessExpectationForFixture(t, spec, h.fixture)
	result, err := h.execute(t.Context())
	if err != nil || !result.TMCommitted || result.Publication.State != publication.Published {
		t.Fatalf("normal counterpart result=%+v error=%v", result, err)
	}
	snapshot, err := h.snapshot(expectation)
	if err != nil {
		t.Fatal(err)
	}
	if problems := pipelineHarnessJudge(expectation, snapshot); len(problems) != 0 {
		t.Fatalf("normal counterpart rejected: %v", problems)
	}
	counts := map[string]int{}
	for _, boundary := range pipelineFaultLaterBoundaries() {
		if snapshot.Reached[boundary] < 1 {
			t.Fatalf("normal counterpart never reached absence-counter boundary %s (reached %d)", boundary, snapshot.Reached[boundary])
		}
		counts[boundary] = snapshot.Reached[boundary]
	}
	for _, boundary := range []string{
		pipelineBoundaryStdoutWriteBefore, pipelineBoundaryStdoutWriteAfter,
		pipelineBoundaryDiagnosticWriteBefore, pipelineBoundaryDiagnosticWriteAfter,
		pipelineBoundaryBackupRenameBefore, pipelineBoundaryBackupRenameAfter,
	} {
		if snapshot.Reached[boundary] != 0 {
			t.Fatalf("structurally absent %s was reached %d time(s) on a Replace file run", boundary, snapshot.Reached[boundary])
		}
	}
	pipelineHarnessSaveJSON(t, "fault-counterpart-"+kind+"-counts.json", counts)
	t.Logf("kind=%s normal counterpart reached every later absence counter: %v", kind, counts)
}

func testPipelineHarnessFaultProcess(t *testing.T, kind, stimulus string, rerun bool) {
	root := t.TempDir()
	t.Setenv(pipelineTask5RootEnv, root)
	spec := pipelineFixtureSpec(kind, pipelineFixtureModes()[2])
	input, _ := pipelineHarnessInput(t, kind)
	oracle := newPipelineFixture(t, input)
	expectation := pipelineFaultExpectation(t, spec, oracle)
	caseName := kind + "-stage-close-" + stimulus
	plan := pipelineBarrierPlan{Case: caseName, Boundary: pipelineBoundaryStageCloseBefore, Occurrence: 1, Inject: true}
	mode := pipelineBarrierModePipelineError
	wantInjected := "close-stage-eio"
	if stimulus == "cancel" {
		mode = pipelineBarrierModePipelineCancel
		wantInjected = "ctx-cancel"
	}
	p := startPipelineBarrier(t, plan, mode)
	ready, err := p.awaitReady(p.ctx)
	if err != nil {
		t.Fatalf("ready: %v", err)
	}
	if ready.Boundary != pipelineBoundaryStageCloseBefore || ready.Occurrence != 1 {
		t.Fatalf("ready boundary=%s#%d, want %s#1", ready.Boundary, ready.Occurrence, pipelineBoundaryStageCloseBefore)
	}
	observation := p.observeAlive(t)
	lockHeld := !pipelineLockAvailable(t, root)
	if !lockHeld {
		t.Fatal("publication directory lock was released while the child was stopped at the stage close")
	}
	stageFDOpen := pipelineChildHasStageFD(t, p.cmd.Process.Pid)
	if !stageFDOpen {
		t.Fatal("child has no open stage descriptor at the ready boundary")
	}
	p.release(t)
	injected, err := p.awaitEvent(p.ctx, "injected")
	if err != nil {
		t.Fatalf("injected: %v", err)
	}
	if injected.Boundary != pipelineBoundaryStageCloseBefore || injected.Occurrence != 1 {
		t.Fatalf("injected boundary=%s#%d", injected.Boundary, injected.Occurrence)
	}
	if _, err := p.awaitEvent(p.ctx, "returned"); err != nil {
		t.Fatalf("returned: %v", err)
	}
	cleanup := p.finish(t, pipelineBarrierExitZero)
	lockReleased := pipelineLockAvailable(t, root)
	if !lockReleased {
		t.Fatal("publication directory lock remained held after the child exited")
	}
	if cleanup.CleanupRescueKills != 0 || cleanup.WaitCalls != 1 || !cleanup.PIDAbsent || !cleanup.GroupAbsent || !cleanup.ProcAbsent {
		t.Fatalf("fault child lifecycle mismatch: %+v", cleanup)
	}
	state := pipelineFaultChildStateAt(t, root)
	snapshot := pipelineFaultCaptureAfterWait(t, root, state, expectation, "process-"+kind+"-"+stimulus)
	snapshot.Exit = fmt.Sprintf("exit=%d signaled=%t signal=%s", cleanup.ExitStatus, cleanup.Signaled, cleanup.Signal)
	snapshot.Signal = fmt.Sprintf("%s", cleanup.Signal)
	snapshot.Reaped = fmt.Sprintf("actual Wait confirmed: wait_calls=%d pid/group/proc absent", cleanup.WaitCalls)
	snapshot.Rerun = "not-run in this child"
	if !state.StageFDClosed || !state.LockReleased {
		t.Fatalf("child post-return evidence: stage_fd_closed=%t lock_released=%t", state.StageFDClosed, state.LockReleased)
	}
	if len(snapshot.Injected) != 1 || snapshot.Injected[0] != wantInjected {
		t.Fatalf("injected=%v, want exactly one %s", snapshot.Injected, wantInjected)
	}
	if problems := pipelineHarnessJudge(expectation, snapshot); len(problems) != 0 {
		t.Fatalf("real child stage-close %s state rejected: %v", stimulus, problems)
	}
	if snapshot.Reached[pipelineBoundaryStageWriteAfter] != 1 || snapshot.Reached[pipelineBoundaryObserveBefore] != 0 {
		t.Fatalf("child fault position write.after=%d observe.before=%d", snapshot.Reached[pipelineBoundaryStageWriteAfter], snapshot.Reached[pipelineBoundaryObserveBefore])
	}
	for _, boundary := range pipelineFaultLaterBoundaries() {
		if snapshot.Reached[boundary] != 0 {
			t.Fatalf("real child fault reached later boundary %s", boundary)
		}
	}
	pipelineFixtureSaveState(t, "process-"+kind+"-"+stimulus, expectation, snapshot, snapshot.Rows)
	pipelineHarnessSaveJSON(t, "process-"+kind+"-"+stimulus+"-lifecycle.json", map[string]any{
		"case": caseName, "stimulus": stimulus, "ready": ready, "injected": injected,
		"observation": observation, "cleanup": cleanup, "lock_held_at_ready": lockHeld,
		"stage_fd_open_at_ready": stageFDOpen, "lock_released_after_wait": lockReleased,
		"child_stage_fd_closed": state.StageFDClosed, "child_lock_released": state.LockReleased,
		"wait_calls": cleanup.WaitCalls, "rescue_kills": cleanup.CleanupRescueKills,
	})
	t.Logf("real child kind=%s stimulus=%s: ready=%s#1, injected=%s, child alive with stage fd open and directory lock held, actual Wait=%d, stage_fd_closed=%t lock_released=%t, D0=%d not-published",
		kind, stimulus, ready.Boundary, wantInjected, cleanup.WaitCalls, state.StageFDClosed, lockReleased, len(snapshot.Rows))
	if !rerun {
		return
	}
	beforeStage := snapshot.Files["stage"]
	beforeRunDirs := pipelineFaultRunDirs(t, root)
	if len(beforeRunDirs) != 1 {
		t.Fatalf("fault run dirs=%v, want one retained run", beforeRunDirs)
	}
	rerunPlan := pipelineBarrierPlan{Case: kind + "-stage-close-rerun", Boundary: pipelineBoundaryStageCloseBefore, Occurrence: 1, Inject: true}
	rerunBarrier := startPipelineBarrier(t, rerunPlan, pipelineBarrierModePipelineRerun)
	if _, err := rerunBarrier.awaitEvent(rerunBarrier.ctx, "returned"); err != nil {
		t.Fatalf("rerun returned: %v", err)
	}
	rerunCleanup := rerunBarrier.finish(t, pipelineBarrierExitZero)
	if rerunCleanup.CleanupRescueKills != 0 || rerunCleanup.WaitCalls != 1 {
		t.Fatalf("rerun lifecycle mismatch: %+v", rerunCleanup)
	}
	rerunState := pipelineFaultChildStateAt(t, root)
	rerunSnapshot := pipelineFaultCaptureAfterWait(t, root, rerunState, expectation, "process-"+kind+"-rerun")
	rerunSnapshot.Exit = fmt.Sprintf("exit=%d signaled=%t signal=%s", rerunCleanup.ExitStatus, rerunCleanup.Signaled, rerunCleanup.Signal)
	rerunSnapshot.Reaped = fmt.Sprintf("actual Wait confirmed: wait_calls=%d pid/group/proc absent", rerunCleanup.WaitCalls)
	rerunSnapshot.Rerun = "typed missing-record refusal; old stage not republished"
	if rerunState.ErrorPhase != "prepare" || rerunState.RecoveryReason != "invalid or missing operation record" ||
		!rerunState.InvalidRecord || !rerunState.NotExist {
		t.Fatalf("rerun refusal phase=%q reason=%q invalid_record=%t not_exist=%t",
			rerunState.ErrorPhase, rerunState.RecoveryReason, rerunState.InvalidRecord, rerunState.NotExist)
	}
	if rerunState.GenerateCalls != 0 {
		t.Fatalf("rerun generated %d unit(s), want 0", rerunState.GenerateCalls)
	}
	if afterRunDirs := pipelineFaultRunDirs(t, root); !slices.Equal(afterRunDirs, beforeRunDirs) {
		t.Fatalf("rerun changed retained run dirs: before=%v after=%v", beforeRunDirs, afterRunDirs)
	}
	for _, label := range []string{"source", "output", "stage"} {
		before, after := snapshot.Files[label], rerunSnapshot.Files[label]
		if before.Exists != after.Exists || !bytes.Equal(before.Bytes, after.Bytes) ||
			before.Dev != after.Dev || before.Inode != after.Inode || before.MountID != after.MountID {
			t.Fatalf("rerun changed %s: before=%+v after=%+v", label, before, after)
		}
	}
	if rerunSnapshot.Files["record"].Exists || rerunSnapshot.Files["backup"].Exists {
		t.Fatal("rerun created a record or backup")
	}
	if diff := pipelineFixtureDiff(expectation.D0, rerunSnapshot.Rows); len(diff) != 0 {
		t.Fatalf("rerun changed D0: %v", diff)
	}
	if rerunSnapshot.Result.TMCommitted || rerunSnapshot.Result.Publication.State != publication.NotPublished {
		t.Fatalf("rerun result=%+v", rerunSnapshot.Result)
	}
	if beforeStage.Exists && !bytes.Equal(beforeStage.Bytes, rerunSnapshot.Files["stage"].Bytes) {
		t.Fatal("rerun republished or changed the retained stage")
	}
	pipelineFixtureSaveState(t, "process-"+kind+"-rerun", expectation, rerunSnapshot, rerunSnapshot.Rows)
	pipelineHarnessSaveJSON(t, "process-"+kind+"-rerun-refusal.json", map[string]any{
		"phase": rerunState.ErrorPhase, "recovery_reason": rerunState.RecoveryReason,
		"invalid_record": rerunState.InvalidRecord, "not_exist": rerunState.NotExist,
		"generate_calls": rerunState.GenerateCalls, "run_dirs": pipelineFaultRunDirs(t, root),
		"cleanup": rerunCleanup,
	})
	t.Logf("fresh child rerun typed refusal phase=%s reason=%q; D0/S/O/stage and the single run dir unchanged; no old-stage publish",
		rerunState.ErrorPhase, rerunState.RecoveryReason)
}

func pipelineChildHasStageFD(t *testing.T, pid int) bool {
	t.Helper()
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		t.Fatalf("read child fds: %v", err)
	}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(fmt.Sprintf("/proc/%d/fd", pid), entry.Name()))
		if err != nil {
			continue
		}
		if strings.Contains(target, "/.yakuori-run-") && strings.HasSuffix(target, "/stage") {
			return true
		}
	}
	return false
}

func testPipelineHarnessFaultRescue(t *testing.T) {
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestPipelineHarnessFaultRescueOwner$", "-test.v")
	cmd.Env = append(pipelineBarrierEnvWithout(pipelineTask5RootEnv, "YAKUORI_PIPELINE_EVIDENCE"),
		pipelineTask5RootEnv+"="+root, "YAKUORI_PIPELINE_EVIDENCE="+filepath.Join(root, "rescue"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return os.ErrProcessDone
	}
	cmd.WaitDelay = 2 * time.Second
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		t.Fatalf("rescue owner monitor ended before the owner exited: %v\n%s", waitErr, output.String())
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Exited() || status.ExitStatus() == 0 {
		t.Fatalf("rescue owner exit status=%#v wait=%v, want a genuine nonzero exit\n%s", status, waitErr, output.String())
	}
	if !strings.Contains(output.String(), "cleanup-rescue") || !strings.Contains(output.String(), "relied on registered cleanup") {
		t.Fatalf("rescue owner did not report the typed rescue failure\n%s", output.String())
	}
	var result pipelineBarrierRescueResult
	pipelineBarrierLoadJSON(t, filepath.Join(root, "rescue", "cleanup-rescue.json"), &result)
	if result.Cleanup.CleanupRescueKills != 1 || !result.Cleanup.Signaled || result.Cleanup.Signal != syscall.SIGKILL ||
		result.Cleanup.WaitCalls != 1 || !result.Cleanup.PIDAbsent || !result.Cleanup.GroupAbsent || !result.Cleanup.ProcAbsent {
		t.Fatalf("cleanup rescue did not end and reap the real child: %+v", result.Cleanup)
	}
	pipelineBarrierRequireAbsent(t, result.Cleanup.PID)
	pipelineHarnessSaveJSON(t, "rescue-owner.json", map[string]any{
		"exit": status.ExitStatus(), "output": output.String(), "cleanup": result.Cleanup,
	})
	t.Logf("cleanup rescue killed real fault child pid=%d and failed the owning test (exit=%d, wait_calls=%d)", result.Cleanup.PID, status.ExitStatus(), result.Cleanup.WaitCalls)
}

// TestPipelineHarnessFaultRescueOwner is selected only by the rescue control.
// It stops at ready and deliberately never releases or reaps; the registered
// cleanup must end the child and fail this owner.
func TestPipelineHarnessFaultRescueOwner(t *testing.T) {
	root := os.Getenv(pipelineTask5RootEnv)
	if root == "" {
		return
	}
	plan := pipelineBarrierPlan{Case: "rescue-text", Boundary: pipelineBoundaryStageCloseBefore, Occurrence: 1, Inject: true}
	p := startPipelineBarrier(t, plan, pipelineBarrierModePipelineError)
	if _, err := p.awaitReady(p.ctx); err != nil {
		t.Fatalf("ready: %v", err)
	}
	p.observeAlive(t)
	// No release, finish or Wait: only the registered cleanup can end this child.
}

func TestPipelineHarnessFault(t *testing.T) {
	for _, kind := range []string{"text", "w3"} {
		t.Run("stage-close/"+kind, func(t *testing.T) { testPipelineHarnessFaultStageClose(t, kind) })
		t.Run("normal-counterpart/"+kind, func(t *testing.T) { testPipelineHarnessFaultCounterpart(t, kind) })
	}
	t.Run("process/error/text", func(t *testing.T) { testPipelineHarnessFaultProcess(t, "text", "error", true) })
	t.Run("process/cancel/text", func(t *testing.T) { testPipelineHarnessFaultProcess(t, "text", "cancel", false) })
	t.Run("process/error/w3", func(t *testing.T) { testPipelineHarnessFaultProcess(t, "w3", "error", true) })
	t.Run("cleanup-rescue", testPipelineHarnessFaultRescue)
}

func pipelineMountInfo(t *testing.T, dir string) (string, string) {
	t.Helper()
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	bestPoint, bestLine := "", ""
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		point := fields[4]
		if dir == point || strings.HasPrefix(dir, strings.TrimSuffix(point, "/")+"/") {
			if len(point) > len(bestPoint) {
				bestPoint, bestLine = point, line
			}
		}
	}
	if bestLine == "" {
		t.Fatalf("no mountinfo entry for %s", dir)
	}
	separator := slices.Index(strings.Fields(bestLine), "-")
	if separator < 0 || separator+1 >= len(strings.Fields(bestLine)) {
		t.Fatalf("mountinfo entry has no filesystem field: %s", bestLine)
	}
	return strings.Fields(bestLine)[separator+1], bestLine
}

func TestPipelineUnsupportedFilesystem(t *testing.T) {
	// Given: an actually unqualified directory under the caller's TMPDIR.
	dir := t.TempDir()
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		t.Fatal(err)
	}
	filesystem, mountLine := pipelineMountInfo(t, dir)
	if filesystem != "tmpfs" && filesystem != "overlay" {
		t.Fatalf("unqualified refusal requires a real tmpfs/overlay, got %s: %s", filesystem, mountLine)
	}
	if stat.Type == unix.EXT4_SUPER_MAGIC || stat.Type == unix.BTRFS_SUPER_MAGIC {
		t.Fatalf("unqualified refusal ran on a qualified magic %#x", stat.Type)
	}
	input, ok := pipelineHarnessInput(t, "text")
	if !ok {
		t.Fatal("missing text fixture input")
	}
	f := newPipelineFixture(t, input)
	options := publication.Options{Source: filepath.Join(dir, "source"), Output: filepath.Join(dir, "output"), Mode: publication.Create}
	if err := os.WriteFile(options.Source, input.source, 0600); err != nil {
		t.Fatal(err)
	}
	sourceBefore, err := pipelineHarnessReadFile(options.Source)
	if err != nil {
		t.Fatal(err)
	}
	rowsBefore := pipelineFixtureReadRows(t, f.db)
	if diff := pipelineFixtureDiff(f.d0, rowsBefore); len(diff) != 0 {
		t.Fatalf("D0 changed before the refusal: %v", diff)
	}

	// When: the public FileWithTM surface meets the real unqualified mount.
	result, err := f.core.FileWithTM(t.Context(), options, f.adapter, f.profile, f.store, f.scope)
	// Then: only the typed prepare refusal is accepted; later stages are not-run.
	var failure *Error
	if !errors.Is(err, publication.ErrUnsupportedFilesystem) || !errors.As(err, &failure) || failure.Phase != "prepare" ||
		result.TMCommitted || result.BytesWritten != 0 || result.Publication.State != publication.NotPublished {
		t.Fatalf("want typed unsupported-filesystem prepare refusal: result=%+v error=%v", result, err)
	}
	refusal := err.Error()
	if len(f.backend.calls) != 0 {
		t.Fatalf("refusal generated %d unit(s), want 0", len(f.backend.calls))
	}
	if diff := pipelineFixtureDiff(rowsBefore, pipelineFixtureReadRows(t, f.db)); len(diff) != 0 {
		t.Fatalf("refusal changed D0: %v", diff)
	}
	sourceAfter, err := pipelineHarnessReadFile(options.Source)
	if err != nil {
		t.Fatal(err)
	}
	if !sourceAfter.Exists || !bytes.Equal(sourceAfter.Bytes, input.source) ||
		sourceAfter.Dev != sourceBefore.Dev || sourceAfter.Inode != sourceBefore.Inode ||
		sourceAfter.MountID != sourceBefore.MountID || sourceAfter.Mode != sourceBefore.Mode || sourceAfter.Nlink != sourceBefore.Nlink {
		t.Fatalf("refusal changed the source identity: before=%+v after=%+v", sourceBefore, sourceAfter)
	}
	if _, err := os.Lstat(options.Output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refusal created output: %v", err)
	}
	runDirs, err := filepath.Glob(filepath.Join(dir, ".yakuori-run-*"))
	if err != nil || len(runDirs) != 0 {
		t.Fatalf("refusal created run dirs=%v error=%v", runDirs, err)
	}
	state := map[string]any{
		"case": "unsupported-filesystem/text", "filesystem": filesystem, "mountinfo": mountLine,
		"statfs_magic": stat.Type, "phase": failure.Phase, "error": refusal,
		"generate_calls": 0, "rows": rowsBefore, "source": sourceBefore, "output_present": false,
		"result": result,
		"later_stages": map[string]string{
			"import":     "not-run: Prepare refused before Import",
			"generate":   "not-run: Prepare refused before planning/generation",
			"stage":      "not-run: Prepare refused before publication Run.Stage",
			"observe":    "not-run: no stage exists",
			"tm-commit":  "not-run: no candidate reached commit",
			"publish":    "not-run: no record or rename",
			"diagnostic": "not-run: this is the Core refusal, not the CLI writer contract",
		},
	}
	pipelineHarnessSaveJSON(t, "refusal.json", state)
	pipelineHarnessSaveJSON(t, "refusal-rows.json", rowsBefore)
	t.Logf("unsupported %s refusal: phase=%s, Generate=0, D0=%d, source identity unchanged, output absent; later stages recorded not-run",
		filesystem, failure.Phase, len(rowsBefore))
}
