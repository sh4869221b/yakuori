//go:build linux && pipelinefaults

package localize

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
)

// allow: SIZE_OK - Task7 owns one file. This same-target rerun matrix keeps
// its first-run stimuli, independent expectations and physical readback local.
func TestPipelineFailureRerunAndTMRevalidation(t *testing.T) {
	for _, row := range []string{
		"D0-unprepared", "D1-unrecorded", "D1-prepared", "Restored", "SourceBackedUp",
		"Published-recorded", "Published-stale-record", "missing-record", "corrupt-record",
		"identity-mismatch", "source-backed-up-conflict", "create-existing",
		"invalid-candidate", "w3-in-place-ja-header",
	} {
		t.Run(row, func(t *testing.T) {
			for _, kind := range []string{"text", "w3"} {
				if row == "w3-in-place-ja-header" && kind != "w3" {
					continue // Text has no source-language header.
				}
				t.Run(kind, func(t *testing.T) {
					for _, mode := range pipelineFixtureModes() {
						switch row {
						case "Restored", "SourceBackedUp", "source-backed-up-conflict", "w3-in-place-ja-header":
							if mode.mode != publication.InPlace {
								continue // Only InPlace moves the source or changes its header.
							}
						case "create-existing":
							if mode.mode != publication.Create {
								continue // Replace/InPlace accept existing output; covered above.
							}
						}
						t.Run(mode.name, func(t *testing.T) {
							testPipelineFailureRerun(t, row, pipelineFixtureSpec(kind, mode))
						})
					}
				})
			}
		})
	}
}

func testPipelineFailureRerun(t *testing.T, row string, spec pipelineHarnessCase) {
	// Given: the accepted real fixture, with expectations authored before Run1.
	h := newPipelineHarness(t, spec, true)
	f := h.fixture
	if fs := pipelineHarnessFilesystem(t, h.sourcePath); fs != "btrfs" && fs != "ext4" {
		t.Fatalf("qualified rerun requires ext4/Btrfs, got %s", fs)
	}
	d1 := f.expectedRows(t, f.input.units)
	var commits [2][2]int
	f.store.SetPipelineCommitHooks(tm.PipelineCommitHooks{
		Before: func(context.Context, *sql.Conn) error { commits[0][0]++; return nil },
		After:  func() { commits[0][1]++ },
	})
	firstWant, stage, fault := pipelineRerunFirst(h, row)
	first, err := h.snapshot(firstWant)
	if err != nil {
		t.Fatal(err)
	}
	pipelineRerunSave(h, "first", first)
	// This comparison precedes every Run2 setup. Inheriting actual first rows
	// would normalize a first-transaction corruption of the unrelated sentinel.
	firstRows := d1
	firstCommit := [2]int{1, 1}
	if row == "D0-unprepared" {
		firstRows, firstCommit = slices.Clone(f.d0), [2]int{}
	}
	if diff := pipelineFixtureDiff(firstRows, first.Rows); len(diff) != 0 {
		t.Fatalf("first-D1: %v", diff)
	}
	if commits[0] != firstCommit {
		t.Fatalf("first SQL checkpoints=%v, want %v", commits[0], firstCommit)
	}
	if problems := pipelineHarnessJudge(firstWant, first); len(problems) != 0 {
		t.Fatalf("first state: %v", problems)
	}
	if firstWant.RecordInstalled {
		expected := pipelineRerunRecord(h, stage, firstWant)
		raw, err := json.Marshal(expected)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(first.Files["record"].Bytes, raw) {
			t.Fatal("first record differs from independent complete record")
		}
	}
	if row == "D1-unrecorded" {
		if problems := pipelineHarnessCheckCaptured("first-retained-stage", first.Files["stage"], &stage, f.input.target); len(problems) != 0 {
			t.Fatalf("first stage: %v", problems)
		}
		collision := pipelineRerunFiles(h)[filepath.Join(filepath.Dir(h.ops.paths.Record), ".record-prepared")]
		if problems := pipelineHarnessCheckLeaf("first-record-collision", collision, []byte("occupied prepared record\n")); len(problems) != 0 {
			t.Fatalf("first collision: %v", problems)
		}
	}
	if fault != nil {
		fault.check(firstWant)
	}
	pipelineFixtureSaveJSON(t, "first-expected.json", firstWant)

	// Author Run2's complete rows and exact generation requests from inputs.
	stageRows, finalRows := slices.Clone(d1), slices.Clone(d1)
	var calls []string
	refusal, oldPhase, oldOperation := "", firstWant.RecordPhase, firstWant.RecordOperation
	var cause error
	switch row {
	case "D0-unprepared":
		stageRows, calls = slices.Clone(f.d0), pipelineFixtureAuthoredCalls(f.input)
	case "D1-unrecorded", "missing-record", "corrupt-record":
		refusal, cause = "prepare", publication.ErrInvalidRecord
	case "identity-mismatch", "source-backed-up-conflict":
		refusal, cause = "prepare", publication.ErrRecoveryConflict
	case "D1-prepared", "invalid-candidate":
		oldPhase, oldOperation = "abandoned", "reconcile-unpublished"
	case "SourceBackedUp":
		oldPhase, oldOperation = "restored", "restore-rename"
	}
	published := row == "Published-recorded" || row == "Published-stale-record" ||
		row == "create-existing" || row == "w3-in-place-ja-header"
	if published {
		oldPhase, oldOperation = "published", "publish-rename"
		switch spec.mode {
		case publication.Create:
			refusal, cause = "prepare", os.ErrExist
		case publication.InPlace:
			if spec.kind == "w3" {
				refusal = "import"
			} else {
				calls = []string{string(f.input.target)}
			}
		}
	}
	switch row {
	case "missing-record":
		if err := os.Remove(h.ops.paths.Record); err != nil {
			t.Fatal(err)
		}
	case "corrupt-record":
		if err := os.WriteFile(h.ops.paths.Record, []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
	case "identity-mismatch":
		bad := pipelineRerunRecord(h, stage, firstWant)
		bad.Stage.Identity.Ino++
		raw, err := json.Marshal(bad)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(h.ops.paths.Record, raw, 0600); err != nil {
			t.Fatal(err)
		}
	case "invalid-candidate":
		candidate := f.authoredRow(t, f.input.units[0])
		calls = []string{f.input.units[0].source}
		for i := range stageRows {
			if stageRows[i].Source == candidate.Source && stageRows[i].Profile == candidate.Profile &&
				stageRows[i].Schema == candidate.Schema {
				stageRows[i].Text = ""
			}
		}
		changed, err := f.db.Exec("UPDATE tm SET translated_text='' WHERE key_schema=? AND source_identity=? AND translation_profile=?",
			candidate.Schema, mustHexBytes(t, candidate.Source), mustHexBytes(t, candidate.Profile))
		if err != nil {
			t.Fatal(err)
		}
		if n, err := changed.RowsAffected(); err != nil || n != 1 {
			t.Fatalf("invalid candidate changed %d rows: %v", n, err)
		}
	}
	if diff := pipelineFixtureDiff(stageRows, pipelineFixtureReadRows(t, f.db)); len(diff) != 0 {
		t.Fatalf("Run2 authored input rows: %v", diff)
	}
	before := pipelineRerunFiles(h)
	pipelineFixtureSaveJSON(t, "run2-protected-before.json", before)
	runsBefore, err := filepath.Glob(filepath.Join(filepath.Dir(h.sourcePath), ".yakuori-run-*"))
	if err != nil {
		t.Fatal(err)
	}
	baseCalls := len(f.backend.calls)

	// Fresh observations and Core share the same real backend/Store and paths.
	// Unlike pipelineHarnessOnFixture, this must not rewrite source or output.
	next := &pipelineHarness{
		t: t, fixture: f, kind: h.kind, surface: "file", mode: h.mode, wrapped: true,
		trace: newPipelineHarnessTrace(), sourcePath: h.sourcePath, options: h.options, stimulus: "none",
		beforeSource: before[h.sourcePath], beforeOutput: before[h.options.Output],
		beforeParents: map[string]pipelineHarnessRecordIdentity{
			"source": pipelineHarnessReadParent(t, h.sourcePath),
			"output": pipelineHarnessReadParent(t, h.options.Output),
		},
	}
	if row == "SourceBackedUp" {
		// Prepare is expected to restore the captured original before Import.
		next.beforeSource, next.beforeOutput = h.beforeSource, h.beforeSource
	}
	next.ops = newPipelineHarnessOperations(t, next.trace, next.sourcePath)
	if adapter, ok := f.adapter.(*w3strings.Adapter); ok {
		next.adapter = &pipelineHarnessW3Adapter{Adapter: adapter, trace: next.trace}
	} else {
		next.adapter = &pipelineHarnessAdapter{Adapter: f.adapter, trace: next.trace}
	}
	next.wireObserveEntry()
	next.core, err = NewCoreWithLimits(NewInferenceEngine(&pipelineHarnessBackend{pipelineBackend: f.backend, trace: next.trace},
		f.requestBuilder()), f.limits)
	if err != nil {
		t.Fatal(err)
	}
	spec.outputPresent = next.beforeOutput.Exists
	want := pipelineFixtureRepeatExpectation(t, spec, f, len(calls), stageRows)
	want.Case, want.SourcePreserved = t.Name(), spec.mode != publication.InPlace
	if published {
		want.OldOutput = slices.Clone(f.input.target)
	}
	if published && spec.mode == publication.InPlace && spec.kind == "text" {
		const translatedAgain = "再訳文。\n"
		f.backend.texts[string(f.input.target)] = translatedAgain
		input := f.input
		input.source, input.target = slices.Clone(f.input.target), []byte(translatedAgain)
		input.units = []pipelineFixtureUnit{{stableID: "document", source: string(f.input.target), translation: translatedAgain}}
		authored := pipelineAuthoredSession(t, input)
		source, err := unit.NewSourceIdentity(authored, authored.Units()[0].ID(), f.scope.GameNamespace, f.scope.ArtifactNamespace, nil)
		if err != nil {
			t.Fatal(err)
		}
		key, err := f.profile.ExactKey(source)
		if err != nil {
			t.Fatal(err)
		}
		extra := pipelineFixtureRow{key.Schema(), hex.EncodeToString(key.SourceBytes()), hex.EncodeToString(key.ProfileBytes()), translatedAgain}
		finalRows = pipelineFixtureSortRows(append(slices.Clone(d1), extra))
		want.SourceBytes, want.TargetBytes, want.OutputBytes = input.source, input.target, input.target
	}
	want.D0, want.D1, want.FinalRows = slices.Clone(stageRows), slices.Clone(finalRows), slices.Clone(finalRows)
	if refusal != "" {
		want.Outcome, want.ErrorPhase = "not-published", refusal
		want.CommitCertainty = pipelineHarnessNotCommitted
		want.StageCheckpoint, want.CommitCheckpoint, want.StageRetained = false, false, false
		want.StageRows, want.BoundaryOrder = nil, nil
		want.StageFDClosed, want.RecordFDClosed, want.RecordInstalled, want.BackupPresent = false, false, false, false
		want.RecordPhase, want.RecordOperation = "", ""
		want.SourcePreserved, want.OutputPresent, want.OutputPreserved = true, next.beforeOutput.Exists, next.beforeOutput.Exists
		want.SourceBytes, want.OutputBytes = slices.Clone(f.input.source), nil
		if spec.mode == publication.InPlace {
			want.OutputBytes = slices.Clone(f.input.source)
			if published {
				want.SourceBytes = slices.Clone(f.input.target)
			}
		} else if spec.outputPresent {
			want.OutputBytes = slices.Clone(pipelineHarnessOldOutput)
		}
		if published {
			want.OutputBytes = slices.Clone(f.input.target)
		}
		for name := range want.RequiredBoundaries {
			want.RequiredBoundaries[name] = 0
		}
		if refusal == "import" {
			want.BoundaryOrder = []string{pipelineBoundaryRunCloseAfter}
			want.RequiredBoundaries[pipelineBoundaryRunCloseAfter] = 1
		}
	}
	updatedRecord := row == "D1-prepared" || row == "invalid-candidate" ||
		row == "SourceBackedUp" || row == "Published-stale-record"
	var expectedOldRecord []byte
	if updatedRecord {
		recordWant := firstWant
		recordWant.RecordPhase, recordWant.RecordOperation = oldPhase, oldOperation
		expected := pipelineRerunRecord(h, stage, recordWant)
		expectedOldRecord, err = json.Marshal(expected)
		if err != nil {
			t.Fatal(err)
		}
		pipelineFixtureSaveJSON(t, "old-record-expected.json", expected)
	}
	pipelineFixtureSaveJSON(t, "run2-expected.json", want)
	pipelineFixtureSaveJSON(t, "run2-expected-calls.json", calls)
	var newStage pipelineHarnessFile
	next.afterStageHook = func() error {
		if err := next.afterStage(); err != nil {
			return err
		}
		newStage, err = pipelineHarnessReadFile(next.ops.paths.Stage)
		return err
	}
	f.store.SetPipelineCommitHooks(tm.PipelineCommitHooks{
		Before: func(context.Context, *sql.Conn) error { commits[1][0]++; return nil },
		After:  func() { commits[1][1]++ },
	})

	// When: one fresh real Import/Run, on exactly the same source and target.
	next.execute(t.Context())

	// Then: the real call history, physical state and independently authored
	// rows distinguish revalidation, one repaired unit and early typed refusal.
	second, err := next.snapshot(want)
	if err != nil {
		t.Fatal(err)
	}
	pipelineRerunSave(next, "second", second)
	after := pipelineRerunFiles(h)
	pipelineFixtureSaveJSON(t, "run2-protected-after.json", after)
	if !slices.Equal(f.backend.calls[baseCalls:], calls) {
		t.Fatalf("Run2 Generate requests=%q, want %q", f.backend.calls[baseCalls:], calls)
	}
	if diff := pipelineFixtureDiff(finalRows, second.Rows); len(diff) != 0 {
		t.Fatalf("Run2 complete rows: %v", diff)
	}
	runDelta, importCount, commitCount := 1, 1, [2]int{1, 1}
	if refusal != "" {
		commitCount = [2]int{}
		var failure *Error
		if !errors.As(next.runErr, &failure) || failure.Phase != refusal ||
			cause != nil && !errors.Is(next.runErr, cause) || next.result.TMCommitted ||
			next.result.Publication.State != publication.NotPublished {
			t.Fatalf("Run2 typed %s refusal: result=%+v error=%+v typed=%+v", refusal, next.result, next.runErr, failure)
		}
		if cause == publication.ErrInvalidRecord || cause == publication.ErrRecoveryConflict {
			var recovery *publication.RecoveryError
			if !errors.As(next.runErr, &recovery) {
				t.Fatalf("Run2 lacks RecoveryError: %v", next.runErr)
			}
		}
		if refusal == "prepare" {
			runDelta, importCount = 0, 0
		}
		if next.memory.run != nil || next.adapterExports() != 0 || next.adapterObserves() != 0 ||
			next.trace.reached[pipelineBoundaryTMCommitBefore] != 0 ||
			next.trace.reached[pipelineBoundaryTMCommitAfter] != 0 ||
			next.trace.reached[pipelineBoundaryPublishRenameBefore] != 0 ||
			next.trace.reached[pipelineBoundaryPublishRenameAfter] != 0 {
			t.Fatal("refused Run2 performed TM/stage/publication work")
		}
		if refusal == "import" {
			entries, err := os.ReadDir(filepath.Dir(next.ops.paths.Record))
			if err != nil || len(entries) != 0 || next.trace.reached[pipelineBoundaryRunCloseAfter] != 1 {
				t.Fatalf("import refusal left files or an open Run: %v, %v", entries, err)
			}
		}
	} else {
		if next.runErr != nil || !next.result.TMCommitted || next.result.Publication.State != publication.Published {
			t.Fatalf("Run2 result=%+v error=%v", next.result, next.runErr)
		}
		if next.memory.run == nil || next.memory.run == h.memory.run ||
			next.ops.paths.Record == h.ops.paths.Record || next.adapterExports() != 1 || next.adapterObserves() != 1 {
			t.Fatal("Run2 reused an old Run or did not re-export/re-observe")
		}
		if problems := pipelineHarnessCheckCaptured("fresh-published-stage", second.Files["output"], &newStage, want.TargetBytes); len(problems) != 0 {
			t.Fatalf("Run2 fresh stage: %v", problems)
		}
		if stage.Exists && first.Files["stage"].Exists && newStage.Dev == stage.Dev && newStage.Inode == stage.Inode {
			t.Fatal("Run2 published the old retained stage")
		}
		if spec.mode == publication.InPlace {
			if problems := pipelineHarnessCheckCaptured("fresh-backup", second.Files["backup"], &next.beforeSource, want.SourceBytes); len(problems) != 0 {
				t.Fatalf("Run2 backup: %v", problems)
			}
		}
	}
	if commits[1] != commitCount || next.adapterImports() != importCount {
		t.Fatalf("Run2 SQL=%v/%v Import=%d/%d", commits[1], commitCount, next.adapterImports(), importCount)
	}
	if problems := pipelineHarnessJudge(want, second); len(problems) != 0 {
		t.Fatalf("Run2 state: %v", problems)
	}
	runsAfter, err := filepath.Glob(filepath.Join(filepath.Dir(h.sourcePath), ".yakuori-run-*"))
	if err != nil || len(runsAfter) != len(runsBefore)+runDelta {
		t.Fatalf("Run2 directories before=%v after=%v delta=%d: %v", runsBefore, runsAfter, runDelta, err)
	}
	for _, path := range runsBefore {
		if !slices.Contains(runsAfter, path) {
			t.Fatalf("Run2 removed old private directory %s", path)
		}
	}
	for path, protected := range before {
		current, exists := after[path]
		if !exists {
			t.Fatalf("Run2 lost protected observation %s", path)
		}
		switch {
		case path == h.ops.paths.Record && updatedRecord:
			if problems := pipelineHarnessCheckLeaf("reconciled-old-record", current, expectedOldRecord); len(problems) != 0 ||
				current.Mode != 0100600 || current.Inode == protected.Inode && current.Dev == protected.Dev {
				t.Fatalf("old record reconciliation: %v", problems)
			}
		case path == h.ops.paths.Backup && row == "SourceBackedUp":
			if current.Exists {
				t.Fatal("Prepare did not restore the old original backup")
			}
		case refusal == "" && (path == h.options.Output || spec.mode == publication.InPlace && path == h.options.Source):
			// Fresh output/backup are checked against pre-run and stage captures above.
		default:
			if current.Exists != protected.Exists {
				t.Fatalf("Run2 changed protected presence %s", path)
			}
			if protected.Exists {
				if problems := pipelineHarnessCheckCaptured("protected-old-file", current, &protected, protected.Bytes); len(problems) != 0 {
					t.Fatalf("Run2 protected %s: %v", path, problems)
				}
			}
		}
	}
	for path := range after {
		if _, exists := before[path]; !exists {
			t.Fatalf("Run2 added an unexpected old-run file %s", path)
		}
	}
	if !pipelineLockAvailable(t, filepath.Dir(h.sourcePath)) {
		t.Fatal("Run2 did not release the publication directory lock")
	}
	pipelineFixtureSaveJSON(t, "rerun-operations.json", struct {
		Row         string
		SQL         [2][2]int
		Generated   []string
		ImportCount int
		Refusal     string
		OldPhase    string
		OldOp       string
		RunDirs     []string
		NewStage    pipelineHarnessFile
	}{row, commits, slices.Clone(f.backend.calls[baseCalls:]), next.adapterImports(), refusal, oldPhase, oldOperation, runsAfter, newStage})
	t.Logf("rerun-proof row=%s first=%s second=%s refusal=%s Generate=%d SQL=%v old-record=%s/%s",
		row, h.result.Publication.State, next.result.Publication.State, refusal, len(calls), commits, oldPhase, oldOperation)
}

func pipelineRerunFirst(h *pipelineHarness, row string) (pipelineHarnessExpectation, pipelineHarnessFile, *pipelinePublicationFault) {
	t, f := h.t, h.fixture
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fault := &pipelinePublicationFault{h: h, cancel: cancel}
	switch row {
	case "D0-unprepared", "D1-unrecorded":
		want := pipelineHarnessExpectationForFixture(t, pipelineHarnessCase{
			name: t.Name(), kind: h.kind, surface: "file", mode: h.mode, outputPresent: h.beforeOutput.Exists,
		}, f)
		want.Outcome, want.RecordInstalled, want.RecordFDClosed, want.BackupPresent = "not-published", false, false, false
		want.RecordPhase, want.RecordOperation = "", ""
		want.StageRetained, want.StageBytes, want.SourcePreserved = true, slices.Clone(f.input.target), true
		want.OutputPresent, want.OutputPreserved, want.OutputBytes = h.beforeOutput.Exists, h.beforeOutput.Exists, slices.Clone(h.beforeOutput.Bytes)
		for name := range want.RequiredBoundaries {
			if strings.HasPrefix(name, "record.") || strings.HasPrefix(name, "backup-rename.") ||
				strings.HasPrefix(name, "publish-rename.") || name == pipelineBoundaryStageCloseAfter {
				want.RequiredBoundaries[name] = 0
			}
		}
		var stage pipelineHarnessFile
		if row == "D0-unprepared" {
			want.StageRetained, want.StageFDClosed, want.StageCheckpoint, want.CommitCheckpoint = false, false, false, false
			want.CommitCertainty, want.FinalRows, want.BoundaryOrder = pipelineHarnessNotCommitted, slices.Clone(f.d0), nil
			for name := range want.RequiredBoundaries {
				want.RequiredBoundaries[name] = 0
			}
			cancel()
		} else {
			h.afterStageHook = func() error {
				if err := h.afterStage(); err != nil {
					return err
				}
				var err error
				stage, err = pipelineHarnessReadFile(h.ops.paths.Stage)
				if err != nil {
					return err
				}
				file, err := os.OpenFile(filepath.Join(filepath.Dir(h.ops.paths.Record), ".record-prepared"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if err != nil {
					return err
				}
				raw := []byte("occupied prepared record\n")
				n, writeErr := file.Write(raw)
				if err := errors.Join(writeErr, file.Close()); err != nil {
					return err
				}
				if n != len(raw) {
					t.Fatalf("collision write=%d/%d", n, len(raw))
				}
				return nil
			}
		}
		h.execute(ctx)
		cause, phase, generated := error(os.ErrExist), "publish", pipelineFixtureAuthoredCalls(f.input)
		if row == "D0-unprepared" {
			cause, phase, generated = context.Canceled, "prepare", nil
		}
		var failure *Error
		if !errors.Is(h.runErr, cause) || !errors.As(h.runErr, &failure) || failure.Phase != phase {
			t.Fatalf("first %s cause: %v", row, h.runErr)
		}
		pipelineFixtureAssertCalls(t, f, generated)
		return want, stage, nil
	case "Restored":
		fault.row.name = "restore-success"
	case "SourceBackedUp":
		fault.row.name = "restore-rename-error"
	case "source-backed-up-conflict":
		fault.row.name = "restore-external-file"
	case "Published-recorded", "create-existing", "w3-in-place-ja-header":
		fault.row.name = "run-close-error"
	case "Published-stale-record":
		fault.row = pipelinePublicationFaultRow{name: "published-write-error", phase: "published", operation: "write", stimulus: "error"}
	case "D1-prepared", "missing-record", "corrupt-record", "identity-mismatch", "invalid-candidate":
		fault.row = pipelinePublicationFaultRow{name: "prepared-directory-sync-error", phase: "prepared", operation: "directory-sync", stimulus: "error"}
	default:
		t.Fatalf("unplanned first outcome %s", row)
	}
	want := fault.expectation()
	fault.execute(ctx)
	return want, fault.stage, fault
}

// The record is built from independently captured original/stage identities,
// not copied from the decoded record that the reconciliation is being tested on.
func pipelineRerunRecord(h *pipelineHarness, stage pipelineHarnessFile, want pipelineHarnessExpectation) pipelineHarnessRecord {
	file := func(capture pipelineHarnessFile) pipelineHarnessRecordFile {
		return pipelineHarnessRecordFile{
			Path: capture.Path, Basename: filepath.Base(capture.Path),
			Identity: pipelineHarnessRecordIdentity{
				Dev: capture.Dev, Ino: capture.Inode, MountID: capture.MountID, Size: capture.Size,
				Mode: uint32(capture.Mode), Nlink: uint64(capture.Nlink),
			},
			SHA256: pipelineHarnessDigest(capture.Bytes), Size: capture.Size,
		}
	}
	record := pipelineHarnessRecord{
		SchemaVersion: 1, RunID: strings.TrimPrefix(filepath.Base(filepath.Dir(h.ops.paths.Record)), ".yakuori-run-"),
		Mode: string(h.mode), Source: file(h.beforeSource), Stage: file(stage),
		Output:       pipelineHarnessRecordPath{Path: h.options.Output, Basename: filepath.Base(h.options.Output)},
		SourceParent: h.beforeParents["source"], OutputParent: h.beforeParents["output"],
		ExistingOutput: pipelineHarnessExistingOutput{Absent: !h.beforeOutput.Exists},
		Phase:          want.RecordPhase, LastCompletedOperation: want.RecordOperation,
	}
	if h.beforeOutput.Exists {
		existing := file(h.beforeOutput)
		record.ExistingOutput.File = &existing
	}
	if h.mode == publication.InPlace {
		record.Backup = &pipelineHarnessRecordPath{Path: h.ops.paths.Backup, Basename: filepath.Base(h.ops.paths.Backup)}
	}
	return record
}

func pipelineRerunFiles(h *pipelineHarness) map[string]pipelineHarnessFile {
	h.t.Helper()
	paths := []string{h.sourcePath, h.options.Output, h.ops.paths.Stage, h.ops.paths.Record, h.ops.paths.Backup}
	if h.ops.paths.Record != "" {
		temporary, err := filepath.Glob(filepath.Join(filepath.Dir(h.ops.paths.Record), ".record-*"))
		if err != nil {
			h.t.Fatal(err)
		}
		paths = append(paths, temporary...)
	}
	files := make(map[string]pipelineHarnessFile)
	for _, path := range paths {
		if path == "" {
			continue
		}
		captured, err := pipelineHarnessReadFile(path)
		if err != nil {
			h.t.Fatal(err)
		}
		files[path] = captured
	}
	return files
}

func pipelineRerunSave(h *pipelineHarness, label string, state pipelineHarnessSnapshot) {
	t := h.t
	t.Helper()
	pipelineFixtureSaveJSON(t, label+"-state.json", state)
	var failure *Error
	if errors.As(h.runErr, &failure) {
		cause := struct {
			Phase, Cause, RecoveryCause string
			InvalidRecord, Conflict     bool
		}{
			Phase: failure.Phase, Cause: failure.Err.Error(),
			InvalidRecord: errors.Is(h.runErr, publication.ErrInvalidRecord),
			Conflict:      errors.Is(h.runErr, publication.ErrRecoveryConflict),
		}
		var recovery *publication.RecoveryError
		if errors.As(h.runErr, &recovery) {
			cause.RecoveryCause = recovery.Cause.Error()
		}
		pipelineFixtureSaveJSON(t, label+"-cause.json", cause)
	}
	for name, file := range state.Files {
		if file.Exists {
			pipelineFixtureSave(t, label+"-"+name+".bin", file.Bytes)
		}
	}
	var sequence int
	var database, path string
	if err := h.fixture.db.QueryRow("PRAGMA database_list").Scan(&sequence, &database, &path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pipelineFixtureSave(t, label+"-tm.db", raw)
}
