//go:build linux && offlinesmoke && pipelinefaults

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/inference/goinfer"
	"github.com/sh4869221b/yakuori/internal/models"
)

const offlinePipelineChild = "YAKUORI_OFFLINE_PIPELINE_CHILD"

// TestOfflinePipelineMatrix traces each selected test process and its children.
// Build and this outer driver are deliberately outside the observation scope.
func TestOfflinePipelineMatrix(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pipelineEvidence := os.Getenv("YAKUORI_PIPELINE_EVIDENCE")
	if pipelineEvidence == "" || !filepath.IsAbs(pipelineEvidence) {
		t.Fatal("YAKUORI_PIPELINE_EVIDENCE must be an absolute dedicated evidence directory")
	}
	cases := []struct{ name, selector string }{
		{"text-success", "^TestOfflinePipelineCase$/^text-success$"},
		{"w3-success", "^TestOfflinePipelineCase$/^w3-success$"},
		{"model-missing", "^TestOfflinePipelineCase$/^model-missing$"},
		{"model-mismatch", "^TestOfflinePipelineCase$/^model-mismatch$"},
		{"cuda-unsupported", "^TestOfflinePipelineCase$/^cuda-unsupported$"},
		{"generation-error", "^TestOfflinePipelineCase$/^generation-error$"},
		{"publication-error", "^TestPipelineCLIPublicationExit$/^postcommit-prepublish$/^text$/^create$"},
		{"cancel-recovery", "^TestPipelinePrecommitSignals$/^cpu-text-file-replace-existing-sigterm$"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			layout := newOfflineSmokeLayout(t)
			env := append(slices.Clone(layout.env), offlinePipelineEnv+"=1", offlinePipelineChild+"=1",
				"YAKUORI_PIPELINE_EVIDENCE="+filepath.Join(pipelineEvidence, tc.name))
			run := layout.runCommand(t, tc.name, 0, executable, env, false, "-test.run="+tc.selector, "-test.v", "-test.timeout=5m")
			leaf := strings.TrimSuffix(strings.TrimPrefix(tc.selector, "^"), "$")
			leaf = strings.ReplaceAll(leaf, "$/^", "/")
			if !strings.Contains(run.Stdout, "=== RUN   "+leaf+"\n") ||
				!strings.Contains(run.Stdout, "--- PASS: "+leaf+" ") ||
				strings.Contains(run.Stdout, "--- SKIP:") || strings.Contains(run.Stdout, "--- FAIL:") {
				t.Fatalf("selected leaf did not execute and pass: %s", leaf)
			}
			checkOfflinePipelineDiagnostics(t, []byte(run.Stdout), []byte(run.Stderr))
			// Existing child-process tests persist the actual CLI streams and
			// production record separately from their test-control transcripts.
			root := filepath.Join(pipelineEvidence, tc.name)
			if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() || (entry.Name() != "stderr.bin" && entry.Name() != "diagnostic.bin" && entry.Name() != "record.json") {
					return nil
				}
				raw, err := os.ReadFile(path)
				if err == nil {
					checkOfflinePipelineDiagnostics(t, raw)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			checkDiagnostic := func(path string, expected ...string) {
				t.Helper()
				raw, err := os.ReadFile(filepath.Join(root, path))
				if err != nil {
					t.Fatal(err)
				}
				for _, part := range expected {
					if !bytes.Contains(raw, []byte(part)) {
						t.Errorf("required diagnostic %q missing from %s", part, path)
					}
				}
			}
			switch tc.name {
			case "publication-error":
				checkDiagnostic("TestPipelineCLIPublicationExit/postcommit-prepublish/text/create/stderr.bin",
					"localize publish failed", "TMCommitted=true", "publication=not-published")
			case "cancel-recovery":
				base := "TestPipelinePrecommitSignals/cpu-text-file-replace-existing-sigterm/"
				checkDiagnostic(base+"initial/stderr.bin", "localize generate failed", "TMCommitted=false", "publication=not-published")
				checkDiagnostic(base+"rerun/stderr.bin", "localize prepare failed", "TMCommitted=false", "publication=not-published",
					"recovery reason=invalid or missing operation record", "state=not-published")
			}
		})
	}
}

func checkOfflinePipelineDiagnostics(t *testing.T, streams ...[]byte) {
	t.Helper()
	for _, stream := range streams {
		if offlinePipelineHasCanary(stream) {
			t.Error("normal diagnostic contains a fixture body canary")
		}
	}
}

func offlinePipelineHasCanary(stream []byte) bool {
	for _, canary := range []string{offlineSourceCanary, offlineTranslationCanary, offlinePromptCanary, offlineErrorCanary, "Hello.", strings.TrimSuffix(cliAuthoredTranslation, "\n")} {
		if bytes.Contains(stream, []byte(canary)) {
			return true
		}
	}
	return false
}

func TestOfflinePipelineCanaryDetection(t *testing.T) {
	for _, fixture := range []string{"Hello.\n", cliAuthoredTranslation} {
		for _, diagnostic := range []string{fixture, fmt.Sprintf("%q", fixture)} {
			if !offlinePipelineHasCanary([]byte(diagnostic)) {
				t.Error("raw or quoted fixture content was not detected")
			}
		}
	}
	if offlinePipelineHasCanary([]byte("yakuori: localize failed; TMCommitted=false\n")) {
		t.Error("ordinary diagnostic was incorrectly classified as a leak")
	}
}

func TestOfflinePipelineCase(t *testing.T) {
	if os.Getenv(offlinePipelineChild) != "1" || os.Getenv(offlinePipelineEnv) != "1" {
		t.Skip("selected only by the offline observer")
	}
	for _, name := range []string{"text-success", "w3-success", "model-missing", "model-mismatch", "cuda-unsupported", "generation-error"} {
		t.Run(name, func(t *testing.T) { runOfflinePipelineCase(t, name) })
	}
}

func runOfflinePipelineCase(t *testing.T, name string) {
	t.Helper()
	backend, surface, row := "cpu", "text-stdout-none", "happy"
	switch name {
	case "w3-success":
		surface = "w3-file-replace-existing"
	case "model-missing", "model-mismatch":
		row = name
	case "cuda-unsupported":
		backend, row = "cuda", "model-unselected"
	case "generation-error":
		row = "partial-plus-error"
	}
	f := newCLIPrecommitFixture(t, backend, surface, row)
	if surface == "w3-file-replace-existing" {
		f.Texts = []string{offlineSourceCanary + " one.", offlineSourceCanary + " two."}
		writeW3File(t, f.Command.options.Source, w3Table("en", f.Texts))
		f.Target = w3Table("jp", []string{"訳文 " + offlineTranslationCanary, "訳文 " + offlineTranslationCanary})
	} else {
		f.Texts = []string{offlineSourceCanary + ".\n"}
		writeFixture(t, f.Command.options.Source, f.Texts[0])
		f.Target = []byte("訳文 " + offlineTranslationCanary + "\n")
	}
	f.Source = cliPrecommitCapture(t, f.Command.options.Source)
	if name == "cuda-unsupported" {
		code, _, diag := storageRun([]string{"models", "use", "test-model"}, f.Paths)
		if code != ExitOK || diag != "" {
			t.Fatal("CUDA fixture selection failed")
		}
		for path := range f.Registry {
			f.Registry[path] = cliPrecommitCapture(t, path)
		}
		row = "model-cuda-unsupported"
	}
	// Construct independent expected TM rows for the authored source/target.
	wantEngine := &translationEngine{info: f.Info}
	f.D1 = slices.Clone(f.D0)
	translation := "訳文 " + offlineTranslationCanary
	if surface == "w3-file-replace-existing" {
		f.D1 = append(f.D1, expectedW3TranslationRows(t, w3TMInput{
			Raw: w3Table("en", f.Texts), SourceLanguage: "en", ModelID: "selected-model",
			Game: "qa", Artifact: "sample", Keys: map[string][][]byte{"1": {{0x78, 0x56, 0x34, 0x12}}},
			Texts: map[string]string{"1": translation, "2": translation},
		}, wantEngine)...)
	} else {
		translation += "\n"
		key := expectedTranslationKey(t, translationTMInput{command: f.Command, modelID: "selected-model", source: []byte(f.Texts[0])}, wantEngine)
		f.D1 = append(f.D1, translationTMRow{Schema: key.Schema(), Source: key.SourceBytes(), Profile: key.ProfileBytes(), Text: translation})
	}
	slices.SortFunc(f.D1, func(a, b translationTMRow) int {
		if n := bytes.Compare(a.Source, b.Source); n != 0 {
			return n
		}
		return bytes.Compare(a.Profile, b.Profile)
	})
	fault := errors.New(offlineErrorCanary)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := f.engine(row, cancel, fault)
	e.translationEngine.text = translation
	configureOfflinePipelineRequest(e)
	if name == "generation-error" {
		e.GenerateFunc = func(_ context.Context, request inference.GenerationRequest) (inference.GenerationResult, error) {
			if !strings.Contains(request.RenderedPrompt(), offlineSourceCanary) || !strings.Contains(request.RenderedPrompt(), offlinePromptCanary) {
				t.Error("generation error request canaries did not reach Generate")
			}
			e.Injected = append(e.Injected, row)
			return inference.GenerationResult{Text: translation, Finish: inference.Stop, PromptTokens: 1}, fault
		}
	}
	var openErr error
	if strings.HasPrefix(row, "model-") {
		e.OpenFunc = func(ctx context.Context, requested string) (string, inference.Engine, error) {
			id, engine, err := models.New(f.Paths).OpenSelected(ctx, requested)
			openErr = err
			return id, engine, err
		}
	}
	out, diag := &cliReachedWriter{}, &cliReachedWriter{}
	code := runWithTranslationOpener(ctx, f.Args, out, diag, func() (config.Paths, error) { return f.Paths, nil }, nil, e.Open)
	s := f.observe(t, e, out, diag)
	s.DispatchReturnCode = &code
	x := f.expectation(row, false, fault)
	if name == "cuda-unsupported" && !errors.Is(openErr, goinfer.ErrUnsupportedBackend) {
		t.Errorf("actual CUDA opener did not return unsupported backend: %v", openErr)
	}
	if name == "model-missing" && !errors.Is(openErr, os.ErrNotExist) {
		t.Errorf("actual Registry opener did not report missing model: %v", openErr)
	}
	if name == "model-mismatch" && (openErr == nil || openErr.Error() != `model "test-model" identity mismatch`) {
		t.Errorf("actual Registry opener did not report identity mismatch: %v", openErr)
	}
	for i := range x.Calls {
		call := &x.Calls[i]
		if call.Request != nil && call.Request.Text != "" {
			call.Request.Text += " " + offlinePromptCanary
		}
		if call.Operation == "generate" && call.Result != nil {
			call.Result.Text = translation
		}
	}
	if x.Code == 0 && surface == "text-stdout-none" {
		x.Stdout = []byte(translation)
	}
	if x.Code == 0 && surface == "w3-file-replace-existing" {
		x.Output = f.Target
	}
	for _, call := range s.Calls {
		if call.Operation == "generate" && (call.Request == nil || !strings.Contains(call.Request.Text, offlineSourceCanary) || !strings.Contains(call.Request.Text, offlinePromptCanary)) {
			t.Error("source and prompt canaries did not reach Generate")
		}
	}
	f.save(t, "initial", x, s)
	f.check(t, x, s)
	checkOfflinePipelineDiagnostics(t, s.Diagnostic)
	if x.Code != 0 && (!bytes.Contains(s.Diagnostic, []byte("localize "+x.Phase+" failed")) || !bytes.Contains(s.Diagnostic, []byte("TMCommitted=false"))) {
		t.Error("failure diagnostic is missing its phase or TM state")
	}
	for _, dir := range s.Runs {
		raw, err := os.ReadFile(filepath.Join(dir, "record.json"))
		if err == nil {
			checkOfflinePipelineDiagnostics(t, raw)
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if name == "generation-error" && (len(s.Calls) == 0 || !reflect.DeepEqual(s.Rows, f.D0)) {
		t.Error("generation error did not retain the original TM")
	}
	t.Logf("case=%s dispatch=%d generated=%d stdout_bytes=%d rows=%d records=%d", name, code, s.Generated, len(s.Stdout), len(s.Rows), len(s.Records))
}
