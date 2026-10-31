//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
	"golang.org/x/sys/unix"
)

type pipelineConcurrentInput struct {
	Kind, DB, Boundary, Action, LockParent string
	Options                                publication.Options
}

type pipelineConcurrentNotice struct {
	Checkpoint, Error, Class string
	PID, Generated           int
	Canceled                 bool
	Recovery                 bool
	Phase                    string
	Rows                     []pipelineFixtureRow
	Paths                    publication.PipelineFaultPaths
	Result                   *Result
}

type pipelineConcurrentChild struct {
	cmd     *exec.Cmd
	decoder *json.Decoder
	release *os.File
	wait    <-chan error
	output  *bytes.Buffer
	done    bool
	trace   string
	target  int
}

func startPipelineConcurrent(t *testing.T, input pipelineConcurrentInput) *pipelineConcurrentChild {
	t.Helper()
	noticeRead, noticeWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(errors.Join(err, noticeRead.Close(), noticeWrite.Close()))
	}
	// Both channels and the bounded read watchdog exist before child launch.
	if err := noticeRead.SetReadDeadline(time.Now().Add(40 * time.Second)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	command := os.Args[0]
	args := []string{"-test.run=^TestPipelineConcurrentHelper$", "-test.v"}
	trace := ""
	if input.Boundary == "ordered-contender" {
		command, err = exec.LookPath("strace")
		if err != nil {
			t.Fatal("qualified Task5 tests require strace on PATH and child tracing permission:", err)
		}
		trace = filepath.Join(t.TempDir(), "flock.log")
		args = append([]string{"-f", "-qq", "-yy", "-e", "trace=flock", "-o", trace, os.Args[0]}, args...)
	}
	cmd := exec.CommandContext(ctx, command, args...)
	if trace != "" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return unix.Kill(-cmd.Process.Pid, unix.SIGKILL) }
	}
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(), "YAKUORI_TASK5_PROCESS="+string(raw))
	cmd.ExtraFiles = []*os.File{noticeWrite, releaseRead}
	output := new(bytes.Buffer)
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(errors.Join(err, noticeRead.Close(), noticeWrite.Close(), releaseRead.Close(), releaseWrite.Close()))
	}
	if err := errors.Join(noticeWrite.Close(), releaseRead.Close()); err != nil {
		t.Error(err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	child := &pipelineConcurrentChild{cmd: cmd, decoder: json.NewDecoder(noticeRead), release: releaseWrite, wait: wait, output: output, trace: trace}
	t.Cleanup(func() {
		cancel()
		if !child.done {
			err := <-child.wait
			child.done = true
			t.Logf("cleanup reaped pid=%d exit=%v", cmd.Process.Pid, err)
		}
		if err := errors.Join(noticeRead.Close(), releaseWrite.Close()); err != nil {
			t.Error(err)
		}
		pipelineFixtureSave(t, fmt.Sprintf("process-%d.log", cmd.Process.Pid), output.Bytes())
		pipelineJSON(t, fmt.Sprintf("process-%d-cleanup.json", cmd.Process.Pid), struct {
			PID         int
			Waited      bool
			Exit        string
			ProcPresent bool
		}{cmd.Process.Pid, child.done, cmd.ProcessState.String(), pipelineImage(t, fmt.Sprintf("/proc/%d", cmd.Process.Pid)).Present})
	})
	t.Logf("started child pid=%d boundary=%s action=%s", cmd.Process.Pid, input.Boundary, input.Action)
	return child
}

func (c *pipelineConcurrentChild) notice(t *testing.T, checkpoint string) pipelineConcurrentNotice {
	t.Helper()
	var notice pipelineConcurrentNotice
	if err := c.decoder.Decode(&notice); err != nil {
		t.Fatalf("child pid=%d checkpoint=%s read=%v", c.cmd.Process.Pid, checkpoint, err)
	}
	wantPID := c.cmd.Process.Pid
	if c.trace != "" {
		if c.target == 0 {
			c.target = notice.PID
		}
		wantPID = c.target
	}
	if notice.Checkpoint != checkpoint || notice.PID != wantPID {
		t.Fatalf("child notice=%+v expected=%s pid=%d", notice, checkpoint, c.cmd.Process.Pid)
	}
	pipelineJSON(t, fmt.Sprintf("process-%d-%s.json", notice.PID, checkpoint), notice)
	return notice
}

func (c *pipelineConcurrentChild) releaseAndWait(t *testing.T) pipelineConcurrentNotice {
	t.Helper()
	if _, err := c.release.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	final := c.notice(t, "final")
	c.waitExit(t, 0)
	return final
}

func (c *pipelineConcurrentChild) waitExit(t *testing.T, wantSignal syscall.Signal) {
	t.Helper()
	err := <-c.wait
	c.done = true
	status, ok := c.cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatal("missing actual Linux wait status")
	}
	if wantSignal != 0 {
		if !status.Signaled() || status.Signal() != wantSignal {
			t.Fatalf("pid=%d actual=%v error=%v expected signal=%v", c.cmd.Process.Pid, status, err, wantSignal)
		}
	} else if err != nil || !status.Exited() || status.ExitStatus() != 0 {
		t.Fatalf("pid=%d exit=%v status=%v output=%s", c.cmd.Process.Pid, err, status, c.output.String())
	}
	pipelineEqual(t, pipelineImage(t, fmt.Sprintf("/proc/%d", c.cmd.Process.Pid)).Present, false)
	if c.trace != "" {
		pipelineEqual(t, pipelineImage(t, fmt.Sprintf("/proc/%d", c.target)).Present, false)
		pipelineFixtureSave(t, fmt.Sprintf("ordered-%d-flock.log", c.target), fileBytes(t, c.trace))
	}
	pipelineJSON(t, fmt.Sprintf("process-%d-wait.json", c.cmd.Process.Pid), struct {
		PID, RawStatus, ExitCode int
		Signaled                 bool
		Signal                   syscall.Signal
		TracedTargetPID          int
	}{c.cmd.Process.Pid, int(status), status.ExitStatus(), status.Signaled(), status.Signal(), c.target})
	t.Logf("reaped pid=%d actual wait_status=%d signal=%v exit=%d", c.cmd.Process.Pid, status, status.Signal(), status.ExitStatus())
}

func TestPipelineConcurrentHelper(t *testing.T) {
	raw := os.Getenv("YAKUORI_TASK5_PROCESS")
	if raw == "" {
		return // Not a matrix row: entry point for owned subprocesses only.
	}
	var input pipelineConcurrentInput
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatal(err)
	}
	notice, release := os.NewFile(3, "notice"), os.NewFile(4, "release")
	defer func() {
		if err := errors.Join(notice.Close(), release.Close()); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	signalCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	send := func(n pipelineConcurrentNotice) {
		t.Helper()
		n.PID = os.Getpid()
		if err := json.NewEncoder(notice).Encode(n); err != nil {
			t.Fatal(err)
		}
	}
	pause := func(paths publication.PipelineFaultPaths) error {
		send(pipelineConcurrentNotice{Checkpoint: "ready", Paths: paths})
		switch input.Action {
		case "cancel":
			cancel()
			send(pipelineConcurrentNotice{Checkpoint: "cancel-observed", Canceled: ctx.Err() != nil})
		case "sigint", "sigterm":
			<-signalCtx.Done()
			send(pipelineConcurrentNotice{Checkpoint: "cancel-observed", Canceled: signalCtx.Err() != nil})
		}
		var token [1]byte
		if _, err := io.ReadFull(release, token[:]); err != nil {
			return err
		}
		if input.Action == "error" {
			return unix.EIO // Deliberate checkpoint error, not a physical I/O fault.
		}
		return nil
	}
	if input.Boundary == "hold-high-lock" {
		file, err := os.Open(input.LockParent)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := file.Close(); err != nil {
				t.Error(err)
			}
		}()
		if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		if err := pause(publication.PipelineFaultPaths{}); err != nil {
			t.Fatal(err)
		}
		send(pipelineConcurrentNotice{Checkpoint: "final"})
		return
	}
	f := pipeline72NewFixture(t, input.Kind)
	if err := errors.Join(f.store.Close(), f.db.Close()); err != nil {
		t.Fatal(err)
	}
	store, err := tm.Open(signalCtx, input.DB, f.limits)
	if err != nil {
		class := ""
		if errors.Is(err, tm.ErrOwnership) {
			class = "ErrOwnership"
		}
		send(pipelineConcurrentNotice{Checkpoint: "final", Error: err.Error(), Class: class})
		return
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	db, err := sql.Open("sqlite", input.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	f.store, f.db = store, db
	if input.Boundary == "store-owned" {
		if err := pause(publication.PipelineFaultPaths{}); err != nil {
			send(pipelineConcurrentNotice{Checkpoint: "final", Error: err.Error(), Class: "injected-EIO"})
			return
		}
	}
	var result Result
	var prepared *publication.Run
	runFD := -1
	if input.Boundary == "public-rerun" {
		result, err = f.core.FileWithTM(signalCtx, input.Options, f.adapter, f.profile, f.store, f.scope)
		if result.Publication.RecordPath != "" {
			f.record, f.backup = result.Publication.RecordPath, result.Publication.BackupPath
			f.stage = filepath.Join(filepath.Dir(f.record), "stage")
		}
	} else {
		result, err = f.core.fileWithTM(signalCtx, input.Options, f.adapter, f.profile, nil,
			&tmExecution{store: f.store, scope: f.scope}, func(ctx context.Context, options publication.Options) (publicationRun, error) {
				run, err := publication.PrepareWithLimits(ctx, options, f.limits)
				if err != nil {
					return nil, err
				}
				prepared = run
				pipeline72SetPaths(f, run.SetPipelineFaultHooks(publication.PipelineFaultHooks{}))
				runFD = pipelineRunFD(t, filepath.Dir(f.stage))
				if input.Boundary == "parents-owned" {
					if err := pause(pipeline72Paths(f)); err != nil {
						return nil, errors.Join(err, run.Close())
					}
				}
				return run, nil
			})
	}
	if prepared != nil {
		pipelineRunClosed(t, prepared, runFD)
	}
	final := pipelineConcurrentNotice{Checkpoint: "final", Result: &result, Paths: pipeline72Paths(f), Generated: len(f.backend.calls), Rows: pipelineFixtureReadRows(t, f.db)}
	if err != nil {
		final.Error = err.Error()
		var phase *Error
		if errors.As(err, &phase) {
			final.Phase = phase.Phase
		}
		var recovery *publication.RecoveryError
		final.Recovery = errors.As(err, &recovery)
		switch {
		case errors.Is(err, publication.ErrBusy):
			final.Class = "ErrBusy"
		case errors.Is(err, context.Canceled):
			final.Class, final.Canceled = "context.Canceled", true
		case errors.Is(err, unix.EIO):
			final.Class = "injected-EIO"
		case errors.Is(err, publication.ErrInvalidRecord):
			final.Class = "ErrInvalidRecord"
		case errors.Is(err, publication.ErrRecoveryConflict):
			final.Class = "ErrRecoveryConflict"
		}
	}
	send(final)
	if input.Boundary == "ordered-contender" {
		// The parent probes partial release while this actual Core caller is alive.
		var token [1]byte
		if _, err := io.ReadFull(release, token[:]); err != nil {
			t.Fatal(err)
		}
	}
}

func pipelinePublicRerun(t *testing.T, f *pipelineFixture, options publication.Options) pipelineConcurrentNotice {
	t.Helper()
	pipelineStoreFree(t, f)
	child := startPipelineConcurrent(t, pipelineConcurrentInput{
		Kind: f.scope.ArtifactNamespace, DB: pipelineDBPath(t, f), Options: options, Boundary: "public-rerun",
	})
	final := child.notice(t, "final")
	child.waitExit(t, 0)
	if final.PID == os.Getpid() {
		t.Fatal("public Core rerun did not use a new PID")
	}
	pipelineEqual(t, final.Rows, f.expectedRows(t, f.input.units))
	return final
}

func pipelineRunNames(t *testing.T, parent string) []string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".yakuori-run-") {
			names = append(names, entry.Name())
		}
	}
	return names
}

func TestPipelineConcurrentProcess(t *testing.T) {
	for _, kind := range []string{"text", "w3"} {
		for _, contention := range []string{"same-tm", "shared-parent", "opposite-parents"} {
			for _, action := range []string{"release", "error", "cancel", "sigint", "sigterm", "sigkill"} {
				t.Run(kind+"/"+contention+"/"+action, func(t *testing.T) {
					// Given: distinct real processes, dedicated ready/release pipes.
					f := pipeline72NewFixture(t, kind)
					second := pipeline72NewFixture(t, kind)
					// This parent owns a distinct adapter instance from the child's.
					// Bind its observer to the same authored input before readback.
					if _, err := f.adapter.Import(f.input.source, f.profile); err != nil {
						t.Fatal(err)
					}
					options := pipelineOptions(t, f, "create")
					secondOptions := options
					secondOptions.Output += "-second"
					parents := []string{filepath.Dir(options.Source)}
					if contention == "opposite-parents" {
						other := t.TempDir()
						writeFile(t, filepath.Join(other, "source"), f.input.source)
						options.Output = filepath.Join(other, "output")
						secondOptions.Source = filepath.Join(other, "source")
						secondOptions.Output = filepath.Join(parents[0], "opposite-output")
						parents = append(parents, other)
						pipelineJSON(t, "lock-parents.json", pipelineImages(t, parents))
					}
					files := []string{options.Source, options.Output, secondOptions.Source, secondOptions.Output}
					before := pipelineImages(t, files)
					db, secondDB := pipelineDBPath(t, f), pipelineDBPath(t, second)
					if err := errors.Join(f.store.Close(), second.store.Close()); err != nil {
						t.Fatal(err)
					}
					boundary, expectedClass := "parents-owned", "ErrBusy"
					if contention == "same-tm" {
						boundary, expectedClass, secondDB = "store-owned", "ErrOwnership", db
					}
					owner := startPipelineConcurrent(t, pipelineConcurrentInput{Kind: kind, DB: db, Options: options, Boundary: boundary, Action: action})
					ready := owner.notice(t, "ready")
					pipeline72SetPaths(f, ready.Paths)
					beforeRuns := make([][]string, 0, len(parents))
					for _, parent := range parents {
						beforeRuns = append(beforeRuns, pipelineRunNames(t, parent))
					}
					// When: the second full Store/Core request arrives while owner is ready.
					contender := startPipelineConcurrent(t, pipelineConcurrentInput{Kind: kind, DB: secondDB, Options: secondOptions})
					refused := contender.notice(t, "final")
					contender.waitExit(t, 0)
					// Then: typed refusal before generation/new run, exact D0 and files.
					if refused.Class != expectedClass || refused.Generated != 0 || refused.Error == "" {
						t.Fatalf("second=%+v expected=%s", refused, expectedClass)
					}
					if refused.Result != nil && (refused.Result.TMCommitted || refused.Result.Publication.State != publication.NotPublished) {
						t.Fatalf("refused request published: %+v", refused)
					}
					for i, parent := range parents {
						pipelineEqual(t, pipelineRunNames(t, parent), beforeRuns[i])
					}
					pipelineEqual(t, pipelineImages(t, files), before)
					pipelineEqual(t, pipelineFixtureReadRows(t, f.db), f.d0)
					pipelineEqual(t, pipelineFixtureReadRows(t, second.db), second.d0)
					pipelineJSON(t, "before-release-files.json", before)
					switch action {
					case "sigkill":
						if err := owner.cmd.Process.Signal(syscall.SIGKILL); err != nil {
							t.Fatal(err)
						}
						owner.waitExit(t, syscall.SIGKILL)
						// No final notice or Result exists for the killed process.
						pipelineEqual(t, pipelineImages(t, files), before)
						pipelineEqual(t, pipelineFixtureReadRows(t, f.db), f.d0)
						pipelineJSON(t, "expected-rows.json", f.d0)
					case "release", "error", "cancel", "sigint", "sigterm":
						if action == "sigint" || action == "sigterm" {
							sig := syscall.SIGINT
							if action == "sigterm" {
								sig = syscall.SIGTERM
							}
							if err := owner.cmd.Process.Signal(sig); err != nil {
								t.Fatal(err)
							}
						}
						if action == "cancel" || action == "sigint" || action == "sigterm" {
							observed := owner.notice(t, "cancel-observed")
							pipelineEqual(t, observed.Canceled, true)
						}
						final := owner.releaseAndWait(t)
						if action == "release" {
							if final.Error != "" || final.Result == nil || !final.Result.TMCommitted ||
								final.Result.Publication.State != publication.Published {
								t.Fatalf("owner did not publish: %+v", final)
							}
							wantCalls := 1
							if kind == "w3" {
								wantCalls = 3
							}
							pipelineEqual(t, final.Generated, wantCalls)
							pipelineEqual(t, pipelineImage(t, options.Source), before[0])
							pipelineEqual(t, pipelineImage(t, secondOptions.Source), before[2])
							pipelineEqual(t, pipelineImage(t, secondOptions.Output), before[3])
							pipelineWantTranslated(t, f, options.Output)
							pipelineEqual(t, pipelineFixtureReadRows(t, f.db), f.expectedRows(t, f.input.units))
							pipelineJSON(t, "expected-rows.json", f.expectedRows(t, f.input.units))
							pipeline72SetPaths(f, final.Paths)
							pipelineBoundaryState(t, f, options, *final.Result, nil)
						} else {
							wantClass := "context.Canceled"
							if action == "error" {
								wantClass = "injected-EIO"
							}
							if final.Class != wantClass || final.Generated != 0 {
								t.Fatalf("owner stop=%+v expected=%s", final, wantClass)
							}
							if final.Result != nil && (final.Result.TMCommitted || final.Result.Publication.State != publication.NotPublished) {
								t.Fatalf("stopped owner returned incorrect state: %+v", final)
							}
							pipelineEqual(t, pipelineImages(t, files), before)
							pipelineEqual(t, pipelineFixtureReadRows(t, f.db), f.d0)
							pipelineJSON(t, "expected-rows.json", f.d0)
						}
					}
					// Fresh OS descriptions and a fresh Store prove release in every outcome.
					pipelineLocksFree(t, parents...)
					pipelineStoreFree(t, f)
					pipelineStoreFree(t, second)
					pipelineJSON(t, "actual-rows.json", pipelineFixtureReadRows(t, f.db))
					pipelineJSON(t, "second-store-rows.json", pipelineFixtureReadRows(t, second.db))
					pipelineJSON(t, "second-store-expected-rows.json", second.d0)
					pipelineJSON(t, "actual-files.json", pipelineImages(t, files))
					t.Logf("whole Core contenders, controlled %s; actual typed %s; terminal stimulus=%s", kind, expectedClass, action)
				})
			}
		}
	}
	t.Run("partial-acquisition", testPipelinePartialAcquisition)
}

func testPipelinePartialAcquisition(t *testing.T) {
	for _, kind := range []string{"text", "w3"} {
		t.Run(kind, func(t *testing.T) {
			// Given: only the high (dev,ino) parent is held by an owned OS process.
			f := pipeline72NewFixture(t, kind)
			a, b := t.TempDir(), t.TempDir()
			ai, bi := pipelineImage(t, a), pipelineImage(t, b)
			if ai.ID.Major > bi.ID.Major || (ai.ID.Major == bi.ID.Major && ai.ID.Minor > bi.ID.Minor) ||
				(ai.ID.Major == bi.ID.Major && ai.ID.Minor == bi.ID.Minor && ai.ID.Inode > bi.ID.Inode) {
				a, b = b, a
			}
			for _, parent := range []string{a, b} {
				writeFile(t, filepath.Join(parent, "source"), f.input.source)
			}
			files := []string{filepath.Join(a, "source"), filepath.Join(b, "source"), filepath.Join(a, "output"), filepath.Join(b, "output")}
			before := pipelineImages(t, files)
			pipelineJSON(t, "before-files.json", before)
			pipelineJSON(t, "ordered-parents.json", pipelineImages(t, []string{a, b}))
			blocker := startPipelineConcurrent(t, pipelineConcurrentInput{Boundary: "hold-high-lock", LockParent: b})
			blocker.notice(t, "ready")
			db := pipelineDBPath(t, f)
			if err := f.store.Close(); err != nil {
				t.Fatal(err)
			}
			for _, direction := range []bool{false, true} {
				source, output := filepath.Join(a, "source"), filepath.Join(b, "output")
				if direction {
					source, output = filepath.Join(b, "source"), filepath.Join(a, "output")
				}
				// When: each opposite-order full Core request hits the held high lock.
				child := startPipelineConcurrent(t, pipelineConcurrentInput{Kind: kind, DB: db, Boundary: "ordered-contender",
					Options: publication.Options{Source: source, Output: output, Mode: publication.Create}})
				final := child.notice(t, "final")
				pipelineEqual(t, pipelineImage(t, fmt.Sprintf("/proc/%d", final.PID)).Present, true)
				pipelineLocksFree(t, a) // Before the contender's process exit can release anything.
				if _, err := child.release.Write([]byte{1}); err != nil {
					t.Fatal(err)
				}
				child.waitExit(t, 0)
				// Then: no deadlock/new run/generation and the partial low lock is free.
				if final.Class != "ErrBusy" || final.Generated != 0 || final.Result == nil || final.Result.TMCommitted {
					t.Fatalf("partial-acquire failure=%+v", final)
				}
				pipelineLocksFree(t, a)
				low, high := -1, -1
				raw := fileBytes(t, child.trace)
				for index, line := range strings.Split(string(raw), "\n") {
					if strings.Contains(line, "<"+a+">, LOCK_EX|LOCK_NB) = 0") {
						low = index
					}
					if strings.Contains(line, "<"+b+">, LOCK_EX|LOCK_NB) = -1 EAGAIN") {
						high = index
					}
				}
				if low < 0 || high <= low {
					t.Errorf("ordered whole-Core contender did not acquire low before high EAGAIN: pid=%d low=%d high=%d trace=%s", final.PID, low, high, raw)
				}
				pipelineEqual(t, len(pipelineRunNames(t, a)), 0)
				pipelineEqual(t, len(pipelineRunNames(t, b)), 0)
				pipelineEqual(t, pipelineFixtureReadRows(t, f.db), f.d0)
			}
			blocker.releaseAndWait(t)
			pipelineLocksFree(t, a, b)
			pipelineEqual(t, pipelineImages(t, files), before)
			pipelineStoreFree(t, f)
			pipelineJSON(t, "actual-rows.json", pipelineFixtureReadRows(t, f.db))
			pipelineJSON(t, "expected-rows.json", f.d0)
			pipelineJSON(t, "actual-files.json", pipelineImages(t, files))
		})
	}
}
