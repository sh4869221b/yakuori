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

	"github.com/sh4869221b/yakuori/internal/cli"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/validate"
)

type RunOptions struct {
	Manifest, Criteria, Root, Repository, Evidence string
	ModelID, ModelSHA256, Game, Artifact           string
	ModelPaths                                     config.Paths
}

type UnitRecord struct {
	ID         string                `json:"id"`
	Kind       string                `json:"kind"`
	Review     []validate.ReviewCode `json:"review"`
	Categories []string              `json:"categories"`
}

type FixtureRun struct {
	RunID                 string                 `json:"run_id"`
	FixtureID             string                 `json:"fixture_id"`
	FixtureRevision       string                 `json:"fixture_revision"`
	FixtureSourceURL      string                 `json:"fixture_source_url"`
	FixtureLicense        string                 `json:"fixture_license"`
	Mode                  string                 `json:"mode"`
	Status                string                 `json:"status"`
	FailurePhase          string                 `json:"failure_phase,omitempty"`
	SourcePath            string                 `json:"source_path"`
	SourceSHA256          string                 `json:"source_sha256"`
	OutputPath            string                 `json:"output_path,omitempty"`
	OutputSHA256          string                 `json:"output_sha256,omitempty"`
	WorkerBuild           BuildInfo              `json:"worker_build"`
	Details               *cli.AcceptanceDetails `json:"details,omitempty"`
	Units                 []UnitRecord           `json:"units,omitempty"`
	GenerateCalls         *int                   `json:"generate_calls"`
	AcceptedUnits         *int                   `json:"accepted_units"`
	GeneratedUnits        *int                   `json:"generated_units"`
	ReusedUnits           *int                   `json:"reused_units"`
	UnchangedUnits        *int                   `json:"unchanged_units"`
	FinalValidated        *bool                  `json:"final_validated"`
	TMCommitted           *bool                  `json:"tm_committed"`
	FinalValidationStatus string                 `json:"final_validation_status"`
	CommitStatus          string                 `json:"commit_status"`
	PublicationStatus     string                 `json:"publication_status"`
	Publication           string                 `json:"publication,omitempty"`
	PublicationRunID      string                 `json:"publication_run_id,omitempty"`
	Oracle                OracleResult           `json:"oracle"`
	WorkerExit            string                 `json:"worker_exit"`
}

type OracleResult struct {
	Execution    string `json:"execution"` // not_run, launch_failed, exited, signaled, wait_failed
	ExitCode     *int   `json:"exit_code"`
	Signal       string `json:"signal,omitempty"`
	Comparison   string `json:"comparison"` // not_run, pass, fail
	FailurePhase string `json:"failure_phase,omitempty"`
}

func oracleNotRun() OracleResult { return OracleResult{Execution: "not_run", Comparison: "not_run"} }

type RunRecord struct {
	Schema          string       `json:"schema"`
	Status          string       `json:"status"`
	FailurePhase    string       `json:"failure_phase,omitempty"`
	RunnerBuild     BuildInfo    `json:"runner_build"`
	ManifestVersion string       `json:"manifest_version,omitempty"`
	CriteriaVersion string       `json:"criteria_version,omitempty"`
	Selected        []string     `json:"selected,omitempty"`
	Root            string       `json:"fixture_root,omitempty"`
	ModelID         string       `json:"model_id,omitempty"`
	ModelSHA256     string       `json:"model_sha256,omitempty"`
	Game            string       `json:"game,omitempty"`
	Artifact        string       `json:"artifact,omitempty"`
	OracleRevision  string       `json:"oracle_revision,omitempty"`
	OracleModified  *bool        `json:"oracle_modified"`
	OracleVersion   string       `json:"oracle_version,omitempty"`
	Runs            []FixtureRun `json:"runs"`
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
	cmd := exec.CommandContext(ctx, oracle, "records", req.Output, recordsPath)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		result.Execution, result.FailurePhase = "launch_failed", "oracle_start"
		return result, errors.New("independent reader could not start")
	}
	err = cmd.Wait()
	if cmd.ProcessState == nil {
		result.Execution, result.FailurePhase = "wait_failed", "oracle_wait"
		return result, errors.New("independent reader exit unavailable")
	}
	if code := cmd.ProcessState.ExitCode(); code >= 0 {
		result.Execution, result.ExitCode = "exited", &code
	} else {
		result.Execution = "signaled"
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			result.Signal = status.Signal().String()
		}
	}
	if err != nil {
		result.FailurePhase = "oracle_exit"
		return result, errors.New("independent reader failed")
	}
	data, err := os.ReadFile(recordsPath)
	if err != nil {
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

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
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
	manifest := filepath.Join(repo, "tools/w3spike/oracle/Cargo.toml")
	lock := filepath.Join(repo, "tools/w3spike/oracle/Cargo.lock")
	contents, err := os.ReadFile(lock)
	if err != nil || !bytes.Contains(contents, []byte("name = \"w3strings\"\nversion = \"0.2.0\"")) {
		return "", "", false, errors.New("oracle lock is unavailable or unsupported")
	}
	revisionCmd := exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "HEAD")
	revision, err := revisionCmd.Output()
	if err != nil {
		return "", "", false, errors.New("oracle revision unavailable")
	}
	statusCmd := exec.CommandContext(ctx, "git", "-C", repo, "status", "--porcelain", "--", "tools/w3spike/oracle")
	status, err := statusCmd.Output()
	if err != nil {
		return "", "", false, errors.New("oracle modification status unavailable")
	}
	target := filepath.Join(evidence, ".oracle-target")
	cmd := exec.CommandContext(ctx, "cargo", "build", "--locked", "--offline", "--manifest-path", manifest, "--target-dir", target, "--message-format=json")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	if err := cmd.Run(); err != nil {
		return "", "", false, errors.New("offline oracle build failed")
	}
	var executable string
	dec := json.NewDecoder(&out)
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
	return runWith(ctx, opts, launch, buildOracle, CurrentBuild())
}

func runWith(ctx context.Context, opts RunOptions, launch WorkerLauncher, oracleBuilder func(context.Context, string, string) (string, string, bool, error), runnerBuild BuildInfo) (record RunRecord, err error) {
	record = RunRecord{Schema: "yakuori-w3-acceptance-run-v1", Status: "not_run", RunnerBuild: runnerBuild, Runs: []FixtureRun{}}
	if opts.Evidence == "" {
		return record, errors.New("missing acceptance arguments")
	}
	if err = prepareEvidence(opts.Evidence); err != nil {
		return record, err
	}
	defer func() {
		if saveErr := saveRecord(opts.Evidence, record); saveErr != nil {
			err = errors.Join(err, saveErr)
		}
	}()
	defer os.RemoveAll(filepath.Join(opts.Evidence, ".oracle-target"))
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
	if err = Load(opts.Manifest, &manifest); err != nil {
		record.FailurePhase = "manifest"
		return
	}
	if err = Load(opts.Criteria, &criteria); err != nil {
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
			record.Runs = append(record.Runs, FixtureRun{RunID: dirName + "-" + mode, FixtureID: fixture.ID, FixtureRevision: fixture.Revision, FixtureSourceURL: fixture.SourceURL, FixtureLicense: fixture.License, Mode: mode, Status: "not_run", SourcePath: fixture.LocalPath, SourceSHA256: fixture.SHA256, OutputPath: filepath.Join(dirName, mode+".w3strings"), Oracle: oracleNotRun(), WorkerExit: "not_run", FinalValidationStatus: "not_run", CommitStatus: "not_run", PublicationStatus: "not_run"})
		}
	}
	record.Root, err = filepath.Abs(opts.Root)
	if err != nil {
		return
	}
	record.ModelID, record.ModelSHA256, record.Game, record.Artifact = opts.ModelID, opts.ModelSHA256, opts.Game, opts.Artifact
	var checked Result
	checked, err = Check(record.Root, manifest, criteria)
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
	oracle, record.OracleRevision, oracleModified, err = oracleBuilder(ctx, opts.Repository, opts.Evidence)
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
		source := fixture.LocalPath
		if !filepath.IsAbs(source) {
			source = filepath.Join(record.Root, source)
		}
		dirName := fmt.Sprintf("fixture-%03d", position/2)
		dir := filepath.Join(opts.Evidence, dirName)
		if err = os.Mkdir(dir, 0700); err != nil {
			record.Status, record.FailurePhase = "fail", "evidence"
			return
		}
		tmDir := filepath.Join(dir, ".tm")
		if err = os.Mkdir(tmDir, 0700); err != nil {
			record.Status, record.FailurePhase = "fail", "tm"
			return
		}
		var freshProfile [32]byte
		for _, mode := range []string{"fresh", "reuse"} {
			req := WorkerRequest{RunID: dirName + "-" + mode, FixtureID: fixture.ID, Mode: mode, Source: source, SourceLanguage: fixture.Language, SourceSHA256: fixture.SHA256, ModelID: opts.ModelID, ModelSHA256: opts.ModelSHA256, Backend: "cuda", Game: opts.Game, Artifact: opts.Artifact, TMData: tmDir, ModelPaths: opts.ModelPaths, Output: filepath.Join(dir, mode+".w3strings")}
			entry := record.Runs[position]
			entry.Status = "fail"
			worker, workerErr := launch(ctx, req)
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
			entry.Oracle, err = FinalizeWorker(ctx, oracle, req, worker, workerErr, filepath.Join(dir, ".oracle.records"))
			os.Remove(filepath.Join(dir, ".oracle.records"))
			if err != nil && entry.FailurePhase == "" {
				entry.FailurePhase = entry.Oracle.FailurePhase
			}
			if err == nil {
				entry.OutputSHA256, err = fileDigest(req.Output)
				if err != nil {
					entry.FailurePhase = "output_hash"
				}
			}
			var sourceSHA string
			if err == nil {
				sourceSHA, err = fileDigest(source)
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
					entry.OutputSHA256, _ = fileDigest(req.Output)
				}
				if entry.FailurePhase == "" {
					entry.FailurePhase = "oracle"
				}
				record.Status, record.FailurePhase = "fail", entry.FailurePhase
				record.Runs[position] = entry
				os.RemoveAll(tmDir)
				return
			}
			entry.Status = "pass"
			record.Runs[position] = entry
			position++
		}
		if err = os.RemoveAll(tmDir); err != nil {
			record.Status, record.FailurePhase = "fail", "cleanup"
			return
		}
	}
	record.Status = "pass"
	return record, nil
}
