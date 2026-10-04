package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

type cliEngine func(context.Context, unit.UnitID, string) (localize.Generation, error)

func (f cliEngine) Generate(ctx context.Context, id unit.UnitID, text string) (localize.Generation, error) {
	return f(ctx, id, text)
}

type cliTM struct {
	rows []validate.AcceptedTranslation
}

func (tm *cliTM) Commit(_ context.Context, _ unit.Session, _ validate.Profile, accepted []validate.AcceptedTranslation) error {
	tm.rows = slices.Clone(accepted)
	return nil
}

func cliPipeline(t *testing.T, target string) (localize.Core, *cliTM, validate.Profile) {
	t.Helper()
	profile, err := validate.NewProfile([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	engine := cliEngine(func(context.Context, unit.UnitID, string) (localize.Generation, error) {
		return localize.Generation{Text: target, Finish: localize.Stop}, nil
	})
	return localize.NewCore(engine), &cliTM{}, profile
}

type partialWriter struct{ bytes.Buffer }

func (w *partialWriter) Write(raw []byte) (int, error) {
	n, err := w.Buffer.Write(raw[:3])
	return n, errors.Join(err, errors.New("private writer failure"))
}

func TestPipelineResults(t *testing.T) {
	t.Run("Core text success", func(t *testing.T) {
		core, tm, profile := cliPipeline(t, "訳\n文")
		var out, diag bytes.Buffer
		result, err := core.Text(context.Background(), []byte("Source\ntext"), textadapter.New("en", "ja"), profile, tm, &out)
		code := reportPipeline(&diag, result, err)
		if code != ExitOK || out.String() != "訳\n文" || diag.Len() != 0 || len(tm.rows) != 1 || tm.rows[0].Text() != "訳\n文" {
			t.Fatalf("code=%d stdout=%q stderr=%q rows=%v", code, out.String(), diag.String(), tm.rows)
		}
	})
	t.Run("file runner receives parsed options and context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var out, diag bytes.Buffer
		calls := 0
		runner := func(gotCtx context.Context, options publication.Options) (localize.Result, error) {
			calls++
			if gotCtx != ctx || options != (publication.Options{Source: "source", Output: "out", Mode: publication.Replace}) {
				t.Fatalf("context=%v options=%+v", gotCtx, options)
			}
			return localize.Result{TMCommitted: true, Publication: publication.Result{State: publication.Published}}, nil
		}
		code := runWithPipeline(ctx, []string{"localize", "--output", "out", "--replace-output", "source"}, &out, &diag, nil, runner)
		if code != ExitOK || calls != 1 || out.Len() != 0 || diag.Len() != 0 {
			t.Fatalf("code=%d calls=%d stdout=%q stderr=%q", code, calls, out.String(), diag.String())
		}
	})
	t.Run("usage and help never invoke runner", func(t *testing.T) {
		for _, tt := range []struct {
			args []string
			code int
		}{{[]string{"localize", "--output", "out"}, ExitUsage}, {[]string{"localize", "--help"}, ExitOK}} {
			calls := 0
			runner := func(context.Context, publication.Options) (localize.Result, error) {
				calls++
				return localize.Result{}, nil
			}
			if code := runWithPipeline(context.Background(), tt.args, io.Discard, io.Discard, nil, runner); code != tt.code || calls != 0 {
				t.Fatalf("code=%d calls=%d", code, calls)
			}
		}
	})
}

func TestPipelineDiagnostics(t *testing.T) {
	for _, name := range []string{"generation", "partial stdout", "published plus error", "recovery", "stderr failure", "file stderr failure"} {
		t.Run(name, func(t *testing.T) {
			core, tm, profile := cliPipeline(t, "訳文")
			var out, diag bytes.Buffer
			var result localize.Result
			var err error
			var code int
			wantDiagnostic := ""
			wantRows := 0
			switch name {
			case "generation":
				core = localize.NewCore(cliEngine(func(context.Context, unit.UnitID, string) (localize.Generation, error) {
					return localize.Generation{Text: "途中", Finish: localize.Stop}, errors.New("private source and prompt")
				}))
				result, err = core.Text(context.Background(), []byte("private source"), textadapter.New("en", "ja"), profile, tm, &out)
				wantDiagnostic = "yakuori: localize generate failed for unit document; TMCommitted=false\n"
				code = reportPipeline(&diag, result, err)
			case "partial stdout", "stderr failure":
				writer := &partialWriter{}
				result, err = core.Text(context.Background(), []byte("Source"), textadapter.New("en", "ja"), profile, tm, writer)
				wantDiagnostic, wantRows = "yakuori: localize write failed; TMCommitted=true; stdout bytes=3 cannot be retracted\n", 1
				var stderr io.Writer = &diag
				if name == "stderr failure" {
					stderr, wantDiagnostic = brokenWriter{}, ""
				}
				code = reportPipeline(stderr, result, err)
				if writer.String() != "訳" || result.BytesWritten != 3 {
					t.Fatalf("stdout=%q result=%+v", writer.String(), result)
				}
			default:
				path := filepath.Join(t.TempDir(), "retained-output")
				if err := os.WriteFile(path, []byte("published file"), 0600); err != nil {
					t.Fatal(err)
				}
				result = localize.Result{TMCommitted: true, Publication: publication.Result{State: publication.Published, OutputPath: path}}
				err = &localize.Error{Phase: "publish", Err: errors.New("private backend cause")}
				wantDiagnostic = "yakuori: localize publish failed; TMCommitted=true; publication=published\n"
				if name == "recovery" {
					result = localize.Result{Publication: publication.Result{State: publication.NotPublished}}
					err = &localize.Error{Phase: "prepare", Err: &publication.RecoveryError{Reason: "manual reconciliation", Cause: errors.New("private recovery cause"), Result: publication.Result{
						State: publication.SourceBackedUp, RecordPath: "retained-record", OutputPath: path, BackupPath: "retained-backup",
					}}}
					wantDiagnostic = "yakuori: localize prepare failed; TMCommitted=false; publication=not-published; recovery reason=manual reconciliation state=source-backed-up record=retained-record output=" + path + " backup=retained-backup\n"
				}
				runner := func(context.Context, publication.Options) (localize.Result, error) { return result, err }
				var stderr io.Writer = &diag
				if name == "file stderr failure" {
					stderr, wantDiagnostic = brokenWriter{}, ""
				}
				code = runWithPipeline(context.Background(), []string{"localize", "--output", path, "source"}, &out, stderr, nil, runner)
				if raw, err := os.ReadFile(path); err != nil || string(raw) != "published file" {
					t.Fatalf("retained output=%q error=%v", raw, err)
				}
			}
			if code != ExitFailure || diag.String() != wantDiagnostic || out.Len() != 0 || len(tm.rows) != wantRows || strings.Contains(diag.String(), "private") {
				t.Fatalf("code=%d stdout=%q stderr=%q rows=%v", code, out.String(), diag.String(), tm.rows)
			}
		})
	}
}
