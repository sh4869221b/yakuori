//go:build linux

package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sh4869221b/yakuori/internal/cli"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/validate"
)

type RunOptions struct {
	Manifest, Criteria, Root, Repository, Evidence string
	ModelID, ModelSHA256, Game, Artifact           string
	ModelPaths                                     config.Paths
	measurement                                    *measurementSession
}

// MeasurementOptions records operator-supplied conditions that cannot be
// inferred from the worker's model or from a GPU memory query.
type MeasurementOptions struct {
	WallLimit, PreparationLimit, StopGrace, ReapTimeout time.Duration
	ModelQuant, ModelQuantSource                        string
	RepeatIndex, RepeatTotal                            int
	Conditions                                          string
}

type measurementSession struct {
	supervisor *Supervisor
	options    MeasurementOptions
}

func measuredContext(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, limit)
}

// MeasurementRecord supplements, but does not replace, the product run result.
type MeasurementRecord struct {
	Status                      string                `json:"status"`
	Environment                 SupervisorEnvironment `json:"environment"`
	ModelQuant                  string                `json:"model_quant,omitempty"`
	ModelQuantSource            string                `json:"model_quant_source,omitempty"`
	RepeatIndex                 int                   `json:"repeat_index"`
	RepeatTotal                 int                   `json:"repeat_total"`
	Conditions                  string                `json:"conditions,omitempty"`
	WallLimitSeconds            float64               `json:"wall_limit_seconds"`
	PreparationSeconds          *float64              `json:"preparation_seconds"`
	PreparationStatus           string                `json:"preparation_status"`
	PreparationStopReason       string                `json:"preparation_stop_reason,omitempty"`
	OracleBuildSeconds          *float64              `json:"oracle_build_seconds"`
	RunnerExcludingBuildSeconds *float64              `json:"runner_excluding_build_seconds"`
	OracleCleanup               string                `json:"oracle_cleanup"`
	PairCleanup                 []PairCleanup         `json:"pair_cleanup"`
	Missing                     []string              `json:"missing,omitempty"`
}

type PairCleanup struct {
	FreshRunID string  `json:"fresh_run_id"`
	ReuseRunID string  `json:"reuse_run_id"`
	Status     string  `json:"status"`
	Seconds    float64 `json:"seconds"`
}

func measurementMissing(session *measurementSession, entry FixtureRun) []string {
	var missing []string
	env, m := session.supervisor.Environment, entry.Measurement
	if env.GPUStatus != "complete" || env.GPUName == "" || env.Driver == "" || env.GPUMemoryBytes == nil {
		missing = append(missing, "gpu_environment")
	}
	if env.QueryCleanup == "unknown" {
		missing = append(missing, "environment_query_cleanup")
	}
	if env.CPUModel == "" || env.LogicalCPUs <= 0 || env.RAMBytes == nil || env.Kernel == "" || env.OS == "" || env.Architecture == "" || env.GoVersion == "" {
		missing = append(missing, "host_environment")
	}
	if session.options.ModelQuant == "" || session.options.ModelQuantSource == "" {
		missing = append(missing, "model_quant")
	}
	if m == nil || m.ResourceStatus != "complete" {
		missing = append(missing, "worker_measurement")
	} else if m.LoadStatus != "complete" || m.LoadSeconds == nil || m.GenerationStatus != "complete" || m.GenerationSeconds == nil || m.GenerationCalls == nil || m.GenerateCompleted == nil || m.GenerateFailed == nil || m.Validation == nil || m.TMValidation == nil {
		missing = append(missing, "worker_observations")
	}
	if m == nil || m.ResultStatus != "available" || m.ReapStatus != "reaped" || m.GroupStatus != "cleared" || m.ChildOutcome != "exited" || m.ExitCode == nil || *m.ExitCode != 0 || m.StopReason != "none" {
		missing = append(missing, "worker_protocol")
	}
	if entry.Details == nil || entry.Details.Info.RequestedBackend == "" || entry.Details.Info.Backend == "" || entry.Details.Info.ContextTokens <= 0 || entry.Details.Info.ConfiguredContextTokens <= 0 || entry.Details.Info.ModelContextTokens <= 0 || entry.Details.Info.ComputeQuant == "" || entry.Details.Policy.Schema <= 0 || entry.WorkerBuild.GoVersion == "" || entry.WorkerBuild.Modules["github.com/townsendmerino/goinfer"] == "" || entry.WorkerBuild.Modules["github.com/townsendmerino/goinfer/cuda"] == "" {
		missing = append(missing, "worker_metadata")
	}
	return missing
}

type UnitRecord struct {
	ID         string                `json:"id"`
	Kind       string                `json:"kind"`
	Review     []validate.ReviewCode `json:"review"`
	Categories []string              `json:"categories"`
}

type FixtureRun struct {
	RunID                  string                 `json:"run_id"`
	FixtureID              string                 `json:"fixture_id"`
	FixtureRevision        string                 `json:"fixture_revision"`
	FixtureSourceURL       string                 `json:"fixture_source_url"`
	FixtureLicense         string                 `json:"fixture_license"`
	Mode                   string                 `json:"mode"`
	Status                 string                 `json:"status"`
	FailurePhase           string                 `json:"failure_phase,omitempty"`
	SourcePath             string                 `json:"source_path"`
	SourceSHA256           string                 `json:"source_sha256"`
	OutputPath             string                 `json:"output_path,omitempty"`
	OutputSHA256           string                 `json:"output_sha256,omitempty"`
	WorkerBuild            BuildInfo              `json:"worker_build"`
	Details                *cli.AcceptanceDetails `json:"details,omitempty"`
	Units                  []UnitRecord           `json:"units,omitempty"`
	GenerateCalls          *int                   `json:"generate_calls"`
	AcceptedUnits          *int                   `json:"accepted_units"`
	GeneratedUnits         *int                   `json:"generated_units"`
	ReusedUnits            *int                   `json:"reused_units"`
	UnchangedUnits         *int                   `json:"unchanged_units"`
	FinalValidated         *bool                  `json:"final_validated"`
	TMCommitted            *bool                  `json:"tm_committed"`
	FinalValidationStatus  string                 `json:"final_validation_status"`
	CommitStatus           string                 `json:"commit_status"`
	PublicationStatus      string                 `json:"publication_status"`
	Publication            string                 `json:"publication,omitempty"`
	PublicationRunID       string                 `json:"publication_run_id,omitempty"`
	Oracle                 OracleResult           `json:"oracle"`
	WorkerExit             string                 `json:"worker_exit"`
	Measurement            *Measurement           `json:"measurement,omitempty"`
	MeasurementStatus      string                 `json:"measurement_status,omitempty"`
	MeasurementMissing     []string               `json:"measurement_missing,omitempty"`
	MeasurementWallSeconds *float64               `json:"measurement_wall_seconds,omitempty"`
	FixtureUnits           *int                   `json:"fixture_units,omitempty"`
	FixtureBytes           *int                   `json:"fixture_bytes,omitempty"`
	FixtureTextBytes       *int                   `json:"fixture_text_bytes,omitempty"`
	OracleRecordsCleanup   string                 `json:"oracle_records_cleanup,omitempty"`
	MeasurementStopReason  string                 `json:"measurement_stop_reason,omitempty"`
}

type OracleResult struct {
	Execution    string `json:"execution"` // not_run, launch_failed, exited, signaled, wait_failed
	ExitCode     *int   `json:"exit_code"`
	Signal       string `json:"signal,omitempty"`
	Comparison   string `json:"comparison"` // not_run, pass, fail
	FailurePhase string `json:"failure_phase,omitempty"`
	GroupStatus  string `json:"group_status,omitempty"`
}

func oracleNotRun() OracleResult { return OracleResult{Execution: "not_run", Comparison: "not_run"} }

type RunRecord struct {
	Schema          string             `json:"schema"`
	Status          string             `json:"status"`
	FailurePhase    string             `json:"failure_phase,omitempty"`
	RunnerBuild     BuildInfo          `json:"runner_build"`
	ManifestVersion string             `json:"manifest_version,omitempty"`
	CriteriaVersion string             `json:"criteria_version,omitempty"`
	Selected        []string           `json:"selected,omitempty"`
	Root            string             `json:"fixture_root,omitempty"`
	ModelID         string             `json:"model_id,omitempty"`
	ModelSHA256     string             `json:"model_sha256,omitempty"`
	Game            string             `json:"game,omitempty"`
	Artifact        string             `json:"artifact,omitempty"`
	OracleRevision  string             `json:"oracle_revision,omitempty"`
	OracleModified  *bool              `json:"oracle_modified"`
	OracleVersion   string             `json:"oracle_version,omitempty"`
	Runs            []FixtureRun       `json:"runs"`
	Measurement     *MeasurementRecord `json:"measurement,omitempty"`
}

// ExecuteWorker launches exactly one translation as a direct child. A supervisor
// may instead launch the same executable's worker subcommand itself and pass
// its result and exit status to FinalizeWorker.
func ExecuteWorker(ctx context.Context, executable string, req WorkerRequest) (WorkerResult, error) {
	return executeWorker(ctx, executable, []string{"worker"}, req)
}

func executeWorker(ctx context.Context, executable string, args []string, req WorkerRequest) (WorkerResult, error) {
	var result WorkerResult
	request, err := json.Marshal(req)
	if err != nil {
		return result, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return result, err
	}
	defer reader.Close()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdin = bytes.NewReader(request)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.ExtraFiles = []*os.File{writer}
	if err := cmd.Start(); err != nil {
		writer.Close()
		return result, err
	}
	writer.Close()
	data, readErr := io.ReadAll(io.LimitReader(reader, 8<<20))
	reader.Close()
	waitErr := cmd.Wait()
	if readErr != nil {
		return result, readErr
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, errors.New("worker result unavailable")
	}
	if err := result.CheckRequest(req); err != nil {
		return result, err
	}
	return result, waitErr
}

// FinalizeWorker only invokes the oracle on the actual published output.
func FinalizeWorker(ctx context.Context, oracle string, req WorkerRequest, worker WorkerResult, childErr error, recordsPath string) (OracleResult, error) {
	return finalizeWorker(ctx, oracle, req, worker, childErr, recordsPath, nil)
}

func finalizeWorker(ctx context.Context, oracle string, req WorkerRequest, worker WorkerResult, childErr error, recordsPath string, options *MeasurementOptions) (OracleResult, error) {
	result := oracleNotRun()
	if err := worker.CheckRequest(req); err != nil {
		result.FailurePhase = "worker"
		return result, err
	}
	if childErr != nil || worker.FailurePhase != "" || !worker.Observation.FinalValidated || !worker.Result.TMCommitted || worker.Result.Publication.State != publication.Published {
		result.FailurePhase = "worker"
		return result, errors.New("worker did not publish successfully")
	}
	wantPath, err := filepath.Abs(req.Output)
	if err != nil || worker.Result.Publication.OutputPath != wantPath {
		result.FailurePhase = "publication"
		return result, errors.New("published output does not match the requested output")
	}
	if err := ctx.Err(); err != nil {
		result.FailurePhase = "oracle_start"
		return result, err
	}
	cmd := exec.CommandContext(ctx, oracle, "records", req.Output, recordsPath)
	if options != nil {
		cmd = exec.Command(oracle, "records", req.Output, recordsPath)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		result.Execution, result.FailurePhase = "launch_failed", "oracle_start"
		return result, errors.New("independent reader could not start")
	}
	var state *os.ProcessState
	if options == nil {
		err = cmd.Wait()
		state = cmd.ProcessState
	} else {
		state, err, result.GroupStatus = waitOracle(ctx, cmd, options.StopGrace, options.ReapTimeout)
	}
	if state == nil {
		result.Execution, result.FailurePhase = "wait_failed", "oracle_wait"
		return result, errors.New("independent reader exit unavailable")
	}
	if code := state.ExitCode(); code >= 0 {
		result.Execution, result.ExitCode = "exited", &code
	} else {
		result.Execution = "signaled"
		if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			result.Signal = status.Signal().String()
		}
	}
	if err != nil {
		result.FailurePhase = "oracle_exit"
		return result, errors.New("independent reader failed")
	}
	recordFile, err := openRegular(ctx, recordsPath)
	if err != nil {
		result.FailurePhase = "oracle_read"
		return result, errors.New("independent reader output unavailable")
	}
	data, readErr := io.ReadAll(contextReader{ctx, recordFile})
	if err = errors.Join(readErr, recordFile.Close()); err != nil {
		result.FailurePhase = "oracle_read"
		return result, errors.New("independent reader output unavailable")
	}
	if err := CompareOracle(data, worker.Observation.Units, worker.Observation.Keys); err != nil {
		result.Comparison, result.FailurePhase = "fail", "oracle_compare"
		return result, err
	}
	result.Comparison = "pass"
	return result, nil
}

// waitOracle has one Wait owner. A stopped oracle receives TERM and then KILL
// as a process group, while an unreapable process is reported as unknown.
func waitOracle(ctx context.Context, cmd *exec.Cmd, grace, reap time.Duration) (*os.ProcessState, error, string) {
	type outcome struct {
		state *os.ProcessState
		err   error
	}
	done := make(chan outcome, 1)
	go func() { err := cmd.Wait(); done <- outcome{cmd.ProcessState, err} }()
	finish := func(result outcome, stopErr error) (*os.ProcessState, error, string) {
		killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
			return result.state, errors.Join(stopErr, result.err), "unknown"
		}
		until := time.Now().Add(grace)
		for {
			if errors.Is(syscall.Kill(-cmd.Process.Pid, 0), syscall.ESRCH) {
				return result.state, errors.Join(stopErr, result.err), "cleared"
			}
			if time.Now().After(until) {
				return result.state, errors.Join(stopErr, result.err), "unknown"
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	select {
	case result := <-done:
		return finish(result, nil)
	case <-ctx.Done():
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case result := <-done:
		return finish(result, ctx.Err())
	case <-timer.C:
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	timer.Reset(reap)
	select {
	case result := <-done:
		return finish(result, ctx.Err())
	case <-timer.C:
		return nil, errors.New("independent reader was not reaped"), "unknown"
	}
}

func fileDigest(ctx context.Context, path string) (string, error) {
	f, err := openRegular(ctx, path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, contextReader{ctx, f}); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func prepareEvidence(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.Mkdir(path, 0700)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("evidence directory must be private")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("evidence directory must be empty")
	}
	return nil
}

func saveRecord(path string, record RunRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(path, "run.json"), data, 0600)
}

func buildOracle(ctx context.Context, repo, evidence string) (string, string, bool, error) {
	return buildOracleWithOptions(ctx, repo, evidence, nil)
}

func buildOracleWithOptions(ctx context.Context, repo, evidence string, options *MeasurementOptions) (string, string, bool, error) {
	manifest := filepath.Join(repo, "tools/w3spike/oracle/Cargo.toml")
	lock := filepath.Join(repo, "tools/w3spike/oracle/Cargo.lock")
	lockFile, err := openRegular(ctx, lock)
	if err != nil {
		return "", "", false, errors.New("oracle lock is unavailable or unsupported")
	}
	contents, readErr := io.ReadAll(io.LimitReader(contextReader{ctx, lockFile}, 1<<20))
	err = errors.Join(readErr, lockFile.Close())
	if err != nil || !bytes.Contains(contents, []byte("name = \"w3strings\"\nversion = \"0.2.0\"")) {
		return "", "", false, errors.New("oracle lock is unavailable or unsupported")
	}
	runCommand := func(name string, args ...string) ([]byte, error) {
		if options == nil {
			return exec.CommandContext(ctx, name, args...).Output()
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cmd := exec.Command(name, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, io.Discard
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		_, err, groupStatus := waitOracle(ctx, cmd, options.StopGrace, options.ReapTimeout)
		if groupStatus != "cleared" {
			err = errors.Join(err, errors.New("oracle build process group cleanup unknown"))
		}
		if err != nil {
			return nil, err
		}
		return out.Bytes(), err
	}
	revision, err := runCommand("git", "-C", repo, "rev-parse", "HEAD")
	if err != nil {
		return "", "", false, errors.New("oracle revision unavailable")
	}
	status, err := runCommand("git", "-C", repo, "status", "--porcelain", "--", "tools/w3spike/oracle")
	if err != nil {
		return "", "", false, errors.New("oracle modification status unavailable")
	}
	target := filepath.Join(evidence, ".oracle-target")
	out, err := runCommand("cargo", "build", "--locked", "--offline", "--manifest-path", manifest, "--target-dir", target, "--message-format=json")
	if err != nil {
		return "", "", false, errors.New("offline oracle build failed")
	}
	var executable string
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var message struct {
			Reason     string  `json:"reason"`
			Executable *string `json:"executable"`
			Target     struct {
				Name string `json:"name"`
			} `json:"target"`
		}
		if err := dec.Decode(&message); err != nil {
			return "", "", false, errors.New("oracle build output invalid")
		}
		if message.Reason == "compiler-artifact" && message.Target.Name == "yakuori-w3strings-oracle" && message.Executable != nil {
			executable = *message.Executable
		}
	}
	if executable == "" {
		return "", "", false, errors.New("oracle executable unavailable")
	}
	return executable, strings.TrimSpace(string(revision)), len(status) != 0, nil
}

// Run executes confirmed acceptance inputs. The result never includes source or
// translation text; the output files remain private for later human review.
func Run(ctx context.Context, opts RunOptions) (record RunRecord, err error) {
	return RunWithLauncher(ctx, opts, func(ctx context.Context, req WorkerRequest) (WorkerResult, error) {
		executable, err := os.Executable()
		if err != nil {
			return WorkerResult{}, err
		}
		return ExecuteWorker(ctx, executable, req)
	})
}

// WorkerLauncher lets a supervisor own the translation child and wait for it.
type WorkerLauncher func(context.Context, WorkerRequest) (WorkerResult, error)

// RunWithLauncher shares preparation, acceptance checks and record creation
// with supervisors that directly launch and wait for the translation child.
func RunWithLauncher(ctx context.Context, opts RunOptions, launch WorkerLauncher) (record RunRecord, err error) {
	if opts.measurement != nil {
		builder := func(ctx context.Context, repo, evidence string) (string, string, bool, error) {
			return buildOracleWithOptions(ctx, repo, evidence, &opts.measurement.options)
		}
		return runWith(ctx, opts, launch, builder, CurrentBuild())
	}
	return runWith(ctx, opts, launch, buildOracle, CurrentBuild())
}

// RunMeasured uses the normal product runner while keeping measurement
// completeness independent of the worker and oracle product results.
func RunMeasured(ctx context.Context, opts RunOptions, supervisor *Supervisor, options MeasurementOptions) (RunRecord, error) {
	if supervisor == nil || options.WallLimit <= 0 || options.PreparationLimit <= 0 || options.StopGrace <= 0 || options.ReapTimeout <= 0 || options.RepeatIndex <= 0 || options.RepeatTotal < options.RepeatIndex {
		return RunRecord{}, errors.New("invalid measurement options")
	}
	opts.measurement = &measurementSession{supervisor: supervisor, options: options}
	return RunWithLauncher(ctx, opts, supervisor.Launch)
}

func runWith(ctx context.Context, opts RunOptions, launch WorkerLauncher, oracleBuilder func(context.Context, string, string) (string, string, bool, error), runnerBuild BuildInfo) (record RunRecord, err error) {
	started := time.Now()
	record = RunRecord{Schema: "yakuori-w3-acceptance-run-v1", Status: "not_run", RunnerBuild: runnerBuild, Runs: []FixtureRun{}}
	measurement := opts.measurement
	var buildDuration time.Duration
	if measurement != nil {
		m := measurement.options
		record.Measurement = &MeasurementRecord{Status: "not_run", PreparationStatus: "not_run", ModelQuant: m.ModelQuant, ModelQuantSource: m.ModelQuantSource, RepeatIndex: m.RepeatIndex, RepeatTotal: m.RepeatTotal, Conditions: m.Conditions, WallLimitSeconds: m.WallLimit.Seconds(), OracleCleanup: "not_run", PairCleanup: []PairCleanup{}}
	}
	if opts.Evidence == "" {
		return record, errors.New("missing acceptance arguments")
	}
	if err = prepareEvidence(opts.Evidence); err != nil {
		return record, err
	}
	defer func() {
		if record.Measurement != nil {
			seconds := (time.Since(started) - buildDuration).Seconds()
			record.Measurement.RunnerExcludingBuildSeconds = &seconds
			if err != nil && record.Measurement.Status != "incomplete" {
				record.Measurement.Status = "incomplete"
			}
		}
		if saveErr := saveRecord(opts.Evidence, record); saveErr != nil {
			err = errors.Join(err, saveErr)
		}
	}()
	defer func() {
		cleanupErr := os.RemoveAll(filepath.Join(opts.Evidence, ".oracle-target"))
		if record.Measurement != nil {
			record.Measurement.OracleCleanup = "pass"
			if cleanupErr != nil {
				record.Measurement.OracleCleanup = "fail"
				record.Measurement.Status = "incomplete"
				for i := range record.Runs {
					if record.Runs[i].MeasurementStatus != "" && record.Runs[i].MeasurementStatus != "not_run" {
						record.Runs[i].MeasurementStatus = "incomplete"
						record.Runs[i].MeasurementMissing = append(record.Runs[i].MeasurementMissing, "oracle_target_cleanup")
					}
				}
				err = errors.Join(err, cleanupErr)
			}
		}
	}()
	prepCtx := ctx
	prepEnded := false
	if measurement != nil {
		var cancel context.CancelFunc
		prepCtx, cancel = context.WithTimeout(ctx, measurement.options.PreparationLimit)
		defer cancel()
		record.Measurement.PreparationStatus = "partial"
		defer func() {
			if !prepEnded {
				if record.Measurement.PreparationSeconds == nil {
					seconds := time.Since(started).Seconds()
					record.Measurement.PreparationSeconds = &seconds
				}
				if prepCtx.Err() != nil {
					record.Measurement.PreparationStopReason = stopReason(prepCtx.Err())
				}
			}
		}()
		measurement.supervisor.CaptureEnvironment(prepCtx)
		record.Measurement.Environment = measurement.supervisor.Environment
		if record.Measurement.Environment.QueryCleanup == "unknown" {
			record.FailurePhase = "measurement"
			record.Measurement.Status = "incomplete"
			record.Measurement.Missing = []string{"environment_query_cleanup"}
			return record, errors.New("environment query cleanup unknown")
		}
	}
	if opts.Manifest == "" || opts.Criteria == "" || opts.Root == "" || opts.Repository == "" || opts.ModelID == "" || opts.Game == "" || opts.Artifact == "" || launch == nil {
		record.FailurePhase = "arguments"
		return record, errors.New("missing acceptance arguments")
	}
	if _, err = decodeDigest(opts.ModelSHA256); err != nil {
		record.FailurePhase = "model_pin"
		return record, err
	}
	var manifest Manifest
	var criteria Criteria
	if err = LoadContext(prepCtx, opts.Manifest, &manifest); err != nil {
		record.FailurePhase = "manifest"
		return
	}
	if err = LoadContext(prepCtx, opts.Criteria, &criteria); err != nil {
		record.FailurePhase = "criteria"
		return
	}
	record.ManifestVersion, record.CriteriaVersion, record.Selected = manifest.Version, criteria.Version, criteria.FixtureIDs
	selected := make(map[string]bool)
	for _, id := range criteria.FixtureIDs {
		selected[id] = true
	}
	for _, fixture := range manifest.Fixtures {
		if !selected[fixture.ID] {
			continue
		}
		dirName := fmt.Sprintf("fixture-%03d", len(record.Runs)/2)
		for _, mode := range []string{"fresh", "reuse"} {
			entry := FixtureRun{RunID: dirName + "-" + mode, FixtureID: fixture.ID, FixtureRevision: fixture.Revision, FixtureSourceURL: fixture.SourceURL, FixtureLicense: fixture.License, Mode: mode, Status: "not_run", SourcePath: fixture.LocalPath, SourceSHA256: fixture.SHA256, OutputPath: filepath.Join(dirName, mode+".w3strings"), Oracle: oracleNotRun(), WorkerExit: "not_run", FinalValidationStatus: "not_run", CommitStatus: "not_run", PublicationStatus: "not_run"}
			if measurement != nil {
				entry.MeasurementStatus = "not_run"
				entry.FixtureUnits, entry.FixtureBytes, entry.FixtureTextBytes = &fixture.Units, &fixture.Bytes, &fixture.TextBytes
			}
			record.Runs = append(record.Runs, entry)
		}
	}
	record.Root, err = filepath.Abs(opts.Root)
	if err != nil {
		return
	}
	record.ModelID, record.ModelSHA256, record.Game, record.Artifact = opts.ModelID, opts.ModelSHA256, opts.Game, opts.Artifact
	var checked Result
	checked, err = CheckContext(prepCtx, record.Root, manifest, criteria)
	if err != nil || !checked.AcceptanceReady {
		record.FailurePhase = "readiness"
		if err == nil {
			err = errors.New("acceptance criteria are not confirmed")
		}
		return
	}
	if opts.ModelPaths == (config.Paths{}) {
		opts.ModelPaths, err = config.Resolve(os.Getenv)
		if err != nil {
			record.FailurePhase = "config"
			return
		}
	}
	if record.RunnerBuild.Revision == "" || record.RunnerBuild.Modified == "" {
		record.FailurePhase = "build_info"
		err = errors.New("runner build revision unavailable")
		return
	}
	var oracle string
	var oracleModified bool
	if err = prepCtx.Err(); err != nil {
		record.FailurePhase = "preparation"
		return
	}
	if record.Measurement != nil {
		seconds := time.Since(started).Seconds()
		record.Measurement.PreparationSeconds = &seconds
		record.Measurement.PreparationStatus = "complete"
	}
	buildStarted := time.Now()
	oracle, record.OracleRevision, oracleModified, err = oracleBuilder(prepCtx, opts.Repository, opts.Evidence)
	buildDuration = time.Since(buildStarted)
	if record.Measurement != nil {
		seconds := buildDuration.Seconds()
		record.Measurement.OracleBuildSeconds = &seconds
		if prepCtx.Err() != nil {
			record.Measurement.PreparationStopReason = stopReason(prepCtx.Err())
		}
		prepEnded = true
	}
	if err != nil {
		record.FailurePhase = "oracle_build"
		return
	}
	record.OracleModified = &oracleModified
	record.OracleVersion = "w3strings 0.2.0"
	position := 0
	for _, fixture := range manifest.Fixtures {
		if !selected[fixture.ID] {
			continue
		}
		firstStarted := time.Now()
		firstCtx := ctx
		firstCancel := func() {}
		if measurement != nil {
			firstCtx, firstCancel = measuredContext(ctx, measurement.options.WallLimit)
		}
		source := fixture.LocalPath
		if !filepath.IsAbs(source) {
			source = filepath.Join(record.Root, source)
		}
		dirName := fmt.Sprintf("fixture-%03d", position/2)
		dir := filepath.Join(opts.Evidence, dirName)
		if err = os.Mkdir(dir, 0700); err != nil {
			firstCancel()
			record.Status, record.FailurePhase = "fail", "evidence"
			return
		}
		tmDir := filepath.Join(dir, ".tm")
		if err = os.Mkdir(tmDir, 0700); err != nil {
			firstCancel()
			record.Status, record.FailurePhase = "fail", "tm"
			return
		}
		cleanupPair := func() error {
			start := time.Now()
			cleanupErr := os.RemoveAll(tmDir)
			if record.Measurement != nil {
				status := "pass"
				if cleanupErr != nil {
					status = "fail"
					for i := range record.Runs {
						entry := &record.Runs[i]
						if (entry.RunID == dirName+"-fresh" || entry.RunID == dirName+"-reuse") && entry.MeasurementStatus != "not_run" {
							entry.MeasurementStatus = "incomplete"
							entry.MeasurementMissing = append(entry.MeasurementMissing, "tm_cleanup")
						}
					}
				}
				record.Measurement.PairCleanup = append(record.Measurement.PairCleanup, PairCleanup{FreshRunID: dirName + "-fresh", ReuseRunID: dirName + "-reuse", Status: status, Seconds: time.Since(start).Seconds()})
			}
			return cleanupErr
		}
		var freshProfile [32]byte
		for _, mode := range []string{"fresh", "reuse"} {
			modeCtx, modeCancel := firstCtx, firstCancel
			modeStarted := firstStarted
			if mode == "reuse" {
				modeStarted = time.Now()
				modeCtx, modeCancel = ctx, func() {}
				if measurement != nil {
					modeCtx, modeCancel = context.WithTimeout(ctx, measurement.options.WallLimit)
				}
			}
			req := WorkerRequest{RunID: dirName + "-" + mode, FixtureID: fixture.ID, Mode: mode, Source: source, SourceLanguage: fixture.Language, SourceSHA256: fixture.SHA256, ModelID: opts.ModelID, ModelSHA256: opts.ModelSHA256, Backend: "cuda", Game: opts.Game, Artifact: opts.Artifact, TMData: tmDir, ModelPaths: opts.ModelPaths, Output: filepath.Join(dir, mode+".w3strings")}
			entry := record.Runs[position]
			entry.Status = "fail"
			if measurement != nil {
				entry.MeasurementStatus = "incomplete"
			}
			worker, workerErr := launch(modeCtx, req)
			if measurement != nil {
				entry.Measurement = measurement.supervisor.Results[req.RunID]
			}
			entry.WorkerBuild = worker.Build
			entry.FinalValidationStatus, entry.CommitStatus, entry.PublicationStatus = "unknown", "unknown", "unknown"
			var generated, reused, unchanged int
			if worker.CheckRequest(req) == nil {
				calls, accepted := worker.Observation.GenerateCalls, len(worker.Observation.Units)
				final, committed := worker.Observation.FinalValidated, worker.Result.TMCommitted
				details := worker.Details
				entry.Details, entry.GenerateCalls, entry.AcceptedUnits = &details, &calls, &accepted
				entry.FinalValidated, entry.TMCommitted, entry.Publication = &final, &committed, string(worker.Result.Publication.State)
				entry.PublicationRunID = worker.Result.Publication.RunID
				entry.FinalValidationStatus, entry.CommitStatus, entry.PublicationStatus = "not_run", "not_run", "not_run"
				if final {
					entry.FinalValidationStatus = "pass"
				} else if worker.FailurePhase == "stage" {
					entry.FinalValidationStatus = "fail"
				}
				if committed {
					entry.CommitStatus = "pass"
				} else if worker.FailurePhase == "commit" {
					entry.CommitStatus = "fail"
				}
				if worker.Result.Publication.State == publication.Published {
					entry.PublicationStatus = "pass"
				} else if worker.FailurePhase == "publish" {
					entry.PublicationStatus = "fail"
				}
			}
			entry.WorkerExit = "pass"
			if workerErr != nil {
				entry.WorkerExit = "fail"
			}
			entry.FailurePhase = worker.FailurePhase
			if workerErr != nil && entry.FailurePhase == "" {
				entry.FailurePhase = "worker"
			}
			for _, unit := range worker.Observation.Units {
				switch unit.Kind {
				case "generated":
					generated++
				case "tm":
					reused++
				case "unchanged":
					unchanged++
				}
				u := UnitRecord{ID: unit.ID, Kind: unit.Kind, Review: unit.Review, Categories: []string{}}
				for _, category := range fixture.Categories {
					for _, id := range category.UnitIDs {
						if id == unit.ID {
							u.Categories = append(u.Categories, category.Name)
						}
					}
				}
				entry.Units = append(entry.Units, u)
			}
			if worker.CheckRequest(req) == nil {
				entry.GeneratedUnits, entry.ReusedUnits, entry.UnchangedUnits = &generated, &reused, &unchanged
			}
			if workerErr == nil && (worker.Build.Revision == "" || worker.Build.Modified == "" || worker.Build.Revision != record.RunnerBuild.Revision || worker.Build.Modified != record.RunnerBuild.Modified) {
				workerErr = errors.New("worker build mismatch")
				entry.FailurePhase = "build_info"
			}
			if workerErr == nil && mode == "fresh" {
				freshProfile = worker.Details.Profile
				if worker.Observation.GenerateCalls == 0 {
					workerErr = errors.New("no generation")
					entry.FailurePhase = "generate"
				}
			} else if workerErr == nil {
				if worker.Details.Profile != freshProfile || worker.Observation.GenerateCalls != 0 {
					workerErr = errors.New("TM reuse profile mismatch or generation")
					entry.FailurePhase = "reuse"
				}
				for _, unit := range worker.Observation.Units {
					if unit.Kind != "tm" {
						workerErr = errors.New("unit not reused")
						entry.FailurePhase = "reuse"
					}
				}
			}
			if workerErr == nil && len(worker.Observation.Units) != fixture.Units {
				workerErr = errors.New("accepted unit count mismatch")
				if entry.FailurePhase == "" {
					entry.FailurePhase = "accept"
				}
			}
			var measureOpts *MeasurementOptions
			if measurement != nil {
				measureOpts = &measurement.options
			}
			entry.Oracle, err = finalizeWorker(modeCtx, oracle, req, worker, workerErr, filepath.Join(dir, ".oracle.records"), measureOpts)
			removeErr := os.Remove(filepath.Join(dir, ".oracle.records"))
			if measurement != nil {
				entry.OracleRecordsCleanup = "pass"
				if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					entry.OracleRecordsCleanup = "fail"
				}
			}
			if err != nil && entry.FailurePhase == "" {
				entry.FailurePhase = entry.Oracle.FailurePhase
			}
			if err == nil {
				entry.OutputSHA256, err = fileDigest(modeCtx, req.Output)
				if err != nil {
					entry.FailurePhase = "output_hash"
				}
			}
			var sourceSHA string
			if err == nil {
				sourceSHA, err = fileDigest(modeCtx, source)
				if err != nil {
					entry.FailurePhase = "source_hash"
				}
				if err == nil && sourceSHA != fixture.SHA256 {
					err = errors.New("source changed")
					entry.FailurePhase = "source_hash"
				}
			}
			if err != nil {
				if worker.CheckRequest(req) == nil && worker.Result.Publication.State == publication.Published {
					entry.OutputSHA256, _ = fileDigest(modeCtx, req.Output)
				}
				if entry.FailurePhase == "" {
					entry.FailurePhase = "oracle"
				}
				record.Status, record.FailurePhase = "fail", entry.FailurePhase
				if measurement != nil {
					seconds := time.Since(modeStarted).Seconds()
					entry.MeasurementWallSeconds = &seconds
					if modeCtx.Err() != nil {
						entry.MeasurementStopReason = stopReason(modeCtx.Err())
					}
					entry.MeasurementMissing = measurementMissing(measurement, entry)
					entry.MeasurementMissing = append(entry.MeasurementMissing, "product_or_oracle")
				}
				modeCancel()
				record.Runs[position] = entry
				if cleanupErr := cleanupPair(); cleanupErr != nil && measurement != nil {
					err = errors.Join(err, cleanupErr)
				}
				return
			}
			entry.Status = "pass"
			if measurement != nil {
				seconds := time.Since(modeStarted).Seconds()
				entry.MeasurementWallSeconds = &seconds
			}
			modeErr := modeCtx.Err()
			modeCancel()
			record.Runs[position] = entry
			position++
			if measurement != nil {
				missing := measurementMissing(measurement, entry)
				if entry.Oracle.GroupStatus != "cleared" {
					missing = append(missing, "oracle_process_group")
				}
				if entry.OracleRecordsCleanup != "pass" {
					missing = append(missing, "oracle_records_cleanup")
				}
				if modeErr != nil {
					entry.MeasurementStopReason = stopReason(modeErr)
					missing = append(missing, "mode_deadline")
				}
				entry.MeasurementMissing = missing
				if len(missing) == 0 {
					entry.MeasurementStatus = "complete"
				}
				record.Runs[position-1] = entry
				if len(missing) == 0 {
					continue
				}
				record.Status, record.FailurePhase = "fail", "measurement"
				record.Measurement.Status = "incomplete"
				record.Measurement.Missing = missing
				err = errors.Join(errors.New("measurement incomplete"), cleanupPair())
				return
			}
		}
		if err = cleanupPair(); err != nil {
			record.Status, record.FailurePhase = "fail", "cleanup"
			return
		}
	}
	record.Status = "pass"
	if record.Measurement != nil {
		record.Measurement.Status = "complete"
	}
	return record, nil
}
