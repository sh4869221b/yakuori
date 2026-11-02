//go:build linux && pipelinefaults

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
)

type cliStdoutFault struct {
	row          string
	calls, bytes int
	actualErr    error
}

func (w *cliStdoutFault) Write(p []byte) (int, error) {
	w.calls++
	if w.row == "epipe" {
		return 0, syscall.EPIPE
	}
	if w.row != "normal" {
		p = p[:3]
	}
	n, err := os.Stdout.Write(p)
	w.bytes += n
	if err != nil || n != len(p) {
		w.actualErr = errors.Join(err, io.ErrShortWrite)
		return n, w.actualErr
	}
	switch w.row {
	case "partial", "diagnostic":
		return n, syscall.EIO
	}
	return n, nil
}
func cliStdoutExitChild(t *testing.T, request signalChildRequest, sink *signalNoticeSink) (int, bool) {
	engine := &translationEngine{info: request.Info, text: cliAuthoredTranslation, finish: inference.Stop}
	writer := &cliStdoutFault{row: request.Row}
	diagnostic := &cliReachedWriter{}
	if request.Row == "diagnostic" {
		diagnostic.err = syscall.ENOSPC
	}
	var result localize.Result
	var executeErr error
	execute := func(command translationCommand, ctx context.Context, out io.Writer, paths config.Paths, open selectedEngineOpener) (localize.Result, error) {
		result, executeErr = command.execute(ctx, out, paths, open)
		return result, executeErr
	}
	code := runWithTranslationExecutor(t.Context(), request.Args, writer, io.MultiWriter(diagnostic, os.Stderr), func() (config.Paths, error) { return request.Paths, nil }, nil, engine.open, execute)
	var failure *localize.Error
	phase, cause := "", ""
	if errors.Is(executeErr, syscall.EIO) {
		cause = "eio"
	}
	if errors.Is(executeErr, syscall.EPIPE) {
		cause = "epipe"
	}
	if errors.Is(executeErr, io.ErrShortWrite) {
		cause = "short"
	}
	if errors.As(executeErr, &failure) {
		phase = failure.Phase
	}
	if writer.actualErr != nil {
		t.Error(writer.actualErr)
	}
	if request.Row == "diagnostic" && (diagnostic.attempts != 1 || diagnostic.delegated != 0) {
		t.Error("diagnostic fault not reached")
	}
	sink.emit(signalNotice{Case: request.Case, PID: os.Getpid(), PPID: os.Getppid(), Event: "final", Occurrence: 1, Phase: phase, Cause: cause, Result: &result, StdoutWrites: writer.calls, StdoutBytes: writer.bytes, TestFailed: t.Failed()})
	return code, t.Failed()
}
func TestPipelineCLIStdoutExit(t *testing.T) {
	for _, row := range []string{"normal", "short", "partial", "epipe", "diagnostic"} {
		t.Run(row, func(t *testing.T) {
			f := newSignalFixture(t, t.TempDir(), signalCase{Backend: "cpu", Surface: "text-translate-stdout", Row: row, Format: "text", Mode: "stdout"})
			child := startSignalChild(t, signalChildRequest{Case: t.Name(), Args: f.Args, Paths: f.Paths, Info: f.Info, Row: row, Mode: "stdout-exit"}, signalWatchdog)
			o, err := child.finish()
			if err != nil {
				t.Fatal(err)
			}
			status, ok := child.cmd.ProcessState.Sys().(syscall.WaitStatus)
			if !ok || !status.Exited() || status.Signaled() || o.Rescues != 0 || o.Waits != 1 || !o.EOF || !o.NoticeClosed || !o.ControlClosed || len(o.Notices) != 1 {
				t.Fatalf("wait: %+v status=%v", o, status)
			}
			n := o.Notices[0]
			want := f.Target
			exit, phase, cause := 0, "", ""
			if row != "normal" {
				exit, phase = 1, "write"
				want = f.Target[:3]
			}
			if row == "epipe" {
				want = nil
			}
			switch row {
			case "short":
				cause = "short"
			case "partial", "diagnostic":
				cause = "eio"
			case "epipe":
				cause = "epipe"
			}
			if n.Event != "final" || n.TestFailed || n.Result == nil || n.StdoutWrites != 1 || n.StdoutBytes != len(want) || n.Phase != phase || n.Cause != cause || status.ExitStatus() != exit || !bytes.Equal(o.Stdout, want) || n.Result.BytesWritten != len(want) || !n.Result.TMCommitted || n.Result.Publication.State != "" {
				t.Fatalf("observation=%+v notice=%+v", o, n)
			}
			if !reflect.DeepEqual(readTranslationTM(t, f.Paths), f.D1) {
				t.Fatal("TM rows differ from D1")
			}
			if row != "normal" && row != "diagnostic" && row != "epipe" && (!strings.Contains(string(o.Stderr), "stdout bytes=3") || !strings.Contains(string(o.Stderr), "cannot")) {
				t.Fatalf("diagnostic=%q", o.Stderr)
			}
			if row == "diagnostic" && len(o.Stderr) != 0 {
				t.Fatalf("stderr=%q", o.Stderr)
			}
		})
	}
}
