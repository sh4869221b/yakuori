//go:build linux

package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/publication"
	"golang.org/x/sys/unix"
)

func measuredRunnerFixture(t *testing.T) (RunOptions, string) {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".measurement-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		t.Fatal(err)
	}
	if stat.Type != unix.EXT4_SUPER_MAGIC && stat.Type != unix.BTRFS_SUPER_MAGIC {
		t.Skip("publication needs ext4/Btrfs")
	}
	source, err := filepath.Abs("testdata/small.w3strings")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	fixture := Fixture{ID: "test-small", Kind: "mod", SourceURL: "test-only synthetic fixture", Revision: "test-only", License: "project-authored test", LocalPath: source, SHA256: hex.EncodeToString(digest[:]), Bytes: len(raw), Units: 3, TextBytes: 16, MaxTextBytes: 11, Format: "w3strings", FormatVersion: 162, Language: "en"}
	for _, name := range categories {
		id := "10"
		if name == "empty_text" {
			id = "20"
		}
		if name == "long_text" {
			id = "30"
		}
		fixture.Categories = append(fixture.Categories, Category{Name: name, UnitIDs: []string{id}, Evidence: "test only"})
	}
	manifest := Manifest{Version: "test-manifest", Fixtures: []Fixture{fixture}}
	criteria := Criteria{Version: "test-criteria", ManifestVersion: manifest.Version, FixtureIDs: []string{fixture.ID}, Values: map[string]Criterion{}, Confirmation: Confirmation{Status: "confirmed", Date: "2026-01-01T00:00:00Z", Reviewer: "test-only", Decision: "approved", ManifestVersion: manifest.Version, CriteriaVersion: "test-criteria"}}
	for _, name := range criterionNames {
		criteria.Values[name] = Criterion{Value: "1", Rationale: "test", Impact: "test"}
	}
	for name, value := range map[string]string{"units": "3", "text_bytes": "16", "artifact_bytes": strconv.Itoa(len(raw))} {
		c := criteria.Values[name]
		c.Value = value
		criteria.Values[name] = c
	}
	manifestPath, criteriaPath := filepath.Join(dir, "manifest.json"), filepath.Join(dir, "criteria.json")
	for path, v := range map[string]any{manifestPath: manifest, criteriaPath: criteria} {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	model := [32]byte{1}
	return RunOptions{Manifest: manifestPath, Criteria: criteriaPath, Root: dir, Repository: dir, Evidence: filepath.Join(dir, "evidence"), ModelID: "test-model", ModelSHA256: hex.EncodeToString(model[:]), Game: "test", Artifact: "test", ModelPaths: config.Paths{Data: dir}}, dir
}

func TestMeasuredRunnerKeepsProductSuccessWhenVRAMMissing(t *testing.T) {
	opts, dir := measuredRunnerFixture(t)
	t.Setenv("YAKUORI_ACCEPTANCE_HELPER", "1")
	smi := filepath.Join(dir, "fake-smi")
	if err := os.WriteFile(smi, []byte("#!/bin/sh\ncase \"$1\" in\n--query-gpu=uuid,name,driver_version,memory.total) echo 'GPU-test, Test GPU, test-driver, 1024';;\n--query-gpu=uuid,memory.used) echo 'GPU-test, 1';;\n*) :;;\nesac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	sup, err := NewSupervisor(os.Args[0], SupervisorOptions{GPUUUID: "GPU-test", Interval: time.Millisecond, QueryTimeout: time.Millisecond * 100, StopGrace: time.Millisecond * 50, ReapTimeout: time.Second, SMIBinary: smi})
	if err != nil {
		t.Fatal(err)
	}
	opts.measurement = &measurementSession{supervisor: sup, options: MeasurementOptions{WallLimit: time.Second * 10, PreparationLimit: time.Second * 10, StopGrace: time.Millisecond * 50, ReapTimeout: time.Second, ModelQuant: "fixture", ModelQuantSource: "test-only", RepeatIndex: 1, RepeatTotal: 1}}
	build := BuildInfo{Revision: "controlled-test-only", Modified: "false", GoVersion: "test-only", Modules: map[string]string{"github.com/townsendmerino/goinfer": "test-only", "github.com/townsendmerino/goinfer/cuda": "test-only"}}
	fakeRecords, fakeReader := filepath.Join(dir, "fake.records"), filepath.Join(dir, "fake-reader")
	if err := os.WriteFile(fakeReader, []byte(fmt.Sprintf("#!/bin/sh\ncp %s \"$3\"\n", strconv.Quote(fakeRecords))), 0700); err != nil {
		t.Fatal(err)
	}
	launches := 0
	launcher := func(ctx context.Context, req WorkerRequest) (WorkerResult, error) {
		launches++
		result, err := sup.launch(ctx, req, []string{"-test.run=^TestAcceptanceWorkerHelper$"})
		if err != nil {
			return result, err
		}
		result.Build = build
		if err := os.WriteFile(fakeRecords, encodeOracleRecords(t, result.Observation.Units, result.Observation.Keys), 0600); err != nil {
			t.Fatal(err)
		}
		return result, nil
	}
	builder := func(context.Context, string, string) (string, string, bool, error) {
		return fakeReader, "test-only", false, nil
	}
	rec, err := runWith(context.Background(), opts, launcher, builder, build)
	if err == nil || rec.Status != "fail" || rec.FailurePhase != "measurement" || rec.Measurement.Status != "incomplete" {
		t.Fatalf("expected incomplete measurement: %+v, %v", rec, err)
	}
	if launches != 1 || len(rec.Runs) != 2 || rec.Runs[0].Status != "pass" || rec.Runs[0].Oracle.Comparison != "pass" || rec.Runs[1].Status != "not_run" {
		t.Fatalf("product/result separation failed: %+v launches=%d", rec.Runs, launches)
	}
	if len(rec.Measurement.PairCleanup) != 1 || rec.Measurement.PairCleanup[0].Status != "pass" || rec.Measurement.OracleCleanup != "pass" {
		t.Fatalf("cleanup not recorded: %+v", rec.Measurement)
	}
	raw, err := os.ReadFile(filepath.Join(opts.Evidence, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved RunRecord
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Runs[0].Status != "pass" || saved.Runs[0].MeasurementStatus != "incomplete" || saved.Runs[1].MeasurementStatus != "not_run" || saved.Runs[0].Measurement.ResourceStatus == "complete" || saved.Runs[0].Measurement.ProcessVRAMBytes != nil || saved.Measurement.Status != "incomplete" || saved.Measurement.Environment.GPUStatus != "complete" {
		t.Fatalf("saved mismatch: %+v", saved)
	}
	if strings.Contains(string(raw), "Long source") || strings.Contains(string(raw), strings.Repeat("長", 160)) {
		t.Fatal("private text leaked")
	}
	if _, err := os.Stat(filepath.Join(opts.Evidence, "fixture-000", ".tm")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("working TM remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(opts.Evidence, "fixture-000", "reuse.w3strings")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reuse ran unexpectedly: %v", err)
	}
}

func TestMeasuredOracleUsesExistingDeadline(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "reader")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntrap '' TERM\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "published")
	req := WorkerRequest{RunID: "r", FixtureID: "f", Mode: "fresh", Output: output}
	worker := WorkerResult{RunID: req.RunID, FixtureID: req.FixtureID, Mode: req.Mode}
	worker.Observation.FinalValidated = true
	worker.Observation.TMVerificationStatus = "pass"
	worker.Result.TMCommitted = true
	worker.Result.Publication.State = publication.Published
	worker.Result.Publication.OutputPath = output
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, err := finalizeWorker(ctx, script, req, worker, nil, filepath.Join(dir, "records"), &MeasurementOptions{StopGrace: 50 * time.Millisecond, ReapTimeout: time.Second})
	if err == nil || result.Execution != "signaled" || result.Signal == "" || result.GroupStatus != "cleared" {
		t.Fatalf("oracle result=%+v err=%v", result, err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("reader exceeded bounded stop")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("reader did not use provided deadline")
	}
}

func TestMeasuredRunnerFreshReuseAndTiming(t *testing.T) {
	opts, dir := measuredRunnerFixture(t)
	t.Setenv("YAKUORI_ACCEPTANCE_HELPER", "1")
	smi := filepath.Join(dir, "fake-smi")
	if err := os.WriteFile(smi, []byte("#!/bin/sh\necho 'GPU-test, Test GPU, test-driver, 1024'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	sup, err := NewSupervisor(os.Args[0], SupervisorOptions{GPUUUID: "GPU-test", Interval: time.Millisecond, QueryTimeout: time.Second, StopGrace: time.Millisecond * 50, ReapTimeout: time.Second, SMIBinary: smi})
	if err != nil {
		t.Fatal(err)
	}
	opts.measurement = &measurementSession{supervisor: sup, options: MeasurementOptions{WallLimit: time.Second * 10, PreparationLimit: time.Second * 10, StopGrace: time.Millisecond * 50, ReapTimeout: time.Second, ModelQuant: "fixture", ModelQuantSource: "test-only", RepeatIndex: 1, RepeatTotal: 1}}
	build := BuildInfo{Revision: "controlled-test-only", Modified: "false", GoVersion: "test-only", Modules: map[string]string{"github.com/townsendmerino/goinfer": "test-only", "github.com/townsendmerino/goinfer/cuda": "test-only"}}
	fakeRecords, fakeReader := filepath.Join(dir, "fake.records"), filepath.Join(dir, "fake-reader")
	if err := os.WriteFile(fakeReader, []byte(fmt.Sprintf("#!/bin/sh\ncp %s \"$3\"\n", strconv.Quote(fakeRecords))), 0700); err != nil {
		t.Fatal(err)
	}
	launcher := func(ctx context.Context, req WorkerRequest) (WorkerResult, error) {
		worker, err := executeWorker(ctx, os.Args[0], []string{"-test.run=^TestAcceptanceWorkerHelper$"}, req)
		if err != nil {
			return worker, err
		}
		worker.Build = build
		generation := worker.Observation.GenerationDuration.Seconds()
		calls, completed, failed := worker.Observation.GenerateCalls, worker.Observation.GenerateCompleted, worker.Observation.GenerateFailed
		validation, tmValidation := worker.Observation.Validation, worker.Observation.TMValidation
		rssBytes, processVRAM, deviceVRAM := int64(4096), int64(1024), int64(2048)
		// Controlled GPU samples are labeled as test data; they establish only
		// runner integration, not a real VRAM measurement.
		exitCode := 0
		sup.Results[req.RunID] = &Measurement{RunID: req.RunID, ResourceStatus: "complete", ChildOutcome: "exited", ExitCode: &exitCode, StopReason: "none", ResultStatus: "available", ReapStatus: "reaped", GroupStatus: "cleared", MonitoringStatus: "complete", RSSBytes: &rssBytes, ProcessVRAMBytes: &processVRAM, DeviceVRAMBytes: &deviceVRAM, LoadStatus: worker.LoadStatus, LoadSeconds: worker.LoadSeconds, GenerationStatus: "complete", GenerationSeconds: &generation, GenerationCalls: &calls, GenerateCompleted: &completed, GenerateFailed: &failed, Validation: &validation, TMValidation: &tmValidation}
		if err := os.WriteFile(fakeRecords, encodeOracleRecords(t, worker.Observation.Units, worker.Observation.Keys), 0600); err != nil {
			t.Fatal(err)
		}
		return worker, nil
	}
	builder := func(context.Context, string, string) (string, string, bool, error) {
		time.Sleep(40 * time.Millisecond)
		return fakeReader, "test-only", false, nil
	}
	rec, err := runWith(context.Background(), opts, launcher, builder, build)
	if err != nil || rec.Status != "pass" || rec.Measurement.Status != "complete" {
		t.Fatalf("run status=%s measurement=%+v err=%v", rec.Status, rec.Measurement, err)
	}
	if len(rec.Runs) != 2 || rec.Runs[0].Status != "pass" || rec.Runs[1].Status != "pass" || rec.Runs[0].MeasurementStatus != "complete" || rec.Runs[1].MeasurementStatus != "complete" {
		t.Fatalf("runs: %+v", rec.Runs)
	}
	if rec.Runs[0].Measurement.GenerationCalls == nil || *rec.Runs[0].Measurement.GenerationCalls != 2 || rec.Runs[1].Measurement.GenerationCalls == nil || *rec.Runs[1].Measurement.GenerationCalls != 0 {
		t.Fatalf("wrong generation attribution: %+v", rec.Runs)
	}
	if rec.Runs[0].MeasurementWallSeconds == nil || rec.Runs[1].MeasurementWallSeconds == nil || rec.Measurement.PreparationSeconds == nil || rec.Measurement.OracleBuildSeconds == nil || *rec.Measurement.OracleBuildSeconds < 0.04 || rec.Measurement.RunnerExcludingBuildSeconds == nil || len(rec.Measurement.PairCleanup) != 1 || rec.Measurement.PairCleanup[0].Status != "pass" {
		t.Fatalf("timing/cleanup missing: %+v", rec.Measurement)
	}
	opts.Evidence = filepath.Join(dir, "missing-model-quant-evidence")
	opts.measurement.options.ModelQuant = ""
	rec, err = runWith(context.Background(), opts, launcher, builder, build)
	if err == nil || rec.Runs[0].Status != "pass" || rec.Runs[0].Measurement.ResourceStatus != "complete" || rec.Runs[0].MeasurementStatus != "incomplete" || rec.Runs[1].MeasurementStatus != "not_run" || !slices.Contains(rec.Runs[0].MeasurementMissing, "model_quant") {
		t.Fatalf("metadata failure not bound to row: %+v err=%v", rec.Runs, err)
	}
	if _, err := os.Stat(filepath.Join(opts.Evidence, "fixture-000", ".tm")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("working TM remains: %v", err)
	}
}

func TestMeasuredPreparationRejectsFIFOAndSavesEvidence(t *testing.T) {
	for _, target := range []string{"manifest", "fixture"} {
		t.Run(target, func(t *testing.T) {
			opts, dir := measuredRunnerFixture(t)
			fifo := filepath.Join(dir, "blocking-fifo")
			if err := unix.Mkfifo(fifo, 0600); err != nil {
				t.Fatal(err)
			}
			if target == "manifest" {
				opts.Manifest = fifo
			} else {
				var manifest Manifest
				if err := Load(opts.Manifest, &manifest); err != nil {
					t.Fatal(err)
				}
				manifest.Fixtures[0].LocalPath = fifo
				raw, err := json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(opts.Manifest, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			sup, err := NewSupervisor(os.Args[0], SupervisorOptions{GPUUUID: "GPU-test", Interval: time.Millisecond, QueryTimeout: time.Millisecond * 50, StopGrace: time.Millisecond * 50, ReapTimeout: time.Second, SMIBinary: filepath.Join(dir, "no-smi")})
			if err != nil {
				t.Fatal(err)
			}
			opts.measurement = &measurementSession{supervisor: sup, options: MeasurementOptions{WallLimit: time.Second, PreparationLimit: time.Millisecond * 500, StopGrace: time.Millisecond * 50, ReapTimeout: time.Second, RepeatIndex: 1, RepeatTotal: 1}}
			build := BuildInfo{Revision: "controlled-test-only", Modified: "false"}
			launch := func(context.Context, WorkerRequest) (WorkerResult, error) {
				return WorkerResult{}, errors.New("worker must not start")
			}
			builder := func(context.Context, string, string) (string, string, bool, error) {
				return "", "", false, errors.New("oracle must not build")
			}
			type outcome struct {
				record RunRecord
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				rec, err := runWith(context.Background(), opts, launch, builder, build)
				done <- outcome{rec, err}
			}()
			select {
			case result := <-done:
				if result.err == nil || result.record.Measurement.PreparationStatus != "partial" || result.record.Measurement.PreparationSeconds == nil {
					t.Fatalf("failure not recorded: %+v err=%v", result.record.Measurement, result.err)
				}
				data, err := os.ReadFile(filepath.Join(opts.Evidence, "run.json"))
				if err != nil {
					t.Fatal(err)
				}
				var saved RunRecord
				if err := json.Unmarshal(data, &saved); err != nil {
					t.Fatal(err)
				}
				if saved.Measurement.Status != "incomplete" || saved.Measurement.PreparationStatus != "partial" || saved.Measurement.PreparationSeconds == nil {
					t.Fatalf("saved prep evidence: %+v", saved.Measurement)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("FIFO blocked preparation")
			}
			if _, err := fileDigest(context.Background(), fifo); err == nil {
				t.Fatal("FIFO accepted for digest")
			}
		})
	}
}

func TestAcceptanceInputCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var manifest Manifest
	if err := LoadContext(ctx, "testdata/small.w3strings", &manifest); !errors.Is(err, context.Canceled) {
		t.Fatalf("LoadContext: %v", err)
	}
	if _, err := CheckContext(ctx, ".", Manifest{}, Criteria{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("CheckContext: %v", err)
	}
	if _, err := fileDigest(ctx, "testdata/small.w3strings"); !errors.Is(err, context.Canceled) {
		t.Fatalf("fileDigest: %v", err)
	}
}

func TestMeasuredPreparationCancellationSaved(t *testing.T) {
	for _, tc := range []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		reason  string
	}{
		{"cancel", func() (context.Context, context.CancelFunc) {
			c, cancel := context.WithCancel(context.Background())
			cancel()
			return c, func() {}
		}, "canceled"},
		{"deadline", func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		}, "wall_limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, dir := measuredRunnerFixture(t)
			sup, err := NewSupervisor(os.Args[0], SupervisorOptions{GPUUUID: "GPU-test", Interval: time.Millisecond, QueryTimeout: time.Millisecond * 50, StopGrace: time.Millisecond * 50, ReapTimeout: time.Second, SMIBinary: filepath.Join(dir, "no-smi")})
			if err != nil {
				t.Fatal(err)
			}
			opts.measurement = &measurementSession{supervisor: sup, options: MeasurementOptions{WallLimit: time.Second, PreparationLimit: time.Second, StopGrace: time.Millisecond * 50, ReapTimeout: time.Second, RepeatIndex: 1, RepeatTotal: 1}}
			ctx, cancel := tc.context()
			defer cancel()
			launch := func(context.Context, WorkerRequest) (WorkerResult, error) {
				return WorkerResult{}, errors.New("unexpected launch")
			}
			builder := func(context.Context, string, string) (string, string, bool, error) {
				return "", "", false, errors.New("unexpected build")
			}
			rec, err := runWith(ctx, opts, launch, builder, BuildInfo{Revision: "controlled", Modified: "false"})
			if err == nil || rec.Measurement.PreparationStatus != "partial" || rec.Measurement.PreparationSeconds == nil || rec.Measurement.PreparationStopReason != tc.reason {
				t.Fatalf("cancel evidence: %+v err=%v", rec.Measurement, err)
			}
			raw, err := os.ReadFile(filepath.Join(opts.Evidence, "run.json"))
			if err != nil {
				t.Fatal(err)
			}
			var saved RunRecord
			if err := json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Measurement.PreparationStopReason != tc.reason || saved.Measurement.Status != "incomplete" {
				t.Fatalf("saved: %+v", saved.Measurement)
			}
		})
	}
}
