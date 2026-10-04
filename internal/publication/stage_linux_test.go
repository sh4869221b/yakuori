//go:build linux

package publication

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func stageFixture(t *testing.T) (*Run, string, string) {
	t.Helper()
	d := publicationFixture(t)
	source, output := filepath.Join(d, "source"), filepath.Join(d, "output")
	putFile(t, source, "original")
	putFile(t, output, "existing")
	r, err := Prepare(context.Background(), Options{source, output, Replace})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeRun(t, r) })
	return r, source, output
}

func TestStageLifecycle(t *testing.T) {
	t.Run("closed-stage-bytes", func(t *testing.T) {
		r, source, output := stageFixture(t)
		var writer io.Writer
		var reader io.Reader
		var written *os.File
		var synced bool
		r.syncStage = func(f *os.File) error { synced = true; written = f; return f.Sync() }
		err := r.Stage(context.Background(), func(w io.Writer) error {
			writer = w
			if _, leaked := w.(*os.File); leaked {
				t.Fatal("producer received raw file")
			}
			_, err := io.WriteString(w, "translated")
			return err
		}, func(rd io.Reader) error {
			reader = rd
			if !synced || written == nil {
				t.Fatal("validation preceded Sync")
			}
			if _, err := written.Write([]byte("late")); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("write FD open at validation: %v", err)
			}
			if _, err := writer.Write([]byte("late")); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("wrapper writable during validation: %v", err)
			}
			if _, writable := rd.(io.Writer); writable {
				t.Fatal("validator received writable handle")
			}
			if _, leaked := rd.(*os.File); leaked {
				t.Fatal("validator received raw file")
			}
			b, err := io.ReadAll(rd)
			if err != nil || string(b) != "translated" {
				t.Fatalf("closed stage bytes: %q %v", b, err)
			}
			return nil
		})
		if err != nil || !r.validated {
			t.Fatalf("stage failed validation: %v", err)
		}
		if _, err := writer.Write([]byte("late")); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("late writer accepted: %v", err)
		}
		if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("reader kept descriptor: %v", err)
		}
		st, err := os.Stat(r.stage.path)
		if err != nil || st.Mode().Perm() != 0600 {
			t.Fatalf("stage permissions: %v %v", st, err)
		}
		wantBytes(t, r.stage.path, "translated")
		wantBytes(t, source, "original")
		wantBytes(t, output, "existing")
	})
	for _, name := range []string{"produce", "write", "sync", "close", "validation", "cancel-before", "cancel-produce", "cancel-validation", "nil-produce", "nil-check", "zero-run", "closed-run"} {
		t.Run(name, func(t *testing.T) {
			r, source, output := stageFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			injected := errors.New("stage fixture failure")
			var writer io.Writer
			var file *os.File
			var checked bool
			produce := func(w io.Writer) error {
				writer = w
				if name == "write" {
					file = w.(*stageWriter).file
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
					if _, err := w.Write([]byte("translated")); !errors.Is(err, os.ErrClosed) {
						t.Fatalf("expected real write failure: %v", err)
					}
					return nil
				}
				if _, err := io.WriteString(w, "translated"); err != nil {
					return err
				}
				if name == "produce" {
					return injected
				}
				if name == "cancel-produce" {
					cancel()
				}
				return nil
			}
			check := func(rd io.Reader) error {
				checked = true
				if name == "validation" {
					return injected
				}
				if name == "cancel-validation" {
					cancel()
				}
				return nil
			}
			switch name {
			case "sync":
				r.syncStage = func(f *os.File) error { file = f; return injected }
			case "close":
				r.closeStage = func(f *os.File) error { file = f; return errors.Join(f.Close(), injected) }
			case "cancel-before":
				cancel()
			case "nil-produce":
				produce = nil
			case "nil-check":
				check = nil
			case "zero-run":
				r = &Run{}
			case "closed-run":
				closeRun(t, r)
			}
			err := r.Stage(ctx, produce, check)
			if err == nil || r.validated {
				t.Fatalf("failed stage validated: %v", err)
			}
			wantErr := ErrInvalidStage
			switch name {
			case "produce", "sync", "close", "validation":
				wantErr = injected
			case "write":
				wantErr = os.ErrClosed
			case "cancel-before", "cancel-produce", "cancel-validation":
				wantErr = context.Canceled
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("wrong stage failure: %v; want %v", err, wantErr)
			}
			if name != "validation" && name != "cancel-validation" && checked {
				t.Fatal("failure reached validator")
			}
			if writer != nil {
				if _, err := writer.Write([]byte("late")); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("writer survived failure: %v", err)
				}
			}
			if file != nil {
				if _, err := file.Write([]byte("late")); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("FD survived failure: %v", err)
				}
			}
			wantBytes(t, source, "original")
			wantBytes(t, output, "existing")
		})
	}
	for _, failure := range []bool{false, true} {
		name := "repeat-after-success"
		if failure {
			name = "repeat-after-failure"
		}
		t.Run(name, func(t *testing.T) {
			r, source, output := stageFixture(t)
			produce := func(w io.Writer) error { _, err := io.WriteString(w, "translated"); return err }
			check := func(io.Reader) error {
				if failure {
					return unix.EIO
				}
				return nil
			}
			first := r.Stage(context.Background(), produce, check)
			if (first != nil) != failure {
				t.Fatalf("unexpected first stage: %v", first)
			}
			called := false
			err := r.Stage(context.Background(), func(io.Writer) error { called = true; return nil }, check)
			if !errors.Is(err, ErrInvalidStage) || called {
				t.Fatalf("stage retry accepted: %v", err)
			}
			wantBytes(t, r.stage.path, "translated")
			wantBytes(t, source, "original")
			wantBytes(t, output, "existing")
		})
	}
}
