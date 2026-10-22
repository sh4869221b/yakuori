//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/publication"
	"golang.org/x/sys/unix"
)

// The state table keeps the expected outcome of a scenario independent from the
// run that produced an observation. S/O/T/D0/D1 come from the authored fixture
// bytes and rows, never from the Core result, reached list or DB readback.
type pipelineHarnessStatus string

const (
	pipelineHarnessPass   pipelineHarnessStatus = "pass"
	pipelineHarnessFail   pipelineHarnessStatus = "fail"
	pipelineHarnessNotRun pipelineHarnessStatus = "not-run"
	pipelineHarnessNA     pipelineHarnessStatus = "na"
)

type pipelineHarnessCertaintyValue string

const (
	pipelineHarnessCommitted    pipelineHarnessCertaintyValue = "committed"
	pipelineHarnessNotCommitted pipelineHarnessCertaintyValue = "not-committed"
	pipelineHarnessUnknown      pipelineHarnessCertaintyValue = "unknown"
)

type pipelineHarnessCase struct {
	name    string
	kind    string
	surface string
	mode    publication.Mode
}

func pipelineHarnessCases() []pipelineHarnessCase {
	return []pipelineHarnessCase{
		{name: "file-replace-existing/text", kind: "text", surface: "file", mode: publication.Replace},
		{name: "file-replace-existing/w3", kind: "w3", surface: "file", mode: publication.Replace},
		{name: "stdout/text", kind: "text", surface: "stdout", mode: publication.Replace},
	}
}

func pipelineHarnessInput(t *testing.T, kind string) (pipelineFixtureInput, bool) {
	t.Helper()
	for _, candidate := range pipelineFixtureTable(t) {
		if candidate.kind == kind {
			return candidate, true
		}
	}
	return pipelineFixtureInput{}, false
}

type pipelineHarnessExpectation struct {
	Case        string                `json:"case"`
	Kind        string                `json:"kind"`
	Mode        string                `json:"mode"`
	Surface     string                `json:"surface"`
	Status      pipelineHarnessStatus `json:"status"`
	Reason      string                `json:"reason,omitempty"`
	Outcome     string                `json:"outcome"`
	PlanOutcome string                `json:"plan_outcome,omitempty"`

	SourceBytes []byte               `json:"source_bytes"`
	OldOutput   []byte               `json:"old_output"`
	TargetBytes []byte               `json:"target_bytes"`
	D0          []pipelineFixtureRow `json:"d0"`
	D1          []pipelineFixtureRow `json:"d1"`
	FinalRows   []pipelineFixtureRow `json:"final_rows"`

	StageRetained   bool   `json:"stage_retained"`
	StageBytes      []byte `json:"stage_bytes,omitempty"`
	OutputBytes     []byte `json:"output_bytes"`
	StdoutBytes     []byte `json:"stdout_bytes,omitempty"`
	BackupPresent   bool   `json:"backup_present"`
	RecordInstalled bool   `json:"record_installed"`
	RecordPhase     string `json:"record_phase,omitempty"`
	RecordOperation string `json:"record_operation,omitempty"`

	CommitCertainty  pipelineHarnessCertaintyValue `json:"commit_certainty"`
	StageCheckpoint  bool                          `json:"stage_checkpoint"`
	CommitCheckpoint bool                          `json:"commit_checkpoint"`
	ResultPresent    bool                          `json:"result_present"`

	BoundaryOrder      []string       `json:"boundary_order"`
	RequiredBoundaries map[string]int `json:"required_boundaries"`
}

func pipelineHarnessExpectationForFixture(t *testing.T, spec pipelineHarnessCase, f *pipelineFixture) pipelineHarnessExpectation {
	t.Helper()
	input := f.input
	d1 := f.expectedRows(t, input.units)
	expectation := pipelineHarnessExpectation{
		Case: spec.name, Kind: spec.kind, Mode: string(spec.mode), Surface: spec.surface,
		Status: pipelineHarnessPass, Outcome: pipelineHarnessPublishedOutcome(spec.surface),
		SourceBytes: slices.Clone(input.source), OldOutput: slices.Clone(pipelineHarnessOldOutput),
		TargetBytes: slices.Clone(input.target),
		D0:          slices.Clone(f.d0), D1: slices.Clone(d1), FinalRows: slices.Clone(d1),
		StageCheckpoint: spec.surface == "file", CommitCheckpoint: true,
		CommitCertainty: pipelineHarnessCommitted, ResultPresent: true,
		BoundaryOrder: pipelineHarnessOrder(input, spec.surface),
	}
	if spec.surface == "file" {
		expectation.OutputBytes = slices.Clone(input.target)
		expectation.RecordInstalled = true
		expectation.RecordPhase = "published"
		expectation.RecordOperation = "publish-rename"
		expectation.RequiredBoundaries = pipelineHarnessPublishedBoundaries(input)
	} else {
		expectation.StdoutBytes = slices.Clone(input.target)
		expectation.RequiredBoundaries = pipelineHarnessStdoutBoundaries(input)
	}
	return expectation
}

func pipelineHarnessOrder(input pipelineFixtureInput, surface string) []string {
	var order []string
	for _, unit := range input.units {
		if unit.source == "" {
			continue
		}
		order = append(order, pipelineBoundaryGenerateBefore, pipelineBoundaryGenerateAfter)
	}
	if surface == "file" {
		order = append(order,
			pipelineBoundaryStageWriteBefore, pipelineBoundaryStageWriteAfter,
			pipelineBoundaryObserveBefore, pipelineBoundaryObserveAfter,
			pipelineBoundaryCompareFinalAfter,
			pipelineBoundaryTMCommitBefore, pipelineBoundaryTMCommitAfter,
			pipelineBoundaryRunCloseAfter)
	} else {
		order = append(order,
			pipelineBoundaryObserveBefore, pipelineBoundaryObserveAfter,
			pipelineBoundaryTMCommitAfter,
			pipelineBoundaryStdoutWriteBefore, pipelineBoundaryStdoutWriteAfter)
	}
	return order
}

func pipelineHarnessGenerateCount(input pipelineFixtureInput) int {
	count := 0
	for _, unit := range input.units {
		if unit.source != "" {
			count++
		}
	}
	return count
}

func pipelineHarnessPublishedBoundaries(input pipelineFixtureInput) map[string]int {
	generate := pipelineHarnessGenerateCount(input)
	return map[string]int{
		pipelineBoundaryGenerateBefore: generate, pipelineBoundaryGenerateAfter: generate,
		pipelineBoundaryStageWriteBefore: 1, pipelineBoundaryStageWriteAfter: 1,
		pipelineBoundaryStageSyncBefore: 1, pipelineBoundaryStageSyncAfter: 1,
		pipelineBoundaryStageCloseBefore: 1, pipelineBoundaryStageCloseAfter: 1,
		pipelineBoundaryObserveBefore: 1, pipelineBoundaryObserveAfter: 1,
		pipelineBoundaryCompareFinalAfter: 1,
		pipelineBoundaryTMCommitBefore:    1, pipelineBoundaryTMCommitAfter: 1,
		pipelineBoundaryRecordWriteBefore: 2, pipelineBoundaryRecordWriteAfter: 2,
		pipelineBoundaryRecordSyncBefore: 4, pipelineBoundaryRecordSyncAfter: 4,
		pipelineBoundaryRecordCloseBefore: 2, pipelineBoundaryRecordCloseAfter: 2,
		pipelineBoundaryRecordInstallBefore: 2, pipelineBoundaryRecordInstallAfter: 2,
		pipelineBoundaryPublishRenameBefore: 1, pipelineBoundaryPublishRenameAfter: 1,
		pipelineBoundaryRunCloseAfter: 1,
	}
}

func pipelineHarnessStdoutBoundaries(input pipelineFixtureInput) map[string]int {
	generate := pipelineHarnessGenerateCount(input)
	return map[string]int{
		pipelineBoundaryGenerateBefore: generate, pipelineBoundaryGenerateAfter: generate,
		pipelineBoundaryObserveBefore: 1, pipelineBoundaryObserveAfter: 1,
		pipelineBoundaryTMCommitAfter:     1,
		pipelineBoundaryStdoutWriteBefore: 1, pipelineBoundaryStdoutWriteAfter: 1,
	}
}

// pipelineHarnessScenarioTable is the initial mode x boundary x stimulus table.
// Only the "pass" rows are executed by this unit. Applicable cells owned by
// later tasks stay not-run, and structurally absent operations are NA; neither
// status is convertible into a pass.
func pipelineHarnessScenarioTable(t *testing.T) []pipelineHarnessExpectation {
	t.Helper()
	text, _ := pipelineHarnessInput(t, "text")
	w3, _ := pipelineHarnessInput(t, "w3")
	textFixture := newPipelineFixture(t, text)
	w3Fixture := newPipelineFixture(t, w3)
	publishedText := pipelineHarnessExpectationForFixture(t, pipelineHarnessCase{name: "file-replace-existing/text", kind: "text", surface: "file", mode: publication.Replace}, textFixture)
	publishedW3 := pipelineHarnessExpectationForFixture(t, pipelineHarnessCase{name: "file-replace-existing/w3", kind: "w3", surface: "file", mode: publication.Replace}, w3Fixture)
	publishedStdout := pipelineHarnessExpectationForFixture(t, pipelineHarnessCase{name: "stdout/text", kind: "text", surface: "stdout", mode: publication.Replace}, textFixture)

	precommit := publishedText
	precommit.Case = "precommit-stage-close/text"
	precommit.Status = pipelineHarnessNotRun
	precommit.Reason = "task5 executes the minimal fault set; this unit records the static expectation and only judges negative observations against it"
	precommit.Outcome = "not-published"
	precommit.PlanOutcome = "D0/F0"
	precommit.StageRetained = true
	precommit.StageBytes = slices.Clone(text.target)
	precommit.OutputBytes = slices.Clone(pipelineHarnessOldOutput)
	precommit.RecordInstalled = false
	precommit.RecordPhase = ""
	precommit.RecordOperation = ""
	precommit.CommitCertainty = pipelineHarnessNotCommitted
	precommit.CommitCheckpoint = false
	precommit.FinalRows = slices.Clone(textFixture.d0)
	precommit.BoundaryOrder = []string{
		pipelineBoundaryGenerateBefore, pipelineBoundaryGenerateAfter,
		pipelineBoundaryStageWriteBefore, pipelineBoundaryStageWriteAfter,
		pipelineBoundaryRunCloseAfter,
	}
	precommit.RequiredBoundaries = map[string]int{
		pipelineBoundaryGenerateBefore: 1, pipelineBoundaryGenerateAfter: 1,
		pipelineBoundaryStageWriteBefore: 1, pipelineBoundaryStageWriteAfter: 1,
		pipelineBoundaryStageSyncBefore: 1, pipelineBoundaryStageSyncAfter: 1,
		pipelineBoundaryStageCloseBefore: 1,
		pipelineBoundaryRunCloseAfter:    1,
	}

	postcommit := publishedText
	postcommit.Case = "postcommit-prepublish/text"
	postcommit.Status = pipelineHarnessNotRun
	postcommit.Reason = "commit accepted but the prepared record is not closed yet; task5 executes this fault"
	postcommit.Outcome = "not-published"
	postcommit.PlanOutcome = "D1/F0"
	postcommit.StageRetained = true
	postcommit.StageBytes = slices.Clone(text.target)
	postcommit.OutputBytes = slices.Clone(pipelineHarnessOldOutput)
	postcommit.RecordInstalled = false
	postcommit.RecordPhase = ""
	postcommit.RecordOperation = ""
	postcommit.BoundaryOrder = []string{
		pipelineBoundaryGenerateBefore, pipelineBoundaryGenerateAfter,
		pipelineBoundaryStageWriteBefore, pipelineBoundaryStageWriteAfter,
		pipelineBoundaryObserveBefore, pipelineBoundaryObserveAfter,
		pipelineBoundaryCompareFinalAfter,
		pipelineBoundaryTMCommitBefore, pipelineBoundaryTMCommitAfter,
		pipelineBoundaryRunCloseAfter,
	}
	postcommit.RequiredBoundaries = map[string]int{
		pipelineBoundaryGenerateBefore: 1, pipelineBoundaryGenerateAfter: 1,
		pipelineBoundaryStageWriteBefore: 1, pipelineBoundaryStageWriteAfter: 1,
		pipelineBoundaryStageSyncBefore: 1, pipelineBoundaryStageSyncAfter: 1,
		pipelineBoundaryStageCloseBefore: 1, pipelineBoundaryStageCloseAfter: 1,
		pipelineBoundaryObserveBefore: 1, pipelineBoundaryObserveAfter: 1,
		pipelineBoundaryCompareFinalAfter: 1,
		pipelineBoundaryTMCommitBefore:    1, pipelineBoundaryTMCommitAfter: 1,
		pipelineBoundaryRunCloseAfter: 1,
	}

	restored := publishedText
	restored.Case = "in-place-error-restored/text"
	restored.Status = pipelineHarnessNotRun
	restored.Reason = "in-place ordinary error recovery is owned by task5; expectation follows the documented Restored outcome"
	restored.Mode = string(publication.InPlace)
	restored.Outcome = "restored"
	restored.PlanOutcome = "Restored"
	restored.OutputBytes = slices.Clone(text.source)
	restored.StageRetained = false
	restored.RecordInstalled = true
	restored.RecordPhase = "restored"
	restored.RecordOperation = "restore"
	restored.BoundaryOrder = nil
	restored.RequiredBoundaries = nil

	publishedThenError := publishedText
	publishedThenError.Case = "published-then-error/text"
	publishedThenError.Status = pipelineHarnessNotRun
	publishedThenError.Reason = "post-publish diagnostic failure is recorded by the existing CLI writer contract in task5; publication must not be rolled back"
	publishedThenError.Outcome = "file-published"
	publishedThenError.PlanOutcome = "D1/Published"

	stdoutPrefix := publishedStdout
	stdoutPrefix.Case = "stdout-prefix/text"
	stdoutPrefix.Status = pipelineHarnessNotRun
	stdoutPrefix.Reason = "a delivered stdout prefix cannot be retracted; task5 owns the short-write stimulus"
	stdoutPrefix.Outcome = "stdout-prefix-error"
	stdoutPrefix.PlanOutcome = "D1/delivered-prefix-unrecoverable"
	stdoutPrefix.StdoutBytes = slices.Clone(text.target[:4])
	stdoutPrefix.BoundaryOrder = []string{
		pipelineBoundaryGenerateBefore, pipelineBoundaryGenerateAfter,
		pipelineBoundaryObserveBefore, pipelineBoundaryObserveAfter,
		pipelineBoundaryTMCommitAfter, pipelineBoundaryStdoutWriteBefore,
	}
	stdoutPrefix.RequiredBoundaries = map[string]int{
		pipelineBoundaryGenerateBefore: 1, pipelineBoundaryGenerateAfter: 1,
		pipelineBoundaryObserveBefore: 1, pipelineBoundaryObserveAfter: 1,
		pipelineBoundaryTMCommitAfter: 1, pipelineBoundaryStdoutWriteBefore: 1,
	}

	closedOnly := precommit
	closedOnly.Case = "record-close-before-install/text"
	closedOnly.Reason = "record close completion is not installation; task5 owns the record-close fault"
	closedOnly.RequiredBoundaries = map[string]int{
		pipelineBoundaryGenerateBefore: 1, pipelineBoundaryGenerateAfter: 1,
		pipelineBoundaryStageWriteBefore: 1, pipelineBoundaryStageWriteAfter: 1,
		pipelineBoundaryObserveBefore: 1, pipelineBoundaryObserveAfter: 1,
		pipelineBoundaryCompareFinalAfter: 1,
		pipelineBoundaryTMCommitBefore:    1, pipelineBoundaryTMCommitAfter: 1,
		pipelineBoundaryRecordWriteBefore: 1, pipelineBoundaryRecordWriteAfter: 1,
		pipelineBoundaryRecordSyncBefore: 1, pipelineBoundaryRecordSyncAfter: 1,
		pipelineBoundaryRecordCloseBefore: 1, pipelineBoundaryRecordCloseAfter: 1,
		pipelineBoundaryRunCloseAfter: 1,
	}
	closedOnly.BoundaryOrder = []string{
		pipelineBoundaryGenerateBefore, pipelineBoundaryGenerateAfter,
		pipelineBoundaryStageWriteBefore, pipelineBoundaryStageWriteAfter,
		pipelineBoundaryObserveBefore, pipelineBoundaryObserveAfter,
		pipelineBoundaryCompareFinalAfter, pipelineBoundaryTMCommitBefore, pipelineBoundaryTMCommitAfter,
		pipelineBoundaryRunCloseAfter,
	}

	killed := publishedText
	killed.Case = "process-kill/text"
	killed.Status = pipelineHarnessNotRun
	killed.Reason = "SIGKILL produces state without a returned Result; task4/5 own process-death evidence"
	killed.ResultPresent = false
	killed.Outcome = "unknown"
	killed.PlanOutcome = "SIGKILL: state without a returned Result"
	killed.CommitCertainty = pipelineHarnessUnknown
	killed.FinalRows = nil
	killed.BoundaryOrder = nil
	killed.RequiredBoundaries = nil

	flush := publishedText
	flush.Case = "stage-flush/text"
	flush.Status = pipelineHarnessNA
	flush.Reason = "publication Run.Stage has no Flush operation; stage durability boundaries are Sync and Close"
	flush.Outcome = "na"
	flush.PlanOutcome = "NA"

	stdoutPublication := publishedStdout
	stdoutPublication.Case = "stdout-file-publication/text"
	stdoutPublication.Status = pipelineHarnessNA
	stdoutPublication.Reason = "the text stdout surface performs no Prepare, record, rename or backup operation"
	stdoutPublication.Outcome = "na"
	stdoutPublication.PlanOutcome = "NA"

	stdoutCommitBefore := publishedStdout
	stdoutCommitBefore.Case = "stdout-tm-commit-before/text"
	stdoutCommitBefore.Status = pipelineHarnessNotRun
	stdoutCommitBefore.Reason = "Core.MText has no strict pre-commit callback; the Observe return is not substituted for it (Issue68 contract)"
	stdoutCommitBefore.Outcome = "not-run"
	stdoutCommitBefore.PlanOutcome = "not-run"

	tmCommitError := postcommit
	tmCommitError.Case = "tm-commit-error/text"
	tmCommitError.Reason = "the commit-error stimulus is a trigger on the same real SQLite database; Store/Run and the transaction implementation stay unfaked, and task5 executes it"
	tmCommitError.Outcome = "not-published"
	tmCommitError.PlanOutcome = "D0/F0"
	tmCommitError.CommitCertainty = pipelineHarnessNotCommitted
	tmCommitError.CommitCheckpoint = false
	tmCommitError.RecordInstalled = false
	tmCommitError.FinalRows = slices.Clone(textFixture.d0)
	tmCommitError.BoundaryOrder = []string{
		pipelineBoundaryGenerateBefore, pipelineBoundaryGenerateAfter,
		pipelineBoundaryStageWriteBefore, pipelineBoundaryStageWriteAfter,
		pipelineBoundaryObserveBefore, pipelineBoundaryObserveAfter,
		pipelineBoundaryCompareFinalAfter, pipelineBoundaryTMCommitBefore,
		pipelineBoundaryRunCloseAfter,
	}
	tmCommitError.RequiredBoundaries = map[string]int{
		pipelineBoundaryGenerateBefore: 1, pipelineBoundaryGenerateAfter: 1,
		pipelineBoundaryStageWriteBefore: 1, pipelineBoundaryStageWriteAfter: 1,
		pipelineBoundaryStageSyncBefore: 1, pipelineBoundaryStageSyncAfter: 1,
		pipelineBoundaryStageCloseBefore: 1, pipelineBoundaryStageCloseAfter: 1,
		pipelineBoundaryObserveBefore: 1, pipelineBoundaryObserveAfter: 1,
		pipelineBoundaryCompareFinalAfter: 1, pipelineBoundaryTMCommitBefore: 1,
		pipelineBoundaryRunCloseAfter: 1,
	}

	publishedDiagnostic := publishedText
	publishedDiagnostic.Case = "published-diagnostic/text"
	publishedDiagnostic.Status = pipelineHarnessNotRun
	publishedDiagnostic.Reason = "post-publication diagnostics are a later contract through the existing CLI reportPipeline writer argument; task5 executes it and publication is never rolled back"
	publishedDiagnostic.Outcome = "file-published"
	publishedDiagnostic.PlanOutcome = "D1/published-unrecoverable"

	return []pipelineHarnessExpectation{
		publishedText, publishedW3, publishedStdout,
		precommit, postcommit, restored, publishedThenError, stdoutPrefix, closedOnly, killed,
		flush, stdoutPublication, stdoutCommitBefore, tmCommitError, publishedDiagnostic,
	}
}

type pipelineHarnessFile struct {
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Bytes   []byte `json:"bytes,omitempty"`
	Inode   uint64 `json:"inode,omitempty"`
	MountID uint64 `json:"mount_id,omitempty"`
	Mode    uint16 `json:"mode,omitempty"`
	Nlink   uint32 `json:"nlink,omitempty"`
	Size    int64  `json:"size,omitempty"`
}

type pipelineHarnessRecordIdentity struct {
	Dev     uint64 `json:"dev"`
	Ino     uint64 `json:"ino"`
	MountID uint64 `json:"mount_id"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
	Nlink   uint64 `json:"nlink"`
}

type pipelineHarnessRecordFile struct {
	Path     string                        `json:"path"`
	Basename string                        `json:"basename"`
	Identity pipelineHarnessRecordIdentity `json:"identity"`
	SHA256   string                        `json:"sha256"`
	Size     int64                         `json:"size"`
}

type pipelineHarnessRecord struct {
	SchemaVersion          int                       `json:"schema_version"`
	RunID                  string                    `json:"run_id"`
	Mode                   string                    `json:"mode"`
	Source                 pipelineHarnessRecordFile `json:"source"`
	Stage                  pipelineHarnessRecordFile `json:"stage"`
	Phase                  string                    `json:"phase"`
	LastCompletedOperation string                    `json:"last_completed_operation"`
}

type pipelineHarnessSnapshot struct {
	Case    string                         `json:"case"`
	Kind    string                         `json:"kind"`
	Mode    string                         `json:"mode"`
	Surface string                         `json:"surface"`
	Files   map[string]pipelineHarnessFile `json:"files"`
	Rows    []pipelineFixtureRow           `json:"rows"`

	RowsAfterStage []pipelineFixtureRow         `json:"rows_after_stage,omitempty"`
	RowsAtCommit   []pipelineFixtureRow         `json:"rows_at_commit,omitempty"`
	Reached        map[string]int               `json:"reached"`
	Order          []string                     `json:"order"`
	RecordEvents   []pipelineHarnessRecordEvent `json:"record_events,omitempty"`
	BoundKeys      []string                     `json:"bound_keys,omitempty"`
	KeyProvider    bool                         `json:"key_provider"`

	Record          *pipelineHarnessRecord        `json:"record,omitempty"`
	RecordClosed    bool                          `json:"record_closed"`
	RecordInstalled bool                          `json:"record_installed"`
	StageClosed     bool                          `json:"stage_closed"`
	TMCommitted     bool                          `json:"tm_committed"`
	ResultPresent   bool                          `json:"result_present"`
	Result          Result                        `json:"result"`
	Error           string                        `json:"error,omitempty"`
	Certainty       pipelineHarnessCertaintyValue `json:"certainty"`
	CertaintyBasis  []string                      `json:"certainty_basis"`
	StdoutBytes     int                           `json:"stdout_bytes"`
	Exit            string                        `json:"exit"`
	Signal          string                        `json:"signal"`
	Rerun           string                        `json:"rerun"`
	Reaped          string                        `json:"reaped"`
	Outcome         string                        `json:"outcome"`
	Filesystem      string                        `json:"filesystem"`
}

func (h *pipelineHarness) snapshot(exp pipelineHarnessExpectation) (pipelineHarnessSnapshot, error) {
	snapshot := pipelineHarnessSnapshot{
		Case: exp.Case, Kind: h.kind, Mode: string(h.mode), Surface: h.surface,
		Files:          map[string]pipelineHarnessFile{},
		Rows:           pipelineFixtureReadRows(h.t, h.fixture.db),
		RowsAfterStage: slices.Clone(h.rowsAfterStage),
		RowsAtCommit:   slices.Clone(h.rowsAtCommit),
		Reached:        map[string]int{},
		TMCommitted:    h.result.TMCommitted,
		ResultPresent:  true,
		Result:         h.result,
		Exit:           "in-process Core call; no child process",
		Signal:         "none",
		Rerun:          "not-run: same-target rerun belongs to task5",
		Reaped:         "NA: no child process",
		Filesystem:     pipelineHarnessFilesystem(h.t, h.stdoutOrSource()),
	}
	if h.runErr != nil {
		snapshot.Error = h.runErr.Error()
	}
	if h.trace != nil {
		snapshot.Reached = maps.Clone(h.trace.reached)
		snapshot.Order = slices.Clone(h.trace.order)
	}
	if h.ops != nil {
		snapshot.RecordEvents = slices.Clone(h.ops.events)
	}
	if h.memory != nil {
		for _, relation := range h.memory.scope.Keys {
			snapshot.BoundKeys = append(snapshot.BoundKeys, relation.UnitID.StableID()+":"+hex.EncodeToString([]byte(relation.Key)))
		}
		_, snapshot.KeyProvider = any(h.adapter).(keyRelationProvider)
	}
	snapshot.StageClosed = snapshot.Reached[pipelineBoundaryStageCloseAfter] > 0
	snapshot.RecordClosed = snapshot.Reached[pipelineBoundaryRecordCloseAfter] > 0

	paths := map[string]string{}
	paths["source"] = h.sourcePath
	if h.surface == "stdout" {
		paths["stdout"] = h.stdoutPath
	} else {
		paths["output"] = h.options.Output
		if h.ops != nil {
			paths["stage"], paths["record"], paths["backup"] = h.ops.paths.Stage, h.ops.paths.Record, h.ops.paths.Backup
		}
		if record := h.result.Publication.RecordPath; record != "" {
			paths["record"] = record
			paths["stage"] = filepath.Join(filepath.Dir(record), "stage")
			paths["backup"] = h.result.Publication.BackupPath
		}
	}
	for label, path := range paths {
		if path == "" {
			continue
		}
		file, err := pipelineHarnessReadFile(path)
		if err != nil {
			return pipelineHarnessSnapshot{}, err
		}
		snapshot.Files[label] = file
	}
	if snapshot.Files["record"].Exists {
		var record pipelineHarnessRecord
		if err := json.Unmarshal(snapshot.Files["record"].Bytes, &record); err != nil {
			return pipelineHarnessSnapshot{}, fmt.Errorf("decode publication record: %w", err)
		}
		snapshot.Record = &record
	}
	snapshot.RecordInstalled = snapshot.Files["record"].Exists && snapshot.Reached[pipelineBoundaryRecordInstallAfter] > 0
	snapshot.StdoutBytes = len(snapshot.Files["stdout"].Bytes)
	snapshot.Outcome = pipelineHarnessObserveOutcome(snapshot)
	snapshot.Certainty, snapshot.CertaintyBasis = pipelineHarnessObserveCertainty(snapshot)
	return snapshot, nil
}

func (h *pipelineHarness) stdoutOrSource() string {
	if h.surface == "stdout" {
		return h.stdoutPath
	}
	return h.options.Source
}

func pipelineHarnessReadFile(path string) (pipelineHarnessFile, error) {
	file := pipelineHarnessFile{Path: path}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return file, err
	}
	var stat unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &stat); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return file, nil
		}
		return file, err
	}
	file.Exists = true
	file.Bytes = raw
	file.Inode, file.MountID, file.Mode = stat.Ino, stat.Mnt_id, stat.Mode
	file.Nlink, file.Size = stat.Nlink, int64(len(raw))
	return file, nil
}

func pipelineHarnessFilesystem(t *testing.T, path string) string {
	t.Helper()
	dir := filepath.Dir(path)
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		t.Fatalf("statfs %s: %v", dir, err)
	}
	switch stat.Type {
	case unix.BTRFS_SUPER_MAGIC:
		return "btrfs"
	case unix.EXT4_SUPER_MAGIC:
		return "ext4"
	default:
		return fmt.Sprintf("0x%x", stat.Type)
	}
}

func (s pipelineHarnessSnapshot) clone() pipelineHarnessSnapshot {
	clone := s
	clone.Files = maps.Clone(s.Files)
	clone.Rows = slices.Clone(s.Rows)
	clone.RowsAfterStage = slices.Clone(s.RowsAfterStage)
	clone.RowsAtCommit = slices.Clone(s.RowsAtCommit)
	clone.Reached = maps.Clone(s.Reached)
	clone.Order = slices.Clone(s.Order)
	clone.RecordEvents = slices.Clone(s.RecordEvents)
	clone.CertaintyBasis = slices.Clone(s.CertaintyBasis)
	if s.Record != nil {
		record := *s.Record
		clone.Record = &record
	}
	return clone
}

// pipelineHarnessObserveCertainty derives the commit state from DB checkpoints,
// never from TMCommitted alone. Without a checkpoint the answer is unknown.
func pipelineHarnessObserveCertainty(snapshot pipelineHarnessSnapshot) (pipelineHarnessCertaintyValue, []string) {
	var basis []string
	if snapshot.RowsAfterStage != nil {
		basis = append(basis, "rows-after-stage")
	}
	if snapshot.RowsAtCommit != nil {
		basis = append(basis, "rows-at-commit")
	}
	switch {
	case snapshot.RowsAtCommit != nil && snapshot.TMCommitted:
		return pipelineHarnessCommitted, basis
	case snapshot.RowsAfterStage != nil && snapshot.RowsAtCommit == nil && !snapshot.TMCommitted:
		return pipelineHarnessNotCommitted, basis
	default:
		if snapshot.TMCommitted {
			basis = append(basis, "tm-committed-flag")
		}
		return pipelineHarnessUnknown, append(basis, "no-commit-checkpoint")
	}
}

func pipelineHarnessObservationNames() map[string]bool {
	return map[string]bool{
		pipelineBoundaryGenerateBefore: true, pipelineBoundaryGenerateAfter: true,
		pipelineBoundaryStageWriteBefore: true, pipelineBoundaryStageWriteAfter: true,
		pipelineBoundaryObserveBefore: true, pipelineBoundaryObserveAfter: true,
		pipelineBoundaryCompareFinalAfter: true,
		pipelineBoundaryTMCommitBefore:    true, pipelineBoundaryTMCommitAfter: true,
		pipelineBoundaryStdoutWriteBefore: true, pipelineBoundaryStdoutWriteAfter: true,
		pipelineBoundaryRunCloseAfter: true,
	}
}

func pipelineHarnessFilteredOrder(order []string) []string {
	names := pipelineHarnessObservationNames()
	filtered := make([]string, 0, len(order))
	for _, event := range order {
		if names[event] {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func pipelineHarnessPublishedOutcome(surface string) string {
	if surface == "stdout" {
		return "stdout-delivered"
	}
	return "file-published"
}

// pipelineHarnessObserveOutcome derives the observable outcome from the returned
// Result, the delivered bytes and the error; it never copies the scenario label.
func pipelineHarnessObserveOutcome(snapshot pipelineHarnessSnapshot) string {
	if snapshot.Surface == "stdout" {
		switch {
		case snapshot.StdoutBytes > 0 && snapshot.Error == "":
			return "stdout-delivered"
		case snapshot.StdoutBytes > 0:
			return "stdout-prefix-error"
		case snapshot.ResultPresent:
			return "stdout-not-delivered"
		default:
			return "unknown"
		}
	}
	if !snapshot.ResultPresent {
		return "unknown"
	}
	switch snapshot.Result.Publication.State {
	case publication.Published:
		return "file-published"
	case publication.Restored:
		return "restored"
	case publication.SourceBackedUp:
		return "source-backed-up"
	default:
		return "not-published"
	}
}

func pipelineHarnessJudge(expectation pipelineHarnessExpectation, snapshot pipelineHarnessSnapshot) []string {
	var problems []string
	problems = append(problems, pipelineHarnessCheckBoundaries(expectation, snapshot)...)
	problems = append(problems, pipelineHarnessCheckFiles(expectation, snapshot)...)
	problems = append(problems, pipelineHarnessCheckRows(expectation, snapshot)...)
	problems = append(problems, pipelineHarnessCheckCertainty(expectation, snapshot)...)
	if snapshot.ResultPresent != expectation.ResultPresent {
		problems = append(problems, fmt.Sprintf("returned Result presence = %t, want %t", snapshot.ResultPresent, expectation.ResultPresent))
	}
	if snapshot.Outcome != expectation.Outcome {
		problems = append(problems, fmt.Sprintf("observation outcome %q, want %q", snapshot.Outcome, expectation.Outcome))
	}
	return problems
}

func pipelineHarnessCheckBoundaries(expectation pipelineHarnessExpectation, snapshot pipelineHarnessSnapshot) []string {
	var problems []string
	observed := pipelineHarnessFilteredOrder(snapshot.Order)
	if !slices.Equal(observed, expectation.BoundaryOrder) {
		problems = append(problems, fmt.Sprintf("boundary order = %v, want %v", observed, expectation.BoundaryOrder))
	}
	for name, want := range expectation.RequiredBoundaries {
		if got := snapshot.Reached[name]; got != want {
			problems = append(problems, fmt.Sprintf("boundary %s reached %d time(s), want %d", name, got, want))
		}
	}
	if expectation.Surface == "file" && expectation.RecordInstalled {
		if !pipelineHarnessBefore(snapshot.Order, pipelineBoundaryTMCommitAfter, pipelineBoundaryRecordWriteBefore) {
			problems = append(problems, "boundary order: commit entry does not precede the publication record write")
		}
		if !pipelineHarnessBefore(snapshot.Order, pipelineBoundaryTMCommitAfter, pipelineBoundaryPublishRenameBefore) {
			problems = append(problems, "boundary order: publish rename was observed before the commit entry")
		}
		if !pipelineHarnessBefore(snapshot.Order, pipelineBoundaryRecordInstallAfter, pipelineBoundaryPublishRenameBefore) {
			problems = append(problems, "boundary order: record installation does not precede the publish rename")
		}
	}
	return problems
}

// pipelineHarnessCheckFiles compares physical state, including record
// installation, against the scenario expectation. Record Close is never read as
// installation: installation requires both the installed file and a completed
// install rename.
func pipelineHarnessCheckFiles(expectation pipelineHarnessExpectation, snapshot pipelineHarnessSnapshot) []string {
	var problems []string
	problems = append(problems, pipelineHarnessCheckLeaf("source", snapshot.Files["source"], expectation.SourceBytes)...)
	if expectation.Surface == "stdout" {
		problems = append(problems, pipelineHarnessCheckLeaf("stdout", snapshot.Files["stdout"], expectation.StdoutBytes)...)
		for _, label := range []string{"output", "stage", "record", "backup"} {
			if snapshot.Files[label].Exists {
				problems = append(problems, fmt.Sprintf("%s exists on the stdout surface", label))
			}
		}
		return problems
	}
	problems = append(problems, pipelineHarnessCheckLeaf("output", snapshot.Files["output"], expectation.OutputBytes)...)
	stage := snapshot.Files["stage"]
	if stage.Exists != expectation.StageRetained {
		problems = append(problems, fmt.Sprintf("stage presence = %t, want %t", stage.Exists, expectation.StageRetained))
	}
	if expectation.StageRetained && stage.Exists {
		problems = append(problems, pipelineHarnessCheckLeaf("stage", stage, expectation.StageBytes)...)
	}
	record := snapshot.Files["record"]
	if record.Exists != expectation.RecordInstalled {
		problems = append(problems, fmt.Sprintf("record presence = %t, want installed=%t", record.Exists, expectation.RecordInstalled))
	}
	installed := record.Exists && snapshot.Reached[pipelineBoundaryRecordInstallAfter] > 0
	if snapshot.RecordInstalled != installed {
		problems = append(problems, fmt.Sprintf("record installed claim = %t, physical install evidence = %t", snapshot.RecordInstalled, installed))
	}
	if snapshot.RecordInstalled && !snapshot.RecordClosed {
		problems = append(problems, "record installation was claimed without a completed Close")
	}
	backup := snapshot.Files["backup"]
	if backup.Exists != expectation.BackupPresent {
		problems = append(problems, fmt.Sprintf("backup presence = %t, want %t", backup.Exists, expectation.BackupPresent))
	}
	if expectation.RecordInstalled && record.Exists {
		problems = append(problems, pipelineHarnessCheckRecord(expectation, snapshot)...)
	}
	return problems
}

func pipelineHarnessCheckLeaf(label string, file pipelineHarnessFile, want []byte) []string {
	if !file.Exists {
		return []string{label + " is missing"}
	}
	var problems []string
	if !bytes.Equal(file.Bytes, want) {
		problems = append(problems, fmt.Sprintf("%s bytes differ from the authored expectation (%d vs %d)", label, len(file.Bytes), len(want)))
	}
	if file.Mode&unix.S_IFMT != unix.S_IFREG || file.Nlink != 1 {
		problems = append(problems, fmt.Sprintf("%s is not a regular single-link file", label))
	}
	return problems
}

func pipelineHarnessCheckRecord(expectation pipelineHarnessExpectation, snapshot pipelineHarnessSnapshot) []string {
	var problems []string
	record := snapshot.Record
	if record == nil {
		return []string{"installed record cannot be decoded"}
	}
	if record.Phase != expectation.RecordPhase {
		problems = append(problems, fmt.Sprintf("record phase %q, want %q", record.Phase, expectation.RecordPhase))
	}
	if record.LastCompletedOperation != expectation.RecordOperation {
		problems = append(problems, fmt.Sprintf("record operation %q, want %q", record.LastCompletedOperation, expectation.RecordOperation))
	}
	if record.Mode != expectation.Mode {
		problems = append(problems, fmt.Sprintf("record mode %q, want %q", record.Mode, expectation.Mode))
	}
	stage, output, source := record.Stage.Identity, snapshot.Files["output"], snapshot.Files["source"]
	if stage.Ino == 0 || stage.Ino != output.Inode || stage.MountID != output.MountID {
		problems = append(problems, "published output identity does not match the recorded stage identity")
	}
	if record.Stage.Size != int64(len(expectation.TargetBytes)) || output.Size != int64(len(expectation.TargetBytes)) {
		problems = append(problems, "published output bytes do not match the recorded stage size")
	}
	if stage.Mode&unix.S_IFMT != unix.S_IFREG || stage.Nlink != 1 {
		problems = append(problems, "recorded stage was not a regular single-link file")
	}
	if source.Inode != snapshot.Files["source"].Inode || source.Size != snapshot.Files["source"].Size {
		problems = append(problems, "recorded source identity does not match the unchanged source file")
	}
	return problems
}

func pipelineHarnessCheckRows(expectation pipelineHarnessExpectation, snapshot pipelineHarnessSnapshot) []string {
	var problems []string
	if expectation.StageCheckpoint {
		if diff := pipelineFixtureDiff(expectation.D0, snapshot.RowsAfterStage); len(diff) != 0 {
			problems = append(problems, "D0 rows at tm-commit.before differ: "+strings.Join(diff, ","))
		}
	} else if snapshot.RowsAfterStage != nil {
		problems = append(problems, "rows-at-stage were recorded on a surface without Stage")
	}
	if expectation.CommitCheckpoint {
		if diff := pipelineFixtureDiff(expectation.D1, snapshot.RowsAtCommit); len(diff) != 0 {
			problems = append(problems, "D1 rows at tm-commit.after differ: "+strings.Join(diff, ","))
		}
	} else if snapshot.RowsAtCommit != nil {
		problems = append(problems, "rows-at-commit were recorded for a scenario that never commits")
	}
	if expectation.FinalRows != nil {
		if diff := pipelineFixtureDiff(expectation.FinalRows, snapshot.Rows); len(diff) != 0 {
			problems = append(problems, "final TM rows differ: "+strings.Join(diff, ","))
		}
	}
	return problems
}

func pipelineHarnessCheckCertainty(expectation pipelineHarnessExpectation, snapshot pipelineHarnessSnapshot) []string {
	var problems []string
	observed, _ := pipelineHarnessObserveCertainty(snapshot)
	if snapshot.Certainty != observed {
		problems = append(problems, fmt.Sprintf("commit certainty claim %q disagrees with observation evidence %q", snapshot.Certainty, observed))
	}
	if snapshot.Certainty != expectation.CommitCertainty {
		problems = append(problems, fmt.Sprintf("commit certainty %q, want %q", snapshot.Certainty, expectation.CommitCertainty))
	}
	switch snapshot.Certainty {
	case pipelineHarnessCommitted:
		if !snapshot.TMCommitted {
			problems = append(problems, "commit was claimed without a committed result")
		}
		if len(snapshot.RowsAtCommit) == 0 {
			problems = append(problems, "commit was claimed without rows observed at the commit entry")
		}
	case pipelineHarnessNotCommitted:
		if len(snapshot.RowsAfterStage) == 0 {
			problems = append(problems, "TMCommitted=false alone is not evidence of a rejected commit")
		}
	case pipelineHarnessUnknown:
		if len(snapshot.CertaintyBasis) == 0 {
			problems = append(problems, "unknown commit certainty has no observation basis")
		}
	}
	return problems
}

func pipelineHarnessBefore(order []string, first, second string) bool {
	a, b := slices.Index(order, first), slices.Index(order, second)
	return a >= 0 && b >= 0 && a < b
}

func pipelineHarnessDrop(order []string, name string) []string {
	result := make([]string, 0, len(order))
	for _, event := range order {
		if event != name {
			result = append(result, event)
		}
	}
	return result
}

func pipelineHarnessMoveBefore(order []string, target, name string) []string {
	result := pipelineHarnessDrop(pipelineHarnessDrop(order, name), target)
	for index, event := range result {
		if event == target {
			out := append([]string{}, result[:index]...)
			out = append(out, name)
			return append(out, result[index:]...)
		}
	}
	return append(result, name)
}

func pipelineHarnessSave(t *testing.T, name string, raw []byte) {
	t.Helper()
	root := os.Getenv("YAKUORI_PIPELINE_EVIDENCE")
	if root == "" {
		return
	}
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func pipelineHarnessSaveJSON(t *testing.T, name string, value any) {
	t.Helper()
	pipelineHarnessSave(t, name, mustJSON(t, value))
}

func TestPipelineHarnessState(t *testing.T) {
	t.Run("valid", testPipelineHarnessStateValid)
	t.Run("reject-invalid-observation", testPipelineHarnessStateReject)
}

func testPipelineHarnessStateValid(t *testing.T) {
	table := pipelineHarnessScenarioTable(t)
	pipelineHarnessSaveJSON(t, "state-table.json", table)
	executed := map[string]bool{}
	states := make([]pipelineHarnessSnapshot, 0, len(pipelineHarnessCases()))
	for _, spec := range pipelineHarnessCases() {
		t.Run(spec.name, func(t *testing.T) {
			// Given: expectation fixed before the run, from authored S/O/T/D0/D1.
			harness := newPipelineHarness(t, spec, true)
			expectation := pipelineHarnessExpectationForFixture(t, spec, harness.fixture)
			// When: the real wrapped pipeline runs.
			if _, err := harness.execute(t.Context()); err != nil {
				t.Fatalf("pipeline: %v", err)
			}
			snapshot, err := harness.snapshot(expectation)
			if err != nil {
				t.Fatal(err)
			}
			// Then: the independent oracle accepts the real observation.
			if problems := pipelineHarnessJudge(expectation, snapshot); len(problems) != 0 {
				t.Fatalf("state oracle rejected the real run: %v", problems)
			}
			executed[spec.name] = true
			states = append(states, snapshot)
			pipelineHarnessSave(t, spec.name+"-source.bin", snapshot.Files["source"].Bytes)
			if spec.surface == "file" {
				pipelineHarnessSave(t, spec.name+"-output.bin", snapshot.Files["output"].Bytes)
			} else {
				pipelineHarnessSave(t, spec.name+"-stdout.bin", snapshot.Files["stdout"].Bytes)
			}
			pipelineHarnessSaveJSON(t, spec.name+"-rows.json", snapshot.Rows)
			t.Logf("kind=%s surface=%s D0=%d D1=%d stage_closed=%t record_closed=%t record_installed=%t certainty=%s basis=%v bound_keys=%v key_provider=%t",
				spec.kind, spec.surface, len(expectation.D0), len(snapshot.Rows), snapshot.StageClosed,
				snapshot.RecordClosed, snapshot.RecordInstalled, snapshot.Certainty, snapshot.CertaintyBasis,
				snapshot.BoundKeys, snapshot.KeyProvider)
		})
	}
	for _, scenario := range table {
		switch scenario.Status {
		case pipelineHarnessPass:
			if !executed[scenario.Case] {
				t.Fatalf("scenario %s is marked pass without an executed observation", scenario.Case)
			}
		case pipelineHarnessFail:
			t.Fatalf("scenario %s is marked fail", scenario.Case)
		case pipelineHarnessNotRun, pipelineHarnessNA:
			if scenario.Reason == "" {
				t.Fatalf("scenario %s has status %s without a reason", scenario.Case, scenario.Status)
			}
			if pipelineHarnessScenarioPassed(scenario, executed[scenario.Case]) {
				t.Fatalf("%s/%s was converted into a pass", scenario.Case, scenario.Status)
			}
		}
	}
	pipelineHarnessSaveJSON(t, "state.json", states)
}

// pipelineHarnessScenarioPassed is the only conversion from table status to a
// pass claim. It can never turn not-run or NA into success.
func pipelineHarnessScenarioPassed(scenario pipelineHarnessExpectation, executed bool) bool {
	return scenario.Status == pipelineHarnessPass && executed
}

func testPipelineHarnessStateReject(t *testing.T) {
	// Given: one real published observation and its static expectation.
	spec := pipelineHarnessCases()[0]
	publishedHarness := newPipelineHarness(t, spec, true)
	publishedExpectation := pipelineHarnessExpectationForFixture(t, spec, publishedHarness.fixture)
	if _, err := publishedHarness.execute(t.Context()); err != nil {
		t.Fatalf("published control: %v", err)
	}
	published, err := publishedHarness.snapshot(publishedExpectation)
	if err != nil {
		t.Fatal(err)
	}
	if problems := pipelineHarnessJudge(publishedExpectation, published); len(problems) != 0 {
		t.Fatalf("valid published observation rejected: %v", problems)
	}
	precommit := pipelineHarnessScenarioByCase(t, "precommit-stage-close/text")
	control := pipelineHarnessPrecommitObservation(t, published, precommit)
	if problems := pipelineHarnessJudge(precommit, control); len(problems) != 0 {
		t.Fatalf("pre-commit positive control rejected: %v", problems)
	}
	pipelineHarnessSaveJSON(t, "reject-controls.json", map[string]any{
		"published": published, "precommit_control": control,
		"note": "oracle acceptance of a hypothetical not-run observation is not an execution pass; the pass count comes only from pipelineHarnessScenarioPassed",
	})
	mutations := []struct {
		name   string
		expect pipelineHarnessExpectation
		base   pipelineHarnessSnapshot
		mutate func(*pipelineHarnessSnapshot)
	}{
		{"stage-missing", precommit, control, func(s *pipelineHarnessSnapshot) {
			delete(s.Files, "stage")
		}},
		{"d0-wrong-row", precommit, control, func(s *pipelineHarnessSnapshot) {
			s.RowsAfterStage[0].Text = "wrong D0 text"
		}},
		{"d1-wrong-row", publishedExpectation, published, func(s *pipelineHarnessSnapshot) {
			s.RowsAtCommit[0].Text = "wrong D1 text"
		}},
		{"record-installed-misread", precommit, control, func(s *pipelineHarnessSnapshot) {
			s.Files["record"] = pipelineHarnessFile{Path: "record.json", Exists: true, Bytes: []byte("{}"), Mode: unix.S_IFREG | 0600, Nlink: 1, Size: 2}
			s.RecordInstalled = true
			s.RecordClosed = true
			s.Reached[pipelineBoundaryRecordCloseBefore] = 1
		}},
		{"certainty-from-flag", precommit, control, func(s *pipelineHarnessSnapshot) {
			s.RowsAfterStage = nil
			s.Reached[pipelineBoundaryStageCloseAfter] = 0
			s.Certainty = pipelineHarnessNotCommitted
			s.CertaintyBasis = []string{"tm-committed-false"}
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			invalid := mutation.base.clone()
			mutation.mutate(&invalid)
			problems := pipelineHarnessJudge(mutation.expect, invalid)
			if len(problems) == 0 {
				t.Fatalf("state oracle accepted invalid observation %s", mutation.name)
			}
			pipelineHarnessSaveJSON(t, "reject-"+mutation.name+".json", map[string]any{
				"case": mutation.name, "rejected": true, "problems": problems,
				"certainty": invalid.Certainty, "basis": invalid.CertaintyBasis,
				"files": invalid.Files,
			})
			t.Logf("invalid state observation %s rejected: %v", mutation.name, problems)
		})
	}
}

func pipelineHarnessScenarioByCase(t *testing.T, name string) pipelineHarnessExpectation {
	t.Helper()
	for _, scenario := range pipelineHarnessScenarioTable(t) {
		if scenario.Case == name {
			return scenario
		}
	}
	t.Fatalf("scenario %s is not in the state table", name)
	return pipelineHarnessExpectation{}
}

// pipelineHarnessPrecommitObservation builds the hypothetical pre-commit
// observation used to judge the oracle. It carries the authored stage bytes and
// D0 rows and is not an execution claim.
func pipelineHarnessPrecommitObservation(t *testing.T, published pipelineHarnessSnapshot, expectation pipelineHarnessExpectation) pipelineHarnessSnapshot {
	t.Helper()
	snapshot := published.clone()
	snapshot.Case = expectation.Case
	snapshot.Files["output"] = pipelineHarnessFile{
		Path: published.Files["output"].Path, Exists: true, Bytes: slices.Clone(expectation.OutputBytes),
		Mode: unix.S_IFREG | 0600, Nlink: 1, Size: int64(len(expectation.OutputBytes)),
	}
	snapshot.Files["stage"] = pipelineHarnessFile{
		Path: published.Files["stage"].Path, Exists: true, Bytes: slices.Clone(expectation.StageBytes),
		Mode: unix.S_IFREG | 0600, Nlink: 1, Size: int64(len(expectation.StageBytes)),
	}
	delete(snapshot.Files, "record")
	delete(snapshot.Files, "backup")
	snapshot.Record = nil
	snapshot.RecordInstalled = false
	snapshot.RecordClosed = false
	snapshot.StageClosed = false
	snapshot.Rows = slices.Clone(expectation.D0)
	snapshot.RowsAfterStage = slices.Clone(expectation.D0)
	snapshot.RowsAtCommit = nil
	snapshot.TMCommitted = false
	snapshot.Reached = maps.Clone(expectation.RequiredBoundaries)
	snapshot.Order = slices.Clone(expectation.BoundaryOrder)
	snapshot.RecordEvents = nil
	snapshot.Result = Result{TMCommitted: false, Publication: publication.Result{State: publication.NotPublished}}
	snapshot.ResultPresent = true
	snapshot.Outcome = pipelineHarnessObserveOutcome(snapshot)
	snapshot.Certainty, snapshot.CertaintyBasis = pipelineHarnessObserveCertainty(snapshot)
	return snapshot
}
