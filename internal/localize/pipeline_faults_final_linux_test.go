//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
	"golang.org/x/sys/unix"
)

// These are operation results, not callback counts or invented kernel errors.
// The Close hook observes entry; the mandatory bridge Close is proved by Stat
// on that same descriptor after Stage and again at every Observe entry.
type pipelineFinalIO struct {
	Operation string `json:"operation"`
	N         int    `json:"n"`
	Bytes     []byte `json:"bytes,omitempty"`
	RealError string `json:"real_error,omitempty"`
	Returned  string `json:"returned_error,omitempty"`
}

type pipelineFinalAdapter struct {
	Adapter
	h        *pipelineHarness
	row      string
	cancel   context.CancelFunc
	io       []pipelineFinalIO
	exports  int
	observes int
	observed artifact.FinalObservation
	foreign  *artifact.Manifest
	checkErr error
}

// Text deliberately has no key provider; W3 forwards the same real Import.
type pipelineFinalW3Adapter struct{ *pipelineFinalAdapter }

func (a *pipelineFinalW3Adapter) KeyRelations(s unit.Session) ([]artifact.KeyRelation, error) {
	return a.Adapter.(keyRelationProvider).KeyRelations(s)
}

func (a *pipelineFinalAdapter) inject() { a.h.injected = append(a.h.injected, a.row) }

func (a *pipelineFinalAdapter) Export(w io.Writer, s unit.Session, p validate.Profile, accepted []validate.AcceptedTranslation) (artifact.Manifest, error) {
	a.exports++
	manifest, err := a.Adapter.Export(&pipelineFinalWriter{target: w, adapter: a}, s, p, accepted)
	if err == nil && a.foreign != nil {
		a.inject()
		manifest = *a.foreign
		a.checkErr = manifest.CheckInput(s)
	}
	return manifest, err
}

func (a *pipelineFinalAdapter) Observe(r io.Reader) (artifact.FinalObservation, error) {
	a.observes++
	observed, err := a.Adapter.Observe(&pipelineFinalReader{target: r, adapter: a})
	a.observed = observed
	if err == nil && a.row == "cancel-observe-after" {
		a.inject()
		a.cancel()
	}
	return observed, err
}

type pipelineFinalWriter struct {
	target  io.Writer
	adapter *pipelineFinalAdapter
}

func (w *pipelineFinalWriter) Write(raw []byte) (int, error) {
	a := w.adapter
	part := raw
	switch a.row {
	case "write-zero-error":
		part = raw[:0]
	case "write-prefix-error":
		part = raw[:3]
	}
	n, realErr := w.target.Write(part)
	returned := realErr
	if realErr == nil {
		switch a.row {
		case "write-zero-error", "write-prefix-error":
			a.inject()
			returned = unix.EIO
		case "cancel-write":
			a.inject()
			a.cancel()
		}
	}
	a.io = append(a.io, pipelineFinalIO{"write", n, bytes.Clone(part[:n]), pipelineFinalError(realErr), pipelineFinalError(returned)})
	return n, returned
}

type pipelineFinalReader struct {
	target  io.Reader
	adapter *pipelineFinalAdapter
	started bool
}

func (r *pipelineFinalReader) Read(raw []byte) (int, error) {
	n, realErr := r.target.Read(raw)
	returned := realErr
	if !r.started && n > 0 && realErr == nil {
		switch r.adapter.row {
		case "observe-read-error":
			r.adapter.inject()
			returned = unix.EIO
		case "cancel-observe-read":
			r.adapter.inject()
			r.adapter.cancel()
		}
	}
	r.started = true
	r.adapter.io = append(r.adapter.io, pipelineFinalIO{"read", n, bytes.Clone(raw[:n]), pipelineFinalError(realErr), pipelineFinalError(returned)})
	return n, returned
}

func pipelineFinalError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestPipelinePrecommitFinal(t *testing.T) {
	fileRows := []string{
		"happy", "create-collision", "write-zero-error", "write-prefix-error",
		"sync-error", "close-error", "cancel-write", "cancel-sync", "cancel-close",
		"observe-read-error", "cancel-observe-read", "cancel-observe-after", "cancel-compare-after",
	}
	w3Rows := []string{
		"missing-empty-id", "duplicate-id", "unknown-id", "foreign-artifact-bytes",
		"string-order-only", "key-order-only", "key-reassigned",
		"foreign-session-manifest", "foreign-artifact-manifest",
	}
	for _, backend := range []string{"cpu", "cuda"} {
		for _, kind := range []string{"text", "w3"} {
			for _, row := range fileRows {
				name := backend + "-" + kind + "-file-replace-existing-" + row
				t.Run(name, func(t *testing.T) {
					pipelineFinalExercise(t, pipelineHarnessCase{name, kind, "file", publication.Replace, true}, backend, row)
				})
			}
		}
		for _, row := range []string{"happy", "observe-read-error", "cancel-observe-read", "cancel-observe-after"} {
			name := backend + "-text-stdout-none-" + row
			t.Run(name, func(t *testing.T) {
				pipelineFinalExercise(t, pipelineHarnessCase{name, "text", "stdout", "", false}, backend, row)
			})
		}
		for _, row := range w3Rows {
			name := backend + "-w3-file-replace-existing-" + row
			t.Run(name, func(t *testing.T) {
				pipelineFinalExercise(t, pipelineHarnessCase{name, "w3", "file", publication.Replace, true}, backend, row)
			})
		}
		for _, kind := range []string{"text", "w3"} {
			for _, mode := range []struct {
				name string
				mode publication.Mode
			}{{"create", publication.Create}, {"replace-absent", publication.Replace}, {"inplace", publication.InPlace}} {
				for _, row := range []string{"sync-error", "cancel-compare-after"} {
					name := backend + "-" + kind + "-file-" + mode.name + "-" + row
					t.Run(name, func(t *testing.T) {
						pipelineFinalExercise(t, pipelineHarnessCase{name, kind, "file", mode.mode, false}, backend, row)
					})
				}
			}
		}
	}
}

// The expectation is prepared before the action. No actual rows, trace, or
// observation is used to decide which operations should have happened.
func pipelineFinalExpectation(t *testing.T, spec pipelineHarnessCase, f *pipelineFixture, row string, stage []byte) pipelineHarnessExpectation {
	t.Helper()
	if row == "happy" {
		return pipelinePrecommitPublishedExpectation(t, spec, f)
	}
	file := spec.surface == "file"
	exp := pipelinePrecommitFailureExpectation(t, spec, f, pipelinePrecommitCase{
		mode: spec.mode, outputPresent: spec.outputPresent, stageRetained: file,
		stageFDClosed: file && row != "create-collision", generate: pipelineHarnessGenerateCount(f.input),
		errorPhase: "stage", causeClass: "other", stageOrder: file, injection: row,
	})
	exp.StageBytes = bytes.Clone(stage)
	exp.StdoutBytes = nil
	exp.RequiredBoundaries = map[string]int{}
	// Zero is explicit for later operations: map omission must not hide a
	// premature commit, record, rename, stdout write, or skipped mandatory Close.
	for name := range pipelineHarnessPublishedBoundaries(f.input, spec.mode) {
		exp.RequiredBoundaries[name] = 0
	}
	for _, name := range []string{pipelineBoundaryStdoutWriteBefore, pipelineBoundaryStdoutWriteAfter,
		pipelineBoundaryBackupRenameBefore, pipelineBoundaryBackupRenameAfter, pipelineBoundaryObserveEntryWriterClosed} {
		exp.RequiredBoundaries[name] = 0
	}
	exp.BoundaryOrder = nil
	for range exp.GenerateCalls {
		exp.BoundaryOrder = append(exp.BoundaryOrder, pipelineBoundaryGenerateBefore, pipelineBoundaryGenerateAfter)
	}
	exp.RequiredBoundaries[pipelineBoundaryGenerateBefore] = exp.GenerateCalls
	exp.RequiredBoundaries[pipelineBoundaryGenerateAfter] = exp.GenerateCalls
	write, syncStage, observe, observeOK := true, true, true, true
	switch row {
	case "create-collision":
		write, syncStage, observe = false, false, false
	case "write-zero-error", "write-prefix-error", "cancel-write", "foreign-artifact-manifest":
		syncStage, observe = false, false
	case "sync-error", "close-error", "cancel-sync", "cancel-close":
		observe = false
	case "observe-read-error", "duplicate-id":
		observeOK = false
	}
	switch row {
	case "write-zero-error", "write-prefix-error", "sync-error", "close-error", "observe-read-error":
		exp.CauseClass = "eio"
	case "cancel-write", "cancel-sync", "cancel-close", "cancel-observe-read", "cancel-observe-after", "cancel-compare-after":
		exp.CauseClass = "context-canceled"
	}
	if file {
		if write {
			exp.RequiredBoundaries[pipelineBoundaryStageWriteBefore] = 1
			exp.RequiredBoundaries[pipelineBoundaryStageWriteAfter] = 1
			exp.RequiredBoundaries[pipelineBoundaryStageCloseBefore] = 1
			exp.BoundaryOrder = append(exp.BoundaryOrder, pipelineBoundaryStageWriteBefore, pipelineBoundaryStageWriteAfter)
		}
		if syncStage {
			exp.RequiredBoundaries[pipelineBoundaryStageSyncBefore] = 1
			exp.RequiredBoundaries[pipelineBoundaryStageSyncAfter] = 1
		}
		exp.ObserveEntryApplicable = true
		exp.WriterClosedAtObserve = observe
		if observe {
			exp.RequiredBoundaries[pipelineBoundaryObserveEntryWriterClosed] = 1
		}
	} else {
		exp.Outcome = "stdout-not-delivered"
		exp.ErrorPhase = "observe"
		if row != "observe-read-error" {
			exp.ErrorPhase = "commit"
		}
	}
	if observe {
		exp.RequiredBoundaries[pipelineBoundaryObserveBefore] = 1
		exp.BoundaryOrder = append(exp.BoundaryOrder, pipelineBoundaryObserveBefore)
		if observeOK {
			exp.RequiredBoundaries[pipelineBoundaryObserveAfter] = 1
			exp.BoundaryOrder = append(exp.BoundaryOrder, pipelineBoundaryObserveAfter)
		}
	}
	if row == "cancel-compare-after" {
		exp.ErrorPhase = "commit"
		exp.StageCheckpoint = true
		exp.RequiredBoundaries[pipelineBoundaryCompareFinalAfter] = 1
		exp.RequiredBoundaries[pipelineBoundaryTMCommitBefore] = 1
		exp.BoundaryOrder = append(exp.BoundaryOrder, pipelineBoundaryCompareFinalAfter, pipelineBoundaryTMCommitBefore)
	}
	if file {
		exp.RequiredBoundaries[pipelineBoundaryRunCloseAfter] = 1
		exp.BoundaryOrder = append(exp.BoundaryOrder, pipelineBoundaryRunCloseAfter)
	}
	return exp
}

func pipelineFinalExercise(t *testing.T, spec pipelineHarnessCase, backend, row string) {
	t.Helper()
	// Given: the accepted shared fixture, real SQLite sentinel and authored
	// source/target, with backend identity selected before profile creation.
	h := newPipelineHarnessWith(t, spec, true, func(f *pipelineFixture) {
		if backend == "cuda" {
			f.backend.info = pipelineControlledCUDAInfo(f.backend.info)
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	a := &pipelineFinalAdapter{Adapter: h.adapter, h: h, row: row, cancel: cancel}
	if spec.kind == "w3" {
		h.adapter = &pipelineFinalW3Adapter{a}
	} else {
		h.adapter = a
	}
	beforeSource, err := pipelineHarnessReadFile(h.sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	stage := bytes.Clone(h.fixture.input.target)
	var wire []byte
	switch row {
	case "create-collision", "write-zero-error":
		stage = []byte{}
	case "write-prefix-error":
		stage = stage[:3]
	case "missing-empty-id", "duplicate-id", "unknown-id", "string-order-only", "key-order-only", "key-reassigned":
		wire = pipelineFinalWire(t, row, stage)
		stage = bytes.Clone(wire)
	case "foreign-artifact-bytes", "foreign-session-manifest", "foreign-artifact-manifest":
		raw, manifest := pipelineFinalForeign(t, h.fixture, row)
		if row == "foreign-artifact-bytes" {
			wire, stage = raw, bytes.Clone(raw)
		} else {
			a.foreign = &manifest
		}
	}
	exp := pipelineFinalExpectation(t, spec, h.fixture, row, stage)
	wantHistory := pipelineFinalHistory(h.fixture.input, backend)
	pipelineFixtureSaveJSON(t, "expectation.json", exp)
	pipelineFixtureSaveJSON(t, "expected-backend.json", wantHistory)
	pipelineFixtureSave(t, "authored-source.bin", h.fixture.input.source)
	pipelineFixtureSave(t, "authored-target.bin", h.fixture.input.target)
	pipelineFixtureSave(t, "expected-stage.bin", stage)
	if row == "cancel-compare-after" {
		h.afterStageHook = func() error {
			h.rowsAfterStage = pipelineFixtureReadRows(t, h.fixture.db)
			a.inject()
			cancel()
			return nil
		}
	}
	// When: drive the product Core, including real Prepare/Stage/check/commit.
	if spec.surface == "file" {
		boundary := pipelineBoundaryStageSyncBefore
		switch row {
		case "happy":
			boundary = ""
		case "create-collision":
			// There is no syscall-entry counter in the accepted API. EEXIST
			// in the real Stage error chain proves this refusal instead.
			boundary = "stage.openat.O_EXCL"
		case "write-zero-error", "write-prefix-error", "cancel-write", "foreign-artifact-manifest":
			boundary = pipelineBoundaryStageWriteAfter
		case "close-error", "cancel-close":
			boundary = pipelineBoundaryStageCloseBefore
		case "observe-read-error", "cancel-observe-read":
			boundary = pipelineBoundaryObserveBefore
		case "cancel-observe-after":
			boundary = pipelineBoundaryObserveAfter
		case "cancel-compare-after":
			boundary = pipelineBoundaryTMCommitBefore
		case "foreign-session-manifest":
			boundary = pipelineBoundaryStageWriteAfter
		}
		h.executeWithHooks(ctx, row, boundary, func() publication.PipelineFaultHooks {
			hooks := h.ops.hooks()
			syncStage, closeStage := hooks.SyncStage, hooks.CloseStage
			if row == "create-collision" {
				f, err := os.OpenFile(h.ops.paths.Stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
				a.inject()
			}
			hooks.SyncStage = func(f *os.File) error {
				if wire != nil {
					// This is the actual exported stage, not FinalObservation.
					before, err := os.ReadFile(h.ops.paths.Stage)
					if err != nil || !bytes.Equal(before, h.fixture.input.target) {
						t.Fatalf("wire mutation did not start from the authored export: %v", err)
					}
					n, err := f.WriteAt(wire, 0)
					a.io = append(a.io, pipelineFinalIO{"write-at", n, bytes.Clone(wire[:n]), pipelineFinalError(err), pipelineFinalError(err)})
					if err != nil || n != len(wire) {
						t.Fatalf("real stage WriteAt = %d, %v", n, err)
					}
					err = f.Truncate(int64(len(wire)))
					a.io = append(a.io, pipelineFinalIO{"truncate", 0, nil, pipelineFinalError(err), pipelineFinalError(err)})
					if err != nil {
						t.Fatal(err)
					}
					a.inject()
				}
				realErr := syncStage(f)
				returned := realErr
				if realErr == nil {
					switch row {
					case "sync-error":
						a.inject()
						returned = unix.EIO
					case "cancel-sync":
						a.inject()
						cancel()
					}
				}
				a.io = append(a.io, pipelineFinalIO{"sync", 0, nil, pipelineFinalError(realErr), pipelineFinalError(returned)})
				return returned
			}
			hooks.CloseStage = func(f *os.File) error {
				if err := closeStage(f); err != nil {
					return err
				}
				var injected error
				switch row {
				case "close-error":
					a.inject()
					injected = unix.EIO
				case "cancel-close":
					a.inject()
					cancel()
				}
				a.io = append(a.io, pipelineFinalIO{"close-hook", 0, nil, "", pipelineFinalError(injected)})
				return injected // The accepted bridge performs the real Close.
			}
			return hooks
		})
	} else {
		h.requestedBoundary, h.requestedOccurrence, h.stimulus = pipelineBoundaryObserveBefore, 1, row
		if row == "cancel-observe-after" {
			h.requestedBoundary = pipelineBoundaryObserveAfter
		}
		h.execute(ctx)
	}
	if row == "happy" {
		h.requestedBoundary, h.requestedOccurrence, h.stimulus = "", 0, "none"
	}
	// Then: save actual physical state before assertions, including failures.
	snapshot, err := h.snapshot(exp)
	if err != nil {
		t.Fatal(err)
	}
	if spec.surface == "stdout" {
		snapshot.BeforeSource = &beforeSource
	}
	snapshot.Rerun = "pending: this capture precedes the same-target rerun"
	if row == "happy" {
		snapshot.Rerun = "not-run: fault-free control"
	}
	pipelineFinalSave(t, h, "after", snapshot)
	pipelineFixtureSaveJSON(t, "actual-io.json", a.io)
	pipelineFixtureSaveJSON(t, "actual-observation.json", a.observed)
	var observedUnits, observedKeys []map[string]string
	for _, u := range a.observed.Units {
		observedUnits = append(observedUnits, map[string]string{"id": u.ID.StableID(), "text": u.Text, "language": u.TargetLanguage})
	}
	for _, k := range a.observed.Keys {
		observedKeys = append(observedKeys, map[string]string{"id": k.UnitID.StableID(), "key_hex": fmt.Sprintf("%x", k.Key)})
	}
	pipelineFixtureSaveJSON(t, "actual-wire-content.json", map[string]any{"units": observedUnits, "keys": observedKeys, "metadata": a.observed.Metadata})
	pipelineFixtureSaveJSON(t, "actual-backend.json", snapshot.BackendCalls)
	if problems := pipelinePrecommitJudge(exp, snapshot); len(problems) != 0 {
		t.Fatalf("precommit final state: %v; error=%v unsupported_filesystem=%t", problems, h.runErr, errors.Is(h.runErr, publication.ErrUnsupportedFilesystem))
	}
	if !reflect.DeepEqual(wantHistory, snapshot.BackendCalls) {
		t.Fatalf("exact backend history differs: got %+v want %+v", snapshot.BackendCalls, wantHistory)
	}
	pipelineFinalAssertIO(t, a, exp, stage, wire != nil)
	pipelineFinalAssertCause(t, h.runErr, spec.surface, row)
	if a.foreign != nil {
		if row == "foreign-session-manifest" && a.checkErr != nil {
			t.Fatalf("same-source foreign manifest failed CheckInput: %v", a.checkErr)
		}
		if row == "foreign-artifact-manifest" && !errors.Is(a.checkErr, artifact.ErrInputMismatch) {
			t.Fatalf("different-source manifest did not fail CheckInput: %v", a.checkErr)
		}
		pipelineFixtureSaveJSON(t, "manifest-check.json", map[string]any{"check_input_error": pipelineFinalError(a.checkErr), "same_source": row == "foreign-session-manifest"})
	}
	if spec.kind == "w3" {
		wantKeys := []string{"1001:04030201", "4294967295:efcdab89"}
		if !snapshot.KeyProvider || !slices.Equal(snapshot.BoundKeys, wantKeys) {
			t.Fatalf("real same-import W3 key binding = %v provider=%t", snapshot.BoundKeys, snapshot.KeyProvider)
		}
		switch row {
		case "missing-empty-id", "unknown-id", "key-reassigned", "foreign-artifact-bytes":
			pipelineFinalAssertWireContent(t, h.fixture, a.observed, row)
		}
	} else if snapshot.KeyProvider || len(snapshot.BoundKeys) != 0 {
		t.Fatal("text gained fabricated keys")
	}
	if row == "string-order-only" || row == "key-order-only" {
		pipelineFinalAssertOrderOnly(t, h.fixture, a.observed, snapshot.Files["stage"].Bytes, row)
	}
	if row != "happy" {
		pipelineFinalRerun(t, h, exp, snapshot, row)
	}
	t.Logf("backend=%s row=%s fs=%s uid=%d exports=%d observes=%d stage_fd_closed=%t writer_closed_at_observe=%t phase=%s cause=%s generated=%d injected=%v",
		backend, row, snapshot.Filesystem, os.Geteuid(), a.exports, a.observes, snapshot.StageFDClosed, snapshot.WriterClosedAtObserve, snapshot.ErrorPhase, snapshot.CauseClass, snapshot.GenerateCalls, snapshot.Injected)
}

func pipelineFinalHistory(input pipelineFixtureInput, backend string) []pipelineBackendCall {
	history := []pipelineBackendCall{{Operation: "info", Ordinal: 1, Backend: backend}}
	for _, operation := range []string{"count", "generate"} {
		for _, u := range input.units {
			if u.source == "" {
				continue
			}
			call := pipelineBackendCall{
				Operation: operation, Ordinal: len(history) + 1, Backend: backend,
				Request: u.source, TokenIDs: []int{1, 2, 3},
				Policy: pipelineBackendPolicyRecord{Schema: 1, MaxOutputTokens: 2, RequestTimeoutNS: 1000000000, Seed: 0, SeedFixed: true, StopIDs: []int{42}},
			}
			if operation == "generate" {
				call.Finish = "stop"
			}
			history = append(history, call)
		}
	}
	return history
}

func pipelineFinalAssertIO(t *testing.T, a *pipelineFinalAdapter, exp pipelineHarnessExpectation, stage []byte, wire bool) {
	t.Helper()
	var want []pipelineFinalIO
	if a.row != "create-collision" {
		raw := a.h.fixture.input.target
		returned := ""
		if a.row == "write-zero-error" || a.row == "write-prefix-error" {
			raw, returned = stage, unix.EIO.Error()
		}
		want = append(want, pipelineFinalIO{"write", len(raw), raw, "", returned})
	}
	if exp.Surface == "file" {
		if wire {
			want = append(want, pipelineFinalIO{"write-at", len(stage), stage, "", ""}, pipelineFinalIO{Operation: "truncate"})
		}
		if exp.RequiredBoundaries[pipelineBoundaryStageSyncBefore] == 1 {
			returned := ""
			if a.row == "sync-error" {
				returned = unix.EIO.Error()
			}
			want = append(want, pipelineFinalIO{"sync", 0, nil, "", returned})
		}
		if a.row != "create-collision" {
			returned := ""
			if a.row == "close-error" {
				returned = unix.EIO.Error()
			}
			want = append(want, pipelineFinalIO{"close-hook", 0, nil, "", returned})
		}
	}
	if exp.RequiredBoundaries[pipelineBoundaryObserveBefore] == 1 {
		returned := ""
		if a.row == "observe-read-error" {
			returned = unix.EIO.Error()
		}
		want = append(want, pipelineFinalIO{"read", len(stage), stage, "", returned})
		if returned == "" {
			want = append(want, pipelineFinalIO{"read", 0, []byte{}, "EOF", "EOF"})
		}
	}
	// Byte-slice nil/empty is immaterial for a zero-byte operation, but both
	// operation occurrences and actual n are exact.
	if len(a.io) != len(want) {
		t.Fatalf("real I/O operations = %+v, want %+v", a.io, want)
	}
	for i, got := range a.io {
		w := want[i]
		if got.Operation != w.Operation || got.N != w.N || !bytes.Equal(got.Bytes, w.Bytes) || got.RealError != w.RealError || got.Returned != w.Returned {
			t.Fatalf("real I/O operation %d = %+v, want %+v", i, got, w)
		}
	}
	exports := 1
	if a.row == "create-collision" {
		exports = 0
	}
	if a.exports != exports || a.observes != exp.RequiredBoundaries[pipelineBoundaryObserveBefore] {
		t.Fatalf("adapter calls export/observe=%d/%d, want %d/%d", a.exports, a.observes, exports, exp.RequiredBoundaries[pipelineBoundaryObserveBefore])
	}
	pipelineFixtureSaveJSON(t, "expected-io.json", want)
}

func pipelineFinalAssertCause(t *testing.T, err error, surface, row string) {
	t.Helper()
	if row == "happy" {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	var cause error
	inner := ""
	switch row {
	case "create-collision":
		cause = unix.EEXIST
	case "write-zero-error", "write-prefix-error":
		cause, inner = unix.EIO, "export"
	case "sync-error", "close-error":
		cause = unix.EIO
	case "observe-read-error":
		cause, inner = unix.EIO, "observe"
	case "duplicate-id":
		inner = "observe"
	case "foreign-session-manifest":
		cause, inner = artifact.ErrInvalidExpected, "compare_final"
	case "foreign-artifact-manifest":
		cause, inner = artifact.ErrInputMismatch, "export"
	case "missing-empty-id", "unknown-id", "foreign-artifact-bytes", "string-order-only", "key-order-only", "key-reassigned":
		cause, inner = artifact.ErrFinalMismatch, "compare_final"
	default:
		cause = context.Canceled
	}
	if err == nil || (cause != nil && !errors.Is(err, cause)) {
		t.Fatalf("typed final cause = %v, want %v", err, cause)
	}
	var phases []string
	var observeErr error
	var visit func(error)
	visit = func(e error) {
		if e == nil {
			return
		}
		if typed, ok := e.(*Error); ok {
			phases = append(phases, typed.Phase)
			if typed.Phase == "observe" {
				observeErr = typed.Err
			}
		}
		switch wrapped := e.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range wrapped.Unwrap() {
				visit(child)
			}
		case interface{ Unwrap() error }:
			visit(wrapped.Unwrap())
		}
	}
	visit(err)
	want := []string{"stage"}
	if surface == "stdout" || row == "cancel-compare-after" {
		want = []string{"commit"}
		if row == "observe-read-error" {
			want = []string{"observe"}
			inner = ""
		}
	}
	if inner != "" {
		want = append(want, inner)
	}
	if !slices.Equal(phases, want) {
		t.Fatalf("outer/inner phases = %v, want %v", phases, want)
	}
	if row == "duplicate-id" && (observeErr == nil || observeErr.Error() != "duplicate w3strings ID") {
		t.Fatalf("duplicate parser rejection was replaced by another error: %v", observeErr)
	}
	pipelineFixtureSaveJSON(t, "typed-error.json", map[string]any{"phases": phases, "cause": pipelineFinalError(cause), "error": err.Error(), "observe_error": pipelineFinalError(observeErr), "errors_is": cause != nil && errors.Is(err, cause)})
}

// Offsets are from the authored four-string/two-key vector, not parsed actual
// output: count@10, 12-byte strings@11/23/35/47, keys@60/68.
func pipelineFinalWire(t *testing.T, row string, raw []byte) []byte {
	t.Helper()
	b := bytes.Clone(raw)
	const magic uint32 = 0xa67da9b9 ^ 0xffffffff
	switch row {
	case "missing-empty-id":
		b[10] = 3
		b = append(b[:23], b[35:]...)
	case "duplicate-id":
		binary.LittleEndian.PutUint32(b[35:39], 1002^magic)
	case "unknown-id":
		binary.LittleEndian.PutUint32(b[35:39], 1004^magic)
	case "string-order-only":
		first := bytes.Clone(b[11:23])
		copy(b[11:23], b[35:47])
		copy(b[35:47], first)
	case "key-order-only":
		first := bytes.Clone(b[60:68])
		copy(b[60:68], b[68:76])
		copy(b[68:76], first)
	case "key-reassigned":
		binary.LittleEndian.PutUint32(b[64:68], 1003^magic)
	default:
		t.Fatalf("unplanned W3 mutation %s", row)
	}
	return b
}

func pipelineFinalForeign(t *testing.T, f *pipelineFixture, row string) ([]byte, artifact.Manifest) {
	t.Helper()
	other, err := w3strings.NewForSource("en", f.limits)
	if err != nil {
		t.Fatal(err)
	}
	source := bytes.Clone(f.input.source)
	if row == "foreign-artifact-manifest" {
		// Swap valid source string records without changing any text or key.
		first := bytes.Clone(source[11:23])
		copy(source[11:23], source[35:47])
		copy(source[35:47], first)
	}
	session, err := other.Import(source, f.profile)
	if err != nil {
		t.Fatal(err)
	}
	var accepted []validate.AcceptedTranslation
	for _, u := range session.Units() {
		text := f.targets[u.ID().StableID()]
		if row == "foreign-artifact-bytes" && text != "" {
			text = "別" + strings.TrimPrefix(text, "訳")
		}
		value, err := validate.Validate(session, u.ID(), f.profile, text)
		if err != nil {
			t.Fatal(err)
		}
		accepted = append(accepted, value)
	}
	var out bytes.Buffer
	manifest, err := other.Export(&out, session, f.profile, accepted)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := artifact.PrepareExport(session, f.profile, accepted)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := other.Observe(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if err := artifact.CompareFinal(manifest, expected, observed); err != nil {
		t.Fatalf("foreign real export is not valid for its own session: %v", err)
	}
	pipelineFixtureSave(t, "foreign-source.bin", source)
	pipelineFixtureSave(t, "foreign-export.bin", out.Bytes())
	return bytes.Clone(out.Bytes()), manifest
}

func pipelineFinalAssertOrderOnly(t *testing.T, f *pipelineFixture, actual artifact.FinalObservation, raw []byte, row string) {
	t.Helper()
	start, end, width := 11, 59, 12
	if row == "key-order-only" {
		start, end, width = 60, 76, 8
	}
	if len(raw) != len(f.input.target) || !bytes.Equal(raw[:start], f.input.target[:start]) || !bytes.Equal(raw[end:], f.input.target[end:]) {
		t.Fatal("order-only mutation changed bytes outside the authored record table")
	}
	beforeRecords, afterRecords := map[string]int{}, map[string]int{}
	for at := start; at < end; at += width {
		beforeRecords[string(f.input.target[at:at+width])]++
		afterRecords[string(raw[at:at+width])]++
	}
	if !maps.Equal(beforeRecords, afterRecords) {
		t.Fatal("order-only mutation changed the authored wire record multiset")
	}
	// An independent authored expectation includes the empty/keyless ID. It
	// never imports the mutator's index list or the observation's ordering.
	units := map[artifact.ObservedUnit]int{}
	keys := map[artifact.KeyRelation]int{}
	for _, entry := range f.input.units {
		id, err := unit.NewUnitID(f.input.adapterName, f.input.formatSchema, entry.stableID)
		if err != nil {
			t.Fatal(err)
		}
		units[artifact.ObservedUnit{ID: id, Text: entry.translation, TargetLanguage: "ja"}]++
		for _, key := range entry.keys {
			keys[artifact.KeyRelation{UnitID: id, Key: string(key)}]++
		}
	}
	gotUnits := map[artifact.ObservedUnit]int{}
	gotKeys := map[artifact.KeyRelation]int{}
	for _, u := range actual.Units {
		gotUnits[u]++
	}
	for _, key := range actual.Keys {
		gotKeys[key]++
	}
	if !maps.Equal(units, gotUnits) || !maps.Equal(keys, gotKeys) {
		t.Fatal("order-only wire mutation changed the authored unit/key multisets")
	}
	wantIDs := []string{"1001", "1002", "1003", "4294967295"}
	wantKeys := []string{"04030201", "efcdab89"}
	if row == "string-order-only" {
		wantIDs = []string{"1003", "1002", "1001", "4294967295"}
	} else {
		wantKeys = []string{"efcdab89", "04030201"}
	}
	for i, u := range actual.Units {
		if u.ID.StableID() != wantIDs[i] {
			t.Fatalf("ordered wire ID %d = %s, want %s", i, u.ID.StableID(), wantIDs[i])
		}
	}
	for i, k := range actual.Keys {
		if fmt.Sprintf("%x", k.Key) != wantKeys[i] {
			t.Fatalf("ordered wire key %d differs", i)
		}
	}
	baseline, err := f.adapter.Observe(bytes.NewReader(f.input.target))
	if err != nil {
		t.Fatal(err)
	}
	field := "decoded-id-order"
	if row == "key-order-only" {
		field = "decoded-key-order"
	}
	if bytes.Equal(actual.Metadata[field], baseline.Metadata[field]) {
		t.Fatal("order-only mutation did not change ordered metadata")
	}
	pipelineFixtureSaveJSON(t, "order-only.json", map[string]any{"wire_record_multiset_equal": true, "outside_table_bytes_equal": true, "table_start": start, "table_end": end, "record_width": width, "unit_multiset_equal": true, "key_multiset_equal": true, "ordered_ids": wantIDs, "ordered_keys": wantKeys, "metadata_field": field, "before": baseline.Metadata[field], "after": actual.Metadata[field]})
}

func pipelineFinalAssertWireContent(t *testing.T, f *pipelineFixture, actual artifact.FinalObservation, row string) {
	t.Helper()
	// Independent semantic vectors prevent a different, also-rejected
	// corruption from satisfying the named wire stimulus.
	ids := []string{"1001", "1002", "1003", "4294967295"}
	texts := []string{"訳一", "", "訳三", "訳終"}
	keyIDs := []string{"1001", "4294967295"}
	switch row {
	case "missing-empty-id":
		ids, texts = []string{"1001", "1003", "4294967295"}, []string{"訳一", "訳三", "訳終"}
	case "unknown-id":
		ids = []string{"1001", "1002", "1004", "4294967295"}
	case "key-reassigned":
		keyIDs = []string{"1003", "4294967295"}
	case "foreign-artifact-bytes":
		texts = []string{"別一", "", "別三", "別終"}
	}
	var wantUnits []artifact.ObservedUnit
	var wantKeys []artifact.KeyRelation
	for i, idText := range ids {
		id, err := unit.NewUnitID(f.input.adapterName, f.input.formatSchema, idText)
		if err != nil {
			t.Fatal(err)
		}
		wantUnits = append(wantUnits, artifact.ObservedUnit{ID: id, Text: texts[i], TargetLanguage: "ja"})
	}
	for i, idText := range keyIDs {
		id, err := unit.NewUnitID(f.input.adapterName, f.input.formatSchema, idText)
		if err != nil {
			t.Fatal(err)
		}
		key := []string{"\x04\x03\x02\x01", "\xef\xcd\xab\x89"}[i]
		wantKeys = append(wantKeys, artifact.KeyRelation{UnitID: id, Key: key})
	}
	if !slices.Equal(actual.Units, wantUnits) || !slices.Equal(actual.Keys, wantKeys) {
		t.Fatalf("wire mutation %s changed more than its authored content relation: units=%+v keys=%+v", row, actual.Units, actual.Keys)
	}
	pipelineFixtureSaveJSON(t, "expected-wire-content.json", map[string]any{"units": wantUnits, "keys": wantKeys, "ids": ids, "texts": texts, "key_ids": keyIDs})
}

func pipelineFinalSave(t *testing.T, h *pipelineHarness, prefix string, snapshot pipelineHarnessSnapshot) {
	t.Helper()
	if prefix == "rerun" {
		// h.snapshot refreshes physical files/rows but its API result and trace
		// describe the first call. Do not mislabel that history as a rerun.
		pipelineFixtureSaveJSON(t, prefix+"-physical.json", map[string]any{"files": snapshot.Files, "rows": snapshot.Rows})
	} else {
		pipelineFixtureSaveJSON(t, prefix+"-state.json", snapshot)
	}
	pipelineFixtureSaveJSON(t, prefix+"-rows.json", snapshot.Rows)
	for label, file := range snapshot.Files {
		if file.Exists {
			pipelineFixtureSave(t, prefix+"-"+label+".bin", file.Bytes)
		}
	}
	if root := os.Getenv("YAKUORI_PIPELINE_EVIDENCE"); root != "" {
		// VACUUM INTO is a consistent real SQLite copy, including any WAL
		// content; copying memory.db alone could silently lose committed rows.
		path := filepath.Join(root, t.Name(), prefix+"-memory.db")
		if _, err := h.fixture.db.Exec("VACUUM INTO ?", path); err != nil {
			t.Fatal(err)
		}
	}
}

func pipelineFinalRerun(t *testing.T, h *pipelineHarness, exp pipelineHarnessExpectation, first pipelineHarnessSnapshot, row string) {
	t.Helper()
	// Stage-absent representative: remove only the collision we injected,
	// after preserving/asserting the original physical collision evidence.
	if row == "create-collision" {
		if err := os.Remove(h.ops.paths.Stage); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(h.ops.paths.Stage); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("injected collision still exists before absent-stage rerun: %v", err)
		}
		exp.StageRetained, exp.StageBytes = false, nil
	}
	before := h.fixture.backend.operationCount("generate")
	historyStart := len(h.fixture.backend.history)
	var wantHistory []pipelineBackendCall
	if h.surface == "stdout" {
		wantHistory = pipelineFinalHistory(h.fixture.input, h.fixture.backend.info.Backend)
		for i := range wantHistory {
			wantHistory[i].Ordinal += historyStart
		}
	}
	pipelineFixtureSaveJSON(t, "expected-rerun-backend.json", wantHistory)
	var out bytes.Buffer
	var result Result
	var err error
	if h.surface == "stdout" {
		result, err = h.fixture.core.TextWithTM(t.Context(), h.fixture.input.source, h.fixture.adapter, h.fixture.profile, h.fixture.store, h.fixture.scope, &out)
		if err != nil || !result.TMCommitted || result.BytesWritten != len(h.fixture.input.target) || !bytes.Equal(out.Bytes(), h.fixture.input.target) {
			t.Fatalf("fresh-context stdout rerun = %+v, %v bytes=%x", result, err, out.Bytes())
		}
	} else {
		result, err = h.fixture.core.FileWithTM(t.Context(), h.options, h.fixture.adapter, h.fixture.profile, h.fixture.store, h.fixture.scope)
		var local *Error
		var recovery *publication.RecoveryError
		if !errors.As(err, &local) || local.Phase != "prepare" || !errors.As(err, &recovery) ||
			!errors.Is(err, publication.ErrInvalidRecord) || !errors.Is(err, unix.ENOENT) ||
			recovery.Result.RecordPath != h.ops.paths.Record {
			t.Fatalf("retained-state rerun must refuse missing-record: %v; recovery=%+v", err, recovery)
		}
		if result.TMCommitted || result.Publication.State != publication.NotPublished {
			t.Fatalf("rerun partially committed or published: %+v", result)
		}
	}
	generated := h.fixture.backend.operationCount("generate") - before
	wantGenerate, wantRows := 0, exp.D0
	if h.surface == "stdout" {
		wantGenerate, wantRows = exp.GenerateCalls, exp.D1
	}
	if generated != wantGenerate {
		t.Fatalf("rerun generated %d, want %d", generated, wantGenerate)
	}
	actualHistory := slices.Clone(h.fixture.backend.history[historyStart:])
	if len(actualHistory) != len(wantHistory) || (len(wantHistory) != 0 && !reflect.DeepEqual(actualHistory, wantHistory)) {
		t.Fatalf("rerun backend history differs: got %+v want %+v", actualHistory, wantHistory)
	}
	pipelineFixtureSaveJSON(t, "actual-rerun-backend.json", actualHistory)
	after, snapErr := h.snapshot(exp)
	if snapErr != nil {
		t.Fatal(snapErr)
	}
	if diff := pipelineFixtureDiff(wantRows, after.Rows); len(diff) != 0 {
		t.Fatalf("rerun rows differ from authored expectations: %v", diff)
	}
	if h.surface == "file" {
		if problems := pipelineHarnessCheckFiles(exp, after); len(problems) != 0 {
			t.Fatalf("rerun changed retained state: %v", problems)
		}
		for _, label := range []string{"source", "output", "backup", "record", "stage"} {
			if label == "stage" && row == "create-collision" {
				continue
			}
			if !reflect.DeepEqual(first.Files[label], after.Files[label]) {
				t.Fatalf("rerun changed %s bytes/identity/presence", label)
			}
		}
	}
	pipelineFinalSave(t, h, "rerun", after)
	var stdout []byte
	if h.surface == "stdout" {
		stdout = h.fixture.input.target
	}
	pipelineFixtureSaveJSON(t, "rerun-expectation.json", map[string]any{"rows": wantRows, "generate": wantGenerate, "stdout": stdout, "file_missing_record": h.surface == "file", "stage_absent": row == "create-collision"})
	var recovery *publication.RecoveryError
	errors.As(err, &recovery)
	pipelineFixtureSaveJSON(t, "rerun-result.json", map[string]any{"result": result, "error": pipelineFinalError(err), "generate": generated, "stdout": out.Bytes(), "cause": pipelinePrecommitCauseClass(err), "recovery": recovery, "missing_record_errno": errors.Is(err, unix.ENOENT)})
}
