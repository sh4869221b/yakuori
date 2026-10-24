//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/segment"
	"github.com/sh4869221b/yakuori/internal/unit"
)

type pipelineInferenceRequestRecord struct {
	Operation string
	Text      string
	Tokens    []int
	Spans     []inference.PromptSpan
	Identity  inference.RequestIdentity
	Policy    inference.EffectivePolicy
}

func TestPipelinePrecommitInference(t *testing.T) {
	primary := []string{
		"happy", "builder-error", "builder-cancel", "count-error", "count-cancel",
		"effective-equal", "effective-plus-one", "policy-over-limit", "foreign-request",
		"partial-plus-error", "max-tokens", "context-limit", "timeout-finish",
		"canceled-finish", "decode-error", "invalid-output", "unknown-finish",
		"prompt-count-mismatch", "deadline", "cancel-after-stop", "core-deadline",
	}
	for _, backend := range []string{"cpu", "cuda"} {
		for _, spec := range []pipelineHarnessCase{
			{kind: "text", surface: "file", mode: publication.Replace, outputPresent: true},
			{kind: "w3", surface: "file", mode: publication.Replace, outputPresent: true},
			{kind: "text", surface: "stdout"},
		} {
			for _, row := range primary {
				pipelineInferenceLeaf(t, backend, spec, row)
			}
		}
		w3 := pipelineHarnessCase{kind: "w3", surface: "file", mode: publication.Replace, outputPresent: true}
		for _, row := range []string{"builder-error", "builder-cancel", "count-error", "count-cancel", "effective-plus-one"} {
			pipelineInferenceLeaf(t, backend, w3, "later-"+row)
			pipelineInferenceLeaf(t, backend, w3, "all-hit-later-"+row)
		}
		for _, row := range []string{"multiunit-first", "multiunit-middle", "multiunit-last", "multiunit-hit-middle"} {
			pipelineInferenceLeaf(t, backend, w3, row)
		}
		for _, kind := range []string{"text", "w3"} {
			for _, mode := range []publication.Mode{publication.Create, publication.Replace, publication.InPlace} {
				for _, row := range []string{"builder-error", "partial-plus-error"} {
					pipelineInferenceLeaf(t, backend, pipelineHarnessCase{kind: kind, surface: "file", mode: mode}, row)
				}
			}
		}
	}
}

func pipelineInferenceLeaf(t *testing.T, backend string, spec pipelineHarnessCase, row string) {
	t.Helper()
	mode := "none"
	if spec.surface == "file" {
		switch spec.mode {
		case publication.Create:
			mode = "create"
		case publication.Replace:
			mode = "replace-absent"
			if spec.outputPresent {
				mode = "replace-existing"
			}
		case publication.InPlace:
			mode = "inplace"
		}
	}
	spec.name = backend + "-" + spec.kind + "-" + spec.surface + "-" + mode + "-" + row
	t.Run(spec.name, func(t *testing.T) { pipelineInferenceExercise(t, backend, spec, row) })
}

func pipelineInferenceExercise(t *testing.T, backend string, spec pipelineHarnessCase, row string) {
	t.Helper()
	// Given: the stimulus, target unit, request vectors and every expected row
	// are authored before Core runs. No observed history determines an expectation.
	fault, target, seed := row, 0, "unrelated"
	switch {
	case strings.HasPrefix(row, "all-hit-later-"):
		fault, target, seed = strings.TrimPrefix(row, "all-hit-later-"), 2, "all-hit"
	case strings.HasPrefix(row, "later-"):
		fault, target = strings.TrimPrefix(row, "later-"), 2
	case row == "multiunit-first":
		fault = "partial-plus-error"
	case row == "multiunit-middle":
		fault, target = "partial-plus-error", 1
	case row == "multiunit-last":
		fault, target = "partial-plus-error", 2
	case row == "multiunit-hit-middle":
		fault, target, seed = "partial-plus-error", 2, "target-hit"
	}
	success := fault == "happy" || fault == "effective-equal"
	admission := slices.Contains([]string{"builder-error", "builder-cancel", "count-error", "count-cancel", "effective-plus-one", "policy-over-limit", "foreign-request"}, fault)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	active := true
	var built []string
	var injected []string
	var entries []pipelineFixtureUnit
	var requests []inference.GenerationRequest
	var deadlineScope string
	var generatedResults []inference.GenerationResult
	var actualRequests []pipelineInferenceRequestRecord
	h := newPipelineHarnessWith(t, spec, true, func(f *pipelineFixture) {
		if backend == "cuda" {
			f.backend.info = pipelineControlledCUDAInfo(f.backend.info)
		}
		if fault == "effective-equal" || fault == "effective-plus-one" {
			f.backend.info.ContextTokens = 5
		}
		if fault == "core-deadline" {
			f.limits.GenerationTimeout = time.Second
		}
		if seed == "target-hit" {
			f.seedKind = "target-hit"
		}
		for _, entry := range f.input.units {
			if entry.source == "" {
				continue
			}
			entries = append(entries, entry)
			tokens, budget := []int{1, 2, 3}, 2
			info := f.backend.info
			if len(entries)-1 == target {
				switch fault {
				case "effective-plus-one":
					tokens = []int{1, 2, 3, 4}
				case "policy-over-limit":
					budget = f.limits.MaxOutputTokens + 1
				case "foreign-request":
					info.ModelSHA256 = [32]byte{69}
				}
			}
			requests = append(requests, pipelineInferenceRequest(t, info, entry.source, tokens, budget))
		}
		f.setRequestBuilder(func(callCtx context.Context, id unit.UnitID, text string) (inference.GenerationRequest, error) {
			if !active {
				return pipelineRequest(f.backend.info, text)
			}
			built = append(built, id.StableID())
			for i, entry := range entries {
				if entry.stableID != id.StableID() {
					continue
				}
				if text != entry.source {
					t.Fatal("request-builder input differs from authored source")
				}
				if i == target && (fault == "builder-error" || fault == "builder-cancel") {
					injected = append(injected, row)
					if fault == "builder-cancel" {
						cancel()
						return inference.GenerationRequest{}, callCtx.Err()
					}
					return inference.GenerationRequest{}, pipelinePrecommitGenerateFault
				}
				if i == target && (fault == "effective-plus-one" || fault == "policy-over-limit" || fault == "foreign-request") {
					injected = append(injected, row)
				}
				return requests[i], nil
			}
			t.Fatal("builder received an unknown or empty unit")
			return inference.GenerationRequest{}, ErrInferenceProfile
		})
	})
	f := h.fixture
	d0, d1 := pipelineInferenceSeed(t, f, seed)
	// The shared fixture's readback D0 is checked, then replaced by independently
	// authored values before shared expectation helpers consume it.
	f.d0 = slices.Clone(d0)
	targetID := entries[target].stableID
	requestFor := func(operation string, request inference.GenerationRequest) int {
		actualRequests = append(actualRequests, pipelineInferenceRequestRecord{
			operation, request.RenderedPrompt(), request.TokenIDs(), request.Spans(), request.Identity(), request.Policy(),
		})
		for i, want := range requests {
			if request.RenderedPrompt() == entries[i].source {
				if !reflect.DeepEqual(request, want) {
					t.Fatal("request-preservation: full request, identity, spans, token IDs or effective policy changed")
				}
				return i
			}
		}
		t.Fatal("backend received unknown request")
		return -1
	}
	f.backend.callbacks.CountTokens = func(callCtx context.Context, request inference.GenerationRequest) (int, error) {
		i := requestFor("count", request)
		if i == target && (fault == "count-error" || fault == "count-cancel") {
			injected = append(injected, row)
			if fault == "count-cancel" {
				cancel()
				return 0, callCtx.Err()
			}
			return 0, pipelinePrecommitGenerateFault
		}
		return len(request.TokenIDs()), callCtx.Err()
	}
	finish, callbackError := inference.Stop, ""
	wantCause, causeClass := error(nil), ""
	if !success {
		wantCause, causeClass = ErrIncompleteGeneration, "other"
		switch fault {
		case "builder-error", "count-error", "partial-plus-error":
			wantCause, causeClass = pipelinePrecommitGenerateFault, "contract-generate-fault"
			callbackError = pipelinePrecommitGenerateFault.Error()
		case "builder-cancel", "count-cancel", "cancel-after-stop":
			wantCause, causeClass = context.Canceled, "context-canceled"
			if fault != "cancel-after-stop" {
				callbackError = context.Canceled.Error()
			}
		case "effective-plus-one":
			wantCause = segment.ErrContextLimit
		case "policy-over-limit":
			wantCause = inference.ErrInvalidPolicy
		case "foreign-request":
			wantCause = ErrInferenceProfile
		case "max-tokens":
			finish = inference.MaxTokens
		case "context-limit":
			finish = inference.ContextLimit
		case "timeout-finish":
			finish = inference.Timeout
		case "canceled-finish":
			finish = inference.Canceled
		case "decode-error":
			finish = inference.DecodeError
		case "invalid-output":
			finish = inference.InvalidOutput
		case "unknown-finish":
			finish = inference.Finish("future")
		case "deadline", "core-deadline":
			finish, wantCause, causeClass = inference.Timeout, context.DeadlineExceeded, "deadline-exceeded"
			callbackError = context.DeadlineExceeded.Error()
		}
	}
	f.backend.callbacks.Generate = func(callCtx context.Context, request inference.GenerationRequest) (got inference.GenerationResult, gotErr error) {
		defer func() { generatedResults = append(generatedResults, got) }()
		i := requestFor("generate", request)
		result := inference.GenerationResult{Text: entries[i].translation, Finish: inference.Stop, PromptTokens: len(request.TokenIDs()), OutputTokens: 2}
		if success || i != target {
			return result, callCtx.Err()
		}
		injected = append(injected, row)
		result.Finish = finish
		switch fault {
		case "partial-plus-error":
			return result, pipelinePrecommitGenerateFault
		case "prompt-count-mismatch":
			result.PromptTokens++
		case "cancel-after-stop":
			cancel()
		case "deadline":
			// The Engine applies the request's policy timeout. Core's longer
			// request/generation context must remain live when this one expires.
			requestCtx, stop := context.WithTimeout(callCtx, request.Policy().RequestTimeout)
			defer stop()
			<-requestCtx.Done()
			if !errors.Is(requestCtx.Err(), context.DeadlineExceeded) || callCtx.Err() != nil {
				t.Fatal("request timeout did not originate in the Engine policy context")
			}
			deadlineScope = "engine-request-policy"
			return result, requestCtx.Err()
		case "core-deadline":
			// No Engine timer: the real Core generation budget cancels this
			// request while the caller's context is still live.
			<-callCtx.Done()
			if !errors.Is(callCtx.Err(), context.DeadlineExceeded) || ctx.Err() != nil {
				t.Fatal("deadline did not originate in Core's generation context")
			}
			deadlineScope = "core-generation"
			return result, callCtx.Err()
		}
		return result, nil
	}
	buildCount, countCount, generateCount := len(entries), len(entries), len(entries)
	firstGenerate := 0
	if seed == "target-hit" {
		firstGenerate = 1
	}
	if !success {
		generateCount = target - firstGenerate + 1
		if admission {
			buildCount, countCount, generateCount = target+1, target+1, 0
			if fault == "builder-error" || fault == "builder-cancel" || fault == "policy-over-limit" {
				countCount--
			}
		}
	}
	phase := "generate"
	if admission {
		phase = "plan"
	}
	exp := pipelinePrecommitPublishedExpectation(t, spec, f)
	exp.D0, exp.D1, exp.FinalRows = d0, d1, d1
	if !success {
		exp = pipelinePrecommitFailureExpectation(t, spec, f, pipelinePrecommitCase{
			mode: spec.mode, outputPresent: spec.outputPresent, generate: generateCount,
			errorPhase: phase, errorUnit: targetID, causeClass: causeClass, injection: row,
		})
		exp.D0, exp.D1, exp.FinalRows = d0, d1, d0
		exp.StdoutBytes = nil
		if spec.surface == "stdout" {
			exp.Outcome = "stdout-not-delivered"
		}
		exp.BoundaryOrder = nil
		exp.RequiredBoundaries = map[string]int{}
		// Explicitly require every downstream operation to remain unreached.
		for _, name := range []string{
			"generate.before", "generate.after", "stage.write.before", "stage.write.after",
			"stage.sync.before", "stage.sync.after", "stage.close.before", "stage.close.after",
			"observe.before", "observe.after", "observe.entry.writer-closed", "compare-final.after",
			"tm-commit.before", "tm-commit.after", "record.write.before", "record.write.after",
			"record.sync.before", "record.sync.after", "record.close.before", "record.close.after",
			"record.install.before", "record.install.after", "backup-rename.before", "backup-rename.after",
			"publish-rename.before", "publish-rename.after", "stdout.write.before", "stdout.write.after",
			"diagnostic.write.before", "diagnostic.write.after", "run.close.after",
		} {
			exp.RequiredBoundaries[name] = 0
		}
		for i := range generateCount {
			exp.BoundaryOrder = append(exp.BoundaryOrder, "generate.before")
			exp.RequiredBoundaries["generate.before"]++
			if i != generateCount-1 || callbackError == "" {
				exp.BoundaryOrder = append(exp.BoundaryOrder, "generate.after")
				exp.RequiredBoundaries["generate.after"]++
			}
		}
		if spec.surface == "file" {
			exp.BoundaryOrder = append(exp.BoundaryOrder, "run.close.after")
			exp.RequiredBoundaries["run.close.after"] = 1
		}
	}
	exp.RerunApplicable, exp.RerunOutcome = true, "succeeded"
	if !success && spec.surface == "file" {
		exp.RerunOutcome, exp.RerunPhase, exp.RerunCause = "typed-rejected", "prepare", "publication-recovery"
	}
	if !success && spec.surface == "stdout" {
		exp.RerunGenerate = len(entries)
	}
	// Independently enumerate the backend's expected operation sequence.
	wantHistory := []pipelineBackendCall{{Operation: "info", Ordinal: 1, Backend: backend}}
	for i := range countCount {
		call := pipelineInferenceCall("count", len(wantHistory)+1, backend, requests[i])
		if i == target && (fault == "count-error" || fault == "count-cancel") {
			call.Error = callbackError
		}
		wantHistory = append(wantHistory, call)
	}
	for i := range generateCount {
		index := firstGenerate + i
		call := pipelineInferenceCall("generate", len(wantHistory)+1, backend, requests[index])
		call.Finish = string(inference.Stop)
		if !success && index == target {
			call.Finish, call.Error = string(finish), callbackError
		}
		wantHistory = append(wantHistory, call)
	}
	if !success {
		h.stimulus = row
		h.requestedBoundary, h.requestedOccurrence = "generate.before", generateCount
		if admission {
			h.requestedBoundary, h.requestedOccurrence = "request.build", target+1
			if fault == "count-error" || fault == "count-cancel" {
				h.requestedBoundary = "request.count"
			}
		}
	}
	var wantBuilt []string
	for i := range buildCount {
		wantBuilt = append(wantBuilt, entries[i].stableID)
	}
	dir := filepath.Join("inference", spec.name)
	pipelineHarnessSaveJSON(t, filepath.Join(dir, "expected.json"), exp)
	pipelineHarnessSaveJSON(t, filepath.Join(dir, "expected-backend.json"), wantHistory)
	pipelineHarnessSaveJSON(t, filepath.Join(dir, "expected-build-units.json"), wantBuilt)
	for i, request := range requests {
		pipelineHarnessSaveJSON(t, filepath.Join(dir, fmt.Sprintf("expected-request-%d.json", i)), pipelineInferenceRequestRecord{
			"", request.RenderedPrompt(), request.TokenIDs(), request.Spans(), request.Identity(), request.Policy(),
		})
	}
	beforeSource, err := pipelineHarnessReadFile(h.sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	// When: real Core, real text/W3 Adapter, SQLite Run and publication.
	result, runErr := h.execute(ctx)
	h.injected = slices.Clone(injected)
	snapshot, err := h.snapshot(exp)
	if err != nil {
		t.Fatal(err)
	}
	if spec.surface == "stdout" {
		snapshot.BeforeSource = &beforeSource
	}
	pipelineHarnessSaveJSON(t, filepath.Join(dir, "actual-before-rerun.json"), snapshot)
	pipelineHarnessSaveJSON(t, filepath.Join(dir, "actual-generation-results.json"), generatedResults)
	pipelineHarnessSaveJSON(t, filepath.Join(dir, "actual-requests.json"), actualRequests)
	pipelineHarnessSaveJSON(t, filepath.Join(dir, "actual-build-units.json"), built)
	for label, file := range snapshot.Files {
		if file.Exists {
			pipelineHarnessSave(t, filepath.Join(dir, "first-"+label+".bin"), file.Bytes)
		}
	}
	pipelineInferenceSaveDB(t, f, filepath.Join(dir, "first.db"))
	// Then: a typed rejection never becomes an accepted subset.
	if success {
		if runErr != nil || !result.TMCommitted {
			t.Fatalf("positive control: result=%+v error=%v unsupported_filesystem=%t", result, runErr, errors.Is(runErr, publication.ErrUnsupportedFilesystem))
		}
	} else {
		var typed *Error
		if !errors.Is(runErr, wantCause) || !errors.As(runErr, &typed) || typed.Phase != phase || typed.UnitID.StableID() != targetID {
			t.Fatalf("typed rejection: error=%v want phase=%s unit=%s cause=%v", runErr, phase, targetID, wantCause)
		}
		if result.TMCommitted || result.BytesWritten != 0 || (spec.surface == "file" && result.Publication.State != publication.NotPublished) {
			t.Fatalf("failure accepted partial effects: %+v", result)
		}
	}
	if !reflect.DeepEqual(wantHistory, snapshot.BackendCalls) || !slices.Equal(built, wantBuilt) {
		t.Fatalf("backend-history: got=%+v want=%+v built=%v want=%v", snapshot.BackendCalls, wantHistory, built, wantBuilt)
	}
	if fault == "deadline" && deadlineScope != "engine-request-policy" || fault == "core-deadline" && deadlineScope != "core-generation" {
		t.Fatal("deadline scope was not reached")
	}
	// Rerun removes only the fault, retaining file run state. The closure sees
	// healthy requests on a fresh context; it never reuses the canceled one.
	active = false
	f.backend.callbacks = pipelineBackendCallbacks{}
	pipelineInferenceRerun(t, h, &snapshot, exp, dir, success, len(wantHistory))
	pipelineHarnessSaveJSON(t, filepath.Join(dir, "actual.json"), snapshot)
	pipelineHarnessSaveJSON(t, filepath.Join(dir, "deadline.json"), struct{ Scope string }{deadlineScope})
	if problems := pipelinePrecommitJudge(exp, snapshot); len(problems) != 0 {
		t.Fatalf("precommit-state: %s", strings.Join(problems, "; "))
	}
	t.Logf("real-core-state backend=%s row=%s generate=%d count=%d D0=%d D1=%d rerun=%s/%d", backend, row, generateCount, countCount, len(d0), len(d1), snapshot.RerunOutcome, snapshot.RerunGenerate)
}

func pipelineInferenceRequest(t *testing.T, info inference.ModelInfo, text string, tokens []int, budget int) inference.GenerationRequest {
	t.Helper()
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, budget, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request, err := inference.NewGenerationRequest(inference.PreparedRequest{
		RenderedPrompt: text, Spans: []inference.PromptSpan{{Text: text}}, TokenIDs: tokens, StopIDs: []int{42},
		Identity: inference.RequestIdentity{
			ModelSHA256: info.ModelSHA256, Tokenizer: info.Tokenizer, Template: info.Template,
			PromptSchema: info.Prompt.Schema, Prompt: info.Prompt, Thinking: info.Thinking,
		},
	}, policy)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func pipelineInferenceCall(operation string, ordinal int, backend string, request inference.GenerationRequest) pipelineBackendCall {
	p := request.Policy()
	return pipelineBackendCall{
		Operation: operation, Ordinal: ordinal, Backend: backend, Request: request.RenderedPrompt(), TokenIDs: request.TokenIDs(),
		Policy: pipelineBackendPolicyRecord{Schema: p.Schema, MaxOutputTokens: p.MaxOutputTokens, RequestTimeoutNS: int64(p.RequestTimeout), Seed: p.Seed, SeedFixed: p.SeedFixed, StopIDs: p.StopIDs},
	}
}

func pipelineInferenceSeed(t *testing.T, f *pipelineFixture, seed string) ([]pipelineFixtureRow, []pipelineFixtureRow) {
	t.Helper()
	var d0 []pipelineFixtureRow
	switch seed {
	case "target-hit":
		d0 = []pipelineFixtureRow{f.authoredRow(t, f.input.units[0])}
	case "all-hit":
		if _, err := f.db.Exec("DELETE FROM tm"); err != nil {
			t.Fatal(err)
		}
		for _, entry := range f.input.units {
			row := f.authoredRow(t, entry)
			d0 = append(d0, row)
			if _, err := f.db.Exec("INSERT INTO tm VALUES(?,?,?,?)", row.Schema, pipelineFixtureKeyBytes(t, row.Source), pipelineFixtureKeyBytes(t, row.Profile), row.Text); err != nil {
				t.Fatal(err)
			}
		}
	default:
		authored := pipelineAuthoredSession(t, pipelineFixtureInput{
			adapterName: "text", formatSchema: "text-v1", language: "en", source: []byte("Unrelated.\n"),
			units: []pipelineFixtureUnit{{stableID: "document", source: "Unrelated.\n"}},
		})
		source, err := unit.NewSourceIdentity(authored, authored.Units()[0].ID(), f.scope.GameNamespace, "unrelated", nil)
		if err != nil {
			t.Fatal(err)
		}
		key, err := f.profile.ExactKey(source)
		if err != nil {
			t.Fatal(err)
		}
		d0 = []pipelineFixtureRow{{Schema: key.Schema(), Source: hex.EncodeToString(key.SourceBytes()), Profile: hex.EncodeToString(key.ProfileBytes()), Text: "無関係。\n"}}
	}
	d1 := slices.Clone(d0)
	for _, entry := range f.input.units {
		row := f.authoredRow(t, entry)
		if !slices.Contains(d1, row) {
			d1 = append(d1, row)
		}
	}
	for _, rows := range [][]pipelineFixtureRow{d0, d1} {
		slices.SortFunc(rows, func(a, b pipelineFixtureRow) int {
			if cmp := strings.Compare(a.Source, b.Source); cmp != 0 {
				return cmp
			}
			return strings.Compare(a.Profile, b.Profile)
		})
	}
	if actual := pipelineFixtureReadRows(t, f.db); !slices.Equal(actual, d0) {
		t.Fatal("seed all-column readback differs from independently authored D0")
	}
	return d0, d1
}

func pipelineInferenceSaveDB(t *testing.T, f *pipelineFixture, name string) {
	t.Helper()
	if root := os.Getenv("YAKUORI_PIPELINE_EVIDENCE"); root != "" {
		// VACUUM INTO produces a consistent real SQLite artifact, not a JSON
		// reconstruction or a copy of a live database without its WAL.
		path := filepath.Join(root, name)
		if _, err := f.db.Exec("VACUUM INTO '" + strings.ReplaceAll(path, "'", "''") + "'"); err != nil {
			t.Fatal(err)
		}
	}
}

func pipelineInferenceRerun(t *testing.T, h *pipelineHarness, first *pipelineHarnessSnapshot, exp pipelineHarnessExpectation, dir string, success bool, prefix int) {
	t.Helper()
	f := h.fixture
	before := len(f.backend.history)
	beforeGenerate := f.backend.operationCount("generate")
	var result Result
	var err error
	var out bytes.Buffer
	if h.surface == "stdout" {
		result, err = f.core.TextWithTM(t.Context(), f.input.source, f.adapter, f.profile, f.store, f.scope, &out)
	} else {
		result, err = f.core.FileWithTM(t.Context(), h.options, f.adapter, f.profile, f.store, f.scope)
	}
	first.RerunGenerate = f.backend.operationCount("generate") - beforeGenerate
	first.RerunCause = pipelinePrecommitCauseClass(err)
	first.RerunOutcome = "succeeded"
	if err != nil {
		first.RerunOutcome = "typed-rejected"
		var typed *Error
		if !errors.As(err, &typed) {
			t.Fatalf("rerun error is untyped: %v", err)
		}
		first.RerunPhase = typed.Phase
	}
	wantRows := exp.D1
	if !success && h.surface == "file" {
		wantRows = exp.D0
		var recovery *publication.RecoveryError
		if !errors.As(err, &recovery) || recovery.Reason != "invalid or missing operation record" || !errors.Is(err, os.ErrNotExist) || result.TMCommitted {
			t.Fatalf("retained file run did not refuse recovery: result=%+v error=%v", result, err)
		}
	} else if err != nil || !result.TMCommitted {
		t.Fatalf("fresh-context rerun failed: result=%+v error=%v", result, err)
	}
	rows := pipelineFixtureReadRows(t, f.db)
	if !slices.Equal(rows, wantRows) {
		t.Fatal("rerun all-column rows differ from authored D0/D1")
	}
	if h.surface == "stdout" && (!bytes.Equal(out.Bytes(), exp.TargetBytes) || result.BytesWritten != len(exp.TargetBytes)) {
		t.Fatal("rerun stdout differs from the complete authored target bytes")
	}
	files := map[string]pipelineHarnessFile{}
	for label, old := range first.Files {
		current, readErr := pipelineHarnessReadFile(old.Path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		files[label] = current
		if current.Exists {
			pipelineHarnessSave(t, filepath.Join(dir, "rerun-"+label+".bin"), current.Bytes)
		}
		if success && label == "output" {
			if !bytes.Equal(current.Bytes, exp.TargetBytes) {
				t.Fatal("successful rerun output differs from authored target")
			}
			continue
		}
		if label == "source" || label == "output" || label == "backup" {
			if !reflect.DeepEqual(old, current) {
				t.Fatalf("rerun changed preserved %s bytes or identity", label)
			}
		}
	}
	var wantCalls []pipelineBackendCall
	if h.surface == "stdout" || success {
		wantCalls = append(wantCalls, pipelineBackendCall{Operation: "info", Ordinal: prefix + 1, Backend: f.backend.info.Backend})
		for _, entry := range f.input.units {
			if entry.source == "" {
				continue
			}
			request := pipelineInferenceRequest(t, f.backend.info, entry.source, []int{1, 2, 3}, 2)
			wantCalls = append(wantCalls, pipelineInferenceCall("count", prefix+len(wantCalls)+1, f.backend.info.Backend, request))
		}
		if !success {
			for _, entry := range f.input.units {
				if entry.source == "" {
					continue
				}
				request := pipelineInferenceRequest(t, f.backend.info, entry.source, []int{1, 2, 3}, 2)
				call := pipelineInferenceCall("generate", prefix+len(wantCalls)+1, f.backend.info.Backend, request)
				call.Finish = string(inference.Stop)
				wantCalls = append(wantCalls, call)
			}
		}
	}
	actualCalls := f.backend.history[before:]
	if len(actualCalls) != len(wantCalls) || len(wantCalls) > 0 && !reflect.DeepEqual(actualCalls, wantCalls) {
		t.Fatalf("rerun backend history=%+v want=%+v", actualCalls, wantCalls)
	}
	pipelineHarnessSaveJSON(t, filepath.Join(dir, "expected-rerun-backend.json"), wantCalls)
	pipelineHarnessSave(t, filepath.Join(dir, "rerun-returned-stdout.bin"), out.Bytes())
	pipelineHarnessSaveJSON(t, filepath.Join(dir, "rerun.json"), struct {
		Result  Result
		Error   string
		Rows    []pipelineFixtureRow
		Stdout  []byte
		Files   map[string]pipelineHarnessFile
		Backend []pipelineBackendCall
	}{result, fmt.Sprint(err), rows, out.Bytes(), files, actualCalls})
	pipelineInferenceSaveDB(t, f, filepath.Join(dir, "rerun.db"))
}
