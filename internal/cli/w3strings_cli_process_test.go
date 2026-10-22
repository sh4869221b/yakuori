package cli

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/publication"
)

type w3ProcessResult struct {
	ExitCode         int    `json:"exit_code"`
	Generated        int    `json:"generated"`
	Closed           int    `json:"closed"`
	Path             string `json:"path"`
	Backend          string `json:"backend"`
	RequestedBackend string `json:"requested_backend"`
	Engine           string `json:"engine"`
	EffectiveProfile string `json:"effective_profile,omitempty"`
	Record           string `json:"record,omitempty"`
	RecordPhase      string `json:"record_phase,omitempty"`
	Backup           string `json:"backup,omitempty"`
}

// TestW3StringsCLIProcess is an owned-process driver, not a unit assertion.
// Without YAKUORI_W3_PROCESS_ROOT it is inactive and the package test continues.
func TestW3StringsCLIProcess(t *testing.T) {
	root := os.Getenv("YAKUORI_W3_PROCESS_ROOT")
	if root == "" {
		return
	}
	args := flag.Args()
	paths := config.Paths{
		Data: filepath.Join(root, "data"), Config: filepath.Join(root, "config"),
		Cache: filepath.Join(root, "cache"), State: filepath.Join(root, "state"),
	}
	for _, dir := range []string{paths.Data, paths.Config, paths.Cache, paths.State} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	engine := newW3CLIEngine()
	before := w3Records(t, root)
	code := runWithTranslationOpener(context.Background(), args, os.Stdout, os.Stderr,
		func() (config.Paths, error) { return paths, nil }, nil, engine.open)
	result := w3ProcessResult{
		ExitCode: code, Generated: engine.generated, Closed: engine.closed,
		Backend: engine.info.Backend, RequestedBackend: engine.requestedBackend,
		Engine: "controlled-common-Engine",
	}
	if engine.opened {
		profile := w3ExpectedProfile(t, engine, engine.modelID, w3ProcessLanguage(args))
		result.EffectiveProfile = hex.EncodeToString(profile.CanonicalBytes())
	}
	for _, path := range w3Records(t, root) {
		if slices.Contains(before, path) {
			continue
		}
		result.Record = path
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var record w3Record
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		result.Path, result.RecordPhase = record.Output.Path, record.Phase
		if record.Backup != nil {
			result.Backup = record.Backup.Path
		}
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "result.json"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	os.Exit(code)
}

func w3ProcessLanguage(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--source-language" {
			return args[i+1]
		}
	}
	return "en"
}

func TestW3StringsCLIProcessAcceptance(t *testing.T) {
	if os.Getenv("YAKUORI_W3_PROCESS_ROOT") != "" {
		return
	}
	raw := w3FixtureRaw(t)
	root := t.TempDir()
	source := filepath.Join(root, "source.w3strings")
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "output.w3strings")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	args := append([]string{"-test.run", "^TestW3StringsCLIProcess$", "--"},
		w3Args("cpu", "en", w3CLIGame, w3CLIArtifact, source, publication.Create, output)...)
	child := exec.CommandContext(ctx, binary, args...)
	child.Env = append(os.Environ(), "YAKUORI_W3_PROCESS_ROOT="+root)
	var out, diag bytes.Buffer
	child.Stdout, child.Stderr = &out, &diag
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	waitErr := child.Wait()
	reaped := true
	if ctx.Err() != nil {
		t.Fatalf("helper did not finish inside the bounded context: %v", ctx.Err())
	}
	data, err := os.ReadFile(filepath.Join(root, "result.json"))
	if err != nil {
		t.Fatalf("helper result: %v stdout=%q stderr=%q", err, out.String(), diag.String())
	}
	var result w3ProcessResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	paths := config.Paths{
		Data: filepath.Join(root, "data"), Config: filepath.Join(root, "config"),
		Cache: filepath.Join(root, "cache"), State: filepath.Join(root, "state"),
	}
	supported := translationPublicationSupported(t, root)
	if supported {
		if waitErr != nil || child.ProcessState.ExitCode() != ExitOK || out.Len() != 0 {
			t.Fatalf("helper: wait=%v exit=%d stdout=%q stderr=%q", waitErr, child.ProcessState.ExitCode(), out.String(), diag.String())
		}
		if result.ExitCode != ExitOK || result.Generated != 19 || result.Closed != 1 || result.RecordPhase != "published" ||
			result.Path != output || result.Backend != "cpu" || result.RequestedBackend != "cpu" || result.Engine != "controlled-common-Engine" {
			t.Fatalf("helper result=%+v", result)
		}
		if current, err := os.ReadFile(source); err != nil || !bytes.Equal(current, raw) {
			t.Fatalf("helper changed source: err=%v", err)
		}
		exp := w3BaseExpectation()
		w3AssertPublished(t, raw, "en", output, exp)
		expected := expectedW3TranslationRows(t, w3TMInput{
			Raw: raw, SourceLanguage: "en", ModelID: w3CLIModel, Game: w3CLIGame, Artifact: w3CLIArtifact,
			Keys: exp.keys(t), Texts: exp.Texts,
		}, newW3CLIEngine())
		w3AssertRows(t, paths, expected)
		t.Logf("helper_pid=%d reaped=%t exit=0 generated=19 closed=1 record_phase=published output_reopened=true", child.Process.Pid, reaped)
	} else {
		if waitErr == nil || child.ProcessState.ExitCode() != ExitFailure || out.Len() != 0 {
			t.Fatalf("unsupported helper: wait=%v exit=%d stdout=%q stderr=%q", waitErr, child.ProcessState.ExitCode(), out.String(), diag.String())
		}
		if result.ExitCode != ExitFailure || result.Generated != 0 || result.Closed != 1 || result.RecordPhase != "" || result.Path != "" {
			t.Fatalf("unsupported result=%+v", result)
		}
		if current, err := os.ReadFile(source); err != nil || !bytes.Equal(current, raw) {
			t.Fatalf("unsupported helper changed source: err=%v", err)
		}
		if rows := readTranslationTM(t, paths); len(rows) != 0 {
			t.Fatalf("unsupported helper committed rows: %v", rows)
		}
		if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsupported helper produced output: %v", err)
		}
		t.Logf("helper_pid=%d reaped=%t typed filesystem refusal generated=0 closed=1", child.Process.Pid, reaped)
	}
	f := &w3CLIFixture{dir: root, paths: paths, engine: newW3CLIEngine(), raw: raw, source: source}
	w3CLIEvidence(t, "process-acceptance", f, result.ExitCode, out.String(), diag.String(), output, result.Record, map[string]any{
		"pid": child.Process.Pid, "reaped": reaped, "generated": result.Generated, "closed": result.Closed,
		"record_phase": result.RecordPhase, "effective_profile": result.EffectiveProfile,
	})
}
