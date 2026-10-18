//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/segment"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
	"golang.org/x/sys/unix"
	sqlite "modernc.org/sqlite"
)

// This file owns the whole-Core error/cancel oracle. The two allowed task2
// files deliberately keep its matrix together; the shared fixture is frozen.
type pipelineFaultScenario struct {
	name, row              string
	recordPhase, operation string
	stimulus               string
	inPlaceOnly, laterOnly bool
}

func pipelineFaultScenarios() []pipelineFaultScenario {
	cases := []pipelineFaultScenario{{name: "success", row: "success"}}
	for _, name := range []string{
		"generate-error", "partial-error", "max_tokens", "context_limit", "timeout",
		"canceled", "decode_error", "invalid_output", "unknown-finish",
		"prompt-count-mismatch", "invalid-candidate", "builder-error", "count-error",
		"context-admission", "profile-mismatch", "cancel-admission",
		"cancel-generate-before", "cancel-generate-after",
	} {
		cases = append(cases, pipelineFaultScenario{name: name, row: "M01"})
	}
	cases = append(cases, pipelineFaultScenario{name: "later-generate-error", row: "M01", laterOnly: true})
	for _, name := range []string{"stage-collision", "export-prefix-error", "cancel-export-before", "cancel-export-after"} {
		cases = append(cases, pipelineFaultScenario{name: name, row: "M02"})
	}
	for _, name := range []string{"stage-sync-error", "stage-close-error", "cancel-sync-before", "cancel-sync-after", "cancel-close-before", "cancel-close-after"} {
		cases = append(cases, pipelineFaultScenario{name: name, row: "M03"})
	}
	for _, name := range []string{"observe-error", "closed-stage-tamper", "input-manifest-mismatch", "tm-manifest-mismatch", "cancel-observe-before", "cancel-observe-after", "cancel-before-commit"} {
		cases = append(cases, pipelineFaultScenario{name: name, row: "M04"})
	}
	for _, name := range []string{"sqlite-abort", "sqlite-rollback", "sqlite-writer", "sqlite-reader", "sqlite-row-cancel", "conflict-miss", "conflict-valid-changed", "conflict-valid-removed", "conflict-invalid-changed", "conflict-invalid-removed"} {
		cases = append(cases, pipelineFaultScenario{name: name, row: "M05"})
	}
	cases = append(cases, pipelineFaultScenario{name: "cancel-publish-entry", row: "M07"})
	cases = append(cases, pipelineFaultScenario{name: "prepared-temp-collision", row: "M08"}, pipelineFaultScenario{name: "prepared-record-corrupt", row: "M08"})
	for _, name := range []string{"backup-collision", "backup-rename-error", "backup-rename-collision"} {
		cases = append(cases, pipelineFaultScenario{name: name, row: "M10", inPlaceOnly: true})
	}
	cases = append(cases, pipelineFaultScenario{name: "cancel-after-backup", row: "M11", inPlaceOnly: true})
	for _, name := range []string{"external-after-backup", "restore-error", "restore-collision"} {
		cases = append(cases, pipelineFaultScenario{name: name, row: "M12", inPlaceOnly: true})
	}
	for _, name := range []string{"publish-rename-error", "cancel-before-publish-rename"} {
		cases = append(cases, pipelineFaultScenario{name: name, row: "M13"})
	}
	for _, name := range []string{"cancel-after-publish-rename", "run-close-error"} {
		cases = append(cases, pipelineFaultScenario{name: name, row: "M14"})
	}
	for _, phase := range []string{"prepared", "source-backed-up", "published"} {
		for _, operation := range []string{"write", "sync", "close", "rename", "directory-sync"} {
			for _, stimulus := range []string{"error", "cancel-before", "cancel-after"} {
				row := "M08"
				switch phase {
				case "prepared":
					if stimulus != "error" {
						row = "M09"
					}
				case "source-backed-up":
					row = "M11"
				case "published":
					row = "M14"
				}
				cases = append(cases, pipelineFaultScenario{
					name: phase + "-" + operation + "-" + stimulus, row: row,
					recordPhase: phase, operation: operation, stimulus: stimulus,
					inPlaceOnly: phase == "source-backed-up",
				})
			}
		}
		row := "M08"
		switch phase {
		case "source-backed-up":
			row = "M11"
		case "published":
			row = "M14"
		}
		cases = append(cases, pipelineFaultScenario{
			name: phase + "-short-write", row: row, recordPhase: phase,
			operation: "write", stimulus: "short-write", inPlaceOnly: phase == "source-backed-up",
		})
	}
	return cases
}

type pipelineFaultMode struct {
	name string
	mode publication.Mode
}

func pipelineFaultModes() []pipelineFaultMode {
	return []pipelineFaultMode{{"create", publication.Create}, {"replace-absent", publication.Replace}, {"replace-existing", publication.Replace}, {"in-place", publication.InPlace}}
}

func TestPipelineFaultMatrix(t *testing.T) {
	for _, scenario := range pipelineFaultScenarios() {
		t.Run(scenario.name, func(t *testing.T) {
			for _, kind := range []string{"text", "w3"} {
				for _, mode := range pipelineFaultModes() {
					if scenario.inPlaceOnly && mode.mode != publication.InPlace {
						t.Logf("N/A %s/%s: no original backup in a separate-output mode", kind, mode.name)
						continue
					}
					if scenario.laterOnly && kind == "text" {
						t.Logf("N/A text/%s: the real text adapter has exactly one unit", mode.name)
						continue
					}
					t.Run(kind+"/"+mode.name, func(t *testing.T) {
						h := newPipelineFault(t, kind, mode)
						h.scenario = scenario
						h.exercise(t)
					})
				}
			}
		})
	}
	t.Log("N/A: Stage has no Flush; file Core has no stdout Write; M06 ambiguous COMMIT and real signals/process death belong to the transaction/process tasks, not simulated cancellation.")
}

// These wrappers always delegate the real adapter and prepared Run. Only the
// named boundary is controlled; no generic Stage-return failure is used.
type pipelineFaultAdapter struct {
	Adapter
	export                      func(io.Writer) io.Writer
	afterExport                 func(artifact.Manifest) (artifact.Manifest, error)
	beforeObserve, afterObserve func()
	exported                    []byte
	exports, observes           int
	observeErr                  error
}

func (a *pipelineFaultAdapter) Export(w io.Writer, s unit.Session, p validate.Profile, accepted []validate.AcceptedTranslation) (artifact.Manifest, error) {
	a.exports++
	if a.export != nil {
		w = a.export(w)
	}
	var capture bytes.Buffer
	manifest, err := a.Adapter.Export(io.MultiWriter(w, &capture), s, p, accepted)
	a.exported = bytes.Clone(capture.Bytes())
	if err == nil && a.afterExport != nil {
		return a.afterExport(manifest)
	}
	return manifest, err
}

func (a *pipelineFaultAdapter) Observe(r io.Reader) (artifact.FinalObservation, error) {
	a.observes++
	if a.beforeObserve != nil {
		a.beforeObserve()
	}
	result, err := a.Adapter.Observe(r)
	if a.afterObserve != nil {
		a.afterObserve()
	}
	return result, errors.Join(err, a.observeErr)
}

type pipelineFaultWriter func([]byte) (int, error)

func (w pipelineFaultWriter) Write(b []byte) (int, error) { return w(b) }

type pipelineFaultRun struct {
	publicationRun
	beforeStage, beforeCheck, afterStage, beforePublish func()
	closeErr                                            error
	publishes, closes                                   int
}

func (r *pipelineFaultRun) Stage(ctx context.Context, produce func(io.Writer) error, check func(io.Reader) error) error {
	if r.beforeStage != nil {
		r.beforeStage()
	}
	err := r.publicationRun.Stage(ctx, produce, func(reader io.Reader) error {
		if r.beforeCheck != nil {
			r.beforeCheck()
		}
		return check(reader)
	})
	if err == nil && r.afterStage != nil {
		r.afterStage()
	}
	return err
}

func (r *pipelineFaultRun) Publish(ctx context.Context) (publication.Result, error) {
	r.publishes++
	if r.beforePublish != nil {
		r.beforePublish()
	}
	return r.publicationRun.Publish(ctx)
}

func (r *pipelineFaultRun) Close() error {
	r.closes++
	return errors.Join(r.publicationRun.Close(), r.closeErr)
}

type pipelineFaultBackend struct {
	*pipelineBackend
	count    func(context.Context, inference.GenerationRequest) (int, error)
	generate func(context.Context, inference.GenerationRequest) (inference.GenerationResult, error)
}

func (b pipelineFaultBackend) CountTokens(ctx context.Context, r inference.GenerationRequest) (int, error) {
	if b.count != nil {
		return b.count(ctx, r)
	}
	return b.pipelineBackend.CountTokens(ctx, r)
}

func (b pipelineFaultBackend) Generate(ctx context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
	if b.generate != nil {
		return b.generate(ctx, r)
	}
	return b.pipelineBackend.Generate(ctx, r)
}

type pipelineFault struct {
	f           *pipelineFixture
	options     publication.Options
	mode        pipelineFaultMode
	scenario    pipelineFaultScenario
	adapter     *pipelineFaultAdapter
	run         *pipelineFaultRun
	reached     map[string]int
	cancel      func()
	ctx         context.Context
	want        error
	phase       string
	state       publication.State
	committed   bool
	rows        []pipelineTMRow
	before      map[string]pipelineFaultAsset
	wantStage   []byte
	stageFD     *os.File
	stageID     unix.Statx_t
	recordFDs   []*os.File
	recordName  string
	submitted   map[string][]byte
	installed   []byte
	holder      *sql.Conn
	watchFile   *os.File
	watchDone   <-chan error
	cancelCount int
}

func newPipelineFault(t *testing.T, kind string, mode pipelineFaultMode) *pipelineFault {
	t.Helper()
	f := newPipelineFixture(t, kind)
	options := f.options(t, mode.mode)
	if mode.name == "replace-existing" {
		writeFile(t, options.Output, []byte("old separate output\n"))
	}
	ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(stop)
	ctx, cancel := context.WithCancel(ctx)
	h := &pipelineFault{f: f, options: options, mode: mode, ctx: ctx,
		reached: make(map[string]int), submitted: make(map[string][]byte),
		rows: slices.Clone(f.initial), state: publication.NotPublished}
	ready, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	// Register the controller before Core can reach the requested cancellation.
	go func() {
		defer close(done)
		select {
		case <-ready:
			cancel()
			close(release)
		case <-ctx.Done():
		}
	}()
	h.cancel = func() {
		h.cancelCount++
		close(ready)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	t.Cleanup(func() { cancel(); <-done })
	h.adapter = &pipelineFaultAdapter{Adapter: f.adapter}
	h.run = &pipelineFaultRun{}
	// Encode authored translations before any Core fault or physical stage exists.
	// Never derive retained bytes from the rejected or potentially missing stage.
	session, err := f.adapter.Import(f.raw, f.profile)
	if err != nil {
		t.Fatal(err)
	}
	var accepted []validate.AcceptedTranslation
	for _, u := range session.Units() {
		translation, err := validate.Validate(session, u.ID(), f.profile, f.texts[u.ID().StableID()])
		if err != nil {
			t.Fatal(err)
		}
		accepted = append(accepted, translation)
	}
	var expected bytes.Buffer
	if _, err := f.adapter.Export(&expected, session, f.profile, accepted); err != nil {
		t.Fatal(err)
	}
	h.wantStage = bytes.Clone(expected.Bytes())
	pipelineSave(t, "expected-stage.bin", h.wantStage)
	return h
}

func (h *pipelineFault) exercise(t *testing.T) {
	t.Helper()
	// Given: authored source/profile keys, real Store, D0 including unrelated rows.
	h.configureGeneration(t)
	h.configureStage(t)
	h.configureDatabase(t)
	h.configurePublication(t)
	h.before = h.assets(t)
	h.f.capture(t, "before", pipelineState{Options: h.options})
	expected, err := json.MarshalIndent(h.rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	pipelineSave(t, "expected-before.json", expected)
	// When: the complete Core reaches the named real operation.
	result, err := h.f.core.fileWithTM(h.ctx, h.options, h.adapter, h.f.profile, nil,
		&tmExecution{store: h.f.store, scope: h.f.scope},
		func(ctx context.Context, options publication.Options) (publicationRun, error) {
			real, err := publication.PrepareWithLimits(ctx, options, h.f.limits)
			if err != nil {
				return nil, err
			}
			h.run.publicationRun = real
			h.f.paths = real.SetPipelineFaultHooks(h.hooks(t))
			h.reached["prepare"]++
			if h.scenario.name == "stage-collision" {
				writeFile(t, h.f.paths.Stage, []byte("owned stage collision\n"))
				if err := unix.Statx(unix.AT_FDCWD, h.f.paths.Stage, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &h.stageID); err != nil {
					t.Fatal(err)
				}
				h.reached["stage-collision-created"]++
			}
			return h.run, nil
		})
	if h.watchFile != nil {
		if closeErr := h.watchFile.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Fatal(closeErr)
		}
		if watchErr := <-h.watchDone; watchErr != nil {
			t.Fatalf("real transaction journal event not observed: %v", watchErr)
		}
		h.reached["real-transaction-journal-cancel"]++
	}
	if h.holder != nil {
		if _, rollbackErr := h.holder.ExecContext(t.Context(), "ROLLBACK"); rollbackErr != nil {
			t.Fatal(rollbackErr)
		}
		if closeErr := h.holder.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	// Then: exact state, not a count-only or fabricated publication oracle.
	h.assertResult(t, result, err)
	h.f.capture(t, "after", pipelineState{Options: h.options, Result: result})
	h.saveReport(t, result, err)
	h.rerun(t)
}

func (h *pipelineFault) configureGeneration(t *testing.T) {
	name := h.scenario.name
	backend := pipelineFaultBackend{pipelineBackend: h.f.backend}
	backend.count = func(ctx context.Context, r inference.GenerationRequest) (int, error) {
		h.reached["count"]++
		switch name {
		case "count-error":
			return 0, unix.EIO
		case "context-admission":
			return h.f.backend.info.ContextTokens, nil
		case "cancel-admission":
			h.cancel()
		}
		return h.f.backend.CountTokens(ctx, r)
	}
	backend.generate = func(ctx context.Context, request inference.GenerationRequest) (inference.GenerationResult, error) {
		h.reached["generate-entry"]++
		if name == "cancel-generate-before" {
			h.cancel()
		}
		result, err := h.f.backend.Generate(ctx, request)
		if err != nil {
			return result, err
		}
		switch name {
		case "generate-error":
			return inference.GenerationResult{}, unix.EIO
		case "partial-error":
			return inference.GenerationResult{Text: "partial sentinel", Finish: inference.Stop, PromptTokens: 3}, unix.EIO
		case "later-generate-error":
			if h.reached["generate-entry"] == 2 {
				return result, unix.EIO
			}
		case "max_tokens", "context_limit", "timeout", "canceled", "decode_error", "invalid_output":
			result.Finish = inference.Finish(name)
		case "unknown-finish":
			result.Finish = inference.Finish("task2-future")
		case "prompt-count-mismatch":
			result.PromptTokens = 2
		case "invalid-candidate":
			result.Text = "Translation: rejected candidate"
		case "cancel-generate-after":
			h.cancel()
		}
		return result, nil
	}
	info := h.f.backend.info
	var err error
	h.f.core, err = NewCoreWithLimits(NewInferenceEngine(backend, func(ctx context.Context, _ unit.UnitID, text string) (inference.GenerationRequest, error) {
		h.reached["build"]++
		if name == "builder-error" {
			return inference.GenerationRequest{}, unix.EIO
		}
		return pipelineRequest(info, text)
	}), h.f.limits)
	if err != nil {
		t.Fatal(err)
	}
	if name == "profile-mismatch" {
		h.f.backend.info.ComputeQuant = "different-from-canonical"
	}
	if h.scenario.row == "M01" {
		h.want, h.phase = ErrIncompleteGeneration, "generate"
		switch name {
		case "generate-error", "partial-error", "later-generate-error":
			h.want = unix.EIO
		case "builder-error", "count-error":
			h.want, h.phase = unix.EIO, "plan"
		case "context-admission":
			h.want, h.phase = segment.ErrContextLimit, "plan"
		case "profile-mismatch":
			h.want, h.phase = ErrInferenceProfile, "input"
		case "invalid-candidate":
			h.want, h.phase = validate.ErrInvalidCandidate, "validate"
		case "cancel-admission":
			h.want, h.phase = context.Canceled, "plan"
		case "cancel-generate-before", "cancel-generate-after":
			h.want = context.Canceled
		}
	}
}

func (h *pipelineFault) configureStage(t *testing.T) {
	name := h.scenario.name
	h.run.beforeCheck = func() {
		h.reached["closed-stage-check"]++
		if h.stageFD == nil {
			t.Fatal("check reached without an actual writer descriptor")
		}
		if _, err := h.stageFD.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("writer still open at Observe: %v", err)
		}
		switch name {
		case "cancel-close-after":
			h.cancel()
		case "closed-stage-tamper":
			raw := fileBytes(t, h.f.paths.Stage)
			switch h.f.scope.ArtifactNamespace {
			case "text":
				raw = bytes.Replace(raw, []byte("訳文"), []byte("異文"), 1)
			case "w3":
				// Authored fixture: four records, one-byte count at offset 10.
				// Swap two complete ordered-table entries, not a reader copy.
				pipelineEqual(t, raw[10], byte(4))
				entry := bytes.Clone(raw[11:23])
				copy(raw[11:23], raw[23:35])
				copy(raw[23:35], entry)
			}
			writeFile(t, h.f.paths.Stage, raw)
			h.reached["physical-stage-tamper"]++
		}
	}
	h.adapter.beforeObserve = func() {
		h.reached["observe"]++
		if name == "cancel-observe-before" {
			h.cancel()
		}
	}
	h.adapter.afterObserve = func() {
		if name == "cancel-observe-after" {
			h.cancel()
		}
	}
	h.adapter.afterExport = func(manifest artifact.Manifest) (artifact.Manifest, error) {
		switch name {
		case "cancel-export-after":
			h.cancel()
		case "input-manifest-mismatch":
			return artifact.Manifest{}, nil
		}
		return manifest, nil
	}
	h.adapter.export = func(w io.Writer) io.Writer {
		// Freeze the just-created writer identity before any write or cancellation.
		if err := unix.Statx(unix.AT_FDCWD, h.f.paths.Stage, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &h.stageID); err != nil {
			t.Fatal(err)
		}
		h.reached["export"]++
		if name == "cancel-export-before" {
			h.cancel()
		}
		if name == "export-prefix-error" {
			return pipelineFaultWriter(func(b []byte) (int, error) {
				n, err := w.Write(b[:min(4, len(b))])
				h.reached["physical-export-prefix"]++
				return n, errors.Join(err, unix.EIO)
			})
		}
		return w
	}
	if name == "observe-error" {
		h.adapter.observeErr = unix.EIO
	}
	if name == "tm-manifest-mismatch" {
		h.f.scope.Keys = append(h.f.scope.Keys, artifact.KeyRelation{UnitID: h.f.input.Units()[0].ID(), Key: "task2-foreign"})
	}
	h.run.afterStage = func() {
		h.reached["after-stage"]++
		if name == "cancel-before-commit" {
			h.cancel()
		}
	}
	switch h.scenario.row {
	case "M02", "M03", "M04":
		h.phase, h.want = "stage", unix.EIO
		if strings.HasPrefix(name, "cancel-") {
			h.want = context.Canceled
		}
		switch name {
		case "stage-collision":
			h.want = unix.EEXIST
		case "closed-stage-tamper":
			h.want = artifact.ErrFinalMismatch
		case "input-manifest-mismatch":
			h.want = artifact.ErrInvalidManifest
		case "tm-manifest-mismatch":
			h.want = tm.ErrInvalidScope
		case "cancel-before-commit":
			h.phase = "commit"
		}
	}
}

func (h *pipelineFault) configureDatabase(t *testing.T) {
	name := h.scenario.name
	if h.scenario.row != "M05" {
		return
	}
	h.phase = "commit"
	h.want = tm.ErrConflict
	want := h.f.expectedRows(t) // Authored input/profile, never persisted key fields.
	target := want[0]
	for _, row := range want {
		if row.Text == h.f.texts[h.f.input.Units()[len(h.f.input.Units())-1].ID().StableID()] {
			target = row
		}
	}
	if name == "sqlite-abort" || name == "sqlite-rollback" {
		// Repairing a rejected earlier row must also roll back on the later INSERT.
		for _, row := range want {
			if row.Text == h.f.texts[h.f.input.Units()[0].ID().StableID()] {
				row.Text = ""
				h.sql(t, "INSERT INTO tm VALUES(?,?,?,?)", row.Schema, row.Source, row.Profile, row.Text)
				h.f.initial = append(h.f.initial, row)
				break
			}
		}
		h.rows = pipelineSortRows(slices.Clone(h.f.initial))
		action := "ABORT"
		if name == "sqlite-rollback" {
			action = "ROLLBACK"
		}
		// The trigger checks that earlier staged rows really ran in this transaction.
		// Text has one unit: its corresponding UPDATE is the only applicable row.
		operation := "INSERT"
		if h.f.scope.ArtifactNamespace == "text" {
			operation = "UPDATE"
		}
		statement := fmt.Sprintf(`CREATE TRIGGER task2_abort BEFORE %s ON tm
			WHEN NEW.source_identity=x'%x' AND NEW.translation_profile=x'%x'
			BEGIN SELECT CASE WHEN (SELECT count(*) FROM tm) < %d
			THEN RAISE(FAIL,'task2-wrong-order') ELSE RAISE(%s,'task2-row-fault') END; END`,
			operation, target.Source, target.Profile, len(h.f.initial)+max(0, h.f.input.UnitCount()-2), action)
		h.sql(t, statement)
		h.want = nil // Assert the actual typed SQLite constraint and trigger marker below.
		return
	}
	if name == "sqlite-writer" || name == "sqlite-reader" || name == "sqlite-row-cancel" {
		h.f.core.limits.DBOperationTimeout = 30 * time.Millisecond
		h.want = context.DeadlineExceeded
		if name == "sqlite-row-cancel" {
			h.f.core.limits.DBOperationTimeout = time.Second
			h.want = context.Canceled
		}
		previous := h.run.afterStage
		h.run.afterStage = func() {
			previous()
			var err error
			h.holder, err = h.f.db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			begin := "BEGIN IMMEDIATE"
			if name == "sqlite-reader" || name == "sqlite-row-cancel" {
				begin = "BEGIN"
			}
			if _, err := h.holder.ExecContext(t.Context(), begin); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := h.holder.QueryRowContext(t.Context(), "SELECT count(*) FROM tm").Scan(&count); err != nil {
				t.Fatal(err)
			}
			pipelineEqual(t, count, len(h.rows))
			h.reached["held-"+begin]++
			if name == "sqlite-row-cancel" {
				h.watchTransactionCancel(t)
			}
		}
		return
	}
	if name != "conflict-miss" {
		switch {
		case strings.Contains(name, "-invalid-"):
			target.Text = ""
		case strings.Contains(name, "-valid-"):
			// Use a real currently valid candidate with a distinct future winner.
		default:
			t.Fatalf("unhandled database scenario %s", name)
		}
		h.sql(t, "INSERT INTO tm VALUES(?,?,?,?)", target.Schema, target.Source, target.Profile, target.Text)
		h.f.initial = append(h.f.initial, target)
		h.rows = pipelineSortRows(slices.Clone(h.f.initial))
	}
	previous := h.run.afterStage
	h.run.afterStage = func() {
		previous()
		args := []any{target.Schema, target.Source, target.Profile}
		switch {
		case name == "conflict-miss":
			target.Text = "external winner\n"
			h.sql(t, "INSERT INTO tm VALUES(?,?,?,?)", target.Schema, target.Source, target.Profile, target.Text)
			h.rows = append(h.rows, target)
		case strings.HasSuffix(name, "-removed"):
			h.sql(t, "DELETE FROM tm WHERE key_schema=? AND source_identity=? AND translation_profile=?", args...)
			h.rows = slices.DeleteFunc(h.rows, func(row pipelineTMRow) bool {
				return bytes.Equal(row.Source, target.Source) && bytes.Equal(row.Profile, target.Profile)
			})
		case strings.HasSuffix(name, "-changed"):
			target.Text = "external changed winner\n"
			h.sql(t, "UPDATE tm SET translated_text=? WHERE key_schema=? AND source_identity=? AND translation_profile=?", append([]any{target.Text}, args...)...)
			for i := range h.rows {
				if bytes.Equal(h.rows[i].Source, target.Source) && bytes.Equal(h.rows[i].Profile, target.Profile) {
					h.rows[i] = target
				}
			}
		default:
			t.Fatalf("unhandled conflict %s", name)
		}
		h.rows = pipelineSortRows(h.rows)
		h.reached["external-row-change"]++
	}
}

func pipelineSortRows(rows []pipelineTMRow) []pipelineTMRow {
	slices.SortFunc(rows, func(a, b pipelineTMRow) int {
		if c := bytes.Compare(a.Source, b.Source); c != 0 {
			return c
		}
		return bytes.Compare(a.Profile, b.Profile)
	})
	return rows
}

func (h *pipelineFault) sql(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := h.f.db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func (h *pipelineFault) configurePublication(t *testing.T) {
	name := h.scenario.name
	switch h.scenario.row {
	case "success", "M07", "M08", "M09", "M10", "M11", "M12", "M13", "M14":
		h.committed, h.rows = true, h.f.expectedRows(t)
		h.want, h.phase = unix.EIO, "publish"
		if h.scenario.row == "success" {
			h.want, h.phase, h.state = nil, "", publication.Published
		}
	}
	if strings.HasPrefix(name, "cancel-") || strings.HasPrefix(h.scenario.stimulus, "cancel-") {
		h.want = context.Canceled
	}
	switch h.scenario.row {
	case "M11":
		h.state = publication.Restored
	case "M12":
		h.state = publication.SourceBackedUp
	case "M13":
		if h.mode.mode == publication.InPlace {
			h.state = publication.Restored
		}
	case "M14":
		h.state = publication.Published
	}
	if h.scenario.stimulus == "short-write" {
		h.want = io.ErrShortWrite
	}
	switch name {
	case "backup-collision", "backup-rename-collision", "prepared-temp-collision":
		h.want = unix.EEXIST
	case "external-after-backup":
		h.want = publication.ErrChanged
	case "restore-collision":
		h.want = unix.EIO // Primary publish error; actual restore additionally gets EEXIST.
	case "run-close-error":
		h.phase, h.run.closeErr = "close", unix.EIO
	}
	h.run.beforePublish = func() {
		h.reached["publish-entry"]++
		switch name {
		case "cancel-publish-entry":
			h.cancel()
		case "backup-collision":
			writeFile(t, h.f.paths.Backup, []byte("owned backup collision\n"))
			h.reached["backup-collision-created"]++
		case "prepared-temp-collision":
			writeFile(t, filepath.Join(filepath.Dir(h.f.paths.Stage), ".record-prepared"), []byte("owned record collision\n"))
			h.reached["record-collision-created"]++
		}
	}
}

func (h *pipelineFault) hooks(t *testing.T) publication.PipelineFaultHooks {
	name := h.scenario.name
	return publication.PipelineFaultHooks{
		SyncStage: func(f *os.File) error {
			h.reached["stage-sync-entry"]++
			h.stageFD = f
			if name == "cancel-sync-before" {
				h.cancel()
			}
			err := f.Sync()
			h.reached["stage-sync-real"]++
			switch name {
			case "stage-sync-error":
				err = errors.Join(err, unix.EIO)
			case "cancel-sync-after":
				h.cancel()
			}
			return err
		},
		CloseStage: func(f *os.File) error {
			h.reached["stage-close-entry"]++
			h.stageFD = f
			switch name {
			case "stage-close-error":
				return unix.EIO // The frozen bridge still executes the real Close.
			case "cancel-close-before":
				h.cancel()
			}
			return nil
		},
		RecordWrite: func(f *os.File, raw []byte) (int, error) {
			h.recordName = f.Name()
			h.recordFDs = append(h.recordFDs, f)
			h.submitted[f.Name()] = bytes.Clone(raw)
			h.reached["record-write:"+f.Name()]++
			if h.recordTarget("write") {
				if h.scenario.stimulus == "cancel-before" {
					h.cancel()
				}
				if h.scenario.stimulus == "error" || h.scenario.stimulus == "short-write" {
					n, err := f.Write(raw[:4])
					if h.scenario.stimulus == "error" {
						err = errors.Join(err, unix.EIO)
					}
					return n, err
				}
			}
			n, err := f.Write(raw)
			if h.recordTarget("write") && h.scenario.stimulus == "cancel-after" {
				h.cancel()
			}
			return n, err
		},
		RecordSync: func(f *os.File) error {
			stat, err := f.Stat()
			if err != nil {
				return err
			}
			op := "sync"
			if stat.IsDir() {
				op = "directory-sync"
			}
			h.reached["record-"+op+":"+h.recordName]++
			if h.recordTarget(op) && h.scenario.stimulus == "cancel-before" {
				h.cancel()
			}
			err = f.Sync()
			h.reached["record-"+op+"-real:"+h.recordName]++
			if h.recordTarget(op) {
				switch h.scenario.stimulus {
				case "error":
					err = errors.Join(err, unix.EIO)
				case "cancel-after":
					h.cancel()
				}
			}
			if name == "prepared-record-corrupt" && stat.IsDir() && h.recordName == ".record-prepared" {
				writeFile(t, h.f.paths.Record, []byte("{"))
				h.installed = []byte("{")
				h.reached["physical-record-corrupt"]++
				return errors.Join(err, unix.EIO)
			}
			return err
		},
		RecordClose: func(f *os.File) error {
			h.reached["record-close:"+f.Name()]++
			if h.recordTarget("close") {
				switch h.scenario.stimulus {
				case "error":
					return unix.EIO
				case "cancel-before":
					h.cancel()
				}
			}
			return nil // The bridge always performs Close, including on injected error.
		},
		Rename: func(a int, from string, b int, to string, flags uint) error {
			h.reached["rename-entry:"+from]++
			if strings.HasPrefix(from, ".record-") {
				if h.recordTarget("close") && h.scenario.stimulus == "cancel-after" {
					if _, err := h.recordFDs[len(h.recordFDs)-1].Stat(); !errors.Is(err, os.ErrClosed) {
						t.Fatalf("record Close-after checkpoint still has an open FD: %v", err)
					}
					h.cancel() // Rename entry is after the mandatory actual Close.
				}
				if h.recordTarget("rename") {
					switch h.scenario.stimulus {
					case "error":
						return unix.EIO
					case "cancel-before":
						h.cancel()
					}
				}
				err := unix.Renameat2(a, from, b, to, flags)
				if err == nil {
					h.installed = bytes.Clone(h.submitted[from])
					h.reached["rename-real:"+from]++
					if h.recordTarget("rename") && h.scenario.stimulus == "cancel-after" {
						h.cancel()
					}
				}
				return err
			}
			if from == filepath.Base(h.options.Source) {
				h.reached["backup-rename-entry"]++
				switch name {
				case "backup-rename-error":
					return unix.EIO
				case "backup-rename-collision":
					writeFile(t, h.f.paths.Backup, []byte("owned backup collision\n"))
				}
				err := unix.Renameat2(a, from, b, to, flags)
				if err == nil {
					h.reached["backup-rename-real"]++
					pipelineEqual(t, flags, uint(unix.RENAME_NOREPLACE))
					switch name {
					case "cancel-after-backup":
						h.cancel()
					case "external-after-backup":
						writeFile(t, h.options.Source, []byte("external final\n"))
					}
				} else if errors.Is(err, unix.EEXIST) {
					h.reached["physical-backup-EEXIST"]++
				}
				return err
			}
			if strings.HasPrefix(from, ".yakuori-backup-") {
				h.reached["restore-rename-entry"]++
				pipelineEqual(t, flags, uint(unix.RENAME_NOREPLACE))
				switch name {
				case "restore-error":
					// Keep this physical permission failure in force for the
					// independent fresh-PID recovery refusal, then restore ownership.
					parent := filepath.Dir(h.options.Source)
					if err := os.Chmod(parent, 0500); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						if err := os.Chmod(parent, 0700); err != nil {
							t.Error(err)
						}
					})
				case "restore-collision":
					writeFile(t, h.options.Source, []byte("external final\n"))
				}
				err := unix.Renameat2(a, from, b, to, flags)
				if err == nil {
					h.reached["restore-rename-real"]++
				} else if errors.Is(err, unix.EEXIST) {
					h.reached["physical-restore-EEXIST"]++
				} else if errors.Is(err, unix.EACCES) {
					h.reached["physical-restore-EACCES"]++
				}
				return err
			}
			if from != "stage" {
				t.Fatalf("unexpected rename from=%s to=%s", from, to)
			}
			h.reached["publish-rename-entry"]++
			switch name {
			case "publish-rename-error", "restore-error", "restore-collision":
				return unix.EIO
			case "cancel-before-publish-rename":
				h.cancel()
				return h.ctx.Err()
			}
			err := unix.Renameat2(a, from, b, to, flags)
			if err == nil {
				h.reached["publish-rename-real"]++
				if name == "cancel-after-publish-rename" {
					h.cancel()
				}
			}
			return err // A successful actual rename must never return fake failure.
		},
	}
}

func (h *pipelineFault) recordTarget(operation string) bool {
	return h.recordName == ".record-"+h.scenario.recordPhase && h.scenario.operation == operation
}

func (h *pipelineFault) assertResult(t *testing.T, result Result, err error) {
	t.Helper()
	if h.scenario.name == "sqlite-abort" || h.scenario.name == "sqlite-rollback" {
		var failure *sqlite.Error
		if !errors.As(err, &failure) || failure.Code() != 1811 || !strings.Contains(failure.Error(), "task2-row-fault") {
			t.Fatalf("later real SQLite trigger not reached: %+v %v", failure, err)
		}
		h.reached["sqlite-trigger-"+h.scenario.name]++
	} else if h.scenario.row == "success" {
		if err != nil {
			t.Fatalf("success error: %v", err)
		}
	} else if err == nil || !errors.Is(err, h.want) {
		t.Fatalf("scenario=%s error=%+v want=%v result=%+v", h.scenario.name, err, h.want, result)
	}
	if h.phase != "" {
		var failure *Error
		if !errors.As(err, &failure) || failure.Phase != h.phase {
			t.Fatalf("phase=%v expected=%s", failure, h.phase)
		}
	}
	pipelineEqual(t, result.TMCommitted, h.committed)
	pipelineEqual(t, result.BytesWritten, 0)
	pipelineEqual(t, result.Publication.State, h.state)
	pipelineEqual(t, h.run.closes, 1)
	pipelineEqual(t, pipelineReadRows(t, h.f.db), h.rows)
	after := h.assets(t)
	source := "source"
	if h.state == publication.Published && h.mode.mode == publication.InPlace {
		source = "backup"
	}
	if h.state == publication.SourceBackedUp {
		pipelineEqual(t, after["backup"].Bytes, h.f.raw)
		if h.scenario.name == "external-after-backup" || h.scenario.name == "restore-collision" {
			pipelineEqual(t, after["source"].Bytes, []byte("external final\n"))
		} else {
			pipelineEqual(t, after["source"].Exists, false)
		}
		var recovery *publication.RecoveryError
		if !errors.As(err, &recovery) || recovery.Result.State != publication.SourceBackedUp {
			t.Fatalf("typed recovery failure missing: %v", err)
		}
	} else {
		pipelineEqual(t, after[source].Bytes, h.f.raw)
		pipelineEqual(t, after[source].Inode, h.before["source"].Inode)
		pipelineEqual(t, after[source].MountID, h.before["source"].MountID)
		pipelineEqual(t, after[source].Mode, h.before["source"].Mode)
		pipelineEqual(t, after[source].Nlink, h.before["source"].Nlink)
	}
	if h.state == publication.Published {
		pipelineEqual(t, after["stage"].Exists, false)
		pipelineEqual(t, after["output"].Bytes, h.adapter.exported)
		pipelineEqual(t, after["output"].Inode, h.stageID.Ino)
		pipelineEqual(t, after["output"].MountID, h.stageID.Mnt_id)
		observed, err := h.f.adapter.Observe(bytes.NewReader(after["output"].Bytes))
		if err != nil {
			t.Fatal(err)
		}
		texts := make(map[string]string)
		for _, u := range observed.Units {
			texts[u.ID.StableID()] = u.Text
		}
		if !maps.Equal(texts, h.f.texts) {
			t.Fatalf("published translations=%v", texts)
		}
		pipelineEqual(t, h.reached["publish-rename-real"], 1)
	} else if h.mode.mode != publication.InPlace {
		pipelineEqual(t, after["output"], h.before["output"])
	}
	if h.state != publication.SourceBackedUp && !(h.state == publication.Published && h.mode.mode == publication.InPlace) {
		switch h.scenario.name {
		case "backup-collision", "backup-rename-collision":
			pipelineEqual(t, after["backup"].Bytes, []byte("owned backup collision\n"))
		default:
			pipelineEqual(t, after["backup"].Exists, false)
		}
	}
	if h.stageFD != nil {
		if _, err := h.stageFD.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("actual stage descriptor leaked: %v", err)
		}
	}
	for _, f := range h.recordFDs {
		if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("actual record descriptor leaked: %v", err)
		}
	}
	h.assertReached(t)
	wantStage := h.state != publication.Published &&
		(h.reached["stage-close-entry"] != 0 || h.scenario.name == "stage-collision")
	if after["stage"].Exists != wantStage {
		t.Fatalf("retained stage existence: path=%s actual=%t expected=%t reached=%v",
			after["stage"].Path, after["stage"].Exists, wantStage, h.reached)
	}
	if wantStage {
		expected := bytes.Clone(h.wantStage)
		switch h.scenario.name {
		case "stage-collision":
			expected = []byte("owned stage collision\n")
		case "export-prefix-error":
			expected = expected[:4]
		case "stage-write-EFBIG":
			expected = expected[:8]
		case "closed-stage-tamper":
			switch h.f.scope.ArtifactNamespace {
			case "text":
				expected = bytes.Replace(expected, []byte("訳文"), []byte("異文"), 1)
			case "w3":
				entry := bytes.Clone(expected[11:23])
				copy(expected[11:23], expected[23:35])
				copy(expected[23:35], entry)
			}
		}
		pipelineSave(t, "expected-retained-stage.bin", expected)
		pipelineEqual(t, after["stage"].Bytes, expected)
		pipelineEqual(t, after["stage"].Inode, h.stageID.Ino)
		pipelineEqual(t, after["stage"].MountID, h.stageID.Mnt_id)
		pipelineEqual(t, after["stage"].Mode, h.stageID.Mode)
		pipelineEqual(t, after["stage"].Nlink, h.stageID.Nlink)
	}
	if h.scenario.row == "M01" {
		pipelineEqual(t, h.adapter.exports, 0)
		pipelineEqual(t, after["stage"].Exists, false)
	}
	switch h.scenario.name {
	case "stage-collision":
		pipelineEqual(t, after["stage"].Bytes, []byte("owned stage collision\n"))
		pipelineEqual(t, h.adapter.exports, 0)
	case "export-prefix-error":
		pipelineEqual(t, len(after["stage"].Bytes), 4)
		pipelineEqual(t, h.adapter.observes, 0)
	case "stage-sync-error", "stage-close-error", "cancel-sync-before", "cancel-sync-after", "cancel-close-before":
		pipelineEqual(t, h.adapter.observes, 0)
		pipelineEqual(t, after["stage"].Bytes, h.adapter.exported)
	case "closed-stage-tamper":
		pipelineEqual(t, h.reached["physical-stage-tamper"], 1)
		if bytes.Equal(after["stage"].Bytes, h.adapter.exported) {
			t.Fatal("tamper did not change the physical stage")
		}
		if _, err := h.f.adapter.Observe(bytes.NewReader(after["stage"].Bytes)); err != nil {
			t.Fatalf("tamper was not a valid decodable artifact: %v", err)
		}
	}
	if after["record"].Exists {
		pipelineEqual(t, after["record"].Bytes, h.installed)
	} else if len(h.installed) != 0 {
		t.Fatal("a successfully installed record disappeared")
	}
	if h.committed && h.state != publication.Published {
		pipelineEqual(t, after["stage"].Bytes, h.adapter.exported)
	}
	if h.state == publication.Restored {
		pipelineEqual(t, h.reached["restore-rename-real"], 1)
	}
	if h.scenario.name == "restore-error" {
		pipelineEqual(t, h.reached["physical-restore-EACCES"], 1)
		if !errors.Is(err, unix.EACCES) {
			t.Fatalf("physical restore EACCES missing: %v", err)
		}
	}
	if h.scenario.name == "restore-collision" {
		pipelineEqual(t, h.reached["physical-restore-EEXIST"], 1)
		if !errors.Is(err, unix.EEXIST) {
			t.Fatalf("physical restore EEXIST missing: %v", err)
		}
	}
	if h.scenario.name == "backup-rename-collision" {
		pipelineEqual(t, h.reached["physical-backup-EEXIST"], 1)
	}
}

func (h *pipelineFault) assertReached(t *testing.T) {
	t.Helper()
	pipelineEqual(t, h.reached["prepare"], 1)
	if strings.HasPrefix(h.scenario.name, "cancel-") || strings.HasPrefix(h.scenario.stimulus, "cancel-") || h.scenario.name == "sqlite-row-cancel" {
		pipelineEqual(t, h.cancelCount, 1)
		pipelineEqual(t, h.ctx.Err(), context.Canceled)
	}
	if h.scenario.recordPhase != "" {
		operation := h.scenario.operation
		key := "record-" + operation + ":.record-" + h.scenario.recordPhase
		if operation == "rename" {
			key = "rename-entry:.record-" + h.scenario.recordPhase
		}
		pipelineEqual(t, h.reached[key], 1)
	}
	switch h.scenario.row {
	case "M01":
		switch h.scenario.name {
		case "builder-error", "count-error", "context-admission", "profile-mismatch", "cancel-admission":
			pipelineEqual(t, h.reached["generate-entry"], 0)
		case "later-generate-error":
			pipelineEqual(t, h.reached["generate-entry"], 2)
		default:
			pipelineEqual(t, h.reached["generate-entry"], 1)
		}
	case "M03":
		pipelineEqual(t, h.reached["stage-sync-real"], 1)
		pipelineEqual(t, h.reached["stage-close-entry"], 1)
	case "M04":
		if h.scenario.name != "input-manifest-mismatch" {
			pipelineEqual(t, h.reached["observe"], 1)
		}
	case "M05":
		pipelineEqual(t, h.reached["after-stage"], 1)
		pipelineEqual(t, h.run.publishes, 0)
	case "M07", "M08", "M09", "M10", "M11", "M12", "M13", "M14", "success":
		pipelineEqual(t, h.reached["publish-entry"], 1)
	}
}

type pipelineFaultAsset struct {
	Path           string
	Exists         bool
	Bytes          []byte
	Inode, MountID uint64
	Mode           uint16
	Nlink          uint32
}

func (h *pipelineFault) assets(t *testing.T) map[string]pipelineFaultAsset {
	t.Helper()
	paths := map[string]string{"source": h.options.Source, "output": h.options.Output,
		"stage": h.f.paths.Stage, "record": h.f.paths.Record, "backup": h.f.paths.Backup}
	if h.f.paths.Stage != "" {
		dir := filepath.Dir(h.f.paths.Stage)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.Name() != "stage" && entry.Name() != "record.json" {
				paths[entry.Name()] = filepath.Join(dir, entry.Name())
			}
		}
	}
	result := make(map[string]pipelineFaultAsset)
	for label, path := range paths {
		value := pipelineFaultAsset{Path: path}
		if path != "" {
			var stat unix.Statx_t
			err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &stat)
			if err != nil && !errors.Is(err, unix.ENOENT) {
				t.Fatal(err)
			}
			if err == nil {
				value.Exists, value.Bytes = true, fileBytes(t, path)
				value.Inode, value.MountID, value.Mode, value.Nlink = stat.Ino, stat.Mnt_id, stat.Mode, stat.Nlink
			}
		}
		result[label] = value
	}
	return result
}

func (h *pipelineFault) saveReport(t *testing.T, result Result, err error) {
	t.Helper()
	assets := h.assets(t)
	for name, asset := range assets {
		if asset.Exists {
			pipelineSave(t, "raw-"+name+".bin", asset.Bytes)
		}
	}
	report := struct {
		Row, Scenario, Layer, Backend, Stimulus string
		Reached                                 map[string]int
		CancelObserved                          bool
		Result                                  Result
		Error                                   string
		ErrorChain                              []string
		StageIdentity                           unix.Statx_t
		Before, After                           map[string]pipelineFaultAsset
		ExpectedRows, ActualRows                []pipelineTMRow
	}{
		Row: h.scenario.row, Scenario: h.scenario.name, Layer: "whole Core / real Adapter / real SQLite",
		Backend:  "controlled common inference.Engine; not native inference",
		Stimulus: "delegating boundary injection or direct context cancellation; EIO is injected, not physical media errno",
		Reached:  h.reached, CancelObserved: errors.Is(h.ctx.Err(), context.Canceled), Result: result,
		Before: h.before, After: assets, ExpectedRows: h.rows, ActualRows: pipelineReadRows(t, h.f.db),
		ErrorChain:    pipelineFaultErrorChain(err),
		StageIdentity: h.stageID,
	}
	if err != nil {
		report.Error = fmt.Sprintf("%+v", err)
	}
	raw, marshalErr := json.MarshalIndent(report, "", "  ")
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	pipelineSave(t, "state.json", raw)
	path := pipelineFaultDBPath(t, h.f.db)
	pipelineSave(t, "memory.db", fileBytes(t, path))
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if data, err := os.ReadFile(path + suffix); err == nil {
			pipelineSave(t, "memory.db"+suffix, data)
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	t.Logf("row=%s scenario=%s mode=%s layer=whole-Core backend=controlled reached=%v cancel=%v TMCommitted=%v state=%s rows=%d error=%v", h.scenario.row, h.scenario.name, h.mode.name, h.reached, report.CancelObserved, result.TMCommitted, result.Publication.State, len(h.rows), err)
}

func pipelineFaultErrorChain(err error) []string {
	if err == nil {
		return nil
	}
	result := []string{fmt.Sprintf("%T: %s", err, err)}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, child := range wrapped.Unwrap() {
			result = append(result, pipelineFaultErrorChain(child)...)
		}
	case interface{ Unwrap() error }:
		result = append(result, pipelineFaultErrorChain(wrapped.Unwrap())...)
	}
	return result
}

func pipelineFaultDBPath(t *testing.T, db *sql.DB) string {
	t.Helper()
	var seq int
	var name, path string
	if err := db.QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	return path
}
