//go:build linux && pipelinefaults

package cli

// allow: SIZE_OK - this plan row owns exactly one new test file; splitting the
// signal harness into more files would create out-of-scope source paths.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
	"golang.org/x/sys/unix"
)

const signalChildEnv = "YAKUORI_SIGNAL_CHILD"

// signalEarlyCloseCause is the authored sentinel a child reports when Close
// is attempted before the Generate consumer drained the token stream.
const signalEarlyCloseCause = "signal stream consumer not complete at close"

var errSignalEarlyClose = errors.New(signalEarlyCloseCause)

const (
	signalWatchdog    = 30 * time.Second
	signalRescueLimit = 2 * time.Second
)

// signalCase is one frozen leaf of the requirement matrix; the names do not
// depend on any observed run.
type signalCase struct {
	Backend string
	Surface string
	Row     string
	Format  string
	Mode    string
}

func signalCases() []signalCase {
	var cases []signalCase
	for _, backend := range []string{"cpu", "cuda"} {
		for _, spec := range []struct{ surface, format, mode string }{
			{"text-translate-stdout", "text", "stdout"},
			{"text-file-replace-existing", "text", "replace-existing"},
			{"w3-file-replace-existing", "w3strings", "replace-existing"},
		} {
			for _, row := range []string{"sigint", "sigterm", "context-cancel", "caller-deadline", "request-deadline"} {
				cases = append(cases, signalCase{Backend: backend, Surface: spec.surface, Row: row, Format: spec.format, Mode: spec.mode})
			}
		}
		for _, format := range []struct{ label, cli string }{{"text", "text"}, {"w3", "w3strings"}} {
			for _, mode := range []string{"create", "replace-absent", "in-place"} {
				cases = append(cases, signalCase{Backend: backend, Surface: format.label + "-file-" + mode, Row: "sigterm", Format: format.cli, Mode: mode})
			}
		}
	}
	return cases
}

func (c signalCase) name() string { return c.Backend + "-" + c.Surface + "-" + c.Row }

func (c signalCase) file() bool { return c.Mode != "stdout" }

func (f signalFixture) file() bool { return f.Case.file() }

func isDeadlineRow(row string) bool { return row == "caller-deadline" || row == "request-deadline" }

func signalTimeouts(row string) (callerMS, requestMS int) {
	switch row {
	case "caller-deadline":
		return 100, 1000
	case "request-deadline":
		return 1000, 100
	default:
		return 0, 0
	}
}

func expectedCancelErrors(row string) (callerErr, generateErr string) {
	switch row {
	case "caller-deadline":
		return context.DeadlineExceeded.Error(), context.DeadlineExceeded.Error()
	case "request-deadline":
		return "", context.DeadlineExceeded.Error()
	default:
		return context.Canceled.Error(), context.Canceled.Error()
	}
}

func expectedDeadlineSource(row string) string {
	switch row {
	case "caller-deadline":
		return "caller"
	case "request-deadline":
		return "request"
	default:
		return ""
	}
}

// signalNotice is the child's only cross-process report. It is causal history
// (events, typed phase, counts); final files and SQLite rows are read by the
// parent from the shared root after Wait, never from this stream.
type signalNotice struct {
	Case               string           `json:"case"`
	PID                int              `json:"pid"`
	PPID               int              `json:"ppid"`
	Event              string           `json:"event"`
	Occurrence         int              `json:"occurrence"`
	Detail             string           `json:"detail,omitempty"`
	CallerErr          string           `json:"caller_err,omitempty"`
	GenerateErr        string           `json:"generate_err,omitempty"`
	Phase              string           `json:"phase,omitempty"`
	Cause              string           `json:"cause,omitempty"`
	Generated          int              `json:"generated"`
	Closed             int              `json:"closed"`
	CloseAfterComplete bool             `json:"close_after_complete"`
	TestFailed         bool             `json:"test_failed"`
	Result             *localize.Result `json:"result,omitempty"`
	StdoutWrites       int              `json:"stdout_writes"`
	StdoutBytes        int              `json:"stdout_bytes"`
	CallerStarted      time.Time        `json:"caller_started,omitzero"`
	CallerDeadline     time.Time        `json:"caller_deadline,omitzero"`
	GenerateStarted    time.Time        `json:"generate_started,omitzero"`
	ReceivedDeadline   time.Time        `json:"received_deadline,omitzero"`
	GenerateDeadline   time.Time        `json:"generate_deadline,omitzero"`
}

// The same writer is supplied to dispatch/execute and delegates to the real
// process stdout. Calls, including empty writes, are distinct from bytes.
type signalStdoutWriter struct {
	writes           int
	bytes            int
	sink             *signalNoticeSink
	caseName         string
	blockBeforeWrite bool
}

func (w *signalStdoutWriter) Write(raw []byte) (int, error) {
	w.writes++
	if w.sink != nil {
		w.sink.emit(signalNotice{Case: w.caseName, PID: os.Getpid(), PPID: os.Getppid(), Event: "stdout-write", Occurrence: 1})
	}
	if w.blockBeforeWrite {
		select {}
	}
	n, err := os.Stdout.Write(raw)
	w.bytes += n
	return n, err
}

type signalChildRequest struct {
	Case             string              `json:"case"`
	Args             []string            `json:"args"`
	Paths            config.Paths        `json:"paths"`
	Info             inference.ModelInfo `json:"info"`
	Surface          string              `json:"surface"`
	Row              string              `json:"row"`
	Mode             string              `json:"mode"`
	CallerTimeoutMS  int                 `json:"caller_timeout_ms,omitempty"`
	RequestTimeoutMS int                 `json:"request_timeout_ms,omitempty"`
}

func (r signalChildRequest) deadline() bool {
	return r.CallerTimeoutMS > 0 && r.RequestTimeoutMS > 0
}

// signalNoticeSink serializes child notices. Deadline rows buffer every event
// inside the synctest bubble and flush after it; no external pipe I/O happens
// while virtual time is being controlled.
type signalNoticeSink struct {
	mu       sync.Mutex
	encoder  *json.Encoder
	delayed  bool
	buffered []signalNotice
	err      error
}

func newSignalNoticeSink(file *os.File, delayed bool) *signalNoticeSink {
	return &signalNoticeSink{encoder: json.NewEncoder(file), delayed: delayed}
}

func (s *signalNoticeSink) emit(notice signalNotice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	if s.delayed {
		s.buffered = append(s.buffered, notice)
		return
	}
	s.err = s.encoder.Encode(notice)
}

func (s *signalNoticeSink) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.delayed {
		for _, notice := range s.buffered {
			if s.err == nil {
				s.err = s.encoder.Encode(notice)
			}
		}
		s.buffered = nil
	}
	s.delayed = false
}

func (s *signalNoticeSink) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

type signalEventLog struct {
	mu             sync.Mutex
	occurrences    map[string]int
	closeCompletes []bool
}

func newSignalEventLog() *signalEventLog {
	return &signalEventLog{occurrences: map[string]int{}}
}

func (l *signalEventLog) record(event string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.occurrences[event]++
	return l.occurrences[event]
}

func (l *signalEventLog) consumerComplete() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.occurrences["consumer-complete"] > 0
}

func (l *signalEventLog) recordClose(complete bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closeCompletes = append(l.closeCompletes, complete)
}

func (l *signalEventLog) closeAfterComplete() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.closeCompletes) == 0 {
		return false
	}
	for _, complete := range l.closeCompletes {
		if !complete {
			return false
		}
	}
	return true
}

// signalStreamEngine owns a real worker goroutine and an unbuffered token
// stream. Generate is the consumer; Close is admitted only after the consumer
// has observed stream closure.
type signalStreamEngine struct {
	*cliPrecommitEngine
	caseName       string
	sink           *signalNoticeSink
	events         *signalEventLog
	release        chan struct{}
	ready          chan struct{}
	drainWait      chan struct{}
	readyOnce      sync.Once
	drainWaitOnce  sync.Once
	requestTimeout time.Duration
}

func (e *signalStreamEngine) Open(ctx context.Context, backend string) (string, inference.Engine, error) {
	id, _, err := e.translationEngine.open(ctx, backend)
	if err != nil {
		return "", nil, err
	}
	return id, e, nil
}

func (e *signalStreamEngine) emit(event string, mutate func(*signalNotice)) int {
	notice := signalNotice{
		Case: e.caseName, PID: os.Getpid(), PPID: os.Getppid(), Event: event,
		Occurrence: e.events.record(event),
	}
	if mutate != nil {
		mutate(&notice)
	}
	e.sink.emit(notice)
	return notice.Occurrence
}

func (e *signalStreamEngine) Generate(ctx context.Context, request inference.GenerationRequest) (inference.GenerationResult, error) {
	policy := request.Policy()
	applied := policy.RequestTimeout
	if e.requestTimeout > 0 {
		applied = e.requestTimeout
	}
	generateStarted := time.Now()
	receivedDeadline, _ := ctx.Deadline()
	generateCtx, cancel := context.WithTimeout(ctx, applied)
	defer cancel()
	generateDeadline, _ := generateCtx.Deadline()
	e.generated++
	tokens := make(chan string)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		e.emit("generate-ready", func(notice *signalNotice) {
			notice.Detail = fmt.Sprintf("applied_request_timeout=%s", applied)
			notice.GenerateStarted = generateStarted
			notice.ReceivedDeadline = receivedDeadline
			notice.GenerateDeadline = generateDeadline
		})
		e.readyOnce.Do(func() { close(e.ready) })
		select {
		case tokens <- "訳":
		case <-generateCtx.Done():
		}
		<-generateCtx.Done()
		e.emit("cancel-observed", func(notice *signalNotice) {
			source := "request"
			if ctx.Err() != nil {
				source = "caller"
			}
			notice.CallerErr = signalErrorString(ctx.Err())
			notice.GenerateErr = signalErrorString(generateCtx.Err())
			notice.Detail = "deadline_source=" + source
		})
		e.emit("drain-wait", nil)
		e.drainWaitOnce.Do(func() { close(e.drainWait) })
		<-e.release
		e.emit("release-received", nil)
		for _, token := range []string{"文", "。\n"} {
			tokens <- token
		}
		close(tokens)
	}()
	var text strings.Builder
	for token := range tokens {
		text.WriteString(token)
	}
	<-workerDone
	e.emit("consumer-complete", nil)
	result := inference.GenerationResult{
		Text: text.String(), Finish: inference.Canceled,
		PromptTokens: len(request.TokenIDs()), OutputTokens: 3, Policy: policy,
	}
	if errors.Is(generateCtx.Err(), context.DeadlineExceeded) {
		result.Finish = inference.Timeout
	}
	e.emit("generate-return", func(notice *signalNotice) {
		notice.GenerateErr = signalErrorString(generateCtx.Err())
		notice.Detail = fmt.Sprintf("consumed_text=%q", text.String())
	})
	return result, generateCtx.Err()
}

func (e *signalStreamEngine) Close() error {
	complete := e.events.consumerComplete()
	e.emit("close-attempt", func(notice *signalNotice) {
		notice.Detail = fmt.Sprintf("consumer_complete=%t", complete)
	})
	e.events.recordClose(complete)
	if !complete {
		e.emit("close-guard-failed", func(notice *signalNotice) { notice.Detail = signalEarlyCloseCause })
		return errSignalEarlyClose
	}
	err := e.cliPrecommitEngine.Close()
	e.emit("close-complete", func(notice *signalNotice) { notice.Detail = signalErrorString(err) })
	return err
}

func signalErrorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func readSignalControl(control *os.File, cancel context.CancelFunc, release chan struct{}, releaseOnce *sync.Once, sink *signalNoticeSink, caseName string) {
	scanner := bufio.NewScanner(control)
	cancels := 0
	for scanner.Scan() {
		switch scanner.Text() {
		case "CANCEL":
			cancels++
			sink.emit(signalNotice{Case: caseName, PID: os.Getpid(), PPID: os.Getppid(), Event: "cancel-requested", Occurrence: cancels})
			cancel()
		case "DRAIN_RELEASE":
			releaseOnce.Do(func() { close(release) })
		}
	}
}

func classifyExecuteError(err error) (phase, cause string) {
	if err == nil {
		return "", ""
	}
	var failure *localize.Error
	if errors.As(err, &failure) {
		phase = failure.Phase
	}
	var recovery *publication.RecoveryError
	switch {
	case errors.As(err, &recovery) && recovery.Reason == "invalid or missing operation record" && errors.Is(err, os.ErrNotExist):
		cause = "missing-record"
	case errors.Is(err, context.DeadlineExceeded):
		cause = "deadline"
	case errors.Is(err, context.Canceled):
		cause = "canceled"
	default:
		cause = "unexpected"
	}
	return phase, cause
}

func TestPipelinePrecommitSignalChild(t *testing.T) {
	raw := os.Getenv(signalChildEnv)
	if raw == "" {
		return
	}
	var request signalChildRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		fmt.Fprintf(os.Stderr, "signal child request: %v\n", err)
		os.Exit(4)
	}
	notice := os.NewFile(3, "signal-notice")
	if notice == nil {
		fmt.Fprintln(os.Stderr, "signal child notice descriptor unavailable")
		os.Exit(4)
	}
	sink := newSignalNoticeSink(notice, request.deadline())
	var code int
	var failed bool
	if request.Mode == "publication-exit" {
		code, failed = cliPublicationExitChild(t, request, sink)
	} else {
		code, failed = executeSignalChild(t, request, sink)
	}
	sink.flush()
	if err := sink.failure(); err != nil {
		fmt.Fprintf(os.Stderr, "signal child notice: %v\n", err)
		os.Exit(4)
	}
	if failed || t.Failed() {
		os.Exit(1)
	}
	os.Exit(code)
}

func executeSignalChild(t *testing.T, request signalChildRequest, sink *signalNoticeSink) (int, bool) {
	fail := func(format string, args ...any) (int, bool) {
		detail := fmt.Sprintf(format, args...)
		sink.emit(signalNotice{Case: request.Case, PID: os.Getpid(), PPID: os.Getppid(), Event: "child-error", Occurrence: 1, Detail: detail})
		fmt.Fprintln(os.Stderr, detail)
		return 4, true
	}
	if len(request.Args) < 2 || request.Case == "" {
		return fail("incomplete signal child request")
	}
	command, help, err := parseTranslation(request.Args[0], request.Args[1:])
	if err != nil || help != "" {
		return fail("parse signal child args: help=%q err=%v", help, err)
	}
	translation := cliAuthoredTranslation
	if request.Mode == "stdout-probe" {
		translation = strings.Repeat("訳文。", 4096)
	}
	base := &translationEngine{info: request.Info, text: translation, finish: inference.Stop}
	inner := &cliPrecommitEngine{translationEngine: base}
	resolve := func() (config.Paths, error) { return request.Paths, nil }
	var open selectedEngineOpener
	var engine *signalStreamEngine
	var execResult localize.Result
	var execErr error
	captured := false
	stdout := &signalStdoutWriter{}
	if request.Mode == "stdout-probe" || request.Mode == "before-write" {
		stdout.sink, stdout.caseName = sink, request.Case
		stdout.blockBeforeWrite = request.Mode == "before-write"
	}
	var callerStarted, callerDeadline time.Time
	dispatchCtx := context.Background()
	cancel := context.CancelFunc(func() {})
	if request.Mode != "rerun" && request.Row == "context-cancel" {
		dispatchCtx, cancel = context.WithCancel(dispatchCtx)
	}
	if request.Mode == "typed-contrast" && (request.Row == "sigint" || request.Row == "sigterm") {
		dispatchCtx, cancel = signal.NotifyContext(dispatchCtx, os.Interrupt, syscall.SIGTERM)
	}
	defer cancel()
	makeEngine := func() (selectedEngineOpener, chan struct{}, *sync.Once) {
		release := make(chan struct{})
		releaseOnce := &sync.Once{}
		engine = &signalStreamEngine{
			cliPrecommitEngine: inner, caseName: request.Case, sink: sink, events: newSignalEventLog(),
			release: release, ready: make(chan struct{}), drainWait: make(chan struct{}),
			requestTimeout: time.Duration(request.RequestTimeoutMS) * time.Millisecond,
		}
		return engine.Open, release, releaseOnce
	}
	if request.Mode == "rerun" || request.Mode == "stdout-probe" || request.Mode == "before-write" {
		open = inner.Open
	} else if !request.deadline() {
		var release chan struct{}
		var releaseOnce *sync.Once
		open, release, releaseOnce = makeEngine()
		control := os.NewFile(4, "signal-control")
		if control == nil {
			return fail("signal control descriptor unavailable")
		}
		go readSignalControl(control, cancel, release, releaseOnce, sink, request.Case)
		switch request.Mode {
		case "early-close":
			go func() {
				<-engine.ready
				earlyErr := engine.Close()
				if !errors.Is(earlyErr, errSignalEarlyClose) {
					sink.emit(signalNotice{Case: request.Case, PID: os.Getpid(), PPID: os.Getppid(), Event: "child-error", Occurrence: 1,
						Detail: fmt.Sprintf("early-close guard returned %v", earlyErr)})
					os.Exit(4)
				}
				os.Exit(3)
			}()
		case "rescue":
			// The worker waits for a cancellation that never arrives; the
			// parent watchdog must rescue-kill and fail the owning case.
		}
	}
	runner := func(ctx context.Context, options publication.Options) (localize.Result, error) {
		command.action, command.options = "localize", options
		result, err := command.execute(ctx, stdout, request.Paths, open)
		execResult, execErr, captured = result, err, true
		return result, err
	}
	dispatch := func(ctx context.Context) int {
		if command.action == "localize" {
			return runWithPipeline(ctx, request.Args, stdout, os.Stderr, resolve, runner)
		}
		return runWithTranslationOpener(ctx, request.Args, stdout, os.Stderr, resolve, nil, open)
	}
	var code int
	if request.deadline() {
		callerTimeout := time.Duration(request.CallerTimeoutMS) * time.Millisecond
		synctest.Test(t, func(t *testing.T) {
			deadlineOpen, release, releaseOnce := makeEngine()
			callerStarted = time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), callerTimeout)
			defer cancel()
			callerDeadline, _ = ctx.Deadline()
			finished := make(chan struct{})
			controllerDone := make(chan struct{})
			go func() {
				defer close(controllerDone)
				select {
				case <-engine.ready:
					select {
					case <-engine.drainWait:
						releaseOnce.Do(func() { close(release) })
					case <-finished:
					}
				case <-finished:
				}
			}()
			result, err := command.execute(ctx, stdout, request.Paths, deadlineOpen)
			execResult, execErr, captured = result, err, true
			close(finished)
			<-controllerDone
		})
		code = reportPipeline(os.Stderr, execResult, execErr)
	} else if request.Mode == "typed-contrast" {
		execResult, execErr = command.execute(dispatchCtx, stdout, request.Paths, open)
		captured = true
		code = reportPipeline(os.Stderr, execResult, execErr)
	} else {
		code = dispatch(dispatchCtx)
	}
	phase, cause := classifyExecuteError(execErr)
	if !captured {
		execResult, phase, cause = localize.Result{}, "", ""
	}
	sink.flush()
	final := signalNotice{
		Case: request.Case, PID: os.Getpid(), PPID: os.Getppid(), Event: "final", Occurrence: 1,
		Phase: phase, Cause: cause, Generated: inner.generated, Closed: inner.closed,
		TestFailed:   t.Failed(),
		StdoutWrites: stdout.writes, StdoutBytes: stdout.bytes,
		CallerStarted: callerStarted, CallerDeadline: callerDeadline,
	}
	if engine != nil {
		final.CloseAfterComplete = engine.events.closeAfterComplete()
	}
	if captured {
		final.Result = &execResult
	}
	sink.emit(final)
	return code, false
}

// signalFixture mirrors the task5 authored fixture at a parent-chosen durable
// root. Only the parent builds it; the child adopts the passed args/paths/info.
type signalFixture struct {
	Dir        string
	Case       signalCase
	Args       []string
	Command    translationCommand
	Paths      config.Paths
	Info       inference.ModelInfo
	Source     cliPrecommitFile
	OldOutput  cliPrecommitFile
	Target     []byte
	OutputPath string
	D0, D1     []translationTMRow
}

func newSignalFixture(t *testing.T, dir string, c signalCase) signalFixture {
	t.Helper()
	command, paths, base := translationFixtureAt(t, dir, "Hello.\n")
	if os.Geteuid() == 0 || !translationPublicationSupported(t, dir) {
		t.Fatal("qualified nonroot ext4/Btrfs required; this is not an unsupported-FS selector")
	}
	base.info.RequestedBackend, base.info.Backend, base.info.Prefill.Path = c.Backend, c.Backend, c.Backend
	if c.Backend == "cuda" {
		base.info.KVPrecision, base.info.CUDABackendPin = "fp16", "cuda-test-v1"
		base.info.ResidentActive, base.info.ResidentContextCap = true, 4096
	}
	f := signalFixture{Dir: dir, Case: c, Paths: paths, Info: base.info}
	f.OutputPath = filepath.Join(dir, "output")
	if c.Format == "w3strings" {
		writeW3File(t, command.options.Source, w3Table("en", []string{"Alpha.", "Beta."}))
		f.Target = w3Table("jp", []string{cliAuthoredTranslation, cliAuthoredTranslation})
	} else {
		f.Target = []byte(cliAuthoredTranslation)
	}
	action := "localize"
	if c.Mode == "stdout" {
		action = "translate"
	}
	args := []string{action, "--source-language", "en", "--target-language", "ja", "--game", "qa", "--artifact", "sample", "--backend", c.Backend}
	if action == "localize" {
		args = append(args, "--format", c.Format)
		switch c.Mode {
		case "replace-existing":
			args = append(args, "--output", f.OutputPath, "--replace-output")
			old := []byte("old output\n")
			if c.Format == "w3strings" {
				old = w3Table("en", []string{"Old."})
			}
			writeW3File(t, f.OutputPath, old)
		case "replace-absent":
			args = append(args, "--output", f.OutputPath, "--replace-output")
		case "create":
			args = append(args, "--output", f.OutputPath)
		case "in-place":
			args = append(args, "--in-place")
		default:
			t.Fatalf("unknown signal surface mode %q", c.Mode)
		}
	}
	args = append(args, command.options.Source)
	parsed, help, err := parseTranslation(action, args[1:])
	if err != nil || help != "" {
		t.Fatalf("parse signal fixture args: help=%q err=%v", help, err)
	}
	f.Args, f.Command = args, parsed
	if action == "localize" {
		f.OutputPath = parsed.options.Output
	}
	f.Source = cliPrecommitCapture(t, f.Command.options.Source)
	if action == "localize" {
		f.OldOutput = cliPrecommitCapture(t, f.OutputPath)
	}

	seedCommand := f.Command
	seedCommand.action, seedCommand.format = "translate", "text"
	seedCommand.options.Source = filepath.Join(dir, "sentinel.txt")
	writeFixture(t, seedCommand.options.Source, "Sentinel source.\n")
	seedEngine := &translationEngine{info: f.Info, text: "Sentinel translation.\n", finish: inference.Stop}
	key := expectedTranslationKey(t, translationTMInput{
		command: seedCommand, modelID: "selected-model", source: []byte("Sentinel source.\n"),
	}, seedEngine)
	f.D0 = []translationTMRow{{Schema: key.Schema(), Source: key.SourceBytes(), Profile: key.ProfileBytes(), Text: "Sentinel translation.\n"}}
	var seedOut bytes.Buffer
	if result, err := seedCommand.execute(context.Background(), &seedOut, f.Paths, seedEngine.open); err != nil || !result.TMCommitted || seedOut.String() != "Sentinel translation.\n" {
		t.Fatalf("valid sentinel seed: result=%+v error=%v stdout=%q", result, err, seedOut.String())
	}
	if actual := readTranslationTM(t, f.Paths); !reflect.DeepEqual(actual, f.D0) {
		t.Fatalf("authored D0 mismatch: actual=%+v expected=%+v", actual, f.D0)
	}
	wantEngine := &translationEngine{info: f.Info}
	f.D1 = slices.Clone(f.D0)
	if c.Format == "w3strings" {
		f.D1 = append(f.D1, expectedW3TranslationRows(t, w3TMInput{
			Raw: w3Table("en", []string{"Alpha.", "Beta."}), SourceLanguage: "en", ModelID: "selected-model",
			Game: "qa", Artifact: "sample", Keys: map[string][][]byte{"1": {{0x78, 0x56, 0x34, 0x12}}},
			Texts: map[string]string{"1": cliAuthoredTranslation, "2": cliAuthoredTranslation},
		}, wantEngine)...)
	} else {
		key := expectedTranslationKey(t, translationTMInput{command: f.Command, modelID: "selected-model", source: []byte("Hello.\n")}, wantEngine)
		f.D1 = append(f.D1, translationTMRow{Schema: key.Schema(), Source: key.SourceBytes(), Profile: key.ProfileBytes(), Text: cliAuthoredTranslation})
	}
	slices.SortFunc(f.D1, func(a, b translationTMRow) int {
		if n := bytes.Compare(a.Source, b.Source); n != 0 {
			return n
		}
		return bytes.Compare(a.Profile, b.Profile)
	})
	return f
}

type signalFailure struct{ Class, Detail string }

func (e *signalFailure) Error() string { return e.Class + ": " + e.Detail }

type signalChildRun struct {
	t            *testing.T
	caseName     string
	cmd          *exec.Cmd
	ctx          context.Context
	cancel       context.CancelFunc
	noticeRead   *os.File
	controlWrite *os.File
	decoder      *json.Decoder
	notices      chan signalNotice
	readDone     chan struct{}
	readErr      error
	waitDone     chan error
	waits        atomic.Int32
	stdout       bytes.Buffer
	stderr       bytes.Buffer
	stdoutPipe   *os.File
	observed     []signalNotice
	occurrences  map[string]int
	reaped       bool
	rescues      int
	eof          bool
}

func startSignalChild(t *testing.T, request signalChildRequest, watchdog time.Duration) *signalChildRun {
	t.Helper()
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	noticeRead, noticeWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(errors.Join(err, noticeRead.Close(), noticeWrite.Close()))
	}
	ctx, cancel := context.WithTimeout(t.Context(), watchdog)
	r := &signalChildRun{
		t: t, caseName: request.Case, ctx: ctx, cancel: cancel,
		noticeRead: noticeRead, controlWrite: controlWrite, decoder: json.NewDecoder(noticeRead),
		notices: make(chan signalNotice, 64), readDone: make(chan struct{}), waitDone: make(chan error, 1),
		occurrences: make(map[string]int),
	}
	r.cmd = exec.Command(executable, "-test.run=^TestPipelinePrecommitSignalChild$")
	env := os.Environ()
	for i := 0; i < len(env); i++ {
		if strings.HasPrefix(env[i], signalChildEnv+"=") {
			env = append(env[:i], env[i+1:]...)
			i--
		}
	}
	r.cmd.Env = append(env, signalChildEnv+"="+string(payload))
	r.cmd.ExtraFiles = []*os.File{noticeWrite, controlRead}
	r.cmd.Stdout, r.cmd.Stderr = &r.stdout, &r.stderr
	var pipeWrite *os.File
	if request.Mode == "stdout-probe" {
		r.stdoutPipe, pipeWrite, err = os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = unix.FcntlInt(pipeWrite.Fd(), unix.F_SETPIPE_SZ, 4096); err != nil {
			t.Fatal(err)
		}
		r.cmd.Stdout = pipeWrite
	}
	r.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	go func() {
		defer close(r.readDone)
		defer close(r.notices)
		for {
			var notice signalNotice
			if err := r.decoder.Decode(&notice); err != nil {
				r.readErr = err
				return
			}
			r.notices <- notice
		}
	}()
	t.Cleanup(r.cleanup)
	if err := r.cmd.Start(); err != nil {
		t.Fatal(errors.Join(err, noticeWrite.Close(), controlRead.Close()))
	}
	if pipeWrite != nil {
		if err := pipeWrite.Close(); err != nil {
			t.Fatal(err)
		}
	}
	go func() {
		r.waits.Add(1)
		r.waitDone <- r.cmd.Wait()
	}()
	if err := errors.Join(noticeWrite.Close(), controlRead.Close()); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *signalChildRun) cleanup() {
	if !r.reaped && r.cmd.Process != nil {
		r.rescue()
	}
	_ = r.noticeRead.Close()
	_ = r.controlWrite.Close()
	if r.stdoutPipe != nil {
		_ = r.stdoutPipe.Close()
	}
	r.cancel()
}

func (r *signalChildRun) rescue() {
	r.rescues++
	if r.cmd.Process != nil {
		_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-r.waitDone:
		r.reaped = true
	case <-timer.C:
	}
}

func (r *signalChildRun) identity(notice signalNotice) error {
	if notice.Case != r.caseName {
		return &signalFailure{Class: "wrong-identity", Detail: fmt.Sprintf("case=%q want %q", notice.Case, r.caseName)}
	}
	if notice.PID != r.cmd.Process.Pid {
		return &signalFailure{Class: "wrong-identity", Detail: fmt.Sprintf("pid=%d want %d", notice.PID, r.cmd.Process.Pid)}
	}
	if notice.PPID != os.Getpid() {
		return &signalFailure{Class: "wrong-identity", Detail: fmt.Sprintf("ppid=%d want %d", notice.PPID, os.Getpid())}
	}
	if r.occurrences["final"] != 0 {
		return &signalFailure{Class: "notice-protocol", Detail: "notice after final: " + notice.Event}
	}
	limit := 1
	switch notice.Event {
	case "cancel-requested":
		limit = 2
	case "generate-ready", "cancel-observed", "drain-wait", "release-received",
		"consumer-complete", "generate-return", "close-attempt", "close-complete",
		"close-guard-failed", "child-error", "final", "stdout-write":
	default:
		return &signalFailure{Class: "notice-protocol", Detail: "unknown event: " + notice.Event}
	}
	want := r.occurrences[notice.Event] + 1
	if notice.Occurrence != want || want > limit {
		// Keep draining the real protocol so occurrence-only mutations retain
		// the complete lifecycle and its event counts as evidence.
		r.t.Errorf("notice-occurrence: %s occurrence=%d want %d (limit %d)", notice.Event, notice.Occurrence, want, limit)
	}
	r.occurrences[notice.Event] = want
	return nil
}

func (r *signalChildRun) await(event string) (signalNotice, error) {
	for {
		select {
		case <-r.ctx.Done():
			r.rescue()
			return signalNotice{}, &signalFailure{Class: "rescue", Detail: "watchdog expired awaiting " + event}
		case notice, ok := <-r.notices:
			if !ok {
				return signalNotice{}, &signalFailure{Class: "early-return", Detail: "notice stream closed awaiting " + event}
			}
			if err := r.identity(notice); err != nil {
				return signalNotice{}, err
			}
			r.observed = append(r.observed, notice)
			switch notice.Event {
			case "child-error":
				return signalNotice{}, &signalFailure{Class: "child-error", Detail: notice.Detail}
			case "final":
				return signalNotice{}, &signalFailure{Class: "early-return", Detail: "final notice before " + event}
			}
			if notice.Event == event {
				return notice, nil
			}
		}
	}
}

func (r *signalChildRun) sendControl(line string) error {
	if r.controlWrite == nil {
		return errors.New("signal control descriptor unavailable")
	}
	_, err := io.WriteString(r.controlWrite, line+"\n")
	return err
}

type signalObservation struct {
	ExitCode      int
	Signal        syscall.Signal
	Signaled      bool
	Notices       []signalNotice
	Events        []string
	Stdout        []byte
	Stderr        []byte
	Rescues       int
	Waits         int
	EOF           bool
	WaitCompleted bool
	NoticeClosed  bool
	ControlClosed bool
}

func (r *signalChildRun) finish() (signalObservation, error) {
	var state *os.ProcessState
	if !r.reaped {
		select {
		case <-r.waitDone:
			r.reaped = true
			state = r.cmd.ProcessState
		case <-r.ctx.Done():
			r.rescue()
			return signalObservation{}, &signalFailure{Class: "rescue", Detail: "watchdog expired awaiting child exit"}
		}
	}
	if state == nil {
		state = r.cmd.ProcessState
	}
	for {
		select {
		case <-r.ctx.Done():
			return signalObservation{}, &signalFailure{Class: "rescue", Detail: "watchdog expired draining notices"}
		case notice, ok := <-r.notices:
			if !ok {
				goto drained
			}
			if err := r.identity(notice); err != nil {
				return signalObservation{}, err
			}
			r.observed = append(r.observed, notice)
		}
	}
drained:
	<-r.readDone
	r.eof = errors.Is(r.readErr, io.EOF)
	observation := signalObservation{
		Stdout: bytes.Clone(r.stdout.Bytes()), Stderr: bytes.Clone(r.stderr.Bytes()),
		Rescues: r.rescues, Waits: int(r.waits.Load()), EOF: r.eof,
		WaitCompleted: r.reaped,
	}
	if err := errors.Join(r.noticeRead.Close(), r.controlWrite.Close()); err != nil {
		return signalObservation{}, err
	}
	_, noticeErr := r.noticeRead.Stat()
	_, controlErr := r.controlWrite.Stat()
	observation.NoticeClosed = errors.Is(noticeErr, os.ErrClosed)
	observation.ControlClosed = errors.Is(controlErr, os.ErrClosed)
	if state != nil {
		observation.ExitCode = state.ExitCode()
		if status, ok := state.Sys().(syscall.WaitStatus); ok {
			observation.Signaled = status.Signaled()
			if observation.Signaled {
				observation.Signal = status.Signal()
			}
		}
	}
	for _, notice := range r.observed {
		observation.Notices = append(observation.Notices, notice)
		if notice.Event != "final" {
			observation.Events = append(observation.Events, notice.Event)
		}
	}
	return observation, nil
}

type signalArmState struct {
	Arm                string             `json:"arm"`
	Root               string             `json:"root"`
	DBPath             string             `json:"db_path"`
	AfterWait          bool               `json:"after_wait"`
	NoticeClosed       bool               `json:"notice_closed"`
	ControlClosed      bool               `json:"control_closed"`
	LockReacquired     bool               `json:"lock_reacquired"`
	ExitCode           int                `json:"exit_code"`
	Signaled           bool               `json:"signaled"`
	Signal             string             `json:"signal,omitempty"`
	Events             []string           `json:"events"`
	Notices            []signalNotice     `json:"notices"`
	StdoutBytes        int                `json:"stdout_bytes"`
	StdoutWrites       int                `json:"stdout_writes"`
	DelegatedBytes     int                `json:"delegated_bytes"`
	Stderr             string             `json:"stderr"`
	Phase              string             `json:"phase,omitempty"`
	Cause              string             `json:"cause,omitempty"`
	Generated          int                `json:"generated"`
	Closed             int                `json:"closed"`
	CloseAfterComplete bool               `json:"close_after_complete"`
	TestFailed         bool               `json:"test_failed"`
	Rescues            int                `json:"rescues"`
	Waits              int                `json:"waits"`
	EOF                bool               `json:"eof"`
	PID                int                `json:"pid"`
	ProcessAbsent      bool               `json:"process_absent"`
	GroupAbsent        bool               `json:"group_absent"`
	Source             cliPrecommitFile   `json:"source"`
	Output             cliPrecommitFile   `json:"output"`
	Rows               []translationTMRow `json:"rows"`
	Runs               int                `json:"runs"`
	Records            int                `json:"records"`
	Stages             int                `json:"stages"`
	Backups            int                `json:"backups"`
	Result             *localize.Result   `json:"result,omitempty"`
	CallerStarted      time.Time          `json:"caller_started,omitzero"`
	CallerDeadline     time.Time          `json:"caller_deadline,omitzero"`
}

type signalLeafState struct {
	Leaf          string          `json:"leaf"`
	Backend       string          `json:"backend"`
	Surface       string          `json:"surface"`
	Row           string          `json:"row"`
	Initial       signalArmState  `json:"initial"`
	Rerun         signalArmState  `json:"rerun"`
	TypedContrast *signalArmState `json:"typed_contrast,omitempty"`
}

func (f signalFixture) actual(t *testing.T, observation signalObservation, arm string) signalArmState {
	t.Helper()
	state := signalArmState{
		Arm: arm, ExitCode: observation.ExitCode, Signaled: observation.Signaled, Signal: observation.Signal.String(),
		Root: f.Dir, DBPath: config.ResolveStoragePaths(f.Paths).TMFile, AfterWait: observation.WaitCompleted,
		NoticeClosed: observation.NoticeClosed, ControlClosed: observation.ControlClosed,
		Events: slices.Clone(observation.Events), Notices: slices.Clone(observation.Notices),
		StdoutBytes: len(observation.Stdout), Stderr: string(observation.Stderr),
		Rescues: observation.Rescues, Waits: observation.Waits, EOF: observation.EOF,
		Source: cliPrecommitCapture(t, f.Command.options.Source),
		Rows:   readTranslationTM(t, f.Paths),
	}
	if !state.AfterWait || !state.NoticeClosed || !state.ControlClosed {
		t.Errorf("%s: post-Wait/notice/control=%t/%t/%t", arm, state.AfterWait, state.NoticeClosed, state.ControlClosed)
	}
	store, err := tm.Open(t.Context(), state.DBPath, config.DefaultLimits())
	if err != nil {
		t.Fatalf("%s: post-Wait lock reacquire: %v", arm, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("%s: post-Wait lock release: %v", arm, err)
	}
	state.LockReacquired = true
	if f.file() {
		state.Output = cliPrecommitCapture(t, f.OutputPath)
	}
	var final *signalNotice
	for i := range state.Notices {
		if state.Notices[i].Event == "final" {
			if final != nil {
				t.Errorf("%s: duplicate final notice", arm)
			}
			final = &state.Notices[i]
		}
	}
	if final != nil {
		state.Phase, state.Cause = final.Phase, final.Cause
		state.Generated, state.Closed = final.Generated, final.Closed
		state.CloseAfterComplete, state.TestFailed = final.CloseAfterComplete, final.TestFailed
		state.Result = final.Result
		state.StdoutWrites, state.DelegatedBytes = final.StdoutWrites, final.StdoutBytes
		state.CallerStarted, state.CallerDeadline = final.CallerStarted, final.CallerDeadline
	}
	state.Runs, state.Records, state.Stages, state.Backups = f.inspectArtifacts(t)
	state.PID = 0
	if len(state.Notices) > 0 {
		state.PID = state.Notices[0].PID
	}
	if state.PID != 0 {
		state.ProcessAbsent = errors.Is(syscall.Kill(state.PID, 0), syscall.ESRCH)
		state.GroupAbsent = errors.Is(syscall.Kill(-state.PID, 0), syscall.ESRCH)
		_, statErr := os.Stat(fmt.Sprintf("/proc/%d", state.PID))
		if !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("%s: process %d still present: %v", arm, state.PID, statErr)
			state.ProcessAbsent = false
		}
		if !state.ProcessAbsent || !state.GroupAbsent {
			t.Errorf("%s: pid=%d absent=%t group_absent=%t", arm, state.PID, state.ProcessAbsent, state.GroupAbsent)
		}
	}
	return state
}

func (f signalFixture) inspectArtifacts(t *testing.T) (runs, records, stages, backups int) {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(f.Dir, ".yakuori-run-*"))
	if err != nil {
		t.Fatal(err)
	}
	runs = len(dirs)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			switch entry.Name() {
			case "stage":
				stages++
			case "record.json":
				records++
			default:
				t.Errorf("unexpected publication artifact: %s", filepath.Join(dir, entry.Name()))
			}
		}
	}
	backupDirs, err := filepath.Glob(filepath.Join(f.Dir, ".yakuori-backup-*"))
	if err != nil {
		t.Fatal(err)
	}
	backups = len(backupDirs)
	return runs, records, stages, backups
}

func (f signalFixture) save(t *testing.T, arm string, expected any, state signalArmState, stdout []byte) {
	t.Helper()
	pipelineCLISaveJSON(t, arm+"/expected.json", expected)
	pipelineCLISaveJSON(t, arm+"/state.json", state)
	pipelineCLISave(t, arm+"/stdout.bin", stdout)
	pipelineCLISave(t, arm+"/stderr.bin", []byte(state.Stderr))
	pipelineCLISave(t, arm+"/source.bin", state.Source.Bytes)
	pipelineCLISave(t, arm+"/output.bin", state.Output.Bytes)
	pipelineCLISave(t, arm+"/logical-rows.log", cliLogicalRows(state.Rows))
	dbPath := config.ResolveStoragePaths(f.Paths).TMFile
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		raw, err := os.ReadFile(dbPath + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		pipelineCLISave(t, arm+"/tm.sqlite"+suffix, raw)
	}
}

func signalRowsEqual(expected, actual []translationTMRow) bool {
	return reflect.DeepEqual(expected, actual)
}

func (f signalFixture) judgeInitial(t *testing.T, observation signalObservation, arm string) signalArmState {
	t.Helper()
	if observation.ExitCode != ExitFailure || observation.Signaled {
		t.Errorf("initial exit=%d signaled=%t want %d/false; stderr=%q", observation.ExitCode, observation.Signaled, ExitFailure, observation.Stderr)
	}
	if len(observation.Stdout) != 0 {
		t.Errorf("initial stdout=%q want empty", observation.Stdout)
	}
	if observation.Rescues != 0 {
		t.Errorf("initial required %d rescue kill(s)", observation.Rescues)
	}
	if observation.Waits != 1 {
		t.Errorf("initial Wait calls=%d want exactly 1", observation.Waits)
	}
	if !observation.EOF {
		t.Errorf("initial notice stream did not end with EOF")
	}
	state := f.actual(t, observation, arm)
	if state.StdoutWrites != 0 || state.DelegatedBytes != 0 {
		t.Errorf("stdout-write-zero: %s writes=%d delegated_bytes=%d want 0/0", arm, state.StdoutWrites, state.DelegatedBytes)
	}
	if state.TestFailed {
		t.Error("child test framework failed; the expected exit code would mask it")
	}
	if !state.CloseAfterComplete {
		t.Error("initial Close did not observe consumer completion")
	}
	if state.Generated != 1 || state.Closed != 1 {
		t.Errorf("initial generate/close=%d/%d want 1/1", state.Generated, state.Closed)
	}
	closeAttempt := slices.Index(observation.Events, "close-attempt")
	release := slices.Index(observation.Events, "release-received")
	if closeAttempt < 0 || release < 0 || closeAttempt < release {
		t.Errorf("close-attempt index=%d release index=%d want close after release", closeAttempt, release)
	}
	if slices.Contains(observation.Events, "close-guard-failed") {
		t.Error("initial run hit the early-close guard")
	}
	requested := 0
	var lifecycle []string
	for _, event := range observation.Events {
		if event == "cancel-requested" {
			requested++
			continue
		}
		lifecycle = append(lifecycle, event)
	}
	wantRequested := 0
	if f.Case.Row == "context-cancel" {
		wantRequested = 2
	}
	if requested != wantRequested {
		t.Errorf("initial cancel requests=%d want %d", requested, wantRequested)
	}
	expectedEvents := []string{"generate-ready", "cancel-observed", "drain-wait", "release-received", "consumer-complete", "generate-return", "close-attempt", "close-complete"}
	if !reflect.DeepEqual(lifecycle, expectedEvents) {
		t.Errorf("initial lifecycle=%v want %v", lifecycle, expectedEvents)
	}
	var cancelObserved signalNotice
	for _, notice := range observation.Notices {
		if notice.Event == "cancel-observed" {
			cancelObserved = notice
		}
	}
	callerErr, generateErr := expectedCancelErrors(f.Case.Row)
	if cancelObserved.CallerErr != callerErr || cancelObserved.GenerateErr != generateErr {
		t.Errorf("initial cancel observation caller=%q generate=%q want %q/%q",
			cancelObserved.CallerErr, cancelObserved.GenerateErr, callerErr, generateErr)
	}
	if source := expectedDeadlineSource(f.Case.Row); source != "" && cancelObserved.Detail != "deadline_source="+source {
		t.Errorf("initial deadline source detail=%q want deadline_source=%s", cancelObserved.Detail, source)
	}
	if isDeadlineRow(f.Case.Row) {
		callerBudget, requestBudget := 100*time.Millisecond, time.Second
		if f.Case.Row == "request-deadline" {
			callerBudget, requestBudget = time.Second, 100*time.Millisecond
		}
		var ready signalNotice
		for _, notice := range observation.Notices {
			if notice.Event == "generate-ready" {
				ready = notice
			}
		}
		wantCaller := state.CallerStarted.Add(callerBudget)
		wantGenerate := ready.GenerateStarted.Add(requestBudget)
		if wantCaller.Before(wantGenerate) {
			wantGenerate = wantCaller
		}
		if state.CallerStarted.IsZero() || ready.GenerateStarted.IsZero() ||
			!state.CallerDeadline.Equal(wantCaller) || !ready.ReceivedDeadline.Equal(wantCaller) ||
			!ready.GenerateDeadline.Equal(wantGenerate) {
			t.Errorf("deadline-values: caller=%s received=%s generate=%s want caller/received=%s generate=%s",
				state.CallerDeadline, ready.ReceivedDeadline, ready.GenerateDeadline, wantCaller, wantGenerate)
		}
	}
	typed := f.file() || isDeadlineRow(f.Case.Row) || arm == "typed-contrast"
	wantPhase, wantCause := "", ""
	if typed {
		wantPhase, wantCause = "generate", "canceled"
		if isDeadlineRow(f.Case.Row) {
			wantCause = "deadline"
		}
	}
	if state.Phase != wantPhase || state.Cause != wantCause {
		t.Errorf("typed-cause-phase: %s phase/cause=%q/%q want %q/%q", arm, state.Phase, state.Cause, wantPhase, wantCause)
	}
	if typed {
		wantPublication := publication.State("")
		if f.file() {
			wantPublication = publication.NotPublished
		}
		if state.Result == nil || state.Result.TMCommitted || state.Result.BytesWritten != 0 || state.Result.Publication.State != wantPublication {
			t.Errorf("initial result=%+v want uncommitted/zero/publication=%q", state.Result, wantPublication)
		}
	}
	if !signalRowsEqual(f.D0, state.Rows) {
		t.Errorf("initial D0 mismatch: actual=%+v expected=%+v", state.Rows, f.D0)
	}
	if !reflect.DeepEqual(state.Source, f.Source) {
		t.Error("initial source identity/bytes changed")
	}
	if f.file() {
		if !reflect.DeepEqual(state.Output, f.OldOutput) {
			t.Error("initial output bytes/presence/identity changed")
		}
		if bytes.Equal(state.Output.Bytes, f.Target) && len(f.Target) != 0 && !bytes.Equal(f.Target, f.OldOutput.Bytes) {
			t.Error("initial run published the authored target")
		}
	}
	wantRuns := 0
	if f.file() {
		wantRuns = 1
	}
	if state.Runs != wantRuns || state.Records != 0 || state.Stages != 0 || state.Backups != 0 {
		t.Errorf("initial artifacts runs/records/stages/backups=%d/%d/%d/%d want %d/0/0/0",
			state.Runs, state.Records, state.Stages, state.Backups, wantRuns)
	}
	f.save(t, arm, f.initialExpectation(arm), state, observation.Stdout)
	return state
}

func (f signalFixture) initialExpectation(arm string) map[string]any {
	callerErr, generateErr := expectedCancelErrors(f.Case.Row)
	typedPhase := ""
	if f.file() || isDeadlineRow(f.Case.Row) || arm == "typed-contrast" {
		typedPhase = "generate"
	}
	wantRuns := 0
	if f.file() {
		wantRuns = 1
	}
	return map[string]any{
		"arm": arm, "exit_code": ExitFailure, "stdout_writes": 0, "stdout_bytes": 0, "generated": 1, "closed": 1,
		"close_after_complete": true, "caller_err": callerErr, "generate_err": generateErr,
		"deadline_source": expectedDeadlineSource(f.Case.Row), "typed_phase": typedPhase,
		"rows": f.D0, "source": f.Source, "output": f.OldOutput,
		"runs": wantRuns, "records": 0, "stages": 0, "backups": 0,
	}
}

func (f signalFixture) judgeRerun(t *testing.T, observation signalObservation) signalArmState {
	t.Helper()
	if len(observation.Events) != 0 {
		t.Errorf("rerun lifecycle=%v want no controlled events", observation.Events)
	}
	if observation.Rescues != 0 || observation.Waits != 1 || !observation.EOF {
		t.Errorf("rerun rescues=%d waits=%d eof=%t", observation.Rescues, observation.Waits, observation.EOF)
	}
	state := f.actual(t, observation, "rerun")
	wantWrites, wantBytes := 0, 0
	if !f.file() {
		wantWrites, wantBytes = 1, len(cliAuthoredTranslation)
	}
	if state.StdoutWrites != wantWrites || state.DelegatedBytes != wantBytes || state.DelegatedBytes != len(observation.Stdout) {
		t.Errorf("stdout-write-positive: writes=%d delegated_bytes=%d pipe_bytes=%d want %d/%d/%d",
			state.StdoutWrites, state.DelegatedBytes, len(observation.Stdout), wantWrites, wantBytes, wantBytes)
	}
	if state.TestFailed {
		t.Error("rerun child test framework failed")
	}
	if !reflect.DeepEqual(state.Source, f.Source) {
		t.Error("rerun source identity/bytes changed")
	}
	if !f.file() {
		if observation.ExitCode != ExitOK {
			t.Errorf("rerun stdout exit=%d want %d; stderr=%q", observation.ExitCode, ExitOK, observation.Stderr)
		}
		if !bytes.Equal(observation.Stdout, []byte(cliAuthoredTranslation)) {
			t.Errorf("rerun stdout=%q want %q", observation.Stdout, cliAuthoredTranslation)
		}
		if state.Generated != 1 || state.Closed != 1 {
			t.Errorf("rerun generate/close=%d/%d want 1/1", state.Generated, state.Closed)
		}
		if !signalRowsEqual(f.D1, state.Rows) {
			t.Errorf("rerun D1 mismatch: actual=%+v expected=%+v", state.Rows, f.D1)
		}
		if state.Runs != 0 || state.Records != 0 || state.Stages != 0 || state.Backups != 0 {
			t.Errorf("rerun artifacts=%d/%d/%d/%d want 0s", state.Runs, state.Records, state.Stages, state.Backups)
		}
		f.save(t, "rerun", f.rerunExpectation(), state, observation.Stdout)
		return state
	}
	if observation.ExitCode != ExitFailure {
		t.Errorf("rerun file exit=%d want %d; stderr=%q", observation.ExitCode, ExitFailure, observation.Stderr)
	}
	if len(observation.Stdout) != 0 {
		t.Errorf("rerun file stdout=%q want empty", observation.Stdout)
	}
	if state.Phase != "prepare" || state.Cause != "missing-record" {
		t.Errorf("rerun typed phase/cause=%q/%q want prepare/missing-record", state.Phase, state.Cause)
	}
	if state.Generated != 0 || state.Closed != 1 {
		t.Errorf("rerun generate/close=%d/%d want 0/1", state.Generated, state.Closed)
	}
	if state.Result == nil || state.Result.TMCommitted || state.Result.BytesWritten != 0 || state.Result.Publication.State != publication.NotPublished {
		t.Errorf("rerun result=%+v want uncommitted/zero/not-published", state.Result)
	}
	if !signalRowsEqual(f.D0, state.Rows) {
		t.Errorf("rerun D0 mismatch: actual=%+v expected=%+v", state.Rows, f.D0)
	}
	if !reflect.DeepEqual(state.Output, f.OldOutput) {
		t.Error("rerun output bytes/presence/identity changed")
	}
	if state.Runs != 1 || state.Records != 0 || state.Stages != 0 || state.Backups != 0 {
		t.Errorf("rerun artifacts=%d/%d/%d/%d want 1/0/0/0", state.Runs, state.Records, state.Stages, state.Backups)
	}
	f.save(t, "rerun", f.rerunExpectation(), state, observation.Stdout)
	return state
}

func (f signalFixture) rerunExpectation() map[string]any {
	if !f.file() {
		return map[string]any{"exit_code": ExitOK, "stdout_writes": 1, "stdout": cliAuthoredTranslation, "rows": f.D1, "generated": 1, "closed": 1}
	}
	return map[string]any{"exit_code": ExitFailure, "typed_phase": "prepare", "typed_cause": "missing-record",
		"rows": f.D0, "generated": 0, "closed": 1, "runs": 1}
}

func (f signalFixture) childRequest(c signalCase, mode string) signalChildRequest {
	callerMS, requestMS := signalTimeouts(c.Row)
	if mode != "initial" {
		callerMS, requestMS = 0, 0
	}
	return signalChildRequest{
		Case: c.name(), Args: slices.Clone(f.Args), Paths: f.Paths, Info: f.Info,
		Surface: c.Surface, Row: c.Row, Mode: mode, CallerTimeoutMS: callerMS, RequestTimeoutMS: requestMS,
	}
}

func (f signalFixture) cancelAndWait(t *testing.T, mode string) signalObservation {
	t.Helper()
	run := startSignalChild(t, f.childRequest(f.Case, mode), signalWatchdog)
	if !isDeadlineRow(f.Case.Row) {
		if _, err := run.await("generate-ready"); err != nil {
			t.Fatalf("%v", err)
		}
		var stimulus error
		switch f.Case.Row {
		case "sigint":
			stimulus = run.cmd.Process.Signal(syscall.SIGINT)
		case "sigterm":
			stimulus = run.cmd.Process.Signal(syscall.SIGTERM)
		case "context-cancel":
			stimulus = run.sendControl("CANCEL")
			if stimulus == nil {
				stimulus = run.sendControl("CANCEL")
			}
		}
		if stimulus != nil {
			t.Fatalf("stimulus: %v", stimulus)
		}
		if _, err := run.await("cancel-observed"); err != nil {
			t.Fatalf("%v", err)
		}
		if _, err := run.await("drain-wait"); err != nil {
			t.Fatalf("%v", err)
		}
		if err := run.sendControl("DRAIN_RELEASE"); err != nil {
			t.Fatalf("drain release: %v", err)
		}
	}
	observation, err := run.finish()
	if err != nil {
		t.Fatalf("%v", err)
	}
	return observation
}

func TestPipelinePrecommitSignals(t *testing.T) {
	if os.Getenv(signalChildEnv) != "" {
		t.Fatal("child selector ran the parent root")
	}
	var mu sync.Mutex
	var leaves []signalLeafState
	t.Cleanup(func() {
		path := os.Getenv("YAKUORI_SIGNAL_STATE")
		if path == "" || len(leaves) == 0 {
			return
		}
		raw, err := json.MarshalIndent(struct {
			Root   string            `json:"root"`
			Leaves []signalLeafState `json:"leaves"`
		}{Root: "TestPipelinePrecommitSignals", Leaves: leaves}, "", "  ")
		if err != nil {
			t.Errorf("signal state: %v", err)
			return
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Errorf("signal state dir: %v", err)
			return
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Errorf("signal state write: %v", err)
		}
	})
	checkSignalChildSelectorContract(t)
	checkPostWaitJudgeSensitivity(t)

	for _, c := range signalCases() {
		c := c
		t.Run(c.name(), func(t *testing.T) {
			f := newSignalFixture(t, t.TempDir(), c)
			observation := f.cancelAndWait(t, "initial")
			initial := f.judgeInitial(t, observation, "initial")
			var typedContrast *signalArmState
			if !f.file() && !isDeadlineRow(c.Row) {
				// translate dispatch has no typed-result seam. A fresh paired
				// direct execute preserves that distinction and real cancellation.
				contrast := newSignalFixture(t, t.TempDir(), c)
				typedObservation := contrast.cancelAndWait(t, "typed-contrast")
				state := contrast.judgeInitial(t, typedObservation, "typed-contrast")
				typedContrast = &state
			}

			rerun := startSignalChild(t, f.childRequest(c, "rerun"), signalWatchdog)
			rerunObservation, err := rerun.finish()
			if err != nil {
				t.Fatalf("rerun: %v", err)
			}
			rerunState := f.judgeRerun(t, rerunObservation)
			mu.Lock()
			leaves = append(leaves, signalLeafState{
				Leaf: c.name(), Backend: c.Backend, Surface: c.Surface, Row: c.Row,
				Initial: initial, Rerun: rerunState, TypedContrast: typedContrast,
			})
			mu.Unlock()
		})
	}

	t.Run("early-close-negative", func(t *testing.T) {
		c := signalCase{Backend: "cpu", Surface: "text-file-replace-existing", Row: "sigterm", Format: "text", Mode: "replace-existing"}
		f := newSignalFixture(t, t.TempDir(), c)
		run := startSignalChild(t, f.childRequest(c, "early-close"), signalWatchdog)
		if _, err := run.await("generate-ready"); err != nil {
			t.Fatalf("%v", err)
		}
		attempt, err := run.await("close-attempt")
		if err != nil {
			t.Fatalf("%v", err)
		}
		if !strings.Contains(attempt.Detail, "consumer_complete=false") {
			t.Fatalf("early close attempt detail=%q", attempt.Detail)
		}
		guarded, err := run.await("close-guard-failed")
		if err != nil {
			t.Fatalf("%v", err)
		}
		if guarded.Detail != signalEarlyCloseCause {
			t.Fatalf("guard detail=%q want %q", guarded.Detail, signalEarlyCloseCause)
		}
		observation, err := run.finish()
		if err != nil {
			t.Fatalf("%v", err)
		}
		if observation.ExitCode != 3 || observation.Signaled || observation.Rescues != 0 {
			t.Fatalf("early-close child exit=%d signaled=%t rescues=%d want 3/false/0", observation.ExitCode, observation.Signaled, observation.Rescues)
		}
		if slices.Contains(observation.Events, "release-received") {
			t.Fatal("early-close negative received a drain release")
		}
		pipelineCLISaveJSON(t, "early-close/observation.json", observation)
		t.Logf("early-close-negative: exit=%d guard=%q events=%v", observation.ExitCode, guarded.Detail, observation.Events)
	})

	t.Run("rescue-negative", func(t *testing.T) {
		c := signalCase{Backend: "cpu", Surface: "text-file-replace-existing", Row: "sigterm", Format: "text", Mode: "replace-existing"}
		f := newSignalFixture(t, t.TempDir(), c)
		run := startSignalChild(t, f.childRequest(c, "rescue"), signalRescueLimit)
		if _, err := run.await("generate-ready"); err != nil {
			t.Fatalf("%v", err)
		}
		observation, err := run.finish()
		var failure *signalFailure
		if !errors.As(err, &failure) || failure.Class != "rescue" {
			t.Fatalf("rescue-negative owner result=%v want named rescue failure", err)
		}
		if observation.Rescues != 0 && observation.Signaled {
			t.Fatalf("rescue-negative observation=%+v want a rescued observation", observation)
		}
		if run.rescues != 1 || !run.reaped {
			t.Fatalf("rescue-negative kills=%d reaped=%t want 1/true", run.rescues, run.reaped)
		}
		if status, ok := run.cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
			t.Fatalf("rescue-negative status=%v want SIGKILL", run.cmd.ProcessState)
		}
		pipelineCLISaveJSON(t, "rescue/owner-failure.json", map[string]any{
			"class": failure.Class, "detail": failure.Detail, "rescues": run.rescues,
			"reaped": run.reaped, "process_state": run.cmd.ProcessState.String(),
		})
		t.Logf("rescue-negative: owner failed class=%q detail=%q rescues=%d status=%s", failure.Class, failure.Detail, run.rescues, run.cmd.ProcessState)
	})
}

func checkSignalChildSelectorContract(t *testing.T) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestPipelinePrecommitSignalChild$")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("child-only selector without env: exit=%v output=%q", err, output)
		return
	}
	if !bytes.Contains(output, []byte("PASS")) || bytes.Contains(output, []byte("SKIP")) {
		t.Errorf("child-only selector output=%q want PASS without SKIP", output)
	}
	pipelineCLISave(t, "selector/child-only.log", output)
}

// checkPostWaitJudgeSensitivity checks the equality helper on synthetic rows.
// It is not sensitivity evidence for the real post-Wait SQLite reader.
func checkPostWaitJudgeSensitivity(t *testing.T) {
	t.Helper()
	authored := []translationTMRow{{Schema: 1, Source: []byte{1}, Profile: []byte{2}, Text: "Sentinel translation.\n"}}
	if !signalRowsEqual(authored, slices.Clone(authored)) {
		t.Error("judge rejected the authored row set")
	}
	tampered := append(slices.Clone(authored), translationTMRow{Schema: 1, Source: []byte{9}, Profile: []byte{9}, Text: "tampered"})
	if signalRowsEqual(authored, tampered) {
		t.Error("judge accepted an extra row appended to the expected set")
	}
	trimmed := slices.Clone(authored)
	trimmed[0].Text = "訳文"
	if signalRowsEqual(authored, trimmed) {
		t.Error("judge accepted a trimmed expected translation")
	}
	if signalRowsEqual(authored, nil) {
		t.Error("judge accepted an empty observation")
	}
	pipelineCLISaveJSON(t, "selector/judge-sensitivity.json", map[string]any{
		"authored_accepted": true, "extra_row_rejected": true, "trimmed_text_rejected": true, "empty_rejected": true,
	})
}
