package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
)

type translationSignalRecord struct {
	Paths           config.Paths `json:"paths"`
	Ready           bool         `json:"ready"`
	Canceled        bool         `json:"canceled"`
	Drained         bool         `json:"drained"`
	CloseAfterDrain bool         `json:"close_after_drain"`
	Generated       int          `json:"generated"`
	Closed          int          `json:"closed"`
	Code            int          `json:"exit_code"`
}

type translationSignalEngine struct {
	*translationEngine
	record *translationSignalRecord
}

func (e *translationSignalEngine) Close() error {
	e.record.CloseAfterDrain = e.record.Drained
	return e.translationEngine.Close()
}

var translationSignalSelected selectedEngineOpener

func translationSignalOpen(original selectedEngineOpener) selectedEngineOpener {
	if translationSignalSelected != nil {
		return translationSignalSelected
	}
	return original
}

func TestTranslationSignalHelper(t *testing.T) {
	root := os.Getenv("YAKUORI_SIGNAL_ROOT")
	if root == "" {
		t.Skip("owned subprocess only")
	}
	code := ExitFailure
	t.Run("dispatch", func(t *testing.T) {
		command, paths, engine := translationFixtureAt(t, root, "private signal source\n")
		pipe := os.NewFile(3, "generate-ready")
		record := translationSignalRecord{Paths: paths}
		engine.duringGenerate = func(ctx context.Context) error {
			if _, err := io.WriteString(pipe, "READY\n"); err != nil {
				return errors.Join(err, pipe.Close())
			}
			if err := pipe.Close(); err != nil {
				return err
			}
			record.Ready = true
			<-ctx.Done()
			record.Canceled = errors.Is(ctx.Err(), context.Canceled)
			record.Drained = true
			return ctx.Err()
		}
		wrapped := &translationSignalEngine{translationEngine: engine, record: &record}
		translationSignalSelected = func(ctx context.Context, backend string) (string, inference.Engine, error) {
			id, _, err := engine.open(ctx, backend)
			return id, wrapped, err
		}
		defer func() { translationSignalSelected = nil }()
		code = run(translationArgs("translate", command.options.Source), os.Stdout, os.Stderr,
			func() (config.Paths, error) { return paths, nil })
		record.Code, record.Generated, record.Closed = code, engine.generated, engine.closed
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "result.json"), append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	})
	os.Exit(code)
}
