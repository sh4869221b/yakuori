//go:build offlinesmoke

package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/publication"
)

// These are injected engine failures, not native backend acceptance claims.
func testOfflineTranslationFailures(t *testing.T) {
	for _, name := range []string{"generation", "cancel", "close_stdout", "close_publication"} {
		t.Run(name, func(t *testing.T) {
			// Given: real assembly/Core/SQLite; only the selected engine is controlled.
			command, paths, engine := translationFixture(t, offlineTranslationSource)
			cause := errors.New("private injected engine failure")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			drained := false
			phase, committed := "generate", false
			switch name {
			case "generation":
				engine.generateErr = cause
			case "cancel":
				cause = context.Canceled
				engine.duringGenerate = func(ctx context.Context) error {
					cancel()
					<-ctx.Done()
					drained = true
					return ctx.Err()
				}
			case "close_stdout", "close_publication":
				engine.closeErr, phase, committed = cause, "close", true
				if name == "close_publication" {
					command.action = "localize"
					command.options.Output = filepath.Join(filepath.Dir(command.options.Source), "output.txt")
					command.options.Mode = publication.Create
				}
			}
			key := expectedTranslationKey(t, translationTMInput{
				command: command, modelID: "selected-model", source: []byte(offlineTranslationSource),
			}, engine)

			// When
			var out, diag bytes.Buffer
			result, err := command.execute(ctx, &out, paths, engine.open)
			code := reportPipeline(&diag, result, err)
			rows := readTranslationTM(t, paths)
			var published []byte
			if name == "close_publication" {
				raw, readErr := os.ReadFile(command.options.Output)
				if readErr != nil {
					t.Fatal(readErr)
				}
				published = raw
			}

			// Then: persist aggregate even if an assertion will fail.
			var failure *localize.Error
			typed := errors.As(err, &failure) && failure.Phase == phase && errors.Is(err, cause)
			passed := code == ExitFailure && typed && result.TMCommitted == committed && engine.closed == 1 &&
				!strings.Contains(diag.String(), "private") && (name != "cancel" || drained)
			if committed {
				passed = passed && len(rows) == 1 && translationTMContains(rows, key, engine.text)
				if name == "close_stdout" {
					passed = passed && out.String() == engine.text && result.BytesWritten == len(engine.text)
				} else {
					passed = passed && out.Len() == 0 && result.Publication.State == publication.Published && string(published) == engine.text
				}
			} else {
				passed = passed && out.Len() == 0 && result.BytesWritten == 0 && len(rows) == 0
			}
			recordOfflineSmokeEvidence(t, "matrix-injected-"+name, map[string]any{
				"kind": "controlled_engine_real_core_store", "case": name, "phase": phase, "typed_error": typed,
				"result": result, "stdout": out.String(), "stderr": diag.String(), "tm": rows,
				"published_bytes": string(published),
				"generated":       engine.generated, "closed": engine.closed, "drained": drained, "pass": passed,
			})
			if !passed {
				t.Fatalf("case=%s result=%+v err=%v stdout=%q rows=%v", name, result, err, out.String(), rows)
			}
		})
	}
}
