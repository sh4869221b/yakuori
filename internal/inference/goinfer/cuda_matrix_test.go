//go:build cuda && cudasmoke

package goinfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/segment"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func TestCUDAEvaluationMatrix(t *testing.T) {
	model, backend := cudaEnvironment(t)
	fixture, pair := os.Getenv("YAKUORI_CUDA_FIXTURE"), os.Getenv("YAKUORI_CUDA_PAIR")
	if fixture == "" || pair == "" {
		t.Fatal("explicit matrix fixture and pair required")
	}
	report := cudaMatrixReport{Go: runtime.Version(), GOMAXPROCS: runtime.GOMAXPROCS(0), Fixture: fixture, Pair: pair, Status: "failed", TM: "none: Core.Generate only; no TM or publication mutation", RequestEquality: "CPU reference; checked by paired CUDA before generation", MetricDefinitions: "load_ms includes snapshot/decoder/tokenizer setup; cold/warm share one loaded model without page-cache flush; TTFT Generate call to first stream token; post-first-token interval first token to stream drain; generation throughput output_tokens/(generation_ms/1000); post-first-token throughput (stream_tokens-1)/(post_first_token_stream_ms/1000); exact prefill/decode phase and per-request prefill path unverified"}
	for _, name := range []string{"cold", "warm"} {
		report.Runs = append(report.Runs, cudaMatrixRun{Name: name, Status: "unrun: earlier failure", Output: []string{}, Requests: []cudaRequest{}, PlannedRequests: []cudaRequest{}})
	}
	report.Limits = config.DefaultLimits()
	started := time.Now()
	defer func() {
		report.ProcessTotalMS = float64(time.Since(started)) / float64(time.Millisecond)
		var usage syscall.Rusage
		if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
			t.Error(err)
		}
		report.PeakRSSKiB = usage.Maxrss
		cudaWriteReport(t, report)
	}()
	session, err := cudaFixture(fixture)
	if err != nil {
		report.Error = err.Error()
		t.Fatal(err)
	}
	report.Units, report.ArtifactBytes = len(session.Units()), len(session.Artifact())
	for _, u := range session.Units() {
		report.TextBytes += len(u.Source())
	}
	engine, setup, err := newCUDAEvaluation(context.Background(), model, backend)
	report.Setup = setup
	if err != nil {
		report.Error = err.Error()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Error(err)
		}
	})
	var reference cudaMatrixReport
	if backend == "cuda" {
		path := os.Getenv("YAKUORI_CUDA_REFERENCE")
		if path == "" {
			t.Fatal("CUDA matrix requires paired CPU reference")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &reference); err != nil {
			t.Fatal(err)
		}
		if reference.Status != "passed" || reference.Setup.EffectiveBackend != "cpu" || reference.Fixture != fixture || reference.Pair != pair {
			t.Fatal("invalid CPU reference")
		}
	}
	for i := range report.Runs {
		ctx, cancel := context.WithTimeout(context.Background(), config.DefaultLimits().GenerationTimeout)
		runErr := cudaMatrixTranslation(ctx, engine, session, &report.Runs[i], func(planned []cudaRequest) error {
			if backend == "cuda" {
				if len(reference.Runs) != 2 || !reflect.DeepEqual(planned, reference.Runs[i].PlannedRequests) {
					return errors.New("paired CPU/CUDA planned prompt/spans/IDs/identity/policy/count differ")
				}
				report.RequestEquality = "passed: every planned prompt/span/token ID/identity/effective policy/count equals paired CPU before generation"
			}
			return nil
		})
		cancel()
		if runErr != nil {
			report.Error = runErr.Error()
			t.Fatal(runErr)
		}
	}
	report.Status = "passed"
}

func cudaMatrixTranslation(ctx context.Context, engine *Engine, session unit.Session, run *cudaMatrixRun, compare func([]cudaRequest) error) (err error) {
	started := time.Now()
	defer func() {
		run.TotalMS = float64(time.Since(started)) / float64(time.Millisecond)
		if err != nil {
			run.Status = "failed"
			run.Error = err.Error()
		}
	}()
	limits := config.DefaultLimits()
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, limits.MaxOutputTokens, limits.RequestTimeout)
	if err != nil {
		return err
	}
	build := func(ctx context.Context, _ unit.UnitID, text string) (inference.GenerationRequest, error) {
		return engine.NewRequest(ctx, prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: text}, policy)
	}
	info, err := engine.Info(ctx)
	if err != nil {
		return err
	}
	for _, u := range session.Units() {
		prepared, err := protect.Prepare(session, u.ID())
		if err != nil {
			return err
		}
		plan, err := segment.Build(ctx, u.ID(), prepared, info, func(ctx context.Context, text string) (inference.GenerationRequest, error) {
			return build(ctx, u.ID(), text)
		}, engine.CountTokens)
		if err != nil {
			return err
		}
		run.Segments += len(plan.Segments())
		for _, piece := range plan.Segments() {
			if piece.GenerationRequired() {
				run.PlannedRequests = append(run.PlannedRequests, cudaRequestRecord(piece.Request()))
			}
		}
	}
	if err := compare(run.PlannedRequests); err != nil {
		return err
	}
	measured := &cudaMeasuredEngine{Engine: engine, run: run}
	profile, err := validate.NewProfileWithSegmentation(info.ModelSHA256, segment.ProfileInput())
	if err != nil {
		return err
	}
	core := localize.NewSegmentedCore(measured, build)
	accepted, err := core.Generate(ctx, session, profile)
	run.AcceptedUnits = len(accepted)
	run.ValidationFailures, run.ValidationDenominator = cudaMechanicalCounts(session, len(accepted), err)
	for _, a := range accepted {
		if _, validationErr := validate.Validate(session, a.UnitID(), profile, a.Text()); validationErr != nil {
			return validationErr
		}
		run.Output = append(run.Output, a.Text())
	}
	// Core may plan twice, but the immutable adopted generation request must match the observed plan exactly.
	for i, actual := range run.Requests {
		if i >= len(run.PlannedRequests) {
			return errors.New("generation exceeded planned requests")
		}
		expected := run.PlannedRequests[i]
		actual.Finish, actual.OutputTokens, actual.Text, actual.GenerationMS, actual.Observation, actual.Error = "", 0, "", 0, cudaObservation{}, ""
		actual.GenerationTokensPerSecond = 0
		if !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("adopted request %d differs from plan", i)
		}
	}
	if err != nil {
		return err
	}
	if len(run.Requests) != len(run.PlannedRequests) {
		return errors.New("successful generation did not execute entire plan")
	}
	run.Status = "passed"
	return nil
}
