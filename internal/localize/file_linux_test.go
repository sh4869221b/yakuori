//go:build linux

package localize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

type fileFixture struct {
	core    Core
	adapter *fakeAdapter
	profile validate.Profile
	options publication.Options
	input   []byte
}

func newFileFixture(t *testing.T, mode publication.Mode) fileFixture {
	t.Helper()
	a := &fakeAdapter{t: t, tm: &fakeTM{}}
	input, err := json.Marshal(fakeImage{Adapter: "fixture", Schema: "v1", Language: "en", Records: []fakeRecord{{"a", "Hello"}, {"b", "World"}}, Keys: []string{"key", "key"}, Header: []byte{7}, Length: 10})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := validate.NewProfile([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	options := publication.Options{Source: filepath.Join(dir, "source"), Output: filepath.Join(dir, "output"), Mode: mode}
	if mode == publication.InPlace {
		options.Output = options.Source
	}
	writeFile(t, options.Source, input)
	if mode == publication.Replace {
		writeFile(t, options.Output, []byte("existing"))
	}
	engine := fakeEngine(func(_ context.Context, id unit.UnitID, _ string) (Generation, error) {
		a.step("generate " + id.StableID())
		return Generation{Text: "訳" + id.StableID(), Finish: Stop}, nil
	})
	return fileFixture{NewCore(engine), a, profile, options, input}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type hookedTM struct {
	*fakeTM
	after func()
}

func (tm hookedTM) Commit(ctx context.Context, session unit.Session, profile validate.Profile, accepted []validate.AcceptedTranslation) error {
	if err := tm.fakeTM.Commit(ctx, session, profile, accepted); err != nil {
		return err
	}
	if tm.after != nil {
		tm.after()
	}
	return nil
}

type testRun struct {
	publicationRun
	stageErr, publishErr, closeErr error
	afterPublish                   func()
	closed, published              int
}

func (r *testRun) Stage(ctx context.Context, produce func(io.Writer) error, check func(io.Reader) error) error {
	if r.stageErr != nil {
		return r.stageErr
	}
	return r.publicationRun.Stage(ctx, produce, check)
}

func (r *testRun) Publish(ctx context.Context) (publication.Result, error) {
	r.published++
	result, err := r.publicationRun.Publish(ctx)
	if r.afterPublish != nil {
		r.afterPublish()
	}
	return result, errors.Join(err, r.publishErr, ctx.Err())
}

func (r *testRun) Close() error {
	r.closed++
	return errors.Join(r.publicationRun.Close(), r.closeErr)
}

func TestFilePipeline(t *testing.T) {
	for _, mode := range []publication.Mode{publication.Create, publication.Replace, publication.InPlace} {
		t.Run(string(mode), func(t *testing.T) {
			f := newFileFixture(t, mode)
			result, err := f.core.File(context.Background(), f.options, f.adapter, f.profile, f.adapter.tm)
			if errors.Is(err, publication.ErrUnsupportedFilesystem) {
				t.Skip("success scenarios require qualified ext4/Btrfs TMPDIR")
			}
			if err != nil || !result.TMCommitted || result.Publication.State != publication.Published || len(f.adapter.tm.rows) != 2 {
				t.Fatalf("result = %+v, error = %v, rows = %v", result, err, f.adapter.tm.rows)
			}
			raw := fileBytes(t, f.options.Output)
			var image fakeImage
			if err := json.Unmarshal(raw, &image); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, f.adapter.observed) || image.Records[0].Text != "訳a" || image.Records[1].Text != "訳b" || f.adapter.tm.calls != 1 {
				t.Fatalf("final image = %+v, commit calls = %d", image, f.adapter.tm.calls)
			}
			originalPath := f.options.Source
			if mode == publication.InPlace {
				originalPath = result.Publication.BackupPath
			}
			if !bytes.Equal(fileBytes(t, originalPath), f.input) {
				t.Fatal("original bytes changed")
			}
		})
	}
}

func TestFilePipelineFailures(t *testing.T) {
	injected := errors.New("file pipeline injected failure")
	for _, name := range []string{"prepare", "generate", "produce", "stage sync", "stage close", "commit", "cancel after commit", "output appeared", "published plus error", "cancel after publish", "close", "import"} {
		t.Run(name, func(t *testing.T) {
			mode := publication.Replace
			if name == "output appeared" {
				mode = publication.Create
			}
			f := newFileFixture(t, mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			run := &testRun{}
			tm := hookedTM{fakeTM: f.adapter.tm}
			wantErr, wantCommitted, wantState := injected, false, publication.NotPublished
			switch name {
			case "generate":
				f.core = NewCore(fakeEngine(func(context.Context, unit.UnitID, string) (Generation, error) {
					return Generation{Text: "途中", Finish: Stop}, injected
				}))
			case "produce":
				f.adapter.exportErr = injected
			case "stage sync", "stage close":
				run.stageErr = injected
			case "commit":
				f.adapter.tm.err = injected
			case "cancel after commit":
				tm.after, wantErr, wantCommitted = cancel, context.Canceled, true
			case "output appeared":
				tm.after = func() { writeFile(t, f.options.Output, []byte("external")) }
				wantErr, wantCommitted = publication.ErrChanged, true
			case "published plus error":
				run.publishErr, wantCommitted, wantState = injected, true, publication.Published
			case "cancel after publish":
				run.afterPublish, wantErr, wantCommitted, wantState = cancel, context.Canceled, true, publication.Published
			case "close":
				run.closeErr, wantCommitted, wantState = injected, true, publication.Published
			case "import":
				writeFile(t, f.options.Source, []byte("invalid codec"))
			}
			prepare := func(ctx context.Context, options publication.Options) (publicationRun, error) {
				f.adapter.step("prepare")
				if name == "prepare" {
					return nil, &publication.RecoveryError{Result: publication.Result{State: publication.SourceBackedUp, RecordPath: "retained-record", BackupPath: "retained-backup"}, Reason: "reconciliation blocked", Cause: injected}
				}
				var err error
				run.publicationRun, err = publication.Prepare(ctx, options)
				return run, err
			}
			result, err := f.core.file(ctx, f.options, f.adapter, f.profile, tm, prepare)
			if errors.Is(err, publication.ErrUnsupportedFilesystem) {
				t.Skip("success scenarios require qualified ext4/Btrfs TMPDIR")
			}
			if name == "import" {
				var decodeErr *json.SyntaxError
				if !errors.As(err, &decodeErr) {
					t.Fatalf("import error = %v", err)
				}
			} else if !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, want %v", err, wantErr)
			}
			wantRows := 0
			wantClosed := 1
			if name == "prepare" {
				wantClosed = 0
				var recovery *publication.RecoveryError
				if !errors.As(err, &recovery) || recovery.Reason != "reconciliation blocked" || recovery.Result.RecordPath != "retained-record" || recovery.Result.BackupPath != "retained-backup" {
					t.Fatalf("recovery diagnostic lost: %v", err)
				}
			}
			if wantCommitted {
				wantRows = 2
			}
			if result.TMCommitted != wantCommitted || result.Publication.State != wantState || len(f.adapter.tm.rows) != wantRows || run.closed != wantClosed {
				t.Fatalf("result = %+v, rows = %v, close calls = %d", result, f.adapter.tm.rows, run.closed)
			}
			wantOutput := []byte("existing")
			if wantState == publication.Published {
				wantOutput = f.adapter.observed
			} else if name == "output appeared" {
				wantOutput = []byte("external")
			}
			if !bytes.Equal(fileBytes(t, f.options.Output), wantOutput) {
				t.Fatal("output bytes disagree with publication state")
			}
			if name != "import" && !bytes.Equal(fileBytes(t, f.options.Source), f.input) {
				t.Fatal("source bytes changed")
			}
			if f.adapter.tm.events[0] != "prepare" {
				t.Fatal("import/generation preceded Prepare")
			}
		})
	}
}
