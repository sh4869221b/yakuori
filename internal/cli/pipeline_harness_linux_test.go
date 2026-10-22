//go:build linux && pipelinefaults

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
)

// cliReachedWriter records what reportPipeline actually attempted to delegate
// and can turn the diagnostic write into a real writer error.
type cliReachedWriter struct {
	target    bytes.Buffer
	attempted []byte
	delegated int
	attempts  int
	err       error
}

func (w *cliReachedWriter) Write(raw []byte) (int, error) {
	w.attempts++
	w.attempted = append(w.attempted, raw...)
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.target.Write(raw)
	w.delegated += n
	return n, err
}

// cliCloseFaultEngine performs the real engine Close first, then reports EIO.
// The CLI must keep the published source/output/record and fail with that cause.
type cliCloseFaultEngine struct {
	*translationEngine
	calls int
	err   error
}

func (e *cliCloseFaultEngine) Close() error {
	e.calls++
	return errors.Join(e.translationEngine.Close(), e.err)
}

type cliPublishedRecord struct {
	SchemaVersion          int    `json:"schema_version"`
	Phase                  string `json:"phase"`
	LastCompletedOperation string `json:"last_completed_operation"`
}

// Task5 shares the authored text fixture T, not the shorter legacy CLI text.
const cliAuthoredTranslation = "訳文。\n"

type cliHarnessResult struct {
	code       int
	command    translationCommand
	paths      config.Paths
	output     string
	stdout     bytes.Buffer
	writer     *cliReachedWriter
	engine     *translationEngine
	wrapper    *cliCloseFaultEngine
	rows       []translationTMRow
	record     cliPublishedRecord
	recordPath string
}

func runCLITranslate(t *testing.T) cliHarnessResult {
	t.Helper()
	dir := t.TempDir()
	command, paths, engine := translationFixtureAt(t, dir, "Hello.\n")
	engine.text = cliAuthoredTranslation
	var out bytes.Buffer
	writer := &cliReachedWriter{}
	code := runWithTranslationOpener(context.Background(), translationArgs("translate", command.options.Source),
		&out, writer, func() (config.Paths, error) { return paths, nil }, nil, engine.open)
	return cliHarnessResult{
		code: code, command: command, paths: paths, stdout: out,
		writer: writer, engine: engine,
	}
}

func runCLILocalizeCloseFault(t *testing.T, writerErr error) cliHarnessResult {
	t.Helper()
	dir := t.TempDir()
	command, paths, engine := translationFixtureAt(t, dir, "Hello.\n")
	engine.text = cliAuthoredTranslation
	output := filepath.Join(dir, "output")
	wrapper := &cliCloseFaultEngine{translationEngine: engine, err: syscall.EIO}
	writer := &cliReachedWriter{err: writerErr}
	open := func(ctx context.Context, backend string) (string, inference.Engine, error) {
		id, _, err := engine.open(ctx, backend)
		return id, wrapper, err
	}
	var out bytes.Buffer
	code := runWithTranslationOpener(context.Background(),
		translationArgs("localize", "--output", output, command.options.Source),
		&out, writer, func() (config.Paths, error) { return paths, nil }, nil, open)
	result := cliHarnessResult{
		code: code, command: command, paths: paths, output: output,
		stdout: out, writer: writer, engine: engine, wrapper: wrapper,
	}
	result.rows = readTranslationTM(t, paths)
	records, err := filepath.Glob(filepath.Join(filepath.Dir(output), ".yakuori-run-*", "record.json"))
	if err != nil || len(records) != 1 {
		t.Fatalf("published records=%v error=%v, want exactly one", records, err)
	}
	result.recordPath = records[0]
	raw, err := os.ReadFile(result.recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &result.record); err != nil {
		t.Fatal(err)
	}
	return result
}

func cliExpectedKey(t *testing.T, result cliHarnessResult) identity.ExactKey {
	t.Helper()
	return expectedTranslationKey(t, translationTMInput{
		command: result.command, modelID: "selected-model", source: []byte("Hello.\n"),
	}, result.engine)
}

func cliLogicalRows(rows []translationTMRow) []byte {
	var logical bytes.Buffer
	for _, row := range rows {
		fmt.Fprintf(&logical, "key_schema=%d source_identity=%x translation_profile=%x translated_text=%q\n",
			row.Schema, row.Source, row.Profile, row.Text)
	}
	return logical.Bytes()
}

func TestPipelineHarnessCLI(t *testing.T) {
	t.Run("translate-text-stdout", func(t *testing.T) {
		// Given: the existing CLI fixture with the real common Engine/Core/SQLite.
		result := runCLITranslate(t)
		want := []byte(cliAuthoredTranslation)
		// Then: the public text stdout path delivers exactly the authored bytes.
		if result.code != ExitOK || !bytes.Equal(result.stdout.Bytes(), want) || result.writer.target.Len() != 0 {
			t.Fatalf("code=%d stdout=%q stderr=%q", result.code, result.stdout.String(), result.writer.target.String())
		}
		if result.engine.generated != 1 || result.engine.closed != 1 {
			t.Fatalf("generated=%d closed=%d, want 1/1", result.engine.generated, result.engine.closed)
		}
		rows := readTranslationTM(t, result.paths)
		key := expectedTranslationKey(t, translationTMInput{
			command: result.command, modelID: "selected-model", source: []byte("Hello.\n"),
		}, result.engine)
		if len(rows) != 1 || !rows[0].matches(key, cliAuthoredTranslation) {
			t.Fatalf("rows=%v, want the authored key with the translated bytes", rows)
		}
		pipelineCLISave(t, "stdout.bin", want)
		pipelineCLISave(t, "diagnostic.bin", nil)
		pipelineCLISave(t, "logical-rows.log", cliLogicalRows(rows))
		pipelineCLISaveJSON(t, "state.json", map[string]any{
			"case": "translate-text-stdout", "code": result.code, "stdout": result.stdout.String(),
			"generated": result.engine.generated, "closed": result.engine.closed, "rows": rows,
		})
		t.Logf("translate: exit=%d stdout_bytes=%d generated=%d closed=%d D1=%d", result.code, result.stdout.Len(), result.engine.generated, result.engine.closed, len(rows))
	})

	wantDiagnostic := "yakuori: localize close failed; TMCommitted=true; publication=published\n"
	for _, tc := range []struct {
		name       string
		writerErr  error
		wantTarget string
	}{
		{name: "localize-close-error", wantTarget: wantDiagnostic},
		{name: "localize-diagnostic-writer-error", writerErr: errors.New("diagnostic writer failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a real publication through the CLI and an Engine whose real
			// Close succeeds but whose wrapper then reports EIO.
			result := runCLILocalizeCloseFault(t, tc.writerErr)
			// Then: close failure is a real ExitFailure, the diagnostic writer is
			// reached (or actually fails), and publication state is untouched.
			if result.code != ExitFailure || result.stdout.Len() != 0 {
				t.Fatalf("code=%d stdout=%q", result.code, result.stdout.String())
			}
			if result.writer.attempts != 1 {
				t.Fatalf("diagnostic writer attempts=%d, want 1", result.writer.attempts)
			}
			if string(result.writer.attempted) != wantDiagnostic {
				t.Fatalf("attempted diagnostic=%q, want %q", result.writer.attempted, wantDiagnostic)
			}
			if tc.writerErr != nil {
				if result.writer.delegated != 0 || result.writer.target.Len() != 0 {
					t.Fatalf("failed writer delegated=%d target=%q", result.writer.delegated, result.writer.target.String())
				}
			} else if result.writer.delegated != len(wantDiagnostic) || result.writer.target.String() != wantDiagnostic {
				t.Fatalf("delegated=%d diagnostic=%q", result.writer.delegated, result.writer.target.String())
			}
			if result.wrapper.calls != 1 || result.engine.closed != 1 {
				t.Fatalf("wrapper Close calls=%d real engine Close calls=%d, want 1/1", result.wrapper.calls, result.engine.closed)
			}
			source, err := os.ReadFile(result.command.options.Source)
			if err != nil || string(source) != "Hello.\n" {
				t.Fatalf("source=%q error=%v", source, err)
			}
			output, err := os.ReadFile(result.output)
			if err != nil || string(output) != cliAuthoredTranslation {
				t.Fatalf("output=%q error=%v", output, err)
			}
			key := cliExpectedKey(t, result)
			if len(result.rows) != 1 || !result.rows[0].matches(key, cliAuthoredTranslation) {
				t.Fatalf("rows=%v, want one committed row despite the close failure", result.rows)
			}
			if result.record.Phase != "published" || result.record.LastCompletedOperation != "publish-rename" {
				t.Fatalf("record phase/operation = %q/%q", result.record.Phase, result.record.LastCompletedOperation)
			}
			pipelineCLISave(t, "stdout.bin", result.stdout.Bytes())
			pipelineCLISave(t, "diagnostic.bin", result.writer.attempted)
			pipelineCLISave(t, "logical-rows.log", cliLogicalRows(result.rows))
			pipelineCLISaveJSON(t, "state.json", map[string]any{
				"case": tc.name, "code": result.code, "writer_error": fmt.Sprint(tc.writerErr),
				"writer_attempts": result.writer.attempts, "delegated": result.writer.delegated,
				"diagnostic": string(result.writer.attempted), "wrapper_close_calls": result.wrapper.calls,
				"real_engine_close_calls": result.engine.closed, "rows": result.rows,
				"record": result.record, "record_path": result.recordPath,
				"source_bytes": len(source), "output_bytes": len(output),
			})
			t.Logf("%s: exit=%d writer_attempts=%d delegated=%d real_close=%d TM row=1 record=%s output_bytes=%d",
				tc.name, result.code, result.writer.attempts, result.writer.delegated, result.engine.closed, result.record.Phase, len(output))
		})
	}
}

func pipelineCLISave(t *testing.T, name string, raw []byte) {
	t.Helper()
	root := os.Getenv("YAKUORI_PIPELINE_EVIDENCE")
	if root == "" {
		return
	}
	path := filepath.Join(root, t.Name(), name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func pipelineCLISaveJSON(t *testing.T, name string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	pipelineCLISave(t, name, append(raw, '\n'))
}
