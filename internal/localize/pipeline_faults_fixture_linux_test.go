//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/unit"
)

// Task5 integrates the Task1 fixture into the public Core surfaces. The state
// expectations come from the authored fixture table (S/O/T/D0/D1) and never
// from the observation under test.
type pipelineFixtureMode struct {
	name          string
	mode          publication.Mode
	outputPresent bool
}

func pipelineFixtureModes() []pipelineFixtureMode {
	return []pipelineFixtureMode{
		{name: "create", mode: publication.Create},
		{name: "replace-absent", mode: publication.Replace},
		{name: "replace-existing", mode: publication.Replace, outputPresent: true},
		{name: "in-place", mode: publication.InPlace},
	}
}

func pipelineFixtureSpec(kind string, mode pipelineFixtureMode) pipelineHarnessCase {
	return pipelineHarnessCase{
		name: "file-" + mode.name + "/" + kind, kind: kind, surface: "file",
		mode: mode.mode, outputPresent: mode.outputPresent,
	}
}

// pipelineHarnessOnFixture builds the observed wrapper around an existing
// fixture so a second run shares exactly the same SQLite store, canonical
// profile and imported source bytes while owning a fresh publication parent.
func pipelineHarnessOnFixture(t *testing.T, spec pipelineHarnessCase, f *pipelineFixture, parent string) *pipelineHarness {
	t.Helper()
	h := &pipelineHarness{t: t, fixture: f, kind: spec.kind, surface: spec.surface, mode: spec.mode, wrapped: true, stimulus: "none", trace: newPipelineHarnessTrace()}
	if adapter, ok := f.adapter.(*w3strings.Adapter); ok {
		h.adapter = &pipelineHarnessW3Adapter{Adapter: adapter, trace: h.trace}
	} else {
		h.adapter = &pipelineHarnessAdapter{Adapter: f.adapter, trace: h.trace}
	}
	info := f.backend.info
	backend := &pipelineHarnessBackend{pipelineBackend: f.backend, trace: h.trace}
	core, err := NewCoreWithLimits(NewInferenceEngine(backend, func(_ context.Context, _ unit.UnitID, text string) (inference.GenerationRequest, error) {
		return pipelineRequest(info, text)
	}), f.limits)
	if err != nil {
		t.Fatal(err)
	}
	h.core = core
	if spec.surface == "stdout" {
		h.stdoutPath = filepath.Join(parent, "stdout.bin")
		return h
	}
	h.sourcePath = filepath.Join(parent, "source")
	if err := os.WriteFile(h.sourcePath, f.input.source, 0600); err != nil {
		t.Fatal(err)
	}
	h.options = publication.Options{Source: h.sourcePath, Output: filepath.Join(parent, "output"), Mode: spec.mode}
	if spec.mode == publication.InPlace {
		h.options.Output = h.options.Source
	}
	if spec.mode == publication.Replace && spec.outputPresent {
		if err := os.WriteFile(h.options.Output, pipelineHarnessOldOutput, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if h.beforeOutput, err = pipelineHarnessReadFile(h.options.Output); err != nil {
		t.Fatal(err)
	}
	if h.beforeSource, err = pipelineHarnessReadFile(h.sourcePath); err != nil {
		t.Fatal(err)
	}
	h.beforeParents = map[string]pipelineHarnessRecordIdentity{
		"source": pipelineHarnessReadParent(t, h.sourcePath),
		"output": pipelineHarnessReadParent(t, h.options.Output),
	}
	h.ops = newPipelineHarnessOperations(t, h.trace, h.options.Source)
	return h
}

func (h *pipelineHarness) commitStateExpectation(row pipelineCommitStateCase) pipelineHarnessExpectation {
	t := h.t
	spec := pipelineHarnessCase{name: row.name, kind: h.kind, surface: h.surface, mode: h.mode, outputPresent: h.beforeOutput.Exists}
	want := pipelineHarnessExpectationForFixture(t, spec, h.fixture)
	want.Case = row.name + "/" + h.kind + "/" + string(h.mode)
	want.RecordPhase, want.RecordOperation = row.phase, row.operation
	want.StageRetained = row.state != publication.Published
	want.StageBytes = slices.Clone(h.fixture.input.target)
	want.BackupPresent = spec.mode == publication.InPlace && (row.state == publication.Published || row.state == publication.SourceBackedUp)
	want.OutputPresent = row.state == publication.Published || spec.outputPresent || spec.mode == publication.InPlace
	want.SourcePreserved = !want.BackupPresent
	want.OutputPreserved = row.name == "prepared" && want.OutputPresent || row.name == "restored"
	if row.state != publication.Published {
		want.OutputBytes = slices.Clone(pipelineHarnessOldOutput)
		if spec.mode == publication.InPlace {
			want.OutputBytes = slices.Clone(h.fixture.input.source)
		}
	}
	switch row.name {
	case "prepared":
		want.Outcome = "not-published"
	case "restored":
		want.Outcome = "restored"
	case "source-backed-up":
		want.Outcome, want.OutputPresent = "source-backed-up", false
	case "external-source":
		want.Outcome = "source-backed-up"
		want.OutputBytes = slices.Clone(h.fixture.input.target)
	case "published-stale":
		if spec.mode == publication.InPlace {
			want.RecordPhase, want.RecordOperation = "source-backed-up", "backup-rename"
		}
	}
	records, attempted, backups, restored := 2, 2, 0, 0
	if spec.mode == publication.InPlace {
		records, attempted, backups = 3, 3, 1
	}
	switch row.name {
	case "prepared":
		records, attempted, backups = 1, 1, 0
	case "restored":
		records, attempted, restored = 3, 3, 1
	case "source-backed-up", "external-source":
		records, attempted = 2, 2
	case "published-stale":
		records--
	}
	for _, name := range []string{pipelineBoundaryRecordWriteBefore, pipelineBoundaryRecordCloseBefore} {
		want.RequiredBoundaries[name] = attempted
	}
	for _, name := range []string{pipelineBoundaryRecordWriteAfter, pipelineBoundaryRecordCloseAfter, pipelineBoundaryRecordInstallBefore, pipelineBoundaryRecordInstallAfter} {
		want.RequiredBoundaries[name] = records
	}
	want.RequiredBoundaries[pipelineBoundaryRecordSyncBefore] = 2 * records
	want.RequiredBoundaries[pipelineBoundaryRecordSyncAfter] = 2 * records
	want.RequiredBoundaries[pipelineBoundaryBackupRenameBefore] = backups + restored
	want.RequiredBoundaries[pipelineBoundaryBackupRenameAfter] = backups + restored
	if row.name == "source-backed-up" {
		want.RequiredBoundaries[pipelineBoundaryBackupRenameBefore]++
	}
	if row.name == "prepared" {
		want.RequiredBoundaries[pipelineBoundaryStageCloseAfter] = 0
		want.RequiredBoundaries[pipelineBoundaryPublishRenameBefore] = 0
	}
	if row.state != publication.Published {
		want.RequiredBoundaries[pipelineBoundaryPublishRenameAfter] = 0
	}
	return want
}

func pipelineFixtureAuthoredCalls(input pipelineFixtureInput) []string {
	calls := make([]string, 0, len(input.units))
	for _, entry := range input.units {
		if entry.source != "" {
			calls = append(calls, entry.source)
		}
	}
	return calls
}

func pipelineFixtureAssertCalls(t *testing.T, f *pipelineFixture, want []string) {
	t.Helper()
	if !slices.Equal(f.backend.calls, want) {
		t.Fatalf("Generate calls = %d, want the %d authored non-empty units", len(f.backend.calls), len(want))
	}
}

func pipelineFixtureSortRows(rows []pipelineFixtureRow) []pipelineFixtureRow {
	slices.SortFunc(rows, func(a, b pipelineFixtureRow) int {
		if c := strings.Compare(a.Source, b.Source); c != 0 {
			return c
		}
		if c := strings.Compare(a.Profile, b.Profile); c != 0 {
			return c
		}
		return a.Schema - b.Schema
	})
	return rows
}

func pipelineFixtureExactRow(t *testing.T, f *pipelineFixture, stableID string, text string) pipelineFixtureRow {
	t.Helper()
	id, err := unit.NewUnitID(f.input.adapterName, f.input.formatSchema, stableID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := unit.NewSourceIdentity(f.authored, id, f.scope.GameNamespace, f.scope.ArtifactNamespace, f.keys[stableID])
	if err != nil {
		t.Fatal(err)
	}
	key, err := f.profile.ExactKey(source)
	if err != nil {
		t.Fatal(err)
	}
	return pipelineFixtureRow{key.Schema(), hex.EncodeToString(key.SourceBytes()), hex.EncodeToString(key.ProfileBytes()), text}
}

func pipelineFixtureSaveState(t *testing.T, label string, expectation pipelineHarnessExpectation, snapshot pipelineHarnessSnapshot, rows []pipelineFixtureRow) {
	t.Helper()
	pipelineFixtureSaveJSON(t, label+"-expected.json", expectation)
	pipelineHarnessSaveJSON(t, label+"-state.json", snapshot)
	pipelineHarnessSaveJSON(t, label+"-rows.json", rows)
	pipelineHarnessSave(t, label+"-source.bin", snapshot.Files["source"].Bytes)
	pipelineHarnessSave(t, label+"-output.bin", snapshot.Files["output"].Bytes)
	if stage := snapshot.Files["stage"]; stage.Exists {
		pipelineHarnessSave(t, label+"-stage.bin", stage.Bytes)
	}
	if record := snapshot.Files["record"]; record.Exists {
		pipelineHarnessSave(t, label+"-record.json", record.Bytes)
	}
	if backup := snapshot.Files["backup"]; backup.Exists {
		pipelineHarnessSave(t, label+"-backup.bin", backup.Bytes)
	}
	var logical strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&logical, "key_schema=%d source_identity=%s translation_profile=%s translated_text=%q\n", row.Schema, row.Source, row.Profile, row.Text)
	}
	pipelineHarnessSave(t, label+"-logical-rows.log", []byte(logical.String()))
}

// pipelineFixtureRepeatExpectation is the static expectation for a run whose
// rows already exist: generation happens only for the named count, the rows
// seen at tm-commit.before are the pre-run rows, and the final rows stay D1.
func pipelineFixtureRepeatExpectation(t *testing.T, spec pipelineHarnessCase, f *pipelineFixture, generate int, stageRows []pipelineFixtureRow) pipelineHarnessExpectation {
	t.Helper()
	expectation := pipelineHarnessExpectationForFixture(t, spec, f)
	expectation.StageRows = slices.Clone(stageRows)
	expectation.BoundaryOrder = nil
	for range generate {
		expectation.BoundaryOrder = append(expectation.BoundaryOrder, pipelineBoundaryGenerateBefore, pipelineBoundaryGenerateAfter)
	}
	expectation.BoundaryOrder = append(expectation.BoundaryOrder,
		pipelineBoundaryStageWriteBefore, pipelineBoundaryStageWriteAfter,
		pipelineBoundaryObserveBefore, pipelineBoundaryObserveAfter,
		pipelineBoundaryCompareFinalAfter,
		pipelineBoundaryTMCommitBefore, pipelineBoundaryTMCommitAfter,
		pipelineBoundaryRunCloseAfter)
	expectation.RequiredBoundaries[pipelineBoundaryGenerateBefore] = generate
	expectation.RequiredBoundaries[pipelineBoundaryGenerateAfter] = generate
	return expectation
}

func TestPipelineFixture(t *testing.T) {
	for _, kind := range []string{"text", "w3"} {
		for _, mode := range pipelineFixtureModes() {
			t.Run(kind+"/"+mode.name, func(t *testing.T) {
				testPipelineFixtureFile(t, kind, mode)
			})
		}
	}
	t.Run("stdout/text", testPipelineFixtureStdout)
	for _, kind := range []string{"text", "w3"} {
		t.Run("repeat/"+kind, func(t *testing.T) { testPipelineFixtureRepeat(t, kind) })
		t.Run("invalid-candidate/"+kind, func(t *testing.T) { testPipelineFixtureInvalidCandidate(t, kind) })
	}
	t.Run("rerun/create-refuses-existing", testPipelineFixtureCreateRerun)
	t.Run("rerun/replace-same-target", testPipelineFixtureReplaceRerun)
	t.Run("rerun/text-in-place", testPipelineFixtureTextInPlaceRerun)
	t.Run("rerun/w3-in-place-ja-header", testPipelineFixtureW3InPlaceRerun)
	t.Run("repeat/stdout-text", testPipelineFixtureStdoutRepeat)
}

func testPipelineFixtureFile(t *testing.T, kind string, mode pipelineFixtureMode) {
	spec := pipelineFixtureSpec(kind, mode)
	// Given: the public FileWithTM surface on a fresh real fixture.
	public := newPipelineHarness(t, spec, false)
	publicExpectation := pipelineHarnessExpectationForFixture(t, spec, public.fixture)
	publicResult, publicErr := public.execute(t.Context())
	if publicErr != nil || !publicResult.TMCommitted || publicResult.Publication.State != publication.Published {
		t.Fatalf("public FileWithTM result=%+v error=%v", publicResult, publicErr)
	}
	wantCalls := pipelineFixtureAuthoredCalls(public.fixture.input)
	pipelineFixtureAssertCalls(t, public.fixture, wantCalls)
	if got := len(wantCalls); got == 0 {
		t.Fatal("fixture has no authored non-empty unit")
	}
	publicSnapshot, err := public.snapshot(publicExpectation)
	if err != nil {
		t.Fatal(err)
	}
	if diff := pipelineFixtureDiff(publicExpectation.D1, publicSnapshot.Rows); len(diff) != 0 {
		t.Fatalf("public D1 differs: %v", diff)
	}
	if !bytes.Equal(publicSnapshot.Files["output"].Bytes, publicExpectation.TargetBytes) {
		t.Fatal("public output differs from the authored target bytes")
	}
	if publicSnapshot.Files["stage"].Exists {
		t.Fatal("public run retained the published stage")
	}
	if !publicSnapshot.Files["record"].Exists || publicSnapshot.Record == nil ||
		publicSnapshot.Record.Phase != "published" || publicSnapshot.Record.LastCompletedOperation != "publish-rename" {
		t.Fatalf("public record is not the installed published record: %+v", publicSnapshot.Record)
	}
	if !publicSnapshot.RecordInstalled || publicSnapshot.InstallBasis == "" {
		t.Fatalf("public capture does not report the physical installed record: installed=%t basis=%q",
			publicSnapshot.RecordInstalled, publicSnapshot.InstallBasis)
	}
	if publicSnapshot.Reached[pipelineBoundaryRecordInstallAfter] != 0 {
		t.Fatal("public capture fabricated an install callback marker")
	}
	if problems := pipelineHarnessCheckRecord(publicExpectation, publicSnapshot); len(problems) != 0 {
		t.Fatalf("public record identity/state differs: %v", problems)
	}
	if mode.mode == publication.InPlace {
		if !publicSnapshot.Files["backup"].Exists || !bytes.Equal(publicSnapshot.Files["backup"].Bytes, publicExpectation.SourceBytes) {
			t.Fatal("InPlace did not retain the original source bytes as the backup")
		}
	} else if publicSnapshot.Files["backup"].Exists {
		t.Fatal("non-InPlace run acquired a backup")
	}

	// When: the observed wrapper runs the same real pipeline on a separate
	// fixture with the same authored inputs.
	wrapped := newPipelineHarness(t, spec, true)
	wrappedExpectation := pipelineHarnessExpectationForFixture(t, spec, wrapped.fixture)
	wrappedResult, wrappedErr := wrapped.execute(t.Context())
	if wrappedErr != nil || !wrappedResult.TMCommitted || wrappedResult.Publication.State != publication.Published {
		t.Fatalf("wrapped FileWithTM result=%+v error=%v", wrappedResult, wrappedErr)
	}
	pipelineFixtureAssertCalls(t, wrapped.fixture, wantCalls)
	if wrapped.adapterExports() != 1 || wrapped.adapterObserves() != 1 {
		t.Fatalf("wrapped Export/Observe calls = %d/%d, want 1/1", wrapped.adapterExports(), wrapped.adapterObserves())
	}
	if wrapped.adapterImports() != 1 {
		t.Fatalf("wrapped Import calls = %d, want 1", wrapped.adapterImports())
	}
	wrappedSnapshot, err := wrapped.snapshot(wrappedExpectation)
	if err != nil {
		t.Fatal(err)
	}
	if problems := pipelineHarnessJudge(wrappedExpectation, wrappedSnapshot); len(problems) != 0 {
		t.Fatalf("observed wrapper state rejected: %v", problems)
	}
	if !wrappedSnapshot.StageFDClosed || !wrappedSnapshot.RecordFDClosed {
		t.Fatal("wrapped run did not prove the real stage/record descriptor Close")
	}
	if !slices.Equal(wrappedSnapshot.Files["output"].Bytes, publicSnapshot.Files["output"].Bytes) {
		t.Fatal("wrapped and public output bytes differ")
	}
	if !slices.Equal(wrappedSnapshot.Rows, publicSnapshot.Rows) {
		t.Fatal("wrapped and public TM rows differ")
	}
	if kind == "w3" {
		empty := pipelineFixtureExactRow(t, wrapped.fixture, "1002", "")
		if !slices.Contains(wrappedSnapshot.Rows, empty) {
			t.Fatal("empty W3 unit 1002 has no empty TM row")
		}
	}
	pipelineFixtureSaveState(t, kind+"-"+mode.name+"-public", publicExpectation, publicSnapshot, publicSnapshot.Rows)
	pipelineFixtureSaveState(t, kind+"-"+mode.name+"-wrapped", wrappedExpectation, wrappedSnapshot, wrappedSnapshot.Rows)
	pipelineHarnessSaveJSON(t, kind+"-"+mode.name+"-record-events.json", wrappedSnapshot.RecordEvents)
	t.Logf("kind=%s mode=%s Generate=%d public/wrapped equal; D0=%d D1=%d stage_retained=%t record_installed=%t backup=%t stage_fd_closed=%t record_fd_closed=%t run_close=%d",
		kind, mode.name, len(wantCalls), len(wrappedExpectation.D0), len(wrappedSnapshot.Rows),
		wrappedSnapshot.Files["stage"].Exists, wrappedSnapshot.RecordInstalled, wrappedSnapshot.Files["backup"].Exists,
		wrappedSnapshot.StageFDClosed, wrappedSnapshot.RecordFDClosed, wrappedSnapshot.Reached[pipelineBoundaryRunCloseAfter])
}

func testPipelineFixtureRepeat(t *testing.T, kind string) {
	mode := pipelineFixtureModes()[2] // replace-existing
	spec := pipelineFixtureSpec(kind, mode)
	first := newPipelineHarness(t, spec, false)
	expectation := pipelineHarnessExpectationForFixture(t, spec, first.fixture)
	if _, err := first.execute(t.Context()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	rows := pipelineFixtureReadRows(t, first.fixture.db)
	if diff := pipelineFixtureDiff(expectation.D1, rows); len(diff) != 0 {
		t.Fatalf("first run D1 differs: %v", diff)
	}
	output := first.outputBytes(t)
	baseCalls := len(first.fixture.backend.calls)
	repeatExpectation := pipelineFixtureRepeatExpectation(t, spec, first.fixture, 0, rows)

	// When: the same original input and profile run into a fresh parent with
	// the same real store, so every unit must be an exact TM hit.
	second := pipelineHarnessOnFixture(t, spec, first.fixture, t.TempDir())
	secondResults := make(map[string]any)
	result, err := second.execute(t.Context())
	if err != nil || !result.TMCommitted || result.Publication.State != publication.Published {
		t.Fatalf("TM-hit run result=%+v error=%v", result, err)
	}
	if extra := len(first.fixture.backend.calls) - baseCalls; extra != 0 {
		t.Fatalf("TM-hit run generated %d unit(s), want 0", extra)
	}
	if second.adapterExports() != 1 || second.adapterObserves() != 1 {
		t.Fatalf("TM-hit Export/Observe calls = %d/%d, want 1/1", second.adapterExports(), second.adapterObserves())
	}
	if second.adapterImports() != 1 {
		t.Fatalf("TM-hit Import calls = %d, want 1", second.adapterImports())
	}
	snapshot, err := second.snapshot(repeatExpectation)
	if err != nil {
		t.Fatal(err)
	}
	if problems := pipelineHarnessJudge(repeatExpectation, snapshot); len(problems) != 0 {
		t.Fatalf("TM-hit observation rejected: %v", problems)
	}
	if !slices.Equal(snapshot.Rows, rows) {
		t.Fatal("TM-hit run changed an existing TM column")
	}
	if !bytes.Equal(second.outputBytes(t), output) {
		t.Fatal("TM-hit run changed the published artifact")
	}
	for _, boundary := range []string{
		pipelineBoundaryObserveBefore, pipelineBoundaryObserveAfter, pipelineBoundaryCompareFinalAfter,
		pipelineBoundaryTMCommitBefore, pipelineBoundaryTMCommitAfter,
		pipelineBoundaryRecordWriteBefore, pipelineBoundaryRecordWriteAfter,
		pipelineBoundaryRecordCloseBefore, pipelineBoundaryRecordCloseAfter,
		pipelineBoundaryRecordInstallBefore, pipelineBoundaryRecordInstallAfter,
		pipelineBoundaryPublishRenameBefore, pipelineBoundaryPublishRenameAfter,
	} {
		if snapshot.Reached[boundary] < 1 {
			t.Fatalf("TM-hit run did not repeat real %s (reached %d)", boundary, snapshot.Reached[boundary])
		}
	}
	secondResults["generate_delta"] = 0
	secondResults["exports"] = second.adapterExports()
	secondResults["observes"] = second.adapterObserves()
	pipelineFixtureSaveState(t, kind+"-repeat", repeatExpectation, snapshot, snapshot.Rows)
	pipelineFixtureSaveJSON(t, kind+"-repeat-summary.json", secondResults)
	t.Logf("kind=%s TM hit: Generate delta=0, Export/Observe/CompareFinal repeated, D1=%d unchanged, bytes=%d", kind, len(rows), len(output))
}

func testPipelineFixtureInvalidCandidate(t *testing.T, kind string) {
	mode := pipelineFixtureModes()[2]
	spec := pipelineFixtureSpec(kind, mode)
	first := newPipelineHarness(t, spec, false)
	expectation := pipelineHarnessExpectationForFixture(t, spec, first.fixture)
	if _, err := first.execute(t.Context()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	target := first.fixture.input.units[0]
	if target.source == "" || target.translation == "" {
		t.Fatalf("first authored unit is not a non-empty candidate: %+v", target)
	}
	row := pipelineFixtureExactRow(t, first.fixture, target.stableID, target.translation)
	if _, err := first.fixture.db.Exec("UPDATE tm SET translated_text='' WHERE key_schema=? AND source_identity=? AND translation_profile=?",
		row.Schema, mustHexBytes(t, row.Source), mustHexBytes(t, row.Profile)); err != nil {
		t.Fatal(err)
	}
	corrupt := pipelineFixtureReadRows(t, first.fixture.db)
	corruptExpected := slices.Clone(expectation.D1)
	for i := range corruptExpected {
		if corruptExpected[i].Schema == row.Schema && corruptExpected[i].Source == row.Source && corruptExpected[i].Profile == row.Profile {
			corruptExpected[i].Text = ""
		}
	}
	if diff := pipelineFixtureDiff(corruptExpected, corrupt); len(diff) != 0 {
		t.Fatalf("corruption did not change exactly the authored candidate: %v", diff)
	}
	baseCalls := len(first.fixture.backend.calls)
	corruptExpectation := pipelineFixtureRepeatExpectation(t, spec, first.fixture, 1, corrupt)

	// When: the same profile revalidates the empty candidate in the current
	// session and regenerates only that unit.
	second := pipelineHarnessOnFixture(t, spec, first.fixture, t.TempDir())
	result, err := second.execute(t.Context())
	if err != nil || !result.TMCommitted || result.Publication.State != publication.Published {
		t.Fatalf("revalidation run result=%+v error=%v", result, err)
	}
	generated := slices.Clone(first.fixture.backend.calls[baseCalls:])
	if !slices.Equal(generated, []string{target.source}) {
		t.Fatalf("revalidation generated %d request(s), want only the corrupted unit", len(generated))
	}
	if second.adapterImports() != 1 {
		t.Fatalf("revalidation Import calls = %d, want 1", second.adapterImports())
	}
	snapshot, err := second.snapshot(corruptExpectation)
	if err != nil {
		t.Fatal(err)
	}
	if problems := pipelineHarnessJudge(corruptExpectation, snapshot); len(problems) != 0 {
		t.Fatalf("repaired observation rejected: %v", problems)
	}
	if diff := pipelineFixtureDiff(expectation.D1, snapshot.Rows); len(diff) != 0 {
		t.Fatalf("corrupted row was not repaired to the authored D1: %v", diff)
	}
	pipelineFixtureSaveJSON(t, kind+"-invalid-candidate-before.json", corrupt)
	pipelineFixtureSaveState(t, kind+"-invalid-candidate", corruptExpectation, snapshot, snapshot.Rows)
	t.Logf("kind=%s invalid candidate regenerated exactly %q and repaired D1=%d", kind, target.source, len(snapshot.Rows))
}

func mustHexBytes(t *testing.T, value string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testPipelineFixtureCreateRerun(t *testing.T) {
	spec := pipelineFixtureSpec("text", pipelineFixtureModes()[0])
	h := newPipelineHarness(t, spec, false)
	if _, err := h.execute(t.Context()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	rows := pipelineFixtureReadRows(t, h.fixture.db)
	output := h.outputBytes(t)
	baseCalls := len(h.fixture.backend.calls)

	// When: the same target is run again in Create mode.
	result, err := h.fixture.core.FileWithTM(t.Context(), h.options, h.fixture.adapter, h.fixture.profile, h.fixture.store, h.fixture.scope)
	// Then: the existing output is refused before any generation or state change.
	var failure *Error
	if !errors.Is(err, os.ErrExist) || !errors.As(err, &failure) || failure.Phase != "prepare" ||
		result.TMCommitted || result.Publication.State != publication.NotPublished {
		t.Fatalf("Create rerun result=%+v error=%v", result, err)
	}
	if extra := len(h.fixture.backend.calls) - baseCalls; extra != 0 {
		t.Fatalf("refused Create rerun generated %d unit(s)", extra)
	}
	if current := pipelineFixtureReadRows(t, h.fixture.db); !slices.Equal(current, rows) {
		t.Fatal("refused Create rerun changed TM rows")
	}
	if !bytes.Equal(h.outputBytes(t), output) {
		t.Fatal("refused Create rerun changed the existing output")
	}
	pipelineFixtureSaveJSON(t, "create-rerun.json", map[string]any{
		"phase": failure.Phase, "error": err.Error(), "generate_delta": 0, "d1_rows": len(rows),
	})
	t.Logf("Create rerun refused existing output at phase=%s, Generate delta=0, D1=%d", failure.Phase, len(rows))
}

func testPipelineFixtureReplaceRerun(t *testing.T) {
	spec := pipelineFixtureSpec("text", pipelineFixtureModes()[2])
	h := newPipelineHarness(t, spec, false)
	expectation := pipelineHarnessExpectationForFixture(t, spec, h.fixture)
	if _, err := h.execute(t.Context()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	rows := pipelineFixtureReadRows(t, h.fixture.db)
	baseCalls := len(h.fixture.backend.calls)

	result, err := h.fixture.core.FileWithTM(t.Context(), h.options, h.fixture.adapter, h.fixture.profile, h.fixture.store, h.fixture.scope)
	if err != nil || !result.TMCommitted || result.Publication.State != publication.Published {
		t.Fatalf("Replace rerun result=%+v error=%v", result, err)
	}
	if extra := len(h.fixture.backend.calls) - baseCalls; extra != 0 {
		t.Fatalf("Replace rerun generated %d unit(s), want 0", extra)
	}
	if current := pipelineFixtureReadRows(t, h.fixture.db); !slices.Equal(current, rows) {
		t.Fatal("Replace rerun changed TM rows")
	}
	if !bytes.Equal(h.outputBytes(t), expectation.TargetBytes) {
		t.Fatal("Replace rerun output differs from the authored target")
	}
	pipelineFixtureSaveJSON(t, "replace-rerun.json", map[string]any{
		"published": true, "generate_delta": 0, "d1_rows": len(rows), "output_sha_len": len(h.outputBytes(t)),
	})
	t.Logf("Replace rerun published with Generate delta=0 and D1=%d unchanged", len(rows))
}

func testPipelineFixtureTextInPlaceRerun(t *testing.T) {
	spec := pipelineFixtureSpec("text", pipelineFixtureModes()[3])
	h := newPipelineHarness(t, spec, false)
	firstExpectation := pipelineHarnessExpectationForFixture(t, spec, h.fixture)
	first, err := h.execute(t.Context())
	if err != nil || first.Publication.State != publication.Published {
		t.Fatalf("first InPlace result=%+v error=%v", first, err)
	}
	firstBackup := first.Publication.BackupPath
	if raw, err := os.ReadFile(firstBackup); err != nil || !bytes.Equal(raw, firstExpectation.SourceBytes) {
		t.Fatalf("first backup=%q error=%v", raw, err)
	}
	firstRows := pipelineFixtureReadRows(t, h.fixture.db)
	if diff := pipelineFixtureDiff(firstExpectation.D1, firstRows); len(diff) != 0 {
		t.Fatalf("first InPlace rows differ from the authored D1: %v", diff)
	}
	baseCalls := len(h.fixture.backend.calls)

	// The text unit's new source is the previously translated bytes, so the
	// controlled engine needs an authored response for that exact request.
	secondTranslation := "再訳文。\n"
	h.fixture.backend.texts[string(firstExpectation.TargetBytes)] = secondTranslation
	second, err := h.fixture.core.FileWithTM(t.Context(), h.options, h.fixture.adapter, h.fixture.profile, h.fixture.store, h.fixture.scope)
	if err != nil || !second.TMCommitted || second.Publication.State != publication.Published {
		t.Fatalf("second InPlace result=%+v error=%v", second, err)
	}
	if extra := len(h.fixture.backend.calls) - baseCalls; extra != 1 {
		t.Fatalf("second InPlace generated %d unit(s), want the changed source only", extra)
	}
	if second.Publication.BackupPath == firstBackup || second.Publication.BackupPath == "" {
		t.Fatalf("second backup path %q is not a new retained backup", second.Publication.BackupPath)
	}
	if raw, err := os.ReadFile(second.Publication.BackupPath); err != nil || !bytes.Equal(raw, firstExpectation.TargetBytes) {
		t.Fatalf("second backup=%q error=%v", raw, err)
	}
	if raw, err := os.ReadFile(second.Publication.BackupPath); err == nil && bytes.Equal(raw, firstExpectation.SourceBytes) {
		t.Fatal("second backup retained the original source instead of the first translation")
	}
	sourceNow, err := os.ReadFile(h.options.Source)
	if err != nil || string(sourceNow) != secondTranslation {
		t.Fatalf("translated source=%q error=%v", sourceNow, err)
	}
	// The TM key is the source fed to this run, which is the first translated
	// artifact; the second translation is the row value, not the source.
	secondSession, err := h.fixture.adapter.Import(firstExpectation.TargetBytes, h.fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	secondSource, err := unit.NewSourceIdentity(secondSession, secondSession.Units()[0].ID(), h.fixture.scope.GameNamespace, h.fixture.scope.ArtifactNamespace, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := h.fixture.profile.ExactKey(secondSource)
	if err != nil {
		t.Fatal(err)
	}
	firstRow := pipelineFixtureExactRow(t, h.fixture, "document", string(firstExpectation.TargetBytes))
	if hex.EncodeToString(secondKey.SourceBytes()) == firstRow.Source {
		t.Fatal("the translated source identity did not change")
	}
	secondRow := pipelineFixtureRow{secondKey.Schema(), hex.EncodeToString(secondKey.SourceBytes()), hex.EncodeToString(secondKey.ProfileBytes()), secondTranslation}
	wantRows := pipelineFixtureSortRows(append(slices.Clone(firstExpectation.D1), secondRow))
	actualRows := pipelineFixtureReadRows(t, h.fixture.db)
	pipelineFixtureSaveJSON(t, "text-in-place-second-rows.json", actualRows)
	pipelineFixtureSaveJSON(t, "text-in-place-want-rows.json", wantRows)
	if diff := pipelineFixtureDiff(wantRows, actualRows); len(diff) != 0 {
		t.Fatalf("second InPlace rows differ from D1 plus the new translated-source row: %v", diff)
	}
	if !slices.Contains(actualRows, firstRow) {
		t.Fatal("second InPlace lost the first translated row")
	}
	var record pipelineHarnessRecord
	raw, err := os.ReadFile(second.Publication.RecordPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.Phase != "published" || record.Backup == nil || record.Backup.Path != second.Publication.BackupPath {
		t.Fatalf("second record phase/backup = %q/%+v", record.Phase, record.Backup)
	}
	pipelineFixtureSaveJSON(t, "text-in-place-second.json", map[string]any{
		"first_backup": firstBackup, "second_backup": second.Publication.BackupPath,
		"generate_delta": 1, "d1_rows": len(actualRows), "source_language_changed": true,
	})
	t.Logf("text InPlace rerun: Generate delta=1, source identity changed, retained backups=%s/%s, D1=%d", firstBackup, second.Publication.BackupPath, len(actualRows))
}

func testPipelineFixtureW3InPlaceRerun(t *testing.T) {
	spec := pipelineFixtureSpec("w3", pipelineFixtureModes()[3])
	h := newPipelineHarness(t, spec, false)
	firstExpectation := pipelineHarnessExpectationForFixture(t, spec, h.fixture)
	first, err := h.execute(t.Context())
	if err != nil || first.Publication.State != publication.Published {
		t.Fatalf("first W3 InPlace result=%+v error=%v", first, err)
	}
	firstBackup := first.Publication.BackupPath
	firstRows := pipelineFixtureReadRows(t, h.fixture.db)
	if raw, err := os.ReadFile(firstBackup); err != nil || !bytes.Equal(raw, firstExpectation.SourceBytes) {
		t.Fatalf("first W3 backup=%q error=%v", raw, err)
	}
	baseCalls := len(h.fixture.backend.calls)

	// When: the same target is now a ja artifact but the fixture adapter is
	// still constructed for en. Import must reject before any TM lookup.
	second, err := h.fixture.core.FileWithTM(t.Context(), h.options, h.fixture.adapter, h.fixture.profile, h.fixture.store, h.fixture.scope)
	var failure *Error
	if err == nil || !errors.As(err, &failure) || failure.Phase != "import" ||
		failure.Err == nil || !strings.Contains(failure.Err.Error(), "source language does not match header") ||
		second.TMCommitted || second.Publication.State != publication.NotPublished {
		t.Fatalf("W3 ja-header rerun result=%+v error=%v", second, err)
	}
	refusal := err.Error()
	if extra := len(h.fixture.backend.calls) - baseCalls; extra != 0 {
		t.Fatalf("rejected W3 rerun generated %d unit(s)", extra)
	}
	if current := pipelineFixtureReadRows(t, h.fixture.db); !slices.Equal(current, firstRows) {
		t.Fatal("rejected W3 rerun changed D1")
	}
	sourceNow, err := os.ReadFile(h.options.Source)
	if err != nil || !bytes.Equal(sourceNow, firstExpectation.TargetBytes) {
		t.Fatalf("rejected W3 rerun changed the translated source: %v", err)
	}
	if raw, err := os.ReadFile(firstBackup); err != nil || !bytes.Equal(raw, firstExpectation.SourceBytes) {
		t.Fatalf("rejected W3 rerun changed the first backup: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(filepath.Dir(h.options.Source), ".yakuori-backup-*"))
	if err != nil || !slices.Equal(backups, []string{firstBackup}) {
		t.Fatalf("W3 rerun backups=%v error=%v, want only the first backup", backups, err)
	}
	pipelineFixtureSaveJSON(t, "w3-in-place-ja-header.json", map[string]any{
		"phase": failure.Phase, "error": refusal, "generate_delta": 0, "d1_rows": len(firstRows),
		"first_backup": firstBackup, "translated_source_bytes": len(sourceNow),
	})
	t.Logf("W3 ja-header rerun rejected at phase=%s before Generate; D1=%d and source/backup unchanged", failure.Phase, len(firstRows))
}

func testPipelineFixtureStdout(t *testing.T) {
	spec := pipelineHarnessCase{name: "stdout/text", kind: "text", surface: "stdout", mode: publication.Replace}
	h := newPipelineHarness(t, spec, true)
	expectation := pipelineHarnessExpectationForFixture(t, spec, h.fixture)
	result, err := h.execute(t.Context())
	if err != nil || !result.TMCommitted || result.BytesWritten != len(expectation.TargetBytes) {
		t.Fatalf("TextWithTM result=%+v error=%v", result, err)
	}
	pipelineFixtureAssertCalls(t, h.fixture, pipelineFixtureAuthoredCalls(h.fixture.input))
	if h.adapterExports() != 1 || h.adapterObserves() != 1 {
		t.Fatalf("stdout Export/Observe calls = %d/%d", h.adapterExports(), h.adapterObserves())
	}
	if h.adapterImports() != 1 {
		t.Fatalf("stdout Import calls = %d, want 1", h.adapterImports())
	}
	snapshot, err := h.snapshot(expectation)
	if err != nil {
		t.Fatal(err)
	}
	if problems := pipelineHarnessJudge(expectation, snapshot); len(problems) != 0 {
		t.Fatalf("stdout observation rejected: %v", problems)
	}
	if !bytes.Equal(snapshot.Files["stdout"].Bytes, expectation.TargetBytes) {
		t.Fatal("stdout bytes differ from the authored target")
	}
	for _, label := range []string{"output", "stage", "record", "backup"} {
		if snapshot.Files[label].Exists {
			t.Fatalf("stdout surface created publication file %s", label)
		}
	}
	pipelineFixtureSaveState(t, "stdout-text", expectation, snapshot, snapshot.Rows)
	pipelineHarnessSave(t, "stdout-text.bin", snapshot.Files["stdout"].Bytes)
	t.Logf("stdout text: D1=%d bytes=%d, no publication file, TMCommitted", len(snapshot.Rows), len(snapshot.Files["stdout"].Bytes))
}

func testPipelineFixtureStdoutRepeat(t *testing.T) {
	spec := pipelineHarnessCase{name: "stdout/text", kind: "text", surface: "stdout", mode: publication.Replace}
	first := newPipelineHarness(t, spec, true)
	expectation := pipelineHarnessExpectationForFixture(t, spec, first.fixture)
	if _, err := first.execute(t.Context()); err != nil {
		t.Fatalf("first stdout run: %v", err)
	}
	rows := pipelineFixtureReadRows(t, first.fixture.db)
	baseCalls := len(first.fixture.backend.calls)
	var out bytes.Buffer
	result, err := first.fixture.core.TextWithTM(t.Context(), first.fixture.input.source, first.fixture.adapter,
		first.fixture.profile, first.fixture.store, first.fixture.scope, &out)
	if err != nil || !result.TMCommitted {
		t.Fatalf("stdout repeat result=%+v error=%v", result, err)
	}
	if extra := len(first.fixture.backend.calls) - baseCalls; extra != 0 {
		t.Fatalf("stdout repeat generated %d unit(s), want 0", extra)
	}
	if !bytes.Equal(out.Bytes(), expectation.TargetBytes) {
		t.Fatal("stdout repeat bytes differ from the authored target")
	}
	if current := pipelineFixtureReadRows(t, first.fixture.db); !slices.Equal(current, rows) {
		t.Fatal("stdout repeat changed D1")
	}
	pipelineFixtureSaveJSON(t, "stdout-repeat.json", map[string]any{
		"generate_delta": 0, "d1_rows": len(rows), "bytes": out.Len(),
	})
	t.Logf("stdout repeat: Generate delta=0, D1=%d unchanged, bytes=%d", len(rows), out.Len())
}
