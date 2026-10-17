package cli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
	"golang.org/x/sys/unix"
)

type brokenWriter struct{ short bool }

func (w brokenWriter) Write(p []byte) (int, error) {
	if w.short {
		return 0, nil
	}
	return 0, errors.New("broken")
}

// Compare logical bytes as well as the row count; counts alone cannot prove scope.
func translationRows(t *testing.T, paths config.Paths) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+config.ResolveStoragePaths(paths).TMFile+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	rows, err := db.Query("SELECT translated_text FROM tm ORDER BY source_identity, translation_profile")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	var texts []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			t.Fatal(err)
		}
		texts = append(texts, text)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return texts
}

func TestTranslationCLIPipeline(t *testing.T) {
	t.Run("success_hit_scope", func(t *testing.T) {
		command, paths, engine := translationFixture(t, "Hello.\r\n")
		for _, scope := range [][2]string{{"qa", "sample"}, {"qa", "sample"}, {"qa", "other"}, {"other-game", "sample"}} {
			command.scope.GameNamespace, command.scope.ArtifactNamespace = scope[0], scope[1]
			var out bytes.Buffer
			result, err := command.execute(context.Background(), &out, paths, engine.open)
			if err != nil || !result.TMCommitted || out.String() != engine.text || result.BytesWritten != len(engine.text) {
				t.Fatalf("result=%+v err=%v stdout=%q", result, err, out.String())
			}
			checkTranslationRow(t, command, paths, engine, out.String())
		}
		if engine.generated != 3 || len(translationRows(t, paths)) != 3 || engine.closed != 4 {
			t.Fatalf("generated=%d closed=%d", engine.generated, engine.closed)
		}
		for _, input := range engine.inputs {
			if input.SourceLanguage != "en" || input.TargetLanguage != "ja" || !input.Terms.Empty() || input.Text != "" && input.Text != "Hello.\r\n" {
				t.Fatalf("input=%+v", input)
			}
		}
	})
	t.Run("empty_input", func(t *testing.T) {
		command, paths, engine := translationFixture(t, "")
		var out bytes.Buffer
		result, err := command.execute(context.Background(), &out, paths, engine.open)
		if err != nil || !result.TMCommitted || out.Len() != 0 || engine.generated != 0 {
			t.Fatalf("result=%+v err=%v generated=%d", result, err, engine.generated)
		}
		checkTranslationRow(t, command, paths, engine, "")
	})
	t.Run("input_limit", func(t *testing.T) {
		for _, size := range []int{980, 981, 1496} {
			t.Run(strconv.Itoa(size), func(t *testing.T) {
				command, paths, engine := translationFixture(t, strings.Repeat("a", size))
				var out bytes.Buffer
				result, err := command.execute(context.Background(), &out, paths, engine.open)
				if size == 980 {
					if err != nil || !result.TMCommitted || engine.generated != 1 {
						t.Fatalf("result=%+v err=%v", result, err)
					}
				} else if err == nil || result.TMCommitted || out.Len() != 0 || engine.generated != 0 || len(translationRows(t, paths)) != 0 {
					t.Fatalf("result=%+v err=%v generated=%d", result, err, engine.generated)
				}
				if size > 980 {
					var failure *localize.Error
					var limit *config.LimitError
					if !errors.As(err, &failure) || !errors.As(err, &limit) || size == 981 && failure.Phase != "import" || size == 1496 && failure.Phase != "input" {
						t.Fatalf("wrong limit phase: %v", err)
					}
				}
			})
		}
	})
	t.Run("generation_failure", func(t *testing.T) {
		for _, tt := range []struct{ name, phase string }{
			{"error", "generate"}, {"invalid_candidate", "restore"}, {"max_tokens", "generate"}, {"commit_conflict", "commit"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				command, paths, engine := translationFixture(t, "private source")
				switch tt.name {
				case "error":
					engine.generateErr = errors.New("private backend prompt")
				case "invalid_candidate":
					engine.text = ""
				case "max_tokens":
					engine.finish = inference.MaxTokens
				case "commit_conflict":
					engine.duringGenerate = func(context.Context) error {
						db, err := sql.Open("sqlite", "file:"+config.ResolveStoragePaths(paths).TMFile+"?mode=rw")
						if err != nil {
							return err
						}
						_, err = db.Exec("CREATE TRIGGER reject_commit BEFORE INSERT ON tm BEGIN SELECT RAISE(ABORT, 'private commit failure'); END")
						return errors.Join(err, db.Close())
					}
				}
				var out, diag bytes.Buffer
				result, err := command.execute(context.Background(), &out, paths, engine.open)
				var failure *localize.Error
				if !errors.As(err, &failure) || failure.Phase != tt.phase || reportPipeline(&diag, result, err) != 1 || result.TMCommitted || result.BytesWritten != 0 || out.Len() != 0 || len(translationRows(t, paths)) != 0 || engine.closed != 1 || strings.Contains(diag.String(), "private") {
					t.Fatalf("result=%+v err=%v diag=%q", result, err, diag.String())
				}
				t.Logf("phase=%s tm_committed=%t bytes_written=%d stdout_bytes=%d", failure.Phase, result.TMCommitted, result.BytesWritten, out.Len())
			})
		}
	})
	t.Run("close_failure", func(t *testing.T) {
		command, paths, engine := translationFixture(t, "Hello")
		engine.closeErr = errors.New("private close")
		var out, diag bytes.Buffer
		result, err := command.execute(context.Background(), &out, paths, engine.open)
		var failure *localize.Error
		if !errors.As(err, &failure) || failure.Phase != "close" || reportPipeline(&diag, result, err) != 1 || !result.TMCommitted || result.BytesWritten != len(engine.text) || out.String() != engine.text || strings.Contains(diag.String(), "private") {
			t.Fatalf("result=%+v err=%v diag=%q", result, err, diag.String())
		}
		checkTranslationRow(t, command, paths, engine, engine.text)
		t.Logf("phase=%s tm_committed=%t bytes_written=%d stdout_bytes=%d", failure.Phase, result.TMCommitted, result.BytesWritten, out.Len())
	})
	t.Run("stdout_failure_after_commit", func(t *testing.T) {
		command, paths, engine := translationFixture(t, "Hello")
		writer := &partialWriter{}
		result, err := command.execute(context.Background(), writer, paths, engine.open)
		if err == nil || !result.TMCommitted || result.BytesWritten != 3 || writer.String() != "訳" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		checkTranslationRow(t, command, paths, engine, engine.text)
	})
	t.Run("cancel_drain", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			command, paths, engine := translationFixture(t, "Hello")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ready, drained, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			engine.duringGenerate = func(ctx context.Context) error { close(ready); <-ctx.Done(); close(drained); return ctx.Err() }
			var result localize.Result
			var err error
			go func() { defer close(done); result, err = command.execute(ctx, io.Discard, paths, engine.open) }()
			<-ready
			cancel()
			<-done
			<-drained
			if !errors.Is(err, context.Canceled) || result.TMCommitted || engine.closed != 1 || len(translationRows(t, paths)) != 0 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	})
	t.Run("corrupt_tm", func(t *testing.T) {
		for _, suffix := range []string{"", "-journal", "-wal", "-shm", ".lock"} {
			t.Run(suffix, func(t *testing.T) {
				command, paths, engine := translationFixture(t, "Hello")
				path := config.ResolveStoragePaths(paths).TMFile + suffix
				if suffix == ".lock" {
					if err := os.Symlink(command.options.Source, path); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, []byte("private corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
				result, err := command.execute(context.Background(), io.Discard, paths, engine.open)
				var failure *localize.Error
				if !errors.As(err, &failure) || failure.Phase != "tm_open" || result.TMCommitted || result.BytesWritten != 0 || engine.generated != 0 || engine.closed != 1 {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				t.Logf("phase=%s tm_committed=%t generated=%d", failure.Phase, result.TMCommitted, engine.generated)
				if suffix != ".lock" {
					raw, err := os.ReadFile(path)
					if err != nil || string(raw) != "private corrupt" {
						t.Fatalf("raw=%q err=%v", raw, err)
					}
				}
			})
		}
	})
	t.Run("invalid_file", func(t *testing.T) {
		for _, kind := range []string{"utf8", "directory", "fifo", "symlink"} {
			t.Run(kind, func(t *testing.T) {
				command, paths, engine := translationFixture(t, "\xff")
				switch kind {
				case "directory":
					command.options.Source = paths.Data
				case "fifo", "symlink":
					path := filepath.Join(paths.Data, kind)
					var err error
					if kind == "fifo" {
						err = unix.Mkfifo(path, 0600)
					} else {
						err = os.Symlink(command.options.Source, path)
					}
					if err != nil {
						t.Fatal(err)
					}
					command.options.Source = path
				}
				result, err := command.execute(context.Background(), io.Discard, paths, engine.open)
				if err == nil || result.TMCommitted || engine.generated != 0 {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			})
		}
	})
	t.Run("localize", testTranslationFiles)
	t.Run("invalid_arguments", testTranslationArguments)
	t.Run("model_profile_admission", testTranslationAdmission)
}
