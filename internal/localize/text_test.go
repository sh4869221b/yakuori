package localize

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

type textSink struct {
	bytes.Buffer
	calls int
	limit int
	err   error
	after func()
}

func (w *textSink) Write(raw []byte) (int, error) {
	w.calls++
	if w.limit >= 0 {
		raw = raw[:w.limit]
	}
	n, err := w.Buffer.Write(raw)
	if w.after != nil {
		w.after()
	}
	return n, errors.Join(err, w.err)
}

type textProbe struct {
	Adapter
	t                     *testing.T
	sink                  *textSink
	tm                    *fakeTM
	retained              *bytes.Buffer
	exportErr, observeErr error
	replace               bool
	afterObserve          func()
}

func (a *textProbe) Export(writer io.Writer, session unit.Session, profile validate.Profile, accepted []validate.AcceptedTranslation) (artifact.Manifest, error) {
	if a.sink.calls != 0 || a.tm.active {
		a.t.Fatal("stdout/transaction started before export")
	}
	a.retained = writer.(*bytes.Buffer)
	if a.exportErr != nil {
		return artifact.Manifest{}, a.exportErr
	}
	return a.Adapter.Export(writer, session, profile, accepted)
}

func (a *textProbe) Observe(reader io.Reader) (artifact.FinalObservation, error) {
	if a.sink.calls != 0 || a.tm.active {
		a.t.Fatal("stdout/transaction started before final validation")
	}
	if a.observeErr != nil {
		return artifact.FinalObservation{}, a.observeErr
	}
	final, err := a.Adapter.Observe(reader)
	if err != nil {
		return final, err
	}
	if a.replace {
		final.Units[0].Text = "差替え"
	}
	if a.afterObserve != nil {
		a.afterObserve()
	}
	return final, nil
}

func textProfile(t *testing.T) validate.Profile {
	t.Helper()
	profile, err := validate.NewProfile([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestTextPipeline(t *testing.T) {
	for _, tt := range []struct{ name, source, target string }{
		{"Japanese and newlines", "Hello\r\nWorld", "こんにちは\r\n世界"},
		{"empty", "", ""},
		{"retained export writer", "Hello", "こんにちは"},
		{"cancel after successful write", "Hello", "こんにちは"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sink, memory := &textSink{limit: -1}, &fakeTM{}
			a := &textProbe{Adapter: textadapter.New("en", "ja"), t: t, sink: sink, tm: memory}
			if tt.name == "retained export writer" {
				a.afterObserve = func() { a.retained.Reset() }
			}
			if tt.name == "cancel after successful write" {
				sink.after = cancel
			}
			tm := hookedTM{fakeTM: memory, after: func() {
				if sink.calls != 0 {
					t.Fatal("stdout preceded commit")
				}
				if tt.name == "retained export writer" {
					a.retained.Reset()
					if _, err := a.retained.WriteString("changed after validation"); err != nil {
						t.Fatal(err)
					}
				}
			}}
			calls := 0
			engine := fakeEngine(func(context.Context, unit.UnitID, string) (Generation, error) {
				calls++
				if sink.calls != 0 || memory.active {
					t.Fatal("stdout/transaction started during generation")
				}
				return Generation{Text: tt.target, Finish: Stop}, nil
			})
			result, err := NewCore(engine).Text(ctx, []byte(tt.source), a, textProfile(t), tm, sink)
			if err != nil || !result.TMCommitted || result.BytesWritten != len(tt.target) || sink.String() != tt.target || sink.calls != 1 || len(memory.rows) != 1 || memory.rows[0].Text() != tt.target {
				t.Fatalf("result = %+v, error = %v, stdout = %q, calls = %d, rows = %v", result, err, sink.String(), sink.calls, memory.rows)
			}
			wantCalls := 1
			if tt.source == "" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("generation calls = %d, want %d", calls, wantCalls)
			}
		})
	}
}

func TestTextPipelineFailures(t *testing.T) {
	injected := errors.New("text pipeline injected failure")
	for _, name := range []string{"generate", "invalid source", "export", "observe", "final compare", "cancel before commit", "commit", "cancel before write", "zero bytes error", "partial error", "short write"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sink, memory := &textSink{limit: -1}, &fakeTM{}
			a := &textProbe{Adapter: textadapter.New("en", "ja"), t: t, sink: sink, tm: memory}
			tm := hookedTM{fakeTM: memory}
			source, target := "Hello", "訳文"
			wantErr, wantRows, wantCalls, wantBytes := injected, 0, 0, 0
			engine := fakeEngine(func(context.Context, unit.UnitID, string) (Generation, error) {
				if name == "generate" {
					return Generation{Text: "途中", Finish: Stop}, injected
				}
				return Generation{Text: target, Finish: Stop}, nil
			})
			switch name {
			case "invalid source":
				source, wantErr = "\xff", protect.ErrInvalidSource
			case "export":
				a.exportErr = injected
			case "observe":
				a.observeErr = injected
			case "final compare":
				a.replace, wantErr = true, artifact.ErrFinalMismatch
			case "cancel before commit":
				a.afterObserve, wantErr = cancel, context.Canceled
			case "commit":
				memory.err = injected
			case "cancel before write":
				tm.after, wantErr, wantRows = cancel, context.Canceled, 1
			case "zero bytes error":
				sink.limit, sink.err, wantRows, wantCalls = 0, injected, 1, 1
			case "partial error":
				sink.limit, sink.err, wantRows, wantCalls, wantBytes = 3, injected, 1, 1, 3
			case "short write":
				sink.limit, wantErr, wantRows, wantCalls, wantBytes = 3, io.ErrShortWrite, 1, 1, 3
			}
			result, err := NewCore(engine).Text(ctx, []byte(source), a, textProfile(t), tm, sink)
			if !errors.Is(err, wantErr) || result.TMCommitted != (wantRows == 1) || result.BytesWritten != wantBytes || sink.calls != wantCalls || sink.String() != target[:wantBytes] || len(memory.rows) != wantRows {
				t.Fatalf("result = %+v, error = %v, stdout = %q, calls = %d, rows = %v", result, err, sink.String(), sink.calls, memory.rows)
			}
		})
	}
}
