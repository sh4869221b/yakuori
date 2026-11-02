//go:build linux

package acceptance

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/sh4869221b/yakuori/internal/cli"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/models"
)

// WorkerRequest describes one production translation; it is also the direct
// child-process interface for a performance supervisor.
type WorkerRequest struct {
	RunID, FixtureID, Mode             string
	Source, Output, SourceLanguage     string
	SourceSHA256, ModelID, ModelSHA256 string
	Backend, Game, Artifact            string
	TMData                             string
	ModelPaths                         config.Paths
}

type BuildInfo struct {
	Revision  string            `json:"revision"`
	Modified  string            `json:"modified"`
	GoVersion string            `json:"go_version"`
	Modules   map[string]string `json:"modules"`
}

func CurrentBuild() BuildInfo {
	out := BuildInfo{Modules: make(map[string]string)}
	if info, ok := debug.ReadBuildInfo(); ok {
		out.GoVersion = info.GoVersion
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				out.Revision = s.Value
			case "vcs.modified":
				out.Modified = s.Value
			}
		}
		for _, dep := range info.Deps {
			if dep.Replace != nil {
				out.Modules[dep.Path] = dep.Replace.Version
			} else {
				out.Modules[dep.Path] = dep.Version
			}
		}
	}
	return out
}

type WorkerResult struct {
	RunID        string                         `json:"run_id"`
	FixtureID    string                         `json:"fixture_id"`
	Mode         string                         `json:"mode"`
	Build        BuildInfo                      `json:"build"`
	Details      cli.AcceptanceDetails          `json:"details"`
	Observation  localize.AcceptanceObservation `json:"observation"`
	Result       localize.Result                `json:"result"`
	FailurePhase string                         `json:"failure_phase,omitempty"`
	LoadSeconds  *float64                       `json:"load_seconds"`
	LoadStatus   string                         `json:"load_status"`
}

type EngineOpener func(context.Context, string) (string, inference.Engine, error)

func decodeDigest(s string) ([32]byte, error) {
	var digest [32]byte
	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) != len(digest) || hex.EncodeToString(raw) != s {
		return digest, errors.New("invalid SHA-256")
	}
	copy(digest[:], raw)
	return digest, nil
}

// RunWorker accepts an opener only so tests can exercise the identical process
// protocol with a controlled Engine. The command always supplies OpenSelected.
func RunWorker(ctx context.Context, req WorkerRequest, open EngineOpener) (result WorkerResult, err error) {
	result = WorkerResult{RunID: req.RunID, FixtureID: req.FixtureID, Mode: req.Mode, Build: CurrentBuild(), LoadStatus: "not_run"}
	defer func() {
		if err != nil {
			result.FailurePhase = "worker"
			var failure *localize.Error
			if errors.As(err, &failure) {
				result.FailurePhase = failure.Phase
			}
		}
	}()
	if req.RunID == "" || req.FixtureID == "" || (req.Mode != "fresh" && req.Mode != "reuse") || req.Source == "" || req.Output == "" || req.TMData == "" || req.ModelID == "" || req.Game == "" || req.Artifact == "" || req.Backend != "cuda" || open == nil {
		return result, errors.New("invalid worker request")
	}
	source, err := decodeDigest(req.SourceSHA256)
	if err != nil {
		return result, err
	}
	model, err := decodeDigest(req.ModelSHA256)
	if err != nil {
		return result, err
	}
	result.Observation.SourceSHA256 = source
	result.Details.ExpectedModelID, result.Details.ExpectedModelSHA256 = req.ModelID, model
	paths := req.ModelPaths
	paths.Data = req.TMData
	args := []string{"--format", "w3strings", "--source-language", req.SourceLanguage, "--target-language", "ja", "--game", req.Game, "--artifact", req.Artifact, "--backend", req.Backend, "--output", req.Output, req.Source}
	timedOpen := func(ctx context.Context, backend string) (string, inference.Engine, error) {
		started := time.Now()
		id, engine, openErr := open(ctx, backend)
		elapsed := time.Since(started).Seconds()
		result.LoadSeconds = &elapsed
		result.LoadStatus = "complete"
		if openErr != nil {
			result.LoadStatus = "failed"
		}
		return id, engine, openErr
	}
	result.Result, err = cli.RunW3Acceptance(ctx, args, paths, timedOpen, &result.Observation, &result.Details)
	return result, err
}

func OpenRegistered(paths config.Paths) EngineOpener { return models.New(paths).OpenSelected }

func (r WorkerResult) CheckRequest(req WorkerRequest) error {
	if r.RunID != req.RunID || r.FixtureID != req.FixtureID || r.Mode != req.Mode {
		return fmt.Errorf("worker result does not match request")
	}
	return nil
}
