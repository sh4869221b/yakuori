//go:build offlinesmoke

package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/inference/goinfer"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/models"
)

type translationCancelEngine struct {
	*goinfer.Engine
	restore         func()
	returned        bool
	restored        bool
	closed          bool
	closeAfterDrain bool
	tokens          int
}

func (e *translationCancelEngine) Generate(ctx context.Context, request inference.GenerationRequest) (inference.GenerationResult, error) {
	result, err := e.Engine.Generate(ctx, request)
	// Native Generate returns only after draining tokens/outcome and activeDone.
	e.returned, e.tokens = true, result.OutputTokens
	e.restore()
	e.restored = true
	return result, err
}

func (e *translationCancelEngine) Close() error {
	e.closeAfterDrain = e.returned && e.restored
	if !e.restored {
		e.restore() // Also restore on any pre-generation failure.
		e.restored = true
	}
	e.closed = true
	return e.Engine.Close()
}

func TestTranslationNativeCancellation(t *testing.T) {
	// Given: actual CLI assembly, selected pinned model, real Core and Store.
	layout := newOfflineSmokeLayout(t)
	model, backend := offlineSmokeModel(t), offlineSmokeBackend(t)
	for _, args := range [][]string{
		{"models", "import", "--id", "copy", model}, {"models", "use", "copy"},
	} {
		code, _, diag := storageRun(args, layout.paths)
		if code != ExitOK {
			t.Fatalf("native setup: code=%d stderr=%q", code, diag)
		}
	}
	source := filepath.Join(layout.root, "source.txt")
	writeFixture(t, source, offlineTranslationSource)
	command, _, err := parseTranslation("translate", translationArgs("translate", "--backend", backend, source)[1:])
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wrapped *translationCancelEngine
	var observed <-chan struct{}
	open := func(ctx context.Context, requested string) (string, inference.Engine, error) {
		id, engine, err := models.New(layout.paths).OpenSelected(ctx, requested)
		if err != nil {
			return "", nil, err
		}
		native, ok := engine.(*goinfer.Engine)
		if !ok {
			return "", nil, errors.Join(errors.New("expected native engine"), engine.Close())
		}
		token, restore, err := native.OfflineSmokeCancelAfterToken(cancel)
		if err != nil {
			return "", nil, errors.Join(err, native.Close())
		}
		observed = token
		wrapped = &translationCancelEngine{Engine: native, restore: restore}
		return id, wrapped, nil
	}

	// When: hook cancels only after the first actual decoder token.
	var out, diag bytes.Buffer
	result, err := command.execute(ctx, &out, layout.paths, open)
	code := reportPipeline(&diag, result, err)
	rows := readTranslationTM(t, layout.paths)

	// Then: no reuse after CLI-owned Close is required.
	tokenObserved := false
	select {
	case <-observed:
		tokenObserved = true
	default:
	}
	var failure *localize.Error
	passed := wrapped != nil && tokenObserved && errors.Is(err, context.Canceled) && errors.As(err, &failure) &&
		failure.Phase == "generate" && code == ExitFailure && !result.TMCommitted &&
		result.BytesWritten == 0 && out.Len() == 0 && len(rows) == 0 &&
		wrapped.tokens > 0 && wrapped.closeAfterDrain && wrapped.closed
	record := map[string]any{
		"kind": "native_cli_assembly_core_store", "backend": backend, "token_observed": tokenObserved,
		"canceled": errors.Is(err, context.Canceled), "result": result, "stdout": out.String(),
		"stderr": diag.String(), "tm": rows, "pass": passed,
	}
	if wrapped != nil {
		record["tokens"], record["generate_returned"], record["hook_restored"], record["close_after_drain"] =
			wrapped.tokens, wrapped.returned, wrapped.restored, wrapped.closeAfterDrain
	}
	if os.Getenv("YAKUORI_PULL_EVIDENCE") != "" {
		recordOfflineSmokeEvidence(t, "native-cancellation", record)
	}
	if !passed {
		t.Fatalf("native cancellation: record=%+v err=%v", record, err)
	}
	t.Logf("native_token_observed=true tokens=%d canceled=true drain_completed=true hook_restored_before_close=true closed=true stdout_bytes=0 tm_rows=0 backend=%s", wrapped.tokens, backend)
}
