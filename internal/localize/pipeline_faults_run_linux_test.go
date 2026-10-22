//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
	"golang.org/x/sys/unix"
)

// Fixed harness boundary names from the Issue68 plan. A name is marked only
// after the real operation it names has completed: a callback returning is not
// evidence for the operation behind it. Boundaries whose completion happens
// after the last callback (stage/record descriptor close) are confirmed at the
// next observable real operation by checking the descriptor reports os.ErrClosed.
const (
	pipelineBoundaryGenerateBefore        = "generate.before"
	pipelineBoundaryGenerateAfter         = "generate.after"
	pipelineBoundaryStageWriteBefore      = "stage.write.before"
	pipelineBoundaryStageWriteAfter       = "stage.write.after"
	pipelineBoundaryStageSyncBefore       = "stage.sync.before"
	pipelineBoundaryStageSyncAfter        = "stage.sync.after"
	pipelineBoundaryStageCloseBefore      = "stage.close.before"
	pipelineBoundaryStageCloseAfter       = "stage.close.after"
	pipelineBoundaryObserveBefore         = "observe.before"
	pipelineBoundaryObserveAfter          = "observe.after"
	pipelineBoundaryCompareFinalAfter     = "compare-final.after"
	pipelineBoundaryTMCommitBefore        = "tm-commit.before"
	pipelineBoundaryTMCommitAfter         = "tm-commit.after"
	pipelineBoundaryRecordWriteBefore     = "record.write.before"
	pipelineBoundaryRecordWriteAfter      = "record.write.after"
	pipelineBoundaryRecordSyncBefore      = "record.sync.before"
	pipelineBoundaryRecordSyncAfter       = "record.sync.after"
	pipelineBoundaryRecordCloseBefore     = "record.close.before"
	pipelineBoundaryRecordCloseAfter      = "record.close.after"
	pipelineBoundaryRecordInstallBefore   = "record.install.before"
	pipelineBoundaryRecordInstallAfter    = "record.install.after"
	pipelineBoundaryBackupRenameBefore    = "backup-rename.before"
	pipelineBoundaryBackupRenameAfter     = "backup-rename.after"
	pipelineBoundaryPublishRenameBefore   = "publish-rename.before"
	pipelineBoundaryPublishRenameAfter    = "publish-rename.after"
	pipelineBoundaryStdoutWriteBefore     = "stdout.write.before"
	pipelineBoundaryStdoutWriteAfter      = "stdout.write.after"
	pipelineBoundaryDiagnosticWriteBefore = "diagnostic.write.before"
	pipelineBoundaryDiagnosticWriteAfter  = "diagnostic.write.after"
	pipelineBoundaryRunCloseAfter         = "run.close.after"
)

// pipelineHarnessOldOutput is authored here rather than derived from the
// pipeline, so the pre-run output (F0) has its own expected bytes.
var pipelineHarnessOldOutput = []byte("old separate output\n")

type pipelineHarnessTrace struct {
	order   []string
	reached map[string]int
}

func newPipelineHarnessTrace() *pipelineHarnessTrace {
	return &pipelineHarnessTrace{reached: map[string]int{}}
}

func (t *pipelineHarnessTrace) mark(name string) {
	if t == nil {
		return
	}
	t.reached[name]++
	t.order = append(t.order, name)
}

// pipelineHarnessAdapter observes Export/Observe on the fixture's concrete
// adapter. It forwards Import unchanged and deliberately has no KeyRelations
// method: wrapping the text adapter must not turn it into a key provider.
type pipelineHarnessAdapter struct {
	Adapter
	trace    *pipelineHarnessTrace
	imports  int
	exports  int
	observes int
}

func (a *pipelineHarnessAdapter) Import(raw []byte, profile validate.Profile) (unit.Session, error) {
	a.imports++
	return a.Adapter.Import(raw, profile)
}

func (a *pipelineHarnessAdapter) Export(w io.Writer, session unit.Session, profile validate.Profile, accepted []validate.AcceptedTranslation) (artifact.Manifest, error) {
	a.exports++
	return a.Adapter.Export(w, session, profile, accepted)
}

func (a *pipelineHarnessAdapter) Observe(reader io.Reader) (artifact.FinalObservation, error) {
	a.observes++
	a.trace.mark(pipelineBoundaryObserveBefore)
	observed, err := a.Adapter.Observe(reader)
	if err == nil {
		a.trace.mark(pipelineBoundaryObserveAfter)
	}
	return observed, err
}

// pipelineHarnessW3Adapter keeps the concrete W3 Import and KeyRelations
// methods (including their same-import check) and wraps only Export/Observe.
type pipelineHarnessW3Adapter struct {
	*w3strings.Adapter
	trace    *pipelineHarnessTrace
	imports  int
	exports  int
	observes int
}

func (a *pipelineHarnessW3Adapter) Import(raw []byte, profile validate.Profile) (unit.Session, error) {
	a.imports++
	return a.Adapter.Import(raw, profile)
}

func (a *pipelineHarnessW3Adapter) Export(w io.Writer, session unit.Session, profile validate.Profile, accepted []validate.AcceptedTranslation) (artifact.Manifest, error) {
	a.exports++
	return a.Adapter.Export(w, session, profile, accepted)
}

func (a *pipelineHarnessW3Adapter) Observe(reader io.Reader) (artifact.FinalObservation, error) {
	a.observes++
	a.trace.mark(pipelineBoundaryObserveBefore)
	observed, err := a.Adapter.Observe(reader)
	if err == nil {
		a.trace.mark(pipelineBoundaryObserveAfter)
	}
	return observed, err
}

// pipelineHarnessBackend observes generation on the fixture's same Engine
// pointer, so its call history remains the fixture's history.
type pipelineHarnessBackend struct {
	*pipelineBackend
	trace *pipelineHarnessTrace
}

func (b *pipelineHarnessBackend) Generate(ctx context.Context, request inference.GenerationRequest) (inference.GenerationResult, error) {
	b.trace.mark(pipelineBoundaryGenerateBefore)
	result, err := b.pipelineBackend.Generate(ctx, request)
	if err == nil {
		b.trace.mark(pipelineBoundaryGenerateAfter)
	}
	return result, err
}

// pipelineHarnessRun observes the prepared publication Run. Stage's success is
// the real CompareFinal success; the afterStage callback runs immediately
// before Core starts the TM transaction, and publishEntry runs at the Publish
// entry, which Core reaches only after a successful commit.
type pipelineHarnessRun struct {
	publicationRun
	trace        *pipelineHarnessTrace
	afterStage   func() error
	publishEntry func() error
}

func (r *pipelineHarnessRun) Stage(ctx context.Context, produce func(io.Writer) error, check func(io.Reader) error) error {
	err := r.publicationRun.Stage(ctx, func(writer io.Writer) error {
		return produce(&pipelineHarnessStageWriter{target: writer, trace: r.trace})
	}, check)
	if err != nil {
		return err
	}
	r.trace.mark(pipelineBoundaryCompareFinalAfter)
	r.trace.mark(pipelineBoundaryTMCommitBefore)
	if r.afterStage != nil {
		return r.afterStage()
	}
	return nil
}

// pipelineHarnessStageWriter wraps the real writer that Stage hands to
// Adapter.Export. Each actual Write is one occurrence, so a producer that
// writes zero or two times cannot be reported as one completed export.
type pipelineHarnessStageWriter struct {
	target io.Writer
	trace  *pipelineHarnessTrace
}

func (w *pipelineHarnessStageWriter) Write(raw []byte) (int, error) {
	w.trace.mark(pipelineBoundaryStageWriteBefore)
	n, err := w.target.Write(raw)
	if err == nil && n == len(raw) {
		w.trace.mark(pipelineBoundaryStageWriteAfter)
	}
	return n, err
}

func (r *pipelineHarnessRun) Publish(ctx context.Context) (publication.Result, error) {
	r.trace.mark(pipelineBoundaryTMCommitAfter)
	if r.publishEntry != nil {
		if err := r.publishEntry(); err != nil {
			return publication.Result{State: publication.NotPublished}, err
		}
	}
	return r.publicationRun.Publish(ctx)
}

func (r *pipelineHarnessRun) Close() error {
	err := r.publicationRun.Close()
	r.trace.mark(pipelineBoundaryRunCloseAfter)
	return err
}

// pipelineHarnessWriter observes the stdout surface. Core calls it once, after
// the real TM commit, so both the commit checkpoint and the write are real.
type pipelineHarnessWriter struct {
	target io.Writer
	trace  *pipelineHarnessTrace
	entry  func() error
}

func (w *pipelineHarnessWriter) Write(raw []byte) (int, error) {
	w.trace.mark(pipelineBoundaryTMCommitAfter)
	w.trace.mark(pipelineBoundaryStdoutWriteBefore)
	if w.entry != nil {
		if err := w.entry(); err != nil {
			return 0, err
		}
	}
	n, err := w.target.Write(raw)
	if err == nil {
		w.trace.mark(pipelineBoundaryStdoutWriteAfter)
	}
	return n, err
}

type pipelineHarnessRecordEvent struct {
	Phase      string `json:"phase"`
	Operation  string `json:"operation"`
	Occurrence int    `json:"occurrence"`
}

// pipelineHarnessOperations installs the per-Run operation bridge from task 2
// and records record write/sync/close/install and rename boundaries. The close
// hooks return nil because the bridge performs the mandatory real Close.
type pipelineHarnessOperations struct {
	trace          *pipelineHarnessTrace
	t              *testing.T
	source         string
	paths          publication.PipelineFaultPaths
	stageFD        *os.File
	recordFDs      []*os.File
	stageFDClosed  bool
	recordFDClosed bool
	lastPhase      string
	counts         map[string]int
	events         []pipelineHarnessRecordEvent
}

func newPipelineHarnessOperations(t *testing.T, trace *pipelineHarnessTrace, source string) *pipelineHarnessOperations {
	t.Helper()
	return &pipelineHarnessOperations{trace: trace, t: t, source: source, counts: map[string]int{}}
}

func (o *pipelineHarnessOperations) phase(name string) string {
	return strings.TrimPrefix(name, ".record-")
}

func (o *pipelineHarnessOperations) event(phase, operation string) {
	key := phase + "/" + operation
	o.counts[key]++
	o.events = append(o.events, pipelineHarnessRecordEvent{Phase: phase, Operation: operation, Occurrence: o.counts[key]})
}

func (o *pipelineHarnessOperations) hooks() publication.PipelineFaultHooks {
	return publication.PipelineFaultHooks{
		SyncStage: func(f *os.File) error {
			o.trace.mark(pipelineBoundaryStageSyncBefore)
			err := f.Sync()
			if err == nil {
				o.trace.mark(pipelineBoundaryStageSyncAfter)
			}
			return err
		},
		CloseStage: func(f *os.File) error {
			o.stageFD = f
			o.trace.mark(pipelineBoundaryStageCloseBefore)
			return nil // The bridge performs the mandatory real Close.
		},
		RecordWrite: func(f *os.File, raw []byte) (int, error) {
			o.lastPhase = o.phase(f.Name())
			o.trace.mark(pipelineBoundaryRecordWriteBefore)
			n, err := f.Write(raw)
			if err == nil && n == len(raw) {
				o.trace.mark(pipelineBoundaryRecordWriteAfter)
			}
			o.event(o.lastPhase, "write")
			return n, err
		},
		RecordSync: func(f *os.File) error {
			stat, statErr := f.Stat()
			if statErr != nil {
				return statErr
			}
			phase, operation := o.phase(f.Name()), "sync"
			if stat.IsDir() {
				phase, operation = o.lastPhase, "directory-sync"
			}
			o.trace.mark(pipelineBoundaryRecordSyncBefore)
			err := f.Sync()
			if err == nil {
				o.trace.mark(pipelineBoundaryRecordSyncAfter)
			}
			o.event(phase, operation)
			return err
		},
		RecordClose: func(f *os.File) error {
			o.recordFDs = append(o.recordFDs, f)
			o.trace.mark(pipelineBoundaryRecordCloseBefore)
			o.event(o.phase(f.Name()), "close")
			return nil // The bridge performs the mandatory real Close.
		},
		Rename: func(fromFD int, from string, toFD int, to string, flags uint) error {
			if strings.HasPrefix(from, ".record-") {
				phase := o.phase(from)
				if len(o.recordFDs) == 0 {
					o.t.Fatalf("record install %q has no recorded close", from)
				}
				closed := o.recordFDs[len(o.recordFDs)-1]
				if _, err := closed.Stat(); !errors.Is(err, os.ErrClosed) {
					o.t.Fatalf("record %s Close-after checkpoint still open: %v", phase, err)
				}
				o.recordFDClosed = true
				o.trace.mark(pipelineBoundaryRecordCloseAfter)
				o.trace.mark(pipelineBoundaryRecordInstallBefore)
				o.event(phase, "install-before")
				err := unix.Renameat2(fromFD, from, toFD, to, flags)
				if err == nil {
					o.trace.mark(pipelineBoundaryRecordInstallAfter)
					o.event(phase, "install")
				}
				return err
			}
			if from == filepath.Base(o.source) || strings.HasPrefix(from, ".yakuori-backup-") {
				o.trace.mark(pipelineBoundaryBackupRenameBefore)
				err := unix.Renameat2(fromFD, from, toFD, to, flags)
				if err == nil {
					o.trace.mark(pipelineBoundaryBackupRenameAfter)
				}
				return err
			}
			if from != "stage" {
				o.t.Fatalf("unexpected publication rename %q -> %q", from, to)
				return unix.EIO
			}
			if o.stageFD == nil {
				o.t.Fatal("publish rename has no staged descriptor")
			}
			if _, err := o.stageFD.Stat(); !errors.Is(err, os.ErrClosed) {
				o.t.Fatalf("stage Close-after checkpoint still open: %v", err)
			}
			o.stageFDClosed = true
			o.trace.mark(pipelineBoundaryStageCloseAfter)
			o.stageFD = nil
			o.trace.mark(pipelineBoundaryPublishRenameBefore)
			err := unix.Renameat2(fromFD, from, toFD, to, flags)
			if err == nil {
				o.trace.mark(pipelineBoundaryPublishRenameAfter)
			}
			return err // A successful real rename must never be reported as failure.
		},
	}
}

type pipelineHarness struct {
	t          *testing.T
	fixture    *pipelineFixture
	kind       string
	surface    string
	mode       publication.Mode
	wrapped    bool
	trace      *pipelineHarnessTrace
	adapter    Adapter
	core       Core
	options    publication.Options
	ops        *pipelineHarnessOperations
	writer     *pipelineHarnessWriter
	stdoutPath string
	sourcePath string
	memory     *tmExecution
	result     Result
	runErr     error

	// A fault cell requests one boundary, occurrence and stimulus. The fields
	// are copied into the observation so evidence records what was asked for
	// and which fault field was actually injected, separately from expectations.
	requestedBoundary   string
	requestedOccurrence int
	stimulus            string
	injected            []string
	afterStageHook      func() error

	rowsAfterStage []pipelineFixtureRow
	rowsAtCommit   []pipelineFixtureRow

	// beforeOutput is captured from the real filesystem before the pipeline
	// starts. The record's existing-output identity is judged against this
	// physical pre-run capture, not against authored bytes alone.
	beforeOutput pipelineHarnessFile
}

func newPipelineHarness(t *testing.T, spec pipelineHarnessCase, wrapped bool) *pipelineHarness {
	t.Helper()
	input, ok := pipelineHarnessInput(t, spec.kind)
	if !ok {
		t.Fatalf("no fixture input for kind %q", spec.kind)
	}
	// Given: a fresh real fixture, SQLite store and unrelated D0 row per harness.
	f := newPipelineFixture(t, input)
	h := &pipelineHarness{t: t, fixture: f, kind: spec.kind, surface: spec.surface, mode: spec.mode, wrapped: wrapped, stimulus: "none"}
	if wrapped {
		h.trace = newPipelineHarnessTrace()
	}
	if adapter, ok := f.adapter.(*w3strings.Adapter); ok {
		if wrapped {
			h.adapter = &pipelineHarnessW3Adapter{Adapter: adapter, trace: h.trace}
		} else {
			h.adapter = adapter
		}
	} else if wrapped {
		h.adapter = &pipelineHarnessAdapter{Adapter: f.adapter, trace: h.trace}
	} else {
		h.adapter = f.adapter
	}
	if wrapped {
		backend := &pipelineHarnessBackend{pipelineBackend: f.backend, trace: h.trace}
		info := f.backend.info
		core, err := NewCoreWithLimits(NewInferenceEngine(backend, func(_ context.Context, _ unit.UnitID, text string) (inference.GenerationRequest, error) {
			return pipelineRequest(info, text)
		}), f.limits)
		if err != nil {
			t.Fatal(err)
		}
		h.core = core
	} else {
		h.core = f.core
	}
	parent := t.TempDir()
	h.sourcePath = filepath.Join(parent, "source")
	if err := os.WriteFile(h.sourcePath, input.source, 0600); err != nil {
		t.Fatal(err)
	}
	if spec.surface == "file" {
		h.options = publication.Options{Source: h.sourcePath, Output: filepath.Join(parent, "output"), Mode: spec.mode}
		if spec.mode == publication.InPlace {
			h.options.Output = h.options.Source
		}
		if spec.mode == publication.Replace && spec.outputPresent {
			if err := os.WriteFile(h.options.Output, pipelineHarnessOldOutput, 0600); err != nil {
				t.Fatal(err)
			}
		}
		captured, err := pipelineHarnessReadFile(h.options.Output)
		if err != nil {
			t.Fatal(err)
		}
		h.beforeOutput = captured
		h.ops = newPipelineHarnessOperations(t, h.trace, h.options.Source)
	} else {
		h.stdoutPath = filepath.Join(parent, "stdout.bin")
	}
	return h
}

func (h *pipelineHarness) execute(ctx context.Context) (Result, error) {
	h.t.Helper()
	switch {
	case h.surface == "file" && h.wrapped:
		h.memory = &tmExecution{store: h.fixture.store, scope: h.fixture.scope}
		h.result, h.runErr = h.core.fileWithTM(ctx, h.options, h.adapter, h.fixture.profile, nil, h.memory, h.prepare)
	case h.surface == "file":
		h.result, h.runErr = h.core.FileWithTM(ctx, h.options, h.adapter, h.fixture.profile, h.fixture.store, h.fixture.scope)
	default:
		out, err := os.OpenFile(h.stdoutPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			h.t.Fatal(err)
		}
		var writer io.Writer = out
		h.memory = &tmExecution{store: h.fixture.store, scope: h.fixture.scope}
		if h.wrapped {
			h.writer = &pipelineHarnessWriter{target: out, trace: h.trace, entry: h.commitEntry}
			writer = h.writer
			h.result, h.runErr = h.core.text(ctx, h.fixture.input.source, h.adapter, h.fixture.profile, nil, h.memory, writer)
		} else {
			h.result, h.runErr = h.core.TextWithTM(ctx, h.fixture.input.source, h.adapter, h.fixture.profile, h.fixture.store, h.fixture.scope, writer)
		}
		if closeErr := out.Close(); closeErr != nil {
			h.t.Fatalf("close stdout surface: %v", closeErr)
		}
	}
	return h.result, h.runErr
}

// executeStageCloseFault runs the real file Core with an injected stage close
// error. The bridge still performs the mandatory real Close, so the observed
// descriptor state is physical.
func (h *pipelineHarness) executeStageCloseFault(ctx context.Context, fault error) (Result, error) {
	return h.executeWithHooks(ctx, "error", pipelineBoundaryStageCloseBefore, func() publication.PipelineFaultHooks {
		hooks := h.ops.hooks()
		stageClose := hooks.CloseStage
		hooks.CloseStage = func(f *os.File) error {
			if err := stageClose(f); err != nil {
				return err
			}
			h.injected = append(h.injected, "close-stage-eio")
			return fault
		}
		return hooks
	})
}

// executePrecommitCancel cancels the caller context at tm-commit.before after
// the real Stage returned nil, so the real final.commit has to reject it.
func (h *pipelineHarness) executePrecommitCancel(ctx context.Context) (Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	h.afterStageHook = func() error {
		h.rowsAfterStage = pipelineFixtureReadRows(h.t, h.fixture.db)
		cancel()
		h.injected = append(h.injected, "ctx-cancel")
		return nil
	}
	return h.executeWithHooks(ctx, "cancel", pipelineBoundaryTMCommitBefore, h.ops.hooks)
}

func (h *pipelineHarness) executeWithHooks(ctx context.Context, stimulus, boundary string, build func() publication.PipelineFaultHooks) (Result, error) {
	h.requestedBoundary, h.requestedOccurrence, h.stimulus = boundary, 1, stimulus
	h.memory = &tmExecution{store: h.fixture.store, scope: h.fixture.scope}
	prepare := func(ctx context.Context, options publication.Options) (publicationRun, error) {
		run, err := h.prepare(ctx, options)
		if err != nil {
			return nil, err
		}
		real := run.(*pipelineHarnessRun).publicationRun.(*publication.Run)
		real.SetPipelineFaultHooks(build())
		return run, nil
	}
	h.result, h.runErr = h.core.fileWithTM(ctx, h.options, h.adapter, h.fixture.profile, nil, h.memory, prepare)
	return h.result, h.runErr
}

func (h *pipelineHarness) prepare(ctx context.Context, options publication.Options) (publicationRun, error) {
	real, err := publication.PrepareWithLimits(ctx, options, h.fixture.limits)
	if err != nil {
		return nil, err
	}
	h.ops.paths = real.SetPipelineFaultHooks(h.ops.hooks())
	afterStage := h.afterStage
	if h.afterStageHook != nil {
		afterStage = h.afterStageHook
	}
	return &pipelineHarnessRun{publicationRun: real, trace: h.trace, afterStage: afterStage, publishEntry: h.commitEntry}, nil
}

// afterStage runs at tm-commit.before: Stage, including the real CompareFinal,
// returned nil and the TM transaction has not started. Returning nil lets the
// real final.commit proceed; a caller may cancel the context here instead.
func (h *pipelineHarness) afterStage() error {
	h.rowsAfterStage = pipelineFixtureReadRows(h.t, h.fixture.db)
	return nil
}

// commitEntry runs at tm-commit.after: Publish/Write entry, which Core reaches
// only after the real TM commit returned. Reading rows here observes commit
// success instead of inferring it from the returned result flag.
func (h *pipelineHarness) commitEntry() error {
	h.rowsAtCommit = pipelineFixtureReadRows(h.t, h.fixture.db)
	return nil
}

func (h *pipelineHarness) outputBytes(t *testing.T) []byte {
	t.Helper()
	path := h.options.Output
	if h.surface == "stdout" {
		path = h.stdoutPath
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (h *pipelineHarness) adapterExports() int {
	switch adapter := h.adapter.(type) {
	case *pipelineHarnessAdapter:
		return adapter.exports
	case *pipelineHarnessW3Adapter:
		return adapter.exports
	}
	return 0
}

func (h *pipelineHarness) adapterImports() int {
	switch adapter := h.adapter.(type) {
	case *pipelineHarnessAdapter:
		return adapter.imports
	case *pipelineHarnessW3Adapter:
		return adapter.imports
	}
	return 0
}

func (h *pipelineHarness) adapterObserves() int {
	switch adapter := h.adapter.(type) {
	case *pipelineHarnessAdapter:
		return adapter.observes
	case *pipelineHarnessW3Adapter:
		return adapter.observes
	}
	return 0
}

func TestPipelineHarnessBoundaries(t *testing.T) {
	t.Run("valid", testPipelineHarnessBoundariesValid)
	t.Run("reject-invalid-observation", testPipelineHarnessBoundariesReject)
}

func testPipelineHarnessBoundariesValid(t *testing.T) {
	for _, spec := range pipelineHarnessCases() {
		t.Run(spec.name, func(t *testing.T) {
			// Given: the scenario expectation is fixed from authored inputs, the
			// real fixture and its D0 row before any pipeline call.
			wrapped := newPipelineHarness(t, spec, true)
			expectation := pipelineHarnessExpectationForFixture(t, spec, wrapped.fixture)
			if spec.kind == "text" {
				if _, ok := any(wrapped.adapter).(keyRelationProvider); ok {
					t.Fatal("wrapping the text adapter added a fabricated key provider")
				}
			}
			// When: the complete wrapped Core runs on the real adapter, SQLite and
			// qualified publication Run.
			result, err := wrapped.execute(t.Context())
			if err != nil || !result.TMCommitted {
				t.Fatalf("wrapped pipeline result=%+v error=%v", result, err)
			}
			snapshot, err := wrapped.snapshot(expectation)
			if err != nil {
				t.Fatal(err)
			}
			if problems := pipelineHarnessJudge(expectation, snapshot); len(problems) != 0 {
				t.Fatalf("boundary/state oracle rejected the real run: %v", problems)
			}
			if spec.kind == "w3" {
				if !slices.Equal(wrapped.memory.scope.Keys, wrapped.fixture.relations) {
					t.Fatal("wrapped W3 run did not bind the current import's key relations")
				}
			} else if wrapped.memory.scope.Keys != nil {
				t.Fatal("text run gained key relations")
			}
			// Then: an independent unwrapped fixture must produce the same
			// artifact and every TM column, and both must match the authored
			// target bytes and D1 rows.
			unwrapped := newPipelineHarness(t, spec, false)
			plainResult, plainErr := unwrapped.execute(t.Context())
			if plainErr != nil || !plainResult.TMCommitted {
				t.Fatalf("unwrapped pipeline result=%+v error=%v", plainResult, plainErr)
			}
			wrappedOutput := wrapped.outputBytes(t)
			plainOutput := unwrapped.outputBytes(t)
			if !bytes.Equal(wrappedOutput, plainOutput) {
				t.Fatal("wrapped and unwrapped artifact bytes differ")
			}
			if !bytes.Equal(wrappedOutput, expectation.TargetBytes) {
				t.Fatal("artifact differs from the independently authored target bytes")
			}
			wrappedRows := pipelineFixtureReadRows(t, wrapped.fixture.db)
			plainRows := pipelineFixtureReadRows(t, unwrapped.fixture.db)
			if !slices.Equal(wrappedRows, plainRows) {
				t.Fatal("wrapped and unwrapped TM rows differ")
			}
			if diff := pipelineFixtureDiff(expectation.D1, plainRows); len(diff) != 0 {
				t.Fatalf("unwrapped TM rows differ from the authored D1 in %d row(s): %v", len(diff), diff)
			}
			pipelineHarnessSave(t, spec.name+"-output.bin", wrappedOutput)
			pipelineHarnessSave(t, spec.name+"-plain-rows.json", mustJSON(t, plainRows))
			pipelineHarnessSaveJSON(t, spec.name+"-snapshot.json", snapshot)
			t.Logf("kind=%s surface=%s mode=%s reached=%v generate=%d exports=%d observes=%d D0=%d D1=%d bytes=%d bound_keys=%v key_provider=%t",
				spec.kind, spec.surface, spec.mode, snapshot.Reached, len(wrapped.fixture.backend.calls),
				wrapped.adapterExports(), wrapped.adapterObserves(), len(expectation.D0), len(plainRows), len(wrappedOutput),
				snapshot.BoundKeys, snapshot.KeyProvider)
		})
	}
}

func testPipelineHarnessBoundariesReject(t *testing.T) {
	spec := pipelineHarnessCases()[0]
	harness := newPipelineHarness(t, spec, true)
	expectation := pipelineHarnessExpectationForFixture(t, spec, harness.fixture)
	if _, err := harness.execute(t.Context()); err != nil {
		t.Fatalf("valid control: %v", err)
	}
	valid, err := harness.snapshot(expectation)
	if err != nil {
		t.Fatal(err)
	}
	if problems := pipelineHarnessJudge(expectation, valid); len(problems) != 0 {
		t.Fatalf("valid control observation rejected: %v", problems)
	}
	mutations := []struct {
		name   string
		mutate func(*pipelineHarnessSnapshot)
	}{
		{"missing-stage-write", func(s *pipelineHarnessSnapshot) {
			s.Order = pipelineHarnessDrop(s.Order, pipelineBoundaryStageWriteAfter)
		}},
		{"observe-after-before-commit", func(s *pipelineHarnessSnapshot) {
			s.Order = pipelineHarnessMoveBefore(s.Order, pipelineBoundaryObserveAfter, pipelineBoundaryTMCommitBefore)
		}},
		{"publish-before-commit", func(s *pipelineHarnessSnapshot) {
			s.Order = pipelineHarnessMoveBefore(s.Order, pipelineBoundaryTMCommitBefore, pipelineBoundaryPublishRenameBefore)
		}},
		{"stdout-boundary-on-file", func(s *pipelineHarnessSnapshot) {
			s.Order = append(s.Order, pipelineBoundaryStdoutWriteBefore)
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			invalid := valid.clone()
			mutation.mutate(&invalid)
			problems := pipelineHarnessJudge(expectation, invalid)
			if len(problems) == 0 {
				t.Fatalf("boundary oracle accepted invalid observation %s", mutation.name)
			}
			if !strings.Contains(strings.Join(problems, "\n"), "boundary") {
				t.Fatalf("rejection of %s came from an unrelated check: %v", mutation.name, problems)
			}
			pipelineHarnessSaveJSON(t, "reject-"+mutation.name+".json", map[string]any{
				"case": mutation.name, "rejected": true, "problems": problems,
				"order": invalid.Order, "expected_order": expectation.BoundaryOrder,
			})
			t.Logf("invalid boundary observation %s rejected: %v", mutation.name, problems)
		})
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}
