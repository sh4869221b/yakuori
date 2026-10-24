//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/segment"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

type pipelineSegmentedCase struct {
	name, phase string
	cause       error
	generate    int
}

func TestPipelinePrecommitSegmented(t *testing.T) {
	cases := []pipelineSegmentedCase{
		{"happy", "", nil, 3},
		{"all-admission", "plan", segment.ErrContextLimit, 0},
		{"second-partial", "generate", pipelinePrecommitGenerateFault, 2},
		{"second-max", "generate", ErrIncompleteGeneration, 2},
		{"second-unknown", "generate", ErrIncompleteGeneration, 2},
		{"second-decode", "generate", ErrIncompleteGeneration, 2},
		{"second-count-mismatch", "generate", segment.ErrInvalidResult, 2},
		{"second-cancel", "generate", context.Canceled, 2},
		{"second-deadline", "generate", context.DeadlineExceeded, 2},
		{"protected-token-invalid", "generate", protect.ErrInvalidCandidate, 3},
		{"parent-validation-invalid", "validate", validate.ErrInvalidCandidate, 3},
	}
	for _, backend := range []string{"cpu", "cuda"} {
		for _, mode := range []struct {
			name, surface string
			mode          publication.Mode
			present       bool
		}{
			{"replace-existing", "file", publication.Replace, true},
			{"none", "stdout", "", false},
			{"create", "file", publication.Create, false},
			{"replace-absent", "file", publication.Replace, false},
			{"inplace", "file", publication.InPlace, true},
		} {
			for _, tc := range cases {
				if mode.name != "replace-existing" && mode.name != "none" && tc.name != "second-partial" {
					continue
				}
				name := backend + "-text-" + mode.surface + "-" + mode.name + "-" + tc.name
				t.Run(name, func(t *testing.T) {
					spec := pipelineHarnessCase{name: name, kind: "text", surface: mode.surface, mode: mode.mode, outputPresent: mode.present}
					pipelineSegmentedExercise(t, spec, backend, tc)
				})
			}
		}
	}
}

func pipelineSegmentedExercise(t *testing.T, spec pipelineHarnessCase, backend string, tc pipelineSegmentedCase) {
	t.Helper()
	// Given: real Adapter/Store/Run and the shared same-pointer observer.
	h := newPipelineHarnessWith(t, spec, true, func(f *pipelineFixture) {
		f.backend.info.ContextTokens = 7
		if backend == "cuda" {
			f.backend.info = pipelineControlledCUDAInfo(f.backend.info)
		}
	})
	f := h.fixture
	source, target := "one two three", "ONE TWO THREE"
	switch tc.name {
	case "all-admission":
		source, target = "one two unbreakable", "ONE TWO UNBREAKABLE"
	case "parent-validation-invalid":
		source, target = "one\ntwo\nthree", "ONE\nTWO\nTHREE"
	}
	f.input.source, f.input.target = []byte(source), []byte(target)
	f.input.units = []pipelineFixtureUnit{{stableID: "document", source: source, translation: target}}
	f.authored = pipelineAuthoredSession(t, f.input)
	// Reuse the existing byte-token/context7/budget2 fixture. Only the model
	// identity is supplied by the canonical CPU/CUDA fixture, not a new splitter.
	fixture := segmentedFixture(t, byteLength, nil)
	f.setRequestBuilder(func(ctx context.Context, id unit.UnitID, text string) (inference.GenerationRequest, error) {
		request, err := fixture.segmented.buildRequest(ctx, id, text)
		if err != nil {
			return inference.GenerationRequest{}, err
		}
		canonical, err := pipelineRequest(f.backend.info, text)
		if err != nil {
			return inference.GenerationRequest{}, err
		}
		effective := request.Policy()
		policy, err := inference.NewGenerationPolicy(effective.Schema, effective.MaxOutputTokens, effective.RequestTimeout)
		if err != nil {
			return inference.GenerationRequest{}, err
		}
		return inference.NewGenerationRequest(inference.PreparedRequest{
			RenderedPrompt: request.RenderedPrompt(), TokenIDs: request.TokenIDs(),
			Identity: canonical.Identity(),
		}, policy)
	})
	request, err := f.requestBuilder()(t.Context(), f.authored.Units()[0].ID(), source)
	if err != nil {
		t.Fatal(err)
	}
	f.profile, err = NewCanonicalInferenceProfile(f.backend.info, request, InferenceProfileInput{
		ModelID: "issue68-controlled", Glossary: identity.GlossaryInput{Mode: "none"}, Limits: f.limits, Segmentation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.session, err = f.adapter.Import(f.input.source, f.profile)
	if err != nil {
		t.Fatal(err)
	}
	f.core, err = NewSegmentedCoreWithLimits(f.backend, f.requestBuilder(), f.limits)
	if err != nil {
		t.Fatal(err)
	}
	core, err := NewSegmentedCoreWithLimits(&pipelineHarnessBackend{pipelineBackend: f.backend, trace: h.trace}, f.requestBuilder(), f.limits)
	if err != nil {
		t.Fatal(err)
	}
	h.useCore(core)
	// The unrelated D0 row stays at its original authored profile. Only the
	// target document uses the new segmented profile; no observed DB row is
	// converted into an expectation or used to reseed the fixture.
	if err := os.WriteFile(h.sourcePath, f.input.source, 0600); err != nil {
		t.Fatal(err)
	}
	h.beforeSource, err = pipelineHarnessReadFile(h.sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if spec.surface == "file" {
		h.beforeOutput, err = pipelineHarnessReadFile(h.options.Output)
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.backend.callbacks.Generate = func(ctx context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
		result := segmentedStop(r)
		ordinal := len(f.backend.calls)
		if tc.name == "parent-validation-invalid" {
			h.injected = append(h.injected, fmt.Sprintf("%s@%d", tc.name, ordinal))
			result.Text = "repeat"
			return result, nil
		}
		if ordinal != 2 || tc.name == "happy" {
			return result, nil
		}
		h.injected = append(h.injected, tc.name+"@2")
		switch tc.name {
		case "second-partial":
			return result, pipelinePrecommitGenerateFault
		case "second-max":
			result.Finish = inference.MaxTokens
		case "second-unknown":
			result.Finish = "future"
		case "second-decode":
			result.Finish = inference.DecodeError
		case "second-count-mismatch":
			result.PromptTokens++
		case "second-cancel":
			cancel()
			return result, ctx.Err()
		case "second-deadline":
			// The request's real one-second deadline is the behavior under test.
			<-ctx.Done()
			return result, ctx.Err()
		case "protected-token-invalid":
			result.Text = "[[YAKUORI_0_9]]"
		default:
			t.Fatalf("unexpected second-segment stimulus %q", tc.name)
		}
		return result, nil
	}
	exp := pipelineSegmentedExpectation(t, h, spec, tc)
	wantHistory := pipelineSegmentedHistory(backend, tc)
	h.stimulus = tc.name
	if tc.cause != nil && tc.name != "all-admission" {
		h.requestedBoundary, h.requestedOccurrence = "generate.before", 2
		if tc.name == "parent-validation-invalid" {
			h.requestedOccurrence = 1
		}
	}
	pipelineHarnessSaveJSON(t, spec.name+"/expected.json", exp)
	pipelineHarnessSaveJSON(t, spec.name+"/expected-history.json", wantHistory)
	pipelineHarnessSaveJSON(t, spec.name+"/model-info.json", f.backend.info)
	// When: real segmented planning and generation enter the real output path.
	result, runErr := h.execute(ctx)
	snapshot, err := h.snapshot(exp)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.BeforeSource = &h.beforeSource
	pipelineSegmentedSave(t, h, spec.name+"/first", snapshot)
	var diagnostic *Error
	rawCause := ""
	if errors.As(runErr, &diagnostic) {
		rawCause = diagnostic.Err.Error()
	}
	pipelineHarnessSaveJSON(t, spec.name+"/first/typed-cause.json", struct {
		Phase, Unit, ExpectedCause, ActualCause string
		MatchesRequestedCause                   bool
	}{snapshot.ErrorPhase, snapshot.ErrorUnit, fmt.Sprint(tc.cause), rawCause, errors.Is(runErr, tc.cause)})
	// Then: typed cause, exact backend history and all physical state agree.
	if tc.cause == nil {
		if runErr != nil || !result.TMCommitted {
			t.Fatalf("happy result=%+v err=%v unsupported_filesystem=%t", result, runErr, errors.Is(runErr, publication.ErrUnsupportedFilesystem))
		}
	} else {
		var typed *Error
		if !errors.Is(runErr, tc.cause) || !errors.As(runErr, &typed) || typed.Phase != tc.phase || typed.UnitID.StableID() != "document" {
			t.Fatalf("typed segmented rejection: err=%v want phase=%s cause=%v", runErr, tc.phase, tc.cause)
		}
		if result.TMCommitted || result.BytesWritten != 0 || (spec.surface == "file" && result.Publication.State != publication.NotPublished) {
			t.Fatalf("segmented failure escaped: %+v", result)
		}
	}
	if !reflect.DeepEqual(wantHistory, snapshot.BackendCalls) {
		t.Fatalf("segmented-history: actual=%+v want=%+v", snapshot.BackendCalls, wantHistory)
	}
	if problems := pipelinePrecommitJudge(exp, snapshot); len(problems) != 0 {
		t.Fatalf("segmented-state: %v", problems)
	}
	pipelineSegmentedRerun(t, h, exp, tc)
}

func pipelineSegmentedExpectation(t *testing.T, h *pipelineHarness, spec pipelineHarnessCase, tc pipelineSegmentedCase) pipelineHarnessExpectation {
	t.Helper()
	exp := pipelinePrecommitPublishedExpectation(t, spec, h.fixture)
	exp.SourcePreserved = true
	exp.GenerateCalls = tc.generate
	exp.BoundaryOrder = nil
	for i := range tc.generate {
		exp.BoundaryOrder = append(exp.BoundaryOrder, "generate.before")
		if i != 1 || (tc.name != "second-partial" && tc.name != "second-cancel" && tc.name != "second-deadline") {
			exp.BoundaryOrder = append(exp.BoundaryOrder, "generate.after")
		}
	}
	if tc.cause == nil {
		exp.RequiredBoundaries["generate.before"], exp.RequiredBoundaries["generate.after"] = 3, 3
		if h.surface == "file" {
			exp.BoundaryOrder = append(exp.BoundaryOrder, "stage.write.before", "stage.write.after", "observe.before", "observe.after", "compare-final.after", "tm-commit.before", "tm-commit.after", "run.close.after")
		} else {
			exp.BoundaryOrder = append(exp.BoundaryOrder, "observe.before", "observe.after", "tm-commit.after", "stdout.write.before", "stdout.write.after")
		}
		return exp
	}
	exp.Outcome, exp.CommitCertainty = "not-published", pipelineHarnessNotCommitted
	if h.surface == "stdout" {
		exp.Outcome = "stdout-not-delivered"
	}
	exp.D0Preserved, exp.FinalRows = true, slices.Clone(exp.D0)
	exp.OutputPresent = spec.outputPresent || spec.mode == publication.InPlace
	exp.OutputPreserved = exp.OutputPresent
	exp.OutputBytes = nil
	if h.mode == publication.InPlace {
		exp.OutputBytes = slices.Clone(exp.SourceBytes)
	} else if spec.outputPresent {
		exp.OutputBytes = slices.Clone(pipelineHarnessOldOutput)
	}
	exp.StdoutBytes = nil
	exp.BackupPresent, exp.RecordInstalled = false, false
	exp.RecordPhase, exp.RecordOperation = "", ""
	exp.StageFDClosed, exp.RecordFDClosed = false, false
	exp.ObserveEntryApplicable, exp.WriterClosedAtObserve = false, false
	exp.StageCheckpoint, exp.CommitCheckpoint = false, false
	exp.ErrorPhase, exp.ErrorUnit, exp.CauseClass = tc.phase, "document", pipelinePrecommitCauseClass(tc.cause)
	exp.RequiredBoundaries = map[string]int{
		"generate.before": tc.generate, "generate.after": tc.generate,
		"stage.write.before": 0, "stage.write.after": 0, "stage.sync.before": 0, "stage.sync.after": 0,
		"stage.close.before": 0, "stage.close.after": 0, "observe.before": 0, "observe.after": 0,
		"compare-final.after": 0, "tm-commit.before": 0, "tm-commit.after": 0,
		"record.install.before": 0, "record.install.after": 0, "publish-rename.before": 0, "publish-rename.after": 0,
		"backup-rename.before": 0, "backup-rename.after": 0, "stdout.write.before": 0, "stdout.write.after": 0,
	}
	if tc.name == "second-partial" || tc.name == "second-cancel" || tc.name == "second-deadline" {
		exp.RequiredBoundaries["generate.after"] = 1
	}
	if tc.name == "parent-validation-invalid" {
		exp.Injected = []string{tc.name + "@1", tc.name + "@2", tc.name + "@3"}
	} else if tc.name != "all-admission" {
		exp.Injected = []string{tc.name + "@2"}
	}
	if h.surface == "file" {
		exp.BoundaryOrder = append(exp.BoundaryOrder, "run.close.after")
		exp.RequiredBoundaries["run.close.after"] = 1
	}
	return exp
}

func pipelineSegmentedHistory(backend string, tc pipelineSegmentedCase) []pipelineBackendCall {
	// Literal candidate requests are independent of the segment planner and
	// backend observer. Candidate count calls are not generation retries.
	counted := []string{"one two three", "one two", "one", "two three", "two", "three"}
	switch tc.name {
	case "all-admission":
		counted = []string{"one two unbreakable", "one two", "one", "two unbreakable", "two", "unbreakable"}
	case "parent-validation-invalid":
		counted = []string{"one\ntwo\nthree", "one\ntwo", "one", "two\nthree", "two", "three"}
	}
	history := []pipelineBackendCall{
		{Operation: "info", Ordinal: 1, Backend: backend},
		{Operation: "info", Ordinal: 2, Backend: backend},
	}
	for _, text := range counted {
		history = append(history, pipelineBackendCall{
			Operation: "count", Ordinal: len(history) + 1, Backend: backend, Request: text,
			TokenIDs: make([]int, len(text)), Policy: pipelineBackendPolicyRecord{Schema: 1, MaxOutputTokens: 2, RequestTimeoutNS: 1000000000, SeedFixed: true},
		})
	}
	for i, text := range []string{"one", "two", "three"}[:tc.generate] {
		call := pipelineBackendCall{
			Operation: "generate", Ordinal: len(history) + 1, Backend: backend, Request: text,
			TokenIDs: make([]int, len(text)), Policy: pipelineBackendPolicyRecord{Schema: 1, MaxOutputTokens: 2, RequestTimeoutNS: 1000000000, SeedFixed: true},
			Finish: string(inference.Stop),
		}
		if i == 1 {
			switch tc.name {
			case "second-partial":
				call.Error = "contract generate fault"
			case "second-max":
				call.Finish = string(inference.MaxTokens)
			case "second-unknown":
				call.Finish = "future"
			case "second-decode":
				call.Finish = string(inference.DecodeError)
			case "second-cancel":
				call.Error = "context canceled"
			case "second-deadline":
				call.Error = "context deadline exceeded"
			}
		}
		history = append(history, call)
	}
	return history
}

func pipelineSegmentedSave(t *testing.T, h *pipelineHarness, name string, snapshot pipelineHarnessSnapshot) {
	t.Helper()
	pipelineHarnessSaveJSON(t, name+"/state.json", snapshot)
	for label, file := range snapshot.Files {
		if file.Exists {
			pipelineHarnessSave(t, name+"/"+label+".bin", file.Bytes)
		}
	}
	if root := os.Getenv("YAKUORI_PIPELINE_EVIDENCE"); root != "" {
		// A real independent SQLite artifact, not merely a serialized row list.
		if _, err := h.fixture.db.Exec("VACUUM INTO ?", filepath.Join(root, name, "tm.sqlite")); err != nil {
			t.Fatal(err)
		}
	}
}

func pipelineSegmentedRerun(t *testing.T, h *pipelineHarness, exp pipelineHarnessExpectation, tc pipelineSegmentedCase) {
	t.Helper()
	f := h.fixture
	f.backend.callbacks.Generate = func(_ context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
		return segmentedStop(r), nil
	}
	wantRows, wantOut, wantGenerate := f.expectedRows(t, f.input.units), f.input.target, 3
	wantPhase, wantCause := "", ""
	if tc.cause == nil {
		wantGenerate = 0
	} else if h.surface == "file" {
		wantRows, wantOut, wantGenerate = slices.Clone(f.d0), nil, 0
		wantPhase, wantCause = "prepare", "publication-recovery"
	} else if tc.name == "all-admission" {
		// No injected backend fault can be removed from an indivisible input.
		// Retrying it must repeat admission refusal, never relax the budget.
		wantRows, wantOut, wantGenerate = slices.Clone(f.d0), nil, 0
		wantPhase, wantCause = "plan", "other"
	}
	pipelineHarnessSaveJSON(t, t.Name()+"/rerun-expected.json", struct {
		Rows          []pipelineFixtureRow
		Stdout        []byte
		Generate      int
		Phase, Cause  string
		SourceBytes   []byte
		Output        []byte
		OutputPresent bool
	}{wantRows, wantOut, wantGenerate, wantPhase, wantCause, exp.SourceBytes, exp.OutputBytes, exp.OutputPresent})
	before := len(f.backend.history)
	var out []byte
	var result Result
	var err error
	if h.surface == "file" {
		result, err = f.core.FileWithTM(t.Context(), h.options, f.adapter, f.profile, f.store, f.scope)
	} else {
		writer, openErr := os.OpenFile(h.stdoutPath+".rerun", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if openErr != nil {
			t.Fatal(openErr)
		}
		result, err = f.core.TextWithTM(t.Context(), f.input.source, f.adapter, f.profile, f.store, f.scope, writer)
		if closeErr := writer.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		var readErr error
		out, readErr = os.ReadFile(h.stdoutPath + ".rerun")
		if readErr != nil {
			t.Fatal(readErr)
		}
	}
	phase := ""
	var typed *Error
	if errors.As(err, &typed) {
		phase = typed.Phase
	}
	if phase != wantPhase || pipelinePrecommitCauseClass(err) != wantCause || result.TMCommitted != (wantPhase == "") {
		t.Fatalf("segmented-rerun: result=%+v err=%v want phase=%s cause=%s", result, err, wantPhase, wantCause)
	}
	if tc.name == "all-admission" && h.surface == "stdout" && !errors.Is(err, segment.ErrContextLimit) {
		t.Fatalf("rerun lost admission cause: %v", err)
	}
	if h.surface == "stdout" && !bytes.Equal(out, wantOut) {
		t.Fatalf("rerun did not deliver the authored whole output: bytes=%d want=%d", len(out), len(wantOut))
	}
	actualRows := pipelineFixtureReadRows(t, f.db)
	if !slices.Equal(actualRows, wantRows) {
		t.Fatalf("rerun rows differ from authored D0/D1: %v", pipelineFixtureDiff(wantRows, actualRows))
	}
	history := slices.Clone(f.backend.history[before:])
	wantHistory := pipelineSegmentedHistory(f.backend.info.Backend, pipelineSegmentedCase{name: tc.name, generate: wantGenerate})
	if h.surface == "file" && tc.cause != nil {
		wantHistory = nil
	}
	for i := range wantHistory {
		wantHistory[i].Ordinal += before
		if wantHistory[i].Operation == "generate" {
			wantHistory[i].Finish, wantHistory[i].Error = string(inference.Stop), ""
		}
	}
	if !slices.EqualFunc(history, wantHistory, func(a, b pipelineBackendCall) bool { return reflect.DeepEqual(a, b) }) {
		t.Fatalf("segmented-rerun-history: actual=%+v want=%+v", history, wantHistory)
	}
	currentSource, readErr := pipelineHarnessReadFile(h.sourcePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if problems := pipelineHarnessCheckCaptured("rerun-source", currentSource, &h.beforeSource, f.input.source); len(problems) != 0 {
		t.Fatal(problems)
	}
	var output pipelineHarnessFile
	artifacts := map[string]pipelineHarnessFile{}
	if h.surface == "file" {
		output, readErr = pipelineHarnessReadFile(h.options.Output)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if tc.cause != nil {
			if output.Exists != exp.OutputPresent || (output.Exists && len(pipelineHarnessCheckCaptured("rerun-output", output, &h.beforeOutput, exp.OutputBytes)) != 0) {
				t.Fatal("rerun changed the refused output")
			}
		} else if !bytes.Equal(output.Bytes, f.input.target) {
			t.Fatal("happy rerun output differs")
		}
		for label, path := range map[string]string{"stage": h.ops.paths.Stage, "record": h.ops.paths.Record, "backup": h.ops.paths.Backup} {
			file, readErr := pipelineHarnessReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			artifacts[label] = file
			if tc.cause != nil && file.Exists {
				t.Fatalf("rerun created forbidden %s", label)
			}
		}
	}
	pipelineHarnessSaveJSON(t, t.Name()+"/rerun-actual.json", struct {
		Result         Result
		Rows           []pipelineFixtureRow
		Stdout         []byte
		Source, Output pipelineHarnessFile
		History        []pipelineBackendCall
		Phase, Cause   string
		Artifacts      map[string]pipelineHarnessFile
	}{result, actualRows, out, currentSource, output, history, phase, pipelinePrecommitCauseClass(err), artifacts})
	if h.surface == "stdout" {
		pipelineHarnessSave(t, t.Name()+"/rerun-stdout.bin", out)
	}
	if root := os.Getenv("YAKUORI_PIPELINE_EVIDENCE"); root != "" {
		if _, err := f.db.Exec("VACUUM INTO ?", filepath.Join(root, t.Name(), "rerun-tm.sqlite")); err != nil {
			t.Fatal(err)
		}
	}
}
