//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	name          string
	kind          string
	surface       string
	mode          publication.Mode
	outputPresent bool
}

func pipelineHarnessCases() []pipelineHarnessCase {
	return []pipelineHarnessCase{
		{name: "file-replace-existing/text", kind: "text", surface: "file", mode: publication.Replace, outputPresent: true},
		{name: "file-replace-existing/w3", kind: "w3", surface: "file", mode: publication.Replace, outputPresent: true},
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
	// StageRows are the rows already present at tm-commit.before. A first run
	// sees D0; a repeat or a revalidation sees the pre-run rows.
	StageRows []pipelineFixtureRow `json:"stage_rows,omitempty"`

	StageRetained   bool   `json:"stage_retained"`
	StageBytes      []byte `json:"stage_bytes,omitempty"`
	OutputBytes     []byte `json:"output_bytes"`
	StdoutBytes     []byte `json:"stdout_bytes,omitempty"`
	BackupPresent   bool   `json:"backup_present"`
	RecordInstalled bool   `json:"record_installed"`
	RecordPhase     string `json:"record_phase,omitempty"`
	RecordOperation string `json:"record_operation,omitempty"`
	OutputPresent   bool   `json:"output_present"`
	StageFDClosed   bool   `json:"stage_fd_closed"`
	RecordFDClosed  bool   `json:"record_fd_closed"`
	Cause           string `json:"cause,omitempty"`

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
		OutputPresent: spec.outputPresent || spec.mode == publication.InPlace,
		StageFDClosed: spec.surface == "file", RecordFDClosed: spec.surface == "file",
		BoundaryOrder: pipelineHarnessOrder(input, spec.surface),
	}
	if spec.surface == "file" {
		expectation.OutputBytes = slices.Clone(input.target)
		expectation.RecordInstalled = true
		expectation.RecordPhase = "published"
		expectation.RecordOperation = "publish-rename"
		expectation.RequiredBoundaries = pipelineHarnessPublishedBoundaries(input, spec.mode)
	} else {
		expectation.StdoutBytes = slices.Clone(input.target)
		expectation.RequiredBoundaries = pipelineHarnessStdoutBoundaries(input)
	}
	if spec.mode == publication.InPlace {
		// The backup rename retains the original source bytes; the published
		// output path itself now carries the translated artifact.
		expectation.BackupPresent = true
		expectation.RequiredBoundaries[pipelineBoundaryBackupRenameBefore] = 1
		expectation.RequiredBoundaries[pipelineBoundaryBackupRenameAfter] = 1
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

func pipelineHarnessPublishedBoundaries(input pipelineFixtureInput, mode publication.Mode) map[string]int {
	generate := pipelineHarnessGenerateCount(input)
	records := 2
	if mode == publication.InPlace {
		// prepared, source-backed-up and published records are all installed.
		records = 3
	}
	return map[string]int{
		pipelineBoundaryGenerateBefore: generate, pipelineBoundaryGenerateAfter: generate,
		pipelineBoundaryStageWriteBefore: 1, pipelineBoundaryStageWriteAfter: 1,
		pipelineBoundaryStageSyncBefore: 1, pipelineBoundaryStageSyncAfter: 1,
		pipelineBoundaryStageCloseBefore: 1, pipelineBoundaryStageCloseAfter: 1,
		pipelineBoundaryObserveBefore: 1, pipelineBoundaryObserveAfter: 1,
		pipelineBoundaryCompareFinalAfter: 1,
		pipelineBoundaryTMCommitBefore:    1, pipelineBoundaryTMCommitAfter: 1,
		pipelineBoundaryRecordWriteBefore: records, pipelineBoundaryRecordWriteAfter: records,
		pipelineBoundaryRecordSyncBefore: 2 * records, pipelineBoundaryRecordSyncAfter: 2 * records,
		pipelineBoundaryRecordCloseBefore: records, pipelineBoundaryRecordCloseAfter: records,
		pipelineBoundaryRecordInstallBefore: records, pipelineBoundaryRecordInstallAfter: records,
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
	publishedText := pipelineHarnessExpectationForFixture(t, pipelineHarnessCase{name: "file-replace-existing/text", kind: "text", surface: "file", mode: publication.Replace, outputPresent: true}, textFixture)
	publishedW3 := pipelineHarnessExpectationForFixture(t, pipelineHarnessCase{name: "file-replace-existing/w3", kind: "w3", surface: "file", mode: publication.Replace, outputPresent: true}, w3Fixture)
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
	precommit.StageCheckpoint = false
	precommit.StageFDClosed = true
	precommit.RecordFDClosed = false
	precommit.Cause = "the stage close reports EIO after the mandatory real descriptor Close; no successful afterStage checkpoint can exist"
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

	precommitCancel := precommit
	precommitCancel.Case = "precommit-cancel/text"
	precommitCancel.Reason = "ctx cancellation after Stage success and before final.commit; task5 executes this fault"
	precommitCancel.Cause = "ctx.Cancel at tm-commit.before after Stage returned nil; the real final.commit rejected the context"
	precommitCancel.StageCheckpoint = true
	precommitCancel.BoundaryOrder = []string{
		pipelineBoundaryGenerateBefore, pipelineBoundaryGenerateAfter,
		pipelineBoundaryStageWriteBefore, pipelineBoundaryStageWriteAfter,
		pipelineBoundaryObserveBefore, pipelineBoundaryObserveAfter,
		pipelineBoundaryCompareFinalAfter, pipelineBoundaryTMCommitBefore,
		pipelineBoundaryRunCloseAfter,
	}
	precommitCancel.RequiredBoundaries = map[string]int{
		pipelineBoundaryGenerateBefore: 1, pipelineBoundaryGenerateAfter: 1,
		pipelineBoundaryStageWriteBefore: 1, pipelineBoundaryStageWriteAfter: 1,
		pipelineBoundaryStageSyncBefore: 1, pipelineBoundaryStageSyncAfter: 1,
		pipelineBoundaryStageCloseBefore: 1,
		pipelineBoundaryObserveBefore:    1, pipelineBoundaryObserveAfter: 1,
		pipelineBoundaryCompareFinalAfter: 1,
		pipelineBoundaryTMCommitBefore:    1,
		pipelineBoundaryRunCloseAfter:     1,
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
	postcommit.StageFDClosed = true
	postcommit.RecordFDClosed = false
	postcommit.Cause = "context cancellation at the Publish entry after the real commit returned; no record operation started"
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
		pipelineBoundaryStageCloseBefore: 1,
		pipelineBoundaryObserveBefore:    1, pipelineBoundaryObserveAfter: 1,
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
	restored.StageFDClosed = true
	restored.RecordFDClosed = true
	restored.Cause = "an ordinary publish error after the InPlace backup rename is reconciled to the Restored outcome"
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

	closedOnly := postcommit
	closedOnly.Case = "record-close-before-install/text"
	closedOnly.Reason = "record close completion is not installation; task5 owns the record-close fault"
	closedOnly.Cause = "the temporary publication record close reports EIO after the real commit; the record is closed but never installed"
	closedOnly.StageRetained = true
	closedOnly.StageBytes = slices.Clone(text.target)
	closedOnly.OutputBytes = slices.Clone(pipelineHarnessOldOutput)
	closedOnly.RecordInstalled = false
	closedOnly.RecordPhase = ""
	closedOnly.RecordOperation = ""
	closedOnly.StageFDClosed = true
	closedOnly.RecordFDClosed = true
	closedOnly.RequiredBoundaries = map[string]int{
		pipelineBoundaryGenerateBefore: 1, pipelineBoundaryGenerateAfter: 1,
		pipelineBoundaryStageWriteBefore: 1, pipelineBoundaryStageWriteAfter: 1,
		pipelineBoundaryStageSyncBefore: 1, pipelineBoundaryStageSyncAfter: 1,
		pipelineBoundaryStageCloseBefore: 1,
		pipelineBoundaryObserveBefore:    1, pipelineBoundaryObserveAfter: 1,
		pipelineBoundaryCompareFinalAfter: 1,
		pipelineBoundaryTMCommitBefore:    1, pipelineBoundaryTMCommitAfter: 1,
		pipelineBoundaryRecordWriteBefore: 1, pipelineBoundaryRecordWriteAfter: 1,
		pipelineBoundaryRecordSyncBefore: 1, pipelineBoundaryRecordSyncAfter: 1,
		pipelineBoundaryRecordCloseBefore: 1,
		pipelineBoundaryRunCloseAfter:     1,
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
	tmCommitError.StageCheckpoint = true
	tmCommitError.RecordInstalled = false
	tmCommitError.RecordPhase = ""
	tmCommitError.RecordOperation = ""
	tmCommitError.StageRetained = true
	tmCommitError.StageBytes = slices.Clone(text.target)
	tmCommitError.OutputBytes = slices.Clone(pipelineHarnessOldOutput)
	tmCommitError.StageFDClosed = true
	tmCommitError.RecordFDClosed = false
	tmCommitError.Cause = "a trigger on the same real SQLite database aborts COMMIT; the all-column final rows remain D0"
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
		pipelineBoundaryStageCloseBefore: 1,
		pipelineBoundaryObserveBefore:    1, pipelineBoundaryObserveAfter: 1,
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
		precommit, precommitCancel, postcommit, restored, publishedThenError, stdoutPrefix, closedOnly, killed,
		flush, stdoutPublication, stdoutCommitBefore, tmCommitError, publishedDiagnostic,
	}
}

// pipelineHarnessFaultCell is one enumerable cell of the fixed contract:
// setup (mode and surface) x boundary occurrence x error/cancel/process-stop.
// The requested fields and the injected descriptor are part of the evidence;
// enumerating a cell never executes it and never makes it a pass.
type pipelineHarnessFaultCell struct {
	Case       string                `json:"case"`
	Setup      string                `json:"setup"`
	Mode       string                `json:"mode"`
	Surface    string                `json:"surface"`
	Boundary   string                `json:"requested_boundary"`
	Occurrence int                   `json:"requested_occurrence"`
	Stimulus   string                `json:"stimulus"`
	Injected   string                `json:"injected"`
	Status     pipelineHarnessStatus `json:"status"`
	Reason     string                `json:"reason"`
}

type pipelineHarnessFaultAbsence struct {
	status pipelineHarnessStatus
	reason string
}

type pipelineHarnessFaultSetup struct {
	name        string
	mode        string
	surface     string
	counts      map[string]int
	absent      map[string]pipelineHarnessFaultAbsence
	note        string
	unsupported string
}

func pipelineHarnessContractBoundaries() []string {
	return []string{
		pipelineBoundaryGenerateBefore, pipelineBoundaryGenerateAfter,
		pipelineBoundaryStageWriteBefore, pipelineBoundaryStageWriteAfter,
		pipelineBoundaryStageSyncBefore, pipelineBoundaryStageSyncAfter,
		pipelineBoundaryStageCloseBefore, pipelineBoundaryStageCloseAfter,
		pipelineBoundaryObserveBefore, pipelineBoundaryObserveAfter,
		pipelineBoundaryCompareFinalAfter,
		pipelineBoundaryTMCommitBefore, pipelineBoundaryTMCommitAfter,
		pipelineBoundaryRecordWriteBefore, pipelineBoundaryRecordWriteAfter,
		pipelineBoundaryRecordSyncBefore, pipelineBoundaryRecordSyncAfter,
		pipelineBoundaryRecordCloseBefore, pipelineBoundaryRecordCloseAfter,
		pipelineBoundaryRecordInstallBefore, pipelineBoundaryRecordInstallAfter,
		pipelineBoundaryBackupRenameBefore, pipelineBoundaryBackupRenameAfter,
		pipelineBoundaryPublishRenameBefore, pipelineBoundaryPublishRenameAfter,
		pipelineBoundaryStdoutWriteBefore, pipelineBoundaryStdoutWriteAfter,
		pipelineBoundaryDiagnosticWriteBefore, pipelineBoundaryDiagnosticWriteAfter,
		pipelineBoundaryRunCloseAfter,
	}
}

func pipelineHarnessFaultSetups() []pipelineHarnessFaultSetup {
	fileCounts := func(records int, inPlace bool) map[string]int {
		counts := map[string]int{
			pipelineBoundaryGenerateBefore: 1, pipelineBoundaryGenerateAfter: 1,
			pipelineBoundaryStageWriteBefore: 1, pipelineBoundaryStageWriteAfter: 1,
			pipelineBoundaryStageSyncBefore: 1, pipelineBoundaryStageSyncAfter: 1,
			pipelineBoundaryStageCloseBefore: 1, pipelineBoundaryStageCloseAfter: 1,
			pipelineBoundaryObserveBefore: 1, pipelineBoundaryObserveAfter: 1,
			pipelineBoundaryCompareFinalAfter: 1,
			pipelineBoundaryTMCommitBefore:    1, pipelineBoundaryTMCommitAfter: 1,
			pipelineBoundaryRecordWriteBefore: records, pipelineBoundaryRecordWriteAfter: records,
			pipelineBoundaryRecordSyncBefore: 2 * records, pipelineBoundaryRecordSyncAfter: 2 * records,
			pipelineBoundaryRecordCloseBefore: records, pipelineBoundaryRecordCloseAfter: records,
			pipelineBoundaryRecordInstallBefore: records, pipelineBoundaryRecordInstallAfter: records,
			pipelineBoundaryPublishRenameBefore: 1, pipelineBoundaryPublishRenameAfter: 1,
			pipelineBoundaryDiagnosticWriteBefore: 1, pipelineBoundaryDiagnosticWriteAfter: 1,
			pipelineBoundaryRunCloseAfter: 1,
		}
		if inPlace {
			counts[pipelineBoundaryBackupRenameBefore] = 1
			counts[pipelineBoundaryBackupRenameAfter] = 1
		}
		return counts
	}
	stdoutCounts := map[string]int{
		pipelineBoundaryGenerateBefore: 1, pipelineBoundaryGenerateAfter: 1,
		pipelineBoundaryObserveBefore: 1, pipelineBoundaryObserveAfter: 1,
		pipelineBoundaryTMCommitAfter:     1,
		pipelineBoundaryStdoutWriteBefore: 1, pipelineBoundaryStdoutWriteAfter: 1,
		pipelineBoundaryDiagnosticWriteBefore: 1, pipelineBoundaryDiagnosticWriteAfter: 1,
	}
	stdoutAbsent := map[string]pipelineHarnessFaultAbsence{
		pipelineBoundaryTMCommitBefore:    {pipelineHarnessNotRun, "Core.MText has no strict pre-commit callback; the Observe return is not substituted for it (Issue68 contract)"},
		pipelineBoundaryCompareFinalAfter: {pipelineHarnessNA, "the text surface has no publication Stage callback, so CompareFinal completion has no separate observable boundary"},
	}
	return []pipelineHarnessFaultSetup{
		{name: "create/text", mode: string(publication.Create), surface: "file", counts: fileCounts(2, false)},
		{name: "replace-absent/text", mode: string(publication.Replace), surface: "file", counts: fileCounts(2, false)},
		{name: "replace-existing/text", mode: string(publication.Replace), surface: "file", counts: fileCounts(2, false)},
		{name: "in-place/text", mode: string(publication.InPlace), surface: "file", counts: fileCounts(3, true)},
		{name: "stdout/text", mode: string(publication.Replace), surface: "stdout", counts: stdoutCounts, absent: stdoutAbsent},
		{name: "stdout/w3-core", mode: string(publication.Replace), surface: "stdout", counts: stdoutCounts, absent: stdoutAbsent,
			note: "Core can export W3 to stdout, but this unit does not execute it; "},
		{name: "w3-cli-text-stdout", mode: string(publication.Replace), surface: "stdout",
			unsupported: "the CLI supports text stdout only; W3 stdout is unsupported at the CLI surface"},
	}
}

func pipelineHarnessFaultCatalog() []pipelineHarnessFaultCell {
	var cells []pipelineHarnessFaultCell
	for _, setup := range pipelineHarnessFaultSetups() {
		for _, boundary := range pipelineHarnessContractBoundaries() {
			count := setup.counts[boundary]
			if count < 1 {
				count = 1
			}
			for occurrence := 1; occurrence <= count; occurrence++ {
				for _, stimulus := range []string{"error", "cancel", "process-stop"} {
					status, reason := pipelineHarnessFaultCellStatus(setup, boundary, stimulus, setup.counts[boundary] > 0)
					cells = append(cells, pipelineHarnessFaultCell{
						Case:  fmt.Sprintf("%s/%s#%d/%s", setup.name, boundary, occurrence, stimulus),
						Setup: setup.name, Mode: setup.mode, Surface: setup.surface,
						Boundary: boundary, Occurrence: occurrence, Stimulus: stimulus,
						Injected: pipelineHarnessInjectedField(boundary, stimulus),
						Status:   status, Reason: reason,
					})
				}
			}
		}
	}
	return cells
}

func pipelineHarnessFaultCellStatus(setup pipelineHarnessFaultSetup, boundary, stimulus string, present bool) (pipelineHarnessStatus, string) {
	if setup.unsupported != "" {
		return pipelineHarnessNA, setup.unsupported
	}
	if !present {
		if absence, ok := setup.absent[boundary]; ok {
			return absence.status, absence.reason
		}
		return pipelineHarnessNA, fmt.Sprintf("%s does not perform %s", setup.name, boundary)
	}
	if boundary == pipelineBoundaryDiagnosticWriteBefore || boundary == pipelineBoundaryDiagnosticWriteAfter {
		return pipelineHarnessNotRun, "post-publication diagnostics use the existing CLI reportPipeline writer; this unit only records the later contract"
	}
	switch stimulus {
	case "error":
		return pipelineHarnessNotRun, setup.note + "error cell enumerated for the fixed contract; task5 executes the minimal set"
	case "cancel":
		if !pipelineHarnessCancellationObserved(setup, boundary) {
			return pipelineHarnessNA, "no context-consuming operation follows " + boundary + " on the " + setup.surface + " surface, so a cancellation there cannot be observed"
		}
		if boundary == pipelineBoundaryTMCommitBefore {
			return pipelineHarnessNotRun, "ctx cancellation is contracted at tm-commit.before: Stage returns nil and the real final.commit rejects the context; task5 executes it"
		}
		return pipelineHarnessNotRun, "ctx cancellation at " + boundary + " is possible and unselected: the real Stage, final.commit, Publish or post-commit context gate observes it; task5 executes the minimal set"
	case "process-stop":
		return pipelineHarnessNotRun, "SIGKILL at this boundary is owned by the task4/5 process harness; the cell is enumerated only"
	}
	return pipelineHarnessNA, "no contract for stimulus " + stimulus
}

// pipelineHarnessCancellationObserved reports whether a cancellation injected at
// boundary can be observed by a later real operation. Stage, final.commit,
// Publish and the post-commit commit gate all read the caller context on the
// file surface. The stdout surface has no publication Stage and its writer
// receives no context, so only a cancellation before the TM commit is
// observable there. Run.Close never consumes a context.
func pipelineHarnessCancellationObserved(setup pipelineHarnessFaultSetup, boundary string) bool {
	switch boundary {
	case pipelineBoundaryRunCloseAfter:
		return false
	case pipelineBoundaryTMCommitAfter, pipelineBoundaryStdoutWriteBefore, pipelineBoundaryStdoutWriteAfter:
		return setup.surface != "stdout"
	}
	return true
}

func pipelineHarnessInjectedField(boundary, stimulus string) string {
	if stimulus == "cancel" {
		return "ctx-cancel"
	}
	if stimulus == "process-stop" {
		return "sigkill-child"
	}
	switch boundary {
	case pipelineBoundaryGenerateBefore, pipelineBoundaryGenerateAfter:
		return "generation-error"
	case pipelineBoundaryStageWriteBefore, pipelineBoundaryStageWriteAfter:
		return "export-writer-error"
	case pipelineBoundaryStageSyncBefore, pipelineBoundaryStageSyncAfter:
		return "sync-stage-eio"
	case pipelineBoundaryStageCloseBefore, pipelineBoundaryStageCloseAfter:
		return "close-stage-eio"
	case pipelineBoundaryObserveBefore, pipelineBoundaryObserveAfter:
		return "observe-error"
	case pipelineBoundaryCompareFinalAfter:
		return "compare-final-mismatch"
	case pipelineBoundaryTMCommitBefore, pipelineBoundaryTMCommitAfter:
		return "sqlite-trigger"
	case pipelineBoundaryRecordWriteBefore, pipelineBoundaryRecordWriteAfter:
		return "record-write-eio"
	case pipelineBoundaryRecordSyncBefore, pipelineBoundaryRecordSyncAfter:
		return "record-sync-eio"
	case pipelineBoundaryRecordCloseBefore, pipelineBoundaryRecordCloseAfter:
		return "record-close-eio"
	case pipelineBoundaryRecordInstallBefore, pipelineBoundaryRecordInstallAfter:
		return "install-rename-eio"
	case pipelineBoundaryBackupRenameBefore, pipelineBoundaryBackupRenameAfter:
		return "backup-rename-eio"
	case pipelineBoundaryPublishRenameBefore, pipelineBoundaryPublishRenameAfter:
		return "publish-rename-eio"
	case pipelineBoundaryStdoutWriteBefore, pipelineBoundaryStdoutWriteAfter:
		return "stdout-write-error"
	case pipelineBoundaryDiagnosticWriteBefore, pipelineBoundaryDiagnosticWriteAfter:
		return "diagnostic-writer-error"
	case pipelineBoundaryRunCloseAfter:
		return "run-close-error"
	}
	return "unspecified-error"
}

func pipelineHarnessCheckFaultCatalog(t *testing.T, cells []pipelineHarnessFaultCell) {
	t.Helper()
	if len(cells) == 0 {
		t.Fatal("fault catalog is empty")
	}
	setups := map[string]bool{}
	applicable := map[string]int{}
	statuses := map[pipelineHarnessStatus]int{}
	for _, cell := range cells {
		switch cell.Status {
		case pipelineHarnessPass, pipelineHarnessFail:
			t.Fatalf("fault cell %s is %s; enumeration cannot pass or fail a cell", cell.Case, cell.Status)
		case pipelineHarnessNotRun, pipelineHarnessNA:
			if cell.Reason == "" {
				t.Fatalf("fault cell %s is %s without a reason", cell.Case, cell.Status)
			}
		}
		if cell.Boundary == "" || cell.Occurrence < 1 || cell.Stimulus == "" || cell.Injected == "" {
			t.Fatalf("fault cell %s lacks requested boundary/occurrence/stimulus or injected field", cell.Case)
		}
		if want := fmt.Sprintf("%s/%s#%d/%s", cell.Setup, cell.Boundary, cell.Occurrence, cell.Stimulus); cell.Case != want {
			t.Fatalf("fault cell name %q, want %q", cell.Case, want)
		}
		setups[cell.Setup] = true
		statuses[cell.Status]++
		if cell.Status != pipelineHarnessNA {
			applicable[cell.Boundary]++
		}
	}
	for _, boundary := range pipelineHarnessContractBoundaries() {
		if applicable[boundary] == 0 {
			t.Fatalf("contract boundary %s has no applicable cell", boundary)
		}
	}
	for _, setup := range pipelineHarnessFaultSetups() {
		if !setups[setup.name] {
			t.Fatalf("setup %s is missing from the fault catalog", setup.name)
		}
	}
	if statuses[pipelineHarnessPass] != 0 {
		t.Fatalf("fault catalog contains %d pass cells", statuses[pipelineHarnessPass])
	}
}

type pipelineHarnessFile struct {
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Bytes   []byte `json:"bytes,omitempty"`
	Dev     uint64 `json:"dev,omitempty"`
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

type pipelineHarnessRecordPath struct {
	Path     string `json:"path"`
	Basename string `json:"basename"`
}

type pipelineHarnessExistingOutput struct {
	Absent bool                       `json:"absent"`
	File   *pipelineHarnessRecordFile `json:"file"`
}

type pipelineHarnessRecord struct {
	SchemaVersion          int                           `json:"schema_version"`
	RunID                  string                        `json:"run_id"`
	Mode                   string                        `json:"mode"`
	Source                 pipelineHarnessRecordFile     `json:"source"`
	Output                 pipelineHarnessRecordPath     `json:"output"`
	Stage                  pipelineHarnessRecordFile     `json:"stage"`
	Backup                 *pipelineHarnessRecordPath    `json:"backup"`
	SourceParent           pipelineHarnessRecordIdentity `json:"source_parent"`
	OutputParent           pipelineHarnessRecordIdentity `json:"output_parent"`
	ExistingOutput         pipelineHarnessExistingOutput `json:"existing_output"`
	Phase                  string                        `json:"phase"`
	LastCompletedOperation string                        `json:"last_completed_operation"`
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

	AuthoredD0 []pipelineFixtureRow `json:"authored_d0"`
	AuthoredD1 []pipelineFixtureRow `json:"authored_d1"`

	// BeforeOutput is the real pre-run capture of the publication output. It is
	// filled from the filesystem before the pipeline runs and never from it.
	BeforeOutput *pipelineHarnessFile `json:"before_output,omitempty"`

	StageFDClosed  bool   `json:"stage_fd_closed"`
	RecordFDClosed bool   `json:"record_fd_closed"`
	ErrorPhase     string `json:"error_phase,omitempty"`

	RequestedBoundary   string   `json:"requested_boundary,omitempty"`
	RequestedOccurrence int      `json:"requested_occurrence,omitempty"`
	Stimulus            string   `json:"stimulus"`
	Injected            []string `json:"injected,omitempty"`
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
	snapshot.AuthoredD0 = slices.Clone(exp.D0)
	snapshot.AuthoredD1 = slices.Clone(exp.D1)
	if h.surface == "file" {
		before := h.beforeOutput
		snapshot.BeforeOutput = &before
	}
	snapshot.RequestedBoundary = h.requestedBoundary
	snapshot.RequestedOccurrence = h.requestedOccurrence
	snapshot.Stimulus = h.stimulus
	snapshot.Injected = slices.Clone(h.injected)
	var typed *Error
	if errors.As(h.runErr, &typed) {
		snapshot.ErrorPhase = typed.Phase
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
	if h.ops != nil {
		snapshot.StageFDClosed = h.ops.stageFDClosed
		snapshot.RecordFDClosed = h.ops.recordFDClosed
		if !snapshot.StageFDClosed && h.ops.stageFD != nil {
			if _, err := h.ops.stageFD.Stat(); errors.Is(err, os.ErrClosed) {
				snapshot.StageFDClosed = true
			}
		}
		if !snapshot.RecordFDClosed {
			for index := len(h.ops.recordFDs) - 1; index >= 0; index-- {
				if _, err := h.ops.recordFDs[index].Stat(); errors.Is(err, os.ErrClosed) {
					snapshot.RecordFDClosed = true
					break
				}
			}
		}
	}

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
	file.Dev = unix.Mkdev(stat.Dev_major, stat.Dev_minor)
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
	clone.AuthoredD0 = slices.Clone(s.AuthoredD0)
	clone.AuthoredD1 = slices.Clone(s.AuthoredD1)
	clone.Injected = slices.Clone(s.Injected)
	if s.BeforeOutput != nil {
		before := *s.BeforeOutput
		clone.BeforeOutput = &before
	}
	if s.Record != nil {
		record := *s.Record
		if s.Record.Backup != nil {
			backup := *s.Record.Backup
			record.Backup = &backup
		}
		if s.Record.ExistingOutput.File != nil {
			existing := *s.Record.ExistingOutput.File
			record.ExistingOutput.File = &existing
		}
		clone.Record = &record
	}
	return clone
}

// pipelineHarnessObserveCertainty derives the commit state from actual final
// rows and real commit-entry checkpoints, never from TMCommitted alone or from
// the historical pre-commit afterStage rows. A commit-entry checkpoint is the
// Publish/Write entry, which Core reaches only after final.commit returned.
func pipelineHarnessObserveCertainty(snapshot pipelineHarnessSnapshot) (pipelineHarnessCertaintyValue, []string) {
	commitEntry := snapshot.Reached[pipelineBoundaryTMCommitAfter] > 0
	commitRows := len(snapshot.RowsAtCommit) > 0
	finalD0 := pipelineHarnessRowsEqual(snapshot.AuthoredD0, snapshot.Rows)
	finalD1 := pipelineHarnessRowsEqual(snapshot.AuthoredD1, snapshot.Rows)
	switch {
	case commitRows && snapshot.TMCommitted && finalD1 && pipelineHarnessRowsEqual(snapshot.AuthoredD1, snapshot.RowsAtCommit):
		return pipelineHarnessCommitted, []string{"rows-at-commit", "final-rows-D1", "tm-committed"}
	case !commitEntry && !commitRows && finalD0:
		return pipelineHarnessNotCommitted, []string{"no-commit-entry", "final-rows-D0"}
	default:
		var basis []string
		if commitEntry {
			basis = append(basis, "commit-entry-reached")
		} else {
			basis = append(basis, "no-commit-entry")
		}
		if commitRows {
			basis = append(basis, "rows-at-commit")
		} else {
			basis = append(basis, "no-commit-rows")
		}
		switch {
		case finalD0:
			basis = append(basis, "final-rows-D0")
		case finalD1:
			basis = append(basis, "final-rows-D1")
		default:
			basis = append(basis, "final-rows-other")
		}
		if snapshot.TMCommitted {
			basis = append(basis, "tm-committed")
		}
		if snapshot.ErrorPhase != "" {
			basis = append(basis, "error-phase-"+snapshot.ErrorPhase)
		}
		return pipelineHarnessUnknown, basis
	}
}

func pipelineHarnessRowsEqual(expected, actual []pipelineFixtureRow) bool {
	return expected != nil && actual != nil && slices.Equal(expected, actual)
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
	if snapshot.StageFDClosed != expectation.StageFDClosed {
		problems = append(problems, fmt.Sprintf("stage descriptor actually closed = %t, want %t", snapshot.StageFDClosed, expectation.StageFDClosed))
	}
	if snapshot.RecordFDClosed != expectation.RecordFDClosed {
		problems = append(problems, fmt.Sprintf("record descriptor actually closed = %t, want %t", snapshot.RecordFDClosed, expectation.RecordFDClosed))
	}
	if expectation.Mode == string(publication.InPlace) {
		// InPlace renames the original source to the backup, so the physical
		// counterpart of S is the backup; the source path holds the target.
		problems = append(problems, pipelineHarnessCheckLeaf("backup(source)", snapshot.Files["backup"], expectation.SourceBytes)...)
	} else {
		problems = append(problems, pipelineHarnessCheckLeaf("source", snapshot.Files["source"], expectation.SourceBytes)...)
	}
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
	if record.SchemaVersion != 1 {
		problems = append(problems, fmt.Sprintf("record schema version %d, want 1", record.SchemaVersion))
	}
	if record.RunID == "" || record.RunID != snapshot.Result.Publication.RunID {
		problems = append(problems, fmt.Sprintf("record run id %q does not match the actual run %q", record.RunID, snapshot.Result.Publication.RunID))
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
	output := snapshot.Files["output"]
	sourcePath := snapshot.Files["source"].Path
	sourcePhysical := snapshot.Files["source"]
	if expectation.Mode == string(publication.InPlace) && snapshot.Files["backup"].Exists {
		// InPlace renames the original source to the backup, so the physical
		// counterpart of record.Source is the backup, not the new output.
		sourcePhysical = snapshot.Files["backup"]
	}
	problems = append(problems, pipelineHarnessCheckRecordedFile("recorded source", record.Source, sourcePhysical, sourcePath, expectation.SourceBytes)...)
	problems = append(problems, pipelineHarnessCheckRecordedFile("recorded stage", record.Stage, output, snapshot.Files["stage"].Path, expectation.TargetBytes)...)
	if record.Output.Path != output.Path || record.Output.Basename != filepath.Base(output.Path) {
		problems = append(problems, fmt.Sprintf("recorded output %q/%q does not match the physical output %q", record.Output.Path, record.Output.Basename, output.Path))
	}
	problems = append(problems, pipelineHarnessCheckRecordedParent("source parent", record.SourceParent, filepath.Dir(sourcePath))...)
	problems = append(problems, pipelineHarnessCheckRecordedParent("output parent", record.OutputParent, filepath.Dir(output.Path))...)
	existing := expectation.OldOutput
	if expectation.Mode == string(publication.InPlace) {
		existing = expectation.SourceBytes
	}
	if expectation.OutputPresent {
		if record.ExistingOutput.Absent || record.ExistingOutput.File == nil {
			problems = append(problems, "record reports an absent output, but this mode requires an existing one")
		} else if snapshot.BeforeOutput == nil {
			problems = append(problems, "no real pre-run capture exists for the required old output")
		} else {
			problems = append(problems, pipelineHarnessCheckRecordedFile("recorded existing output", *record.ExistingOutput.File, *snapshot.BeforeOutput, snapshot.BeforeOutput.Path, existing)...)
		}
	} else if !record.ExistingOutput.Absent || record.ExistingOutput.File != nil {
		problems = append(problems, "record claims an existing output for a create/absent setup")
	}
	if expectation.Mode == string(publication.InPlace) {
		backup := snapshot.Files["backup"]
		if record.Backup == nil || record.Backup.Path != backup.Path || record.Backup.Basename != filepath.Base(backup.Path) {
			problems = append(problems, "recorded backup path does not match the physical InPlace backup")
		}
	} else if record.Backup != nil {
		problems = append(problems, "record carries a backup for a mode that never creates one")
	}
	return problems
}

// pipelineHarnessCheckRecordedFile compares an established record entry to the
// independently captured physical file. Path and basename are checked against
// the logical location the record names; identity, size and the publication
// record's own SHA-256 integrity field are checked against actual bytes.
func pipelineHarnessCheckRecordedFile(label string, recorded pipelineHarnessRecordFile, physical pipelineHarnessFile, logicalPath string, authored []byte) []string {
	var problems []string
	if !physical.Exists {
		return []string{label + " has no physical counterpart"}
	}
	if recorded.Path != logicalPath || recorded.Basename != filepath.Base(logicalPath) {
		problems = append(problems, fmt.Sprintf("%s path %q/%q, want %q/%q", label, recorded.Path, recorded.Basename, logicalPath, filepath.Base(logicalPath)))
	}
	identity := recorded.Identity
	if identity.Dev != physical.Dev || identity.Ino != physical.Inode || identity.MountID != physical.MountID ||
		identity.Size != physical.Size || identity.Mode != uint32(physical.Mode) || identity.Nlink != uint64(physical.Nlink) {
		problems = append(problems, fmt.Sprintf("%s identity %+v does not match physical dev=%d ino=%d mount=%d size=%d mode=%#x nlink=%d",
			label, identity, physical.Dev, physical.Inode, physical.MountID, physical.Size, physical.Mode, physical.Nlink))
	}
	if recorded.Size != physical.Size || recorded.Size != int64(len(physical.Bytes)) {
		problems = append(problems, fmt.Sprintf("%s recorded size %d does not match physical bytes %d", label, recorded.Size, len(physical.Bytes)))
	}
	if !bytes.Equal(physical.Bytes, authored) {
		problems = append(problems, label+" physical bytes differ from the authored bytes")
	}
	if digest := pipelineHarnessDigest(physical.Bytes); recorded.SHA256 != digest {
		problems = append(problems, label+" SHA-256 does not match the physical bytes")
	}
	return problems
}

func pipelineHarnessCheckRecordedParent(label string, recorded pipelineHarnessRecordIdentity, dir string) []string {
	var stat unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, dir, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &stat); err != nil {
		return []string{fmt.Sprintf("%s %q cannot be inspected: %v", label, dir, err)}
	}
	dev := unix.Mkdev(stat.Dev_major, stat.Dev_minor)
	if recorded.Dev != dev || recorded.Ino != stat.Ino || recorded.MountID != stat.Mnt_id {
		return []string{fmt.Sprintf("%s identity dev=%d ino=%d mount=%d does not match the physical directory dev=%d ino=%d mount=%d",
			label, recorded.Dev, recorded.Ino, recorded.MountID, dev, stat.Ino, stat.Mnt_id)}
	}
	return nil
}

func pipelineHarnessDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func pipelineHarnessCheckRows(expectation pipelineHarnessExpectation, snapshot pipelineHarnessSnapshot) []string {
	var problems []string
	if expectation.StageCheckpoint {
		stageRows := expectation.StageRows
		if stageRows == nil {
			stageRows = expectation.D0
		}
		if diff := pipelineFixtureDiff(stageRows, snapshot.RowsAfterStage); len(diff) != 0 {
			problems = append(problems, "rows at tm-commit.before differ: "+strings.Join(diff, ","))
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
	observed, basis := pipelineHarnessObserveCertainty(snapshot)
	if snapshot.Certainty != observed {
		problems = append(problems, fmt.Sprintf("commit certainty claim %q disagrees with observation evidence %q", snapshot.Certainty, observed))
	}
	if !slices.Equal(snapshot.CertaintyBasis, basis) {
		problems = append(problems, fmt.Sprintf("commit certainty basis %v disagrees with observation evidence %v", snapshot.CertaintyBasis, basis))
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
		if !pipelineHarnessRowsEqual(snapshot.AuthoredD1, snapshot.Rows) {
			problems = append(problems, "commit was claimed without the authored final D1 rows")
		}
	case pipelineHarnessNotCommitted:
		if !slices.Contains(basis, "final-rows-D0") || !slices.Contains(basis, "no-commit-entry") {
			problems = append(problems, "not-committed was claimed without final D0 rows and an unreached commit entry")
		}
	case pipelineHarnessUnknown:
		if len(basis) == 0 {
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
	catalog := pipelineHarnessFaultCatalog()
	pipelineHarnessCheckFaultCatalog(t, catalog)
	pipelineHarnessSaveJSON(t, "fault-catalog.json", catalog)
	executed := map[string]bool{}
	states := make([]pipelineHarnessSnapshot, 0, len(pipelineHarnessCases()))
	for _, spec := range pipelineHarnessCases() {
		t.Run(spec.name, func(t *testing.T) {
			// Given: the static table's own selected expectation, fixed before the
			// run from authored S/O/T/D0/D1 rather than regenerated afterwards.
			harness := newPipelineHarness(t, spec, true)
			expectation := pipelineHarnessScenarioSelected(t, table, spec.name)
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
	// Given: one real published observation and its static table expectation.
	spec := pipelineHarnessCases()[0]
	publishedHarness := newPipelineHarness(t, spec, true)
	publishedExpectation := pipelineHarnessScenarioByCase(t, spec.name)
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
	// The negative controls are actual fault observations: a real stage close
	// error and a real cancellation that the final commit rejects.
	precommit := pipelineHarnessScenarioByCase(t, "precommit-stage-close/text")
	stageFault := errors.New("stage close fault")
	stageCloseHarness := newPipelineHarness(t, spec, true)
	if _, err := stageCloseHarness.executeStageCloseFault(t.Context(), stageFault); !errors.Is(err, stageFault) {
		t.Fatalf("stage-close fault was not reached: %v", err)
	}
	control, err := stageCloseHarness.snapshot(precommit)
	if err != nil {
		t.Fatal(err)
	}
	if problems := pipelineHarnessJudge(precommit, control); len(problems) != 0 {
		t.Fatalf("real pre-commit stage-close control rejected: %v", problems)
	}
	cancelScenario := pipelineHarnessScenarioByCase(t, "precommit-cancel/text")
	cancelHarness := newPipelineHarness(t, spec, true)
	if _, err := cancelHarness.executePrecommitCancel(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("real pre-commit cancellation was not observed: %v", err)
	}
	cancelControl, err := cancelHarness.snapshot(cancelScenario)
	if err != nil {
		t.Fatal(err)
	}
	if problems := pipelineHarnessJudge(cancelScenario, cancelControl); len(problems) != 0 {
		t.Fatalf("real pre-commit cancellation control rejected: %v", problems)
	}
	pipelineHarnessSaveJSON(t, "reject-controls.json", map[string]any{
		"published": published, "stage_close_control": control, "cancel_control": cancelControl,
		"note": "controls are real captured fault states; only cloned single-field corruptions of them are judged as negatives, and no execution pass follows from acceptance",
	})
	type mutation struct {
		name   string
		expect pipelineHarnessExpectation
		base   pipelineHarnessSnapshot
		mutate func(*pipelineHarnessSnapshot)
	}
	mutations := []mutation{
		{"stage-missing", precommit, control, func(s *pipelineHarnessSnapshot) {
			delete(s.Files, "stage")
		}},
		{"d0-wrong-row", precommit, control, func(s *pipelineHarnessSnapshot) {
			s.Rows[0].Text = "wrong D0 text"
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
		{"certainty-from-flag", cancelScenario, cancelControl, func(s *pipelineHarnessSnapshot) {
			s.RowsAfterStage = nil
			s.Reached[pipelineBoundaryStageCloseAfter] = 0
			s.Certainty = pipelineHarnessNotCommitted
			s.CertaintyBasis = []string{"tm-committed-false"}
		}},
	}
	recordMutations := []struct {
		name   string
		mutate func(*pipelineHarnessRecord)
	}{
		{"record-source-inode", func(record *pipelineHarnessRecord) { record.Source.Identity.Ino++ }},
		{"record-source-dev", func(record *pipelineHarnessRecord) { record.Source.Identity.Dev++ }},
		{"record-source-size", func(record *pipelineHarnessRecord) { record.Source.Identity.Size++ }},
		{"record-source-mount", func(record *pipelineHarnessRecord) { record.Source.Identity.MountID++ }},
		{"record-source-mode", func(record *pipelineHarnessRecord) { record.Source.Identity.Mode++ }},
		{"record-source-nlink", func(record *pipelineHarnessRecord) { record.Source.Identity.Nlink++ }},
		{"record-source-hash", func(record *pipelineHarnessRecord) { record.Source.SHA256 = "invalid-existing-integrity-field" }},
		{"record-source-path", func(record *pipelineHarnessRecord) {
			record.Source.Path = filepath.Join(filepath.Dir(record.Source.Path), "other-source")
		}},
		{"record-stage-inode", func(record *pipelineHarnessRecord) { record.Stage.Identity.Ino++ }},
		{"record-stage-size", func(record *pipelineHarnessRecord) { record.Stage.Identity.Size++ }},
		{"record-stage-mount", func(record *pipelineHarnessRecord) { record.Stage.Identity.MountID++ }},
		{"record-stage-mode", func(record *pipelineHarnessRecord) { record.Stage.Identity.Mode++ }},
		{"record-stage-nlink", func(record *pipelineHarnessRecord) { record.Stage.Identity.Nlink++ }},
		{"record-stage-hash", func(record *pipelineHarnessRecord) { record.Stage.SHA256 = "invalid-existing-integrity-field" }},
		{"record-existing-output-hash", func(record *pipelineHarnessRecord) {
			record.ExistingOutput.File.SHA256 = "invalid-existing-integrity-field"
		}},
	}
	for _, field := range recordMutations {
		mutations = append(mutations, mutation{
			name: field.name, expect: publishedExpectation, base: published,
			mutate: func(s *pipelineHarnessSnapshot) { field.mutate(s.Record) },
		})
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
	stageCheckpoint := cancelControl.clone()
	stageCheckpoint.RowsAfterStage[0].Text = "wrong D0 text"
	if problems := pipelineHarnessJudge(cancelScenario, stageCheckpoint); len(problems) == 0 {
		t.Fatal("oracle accepted a wrong D0 row at the tm-commit.before checkpoint")
	} else {
		pipelineHarnessSaveJSON(t, "reject-stage-checkpoint-wrong-row.json", map[string]any{"problems": problems})
		t.Logf("invalid state observation stage-checkpoint-wrong-row rejected: %v", problems)
	}
	t.Run("ambiguous-commit-is-unknown", func(t *testing.T) {
		ambiguous := published.clone()
		ambiguous.RowsAtCommit = nil
		ambiguous.TMCommitted = false
		ambiguous.Result.TMCommitted = false
		ambiguous.Error = "ambiguous COMMIT result; checkpoint unavailable"
		ambiguous.ErrorPhase = "commit"
		ambiguous.Certainty, ambiguous.CertaintyBasis = pipelineHarnessObserveCertainty(ambiguous)
		if ambiguous.Certainty != pipelineHarnessUnknown {
			t.Fatalf("historical precommit evidence plus a false flag classified an ambiguous COMMIT as %s", ambiguous.Certainty)
		}
		if problems := pipelineHarnessJudge(publishedExpectation, ambiguous); len(problems) == 0 {
			t.Fatal("oracle accepted an ambiguous COMMIT as a committed observation")
		}
		pipelineHarnessSaveJSON(t, "reject-ambiguous-commit.json", map[string]any{
			"certainty": ambiguous.Certainty, "basis": ambiguous.CertaintyBasis, "observation": ambiguous,
		})
		t.Logf("ambiguous COMMIT classified unknown with basis %v", ambiguous.CertaintyBasis)
	})
	t.Run("historical-precommit-rows-cannot-prove-rollback", func(t *testing.T) {
		historical := cancelControl.clone()
		historical.Rows = slices.Clone(historical.AuthoredD1)
		certainty, basis := pipelineHarnessObserveCertainty(historical)
		if certainty == pipelineHarnessNotCommitted {
			t.Fatalf("historical afterStage D0 plus TMCommitted=false was accepted as not-committed although final rows are D1 (basis %v)", basis)
		}
		pipelineHarnessSaveJSON(t, "reject-historical-precommit.json", map[string]any{"certainty": certainty, "basis": basis})
	})
}

func pipelineHarnessScenarioByCase(t *testing.T, name string) pipelineHarnessExpectation {
	t.Helper()
	return pipelineHarnessScenarioSelected(t, pipelineHarnessScenarioTable(t), name)
}

// pipelineHarnessScenarioSelected returns the table object itself, so a valid
// run is judged against the same expectation the static table publishes.
func pipelineHarnessScenarioSelected(t *testing.T, table []pipelineHarnessExpectation, name string) pipelineHarnessExpectation {
	t.Helper()
	for _, scenario := range table {
		if scenario.Case == name {
			return scenario
		}
	}
	t.Fatalf("scenario %s is not in the state table", name)
	return pipelineHarnessExpectation{}
}
