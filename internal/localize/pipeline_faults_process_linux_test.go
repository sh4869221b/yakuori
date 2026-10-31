//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The barrier owns only the control wire and the child lifecycle. It never
// builds publication/Core state; Task5 connects the real pipeline to the same
// ready/release contract.
const (
	pipelineBarrierChildEnv           = "YAKUORI_PIPELINE_TASK4_CHILD"
	pipelineBarrierGuardCaseEnv       = "YAKUORI_PIPELINE_TASK4_GUARD_CASE"
	pipelineBarrierNoticeFD           = 3
	pipelineBarrierReleaseFD          = 4
	pipelineBarrierWatchdog           = 30 * time.Second
	pipelineBarrierReadWindow         = 25 * time.Second
	pipelineBarrierReleaseSignal      = byte('G')
	pipelineBarrierWrongBoundaryValue = "generate.after"
	pipelineBarrierModeHappy          = "happy"
	pipelineBarrierModeWrong          = "wrong-boundary"
	pipelineBarrierModeNoReady        = "no-ready"
	pipelineBarrierModeNoInject       = "no-inject"
	pipelineBarrierModeEarlyExit      = "early-exit"
	pipelineBarrierModeHang           = "hang"
	pipelineBarrierModeMalformed      = "malformed"
	// Task5 connects the same ready/release wire to the real stage-close fault.
	// Selection remains inside the tagged test binary; there is no product flag.
	pipelineBarrierModePipelineError  = "pipeline-error"
	pipelineBarrierModePipelineCancel = "pipeline-cancel"
	pipelineBarrierModePipelineRerun  = "pipeline-rerun"
	pipelineBarrierModeCommitProbe    = "commit-probe"
	pipelineBarrierModeRecoveryProbe  = "recovery-probe"
	pipelineBarrierModeInvalidRerun   = "invalid-rerun"
	pipelineBarrierModePreparedRerun  = "prepared-rerun"
	// pipelineBarrierModeRescue schedules a case whose owning test stops after
	// ready and never releases: only the registered cleanup can end the child.
	pipelineBarrierModeRescue = "cleanup-rescue"
)

// Typed outcomes. Every class except the empty value is a FAIL for a scheduled
// case; the guards file turns these into named test failures.
const (
	pipelineBarrierUnreached     = "unreached"
	pipelineBarrierUninjected    = "uninjected"
	pipelineBarrierTimeout       = "timeout"
	pipelineBarrierUnreaped      = "unreaped"
	pipelineBarrierWrongBoundary = "wrong-boundary"
	pipelineBarrierWrongIdentity = "wrong-identity"
	pipelineBarrierEarlyReturn   = "early-return"
	pipelineBarrierCleanupRescue = "cleanup-rescue"
)

type pipelineBarrierPlan struct {
	Case       string
	Boundary   string
	Occurrence int
	Inject     bool
}

type pipelineBarrierNotice struct {
	Event      string
	Case       string `json:",omitempty"`
	Boundary   string `json:",omitempty"`
	Occurrence int    `json:",omitempty"`
	PID        int
	PPID       int
	Paths      []string `json:",omitempty"`
	Result     string   `json:",omitempty"`
	Error      string   `json:",omitempty"`
}

type pipelineBarrierChildRequest struct {
	Case          string
	Boundary      string
	Mode          string
	WrongBoundary string
	Occurrence    int
}

// pipelineBarrierGuardCase is one scheduled case: the wire plan, the child
// behaviour, and whether the unreaped control judges the pre-Wait lifecycle
// input instead of the reaped record.
type pipelineBarrierGuardCase struct {
	Plan    pipelineBarrierPlan
	Mode    string
	PreWait bool
}

// pipelineBarrierScheduledResult is the on-disk hand-off from the scheduled
// case runner (a real child process) to the guard test that watches it.
type pipelineBarrierScheduledResult struct {
	Spec    pipelineBarrierGuardCase
	Cleanup pipelineBarrierCleanup
	Record  pipelineBarrierRecord
	Outcome string
}

// pipelineBarrierRescueResult is written by the registered cleanup when it had
// to end a child the scheduled case did not reap.
type pipelineBarrierRescueResult struct {
	Case    string
	Cleanup pipelineBarrierCleanup
}

type pipelineBarrierFailure struct {
	Class  string
	Detail string
}

func (e *pipelineBarrierFailure) Error() string { return e.Class + ": " + e.Detail }

type pipelineBarrierObservation struct {
	PID         int
	PPID        int
	PGID        int
	PIDAlive    bool
	GroupAlive  bool
	ProcPresent bool
	At          string
}

type pipelineBarrierRecord struct {
	Plan           pipelineBarrierPlan
	Events         []pipelineBarrierNotice
	Observations   []pipelineBarrierObservation
	Releases       int
	WaitConfirmed  bool
	CleanupRescues int
}

type pipelineBarrierCleanup struct {
	PID                int
	WaitCalls          int32
	ExitStatus         int
	Signaled           bool
	Signal             syscall.Signal
	Waited             bool
	CleanupRescueKills int
	PIDAbsent          bool
	GroupAbsent        bool
	ProcAbsent         bool
	ReadEOF            bool
	ResidualNotices    int
}

type pipelineBarrierExit int

const (
	pipelineBarrierExitZero pipelineBarrierExit = iota
	pipelineBarrierExitKilled
	pipelineBarrierExitNonzero
)

type pipelineBarrier struct {
	t            *testing.T
	plan         pipelineBarrierPlan
	cmd          *exec.Cmd
	ctx          context.Context
	cancel       context.CancelFunc
	noticeFile   *os.File
	releaseFile  *os.File
	childNotice  *os.File
	childRelease *os.File
	decoder      *json.Decoder
	notices      chan pipelineBarrierNotice
	readDone     chan struct{}
	readErr      error
	done         chan error
	waits        atomic.Int32
	output       bytes.Buffer
	waited       bool
	rescues      int
	releases     int
	events       []pipelineBarrierNotice
	observations []pipelineBarrierObservation
}

func startPipelineBarrier(t *testing.T, plan pipelineBarrierPlan, mode string) *pipelineBarrier {
	t.Helper()
	return startPipelineBarrierRaw(t, plan, mode, true)
}

func startPipelineBarrierRaw(t *testing.T, plan pipelineBarrierPlan, mode string, expectArmed bool) *pipelineBarrier {
	t.Helper()
	if plan.Case == "" || plan.Boundary == "" || plan.Occurrence < 1 {
		t.Fatalf("incomplete barrier plan: %+v", plan)
	}
	noticeRead, noticeWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(errors.Join(err, noticeRead.Close(), noticeWrite.Close()))
	}
	ctx, cancel := context.WithTimeout(t.Context(), pipelineBarrierWatchdog)
	p := &pipelineBarrier{
		t: t, plan: plan, ctx: ctx, cancel: cancel,
		noticeFile: noticeRead, releaseFile: releaseWrite,
		childNotice: noticeWrite, childRelease: releaseRead,
		notices: make(chan pipelineBarrierNotice, 16), readDone: make(chan struct{}),
		done: make(chan error, 1),
	}
	p.decoder = json.NewDecoder(noticeRead)
	// The parent read deadline, child descriptors, Wait channel and watchdog all
	// exist before Start. One goroutine performs the only cmd.Wait.
	if err := noticeRead.SetReadDeadline(time.Now().Add(pipelineBarrierReadWindow)); err != nil {
		t.Fatal(errors.Join(err, noticeRead.Close(), noticeWrite.Close(), releaseRead.Close(), releaseWrite.Close()))
	}
	request := pipelineBarrierChildRequest{Case: plan.Case, Boundary: plan.Boundary, Occurrence: plan.Occurrence, Mode: mode}
	if mode == pipelineBarrierModeWrong {
		request.WrongBoundary = pipelineBarrierWrongBoundaryValue
	}
	payload := ""
	if mode == pipelineBarrierModeMalformed {
		payload = `{"Case":`
	} else {
		raw, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		payload = string(raw)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := executable
	args := []string{"-test.run=^TestPipelineHarnessChild$", "-test.v"}
	if mode == pipelineBarrierModeCommitProbe {
		command, err = exec.LookPath("strace")
		if err != nil {
			t.Fatal(err)
		}
		args = append([]string{"-DD", "-f", "-qq", "-yy", "-e", "trace=fcntl", "-o", filepath.Join(os.Getenv(pipelineTask5RootEnv), "commit.trace"), executable}, args...)
	}
	p.cmd = exec.CommandContext(ctx, command, args...)
	env := os.Environ()
	for i := 0; i < len(env); i++ {
		if strings.HasPrefix(env[i], pipelineBarrierChildEnv+"=") {
			env = append(env[:i], env[i+1:]...)
			i--
		}
	}
	p.cmd.Env = append(env, pipelineBarrierChildEnv+"="+payload)
	p.cmd.ExtraFiles = []*os.File{noticeWrite, releaseRead}
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	p.cmd.Cancel = func() error {
		if p.cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	p.cmd.WaitDelay = 2 * time.Second
	p.cmd.Stdout, p.cmd.Stderr = &p.output, &p.output
	t.Cleanup(p.cleanup)
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		p.waits.Add(1)
		p.done <- p.cmd.Wait()
	}()
	if err := errors.Join(noticeWrite.Close(), releaseRead.Close()); err != nil {
		t.Fatal(err)
	}
	go p.readNotices()
	if expectArmed {
		notice, err := p.receive(p.ctx)
		if err != nil {
			t.Fatalf("child pid=%d armed notice: %v", p.cmd.Process.Pid, err)
		}
		if err := p.checkIdentity(notice); err != nil {
			t.Fatal(err)
		}
		if notice.Event != "armed" {
			t.Fatalf("first child notice = %q, want armed", notice.Event)
		}
	}
	t.Logf("barrier child pid=%d ppid=%d pgid=%d case=%s boundary=%s occurrence=%d watchdog=%s read_window=%s",
		p.cmd.Process.Pid, os.Getpid(), p.cmd.Process.Pid, plan.Case, plan.Boundary, plan.Occurrence,
		pipelineBarrierWatchdog, pipelineBarrierReadWindow)
	return p
}

func (p *pipelineBarrier) readNotices() {
	defer close(p.readDone)
	for {
		var notice pipelineBarrierNotice
		if err := p.decoder.Decode(&notice); err != nil {
			p.readErr = err
			close(p.notices)
			return
		}
		p.notices <- notice
	}
}

func (p *pipelineBarrier) checkIdentity(notice pipelineBarrierNotice) error {
	if notice.Case != p.plan.Case {
		return &pipelineBarrierFailure{Class: pipelineBarrierWrongIdentity, Detail: fmt.Sprintf("case=%q want %q", notice.Case, p.plan.Case)}
	}
	if notice.PID != p.cmd.Process.Pid {
		return &pipelineBarrierFailure{Class: pipelineBarrierWrongIdentity, Detail: fmt.Sprintf("pid=%d want %d", notice.PID, p.cmd.Process.Pid)}
	}
	if notice.PPID != os.Getpid() {
		return &pipelineBarrierFailure{Class: pipelineBarrierWrongIdentity, Detail: fmt.Sprintf("ppid=%d want %d", notice.PPID, os.Getpid())}
	}
	return nil
}

func (p *pipelineBarrier) checkPlan(notice pipelineBarrierNotice) error {
	if notice.Boundary != p.plan.Boundary || notice.Occurrence != p.plan.Occurrence {
		return &pipelineBarrierFailure{Class: pipelineBarrierWrongBoundary, Detail: fmt.Sprintf(
			"%s boundary=%q occurrence=%d, want boundary=%q occurrence=%d",
			notice.Event, notice.Boundary, notice.Occurrence, p.plan.Boundary, p.plan.Occurrence)}
	}
	return nil
}

func (p *pipelineBarrier) receive(ctx context.Context) (pipelineBarrierNotice, error) {
	// A deadline/cancel already observed by the caller must not depend on which
	// channel select picks when the killed child also closes the pipe.
	select {
	case <-ctx.Done():
		return pipelineBarrierNotice{}, &pipelineBarrierFailure{Class: pipelineBarrierTimeout, Detail: "monitor context ended before the expected notice: " + ctx.Err().Error()}
	default:
	}
	select {
	case <-ctx.Done():
		return pipelineBarrierNotice{}, &pipelineBarrierFailure{Class: pipelineBarrierTimeout, Detail: "monitor context ended before the expected notice: " + ctx.Err().Error()}
	case notice, ok := <-p.notices:
		if !ok {
			if errors.Is(p.readErr, io.EOF) {
				return pipelineBarrierNotice{}, io.EOF
			}
			if errors.Is(p.readErr, os.ErrDeadlineExceeded) {
				return pipelineBarrierNotice{}, &pipelineBarrierFailure{Class: pipelineBarrierTimeout, Detail: "notice read deadline exceeded: " + p.readErr.Error()}
			}
			return pipelineBarrierNotice{}, fmt.Errorf("barrier notice stream: %w", p.readErr)
		}
		p.events = append(p.events, notice)
		return notice, nil
	}
}

func pipelineBarrierStreamFailure(err error, class, stage string) error {
	if errors.Is(err, io.EOF) {
		return &pipelineBarrierFailure{Class: class, Detail: "notice stream closed " + stage}
	}
	return err
}

func (p *pipelineBarrier) awaitReady(ctx context.Context) (pipelineBarrierNotice, error) {
	notice, err := p.receive(ctx)
	if err != nil {
		return pipelineBarrierNotice{}, pipelineBarrierStreamFailure(err, pipelineBarrierEarlyReturn, "before ready")
	}
	if err := p.checkIdentity(notice); err != nil {
		return notice, err
	}
	switch notice.Event {
	case "ready":
		if err := p.checkPlan(notice); err != nil {
			return notice, err
		}
		return notice, nil
	case "returned":
		return notice, &pipelineBarrierFailure{Class: pipelineBarrierUnreached, Detail: "child returned before the ready boundary " + p.plan.Boundary}
	default:
		return notice, &pipelineBarrierFailure{Class: pipelineBarrierEarlyReturn, Detail: "unexpected event " + notice.Event + " before ready"}
	}
}

func (p *pipelineBarrier) awaitEvent(ctx context.Context, event string) (pipelineBarrierNotice, error) {
	notice, err := p.receive(ctx)
	if err != nil {
		class := pipelineBarrierEarlyReturn
		if event == "injected" {
			class = pipelineBarrierUninjected
		}
		return pipelineBarrierNotice{}, pipelineBarrierStreamFailure(err, class, "while awaiting "+event)
	}
	if err := p.checkIdentity(notice); err != nil {
		return notice, err
	}
	if notice.Event == event {
		if event != "returned" {
			if err := p.checkPlan(notice); err != nil {
				return notice, err
			}
		}
		return notice, nil
	}
	if notice.Event == "returned" {
		return notice, &pipelineBarrierFailure{Class: pipelineBarrierUninjected, Detail: "child returned before the injected notice for boundary " + p.plan.Boundary}
	}
	return notice, &pipelineBarrierFailure{Class: pipelineBarrierEarlyReturn, Detail: "unexpected event " + notice.Event + " while awaiting " + event}
}

func (p *pipelineBarrier) drain(t *testing.T) {
	t.Helper()
	for {
		notice, err := p.receive(p.ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			t.Fatalf("drain child pid=%d: %v", p.cmd.Process.Pid, err)
		}
		if notice.Event == "returned" {
			return
		}
	}
}

func (p *pipelineBarrier) observeAlive(t *testing.T) pipelineBarrierObservation {
	t.Helper()
	pid := p.cmd.Process.Pid
	observation := pipelineBarrierObservation{PID: pid, PPID: os.Getpid(), PGID: pid, At: time.Now().UTC().Format(time.RFC3339Nano)}
	pidErr := syscall.Kill(pid, 0)
	groupErr := syscall.Kill(-pid, 0)
	_, statErr := os.Stat(fmt.Sprintf("/proc/%d", pid))
	observation.PIDAlive = pidErr == nil
	observation.GroupAlive = groupErr == nil
	observation.ProcPresent = statErr == nil
	if !observation.PIDAlive || !observation.GroupAlive || !observation.ProcPresent {
		t.Fatalf("owned child is not synchronized alive: pid=%d pid_err=%v group_err=%v proc_err=%v", pid, pidErr, groupErr, statErr)
	}
	p.observations = append(p.observations, observation)
	return observation
}

func (p *pipelineBarrier) release(t *testing.T) {
	t.Helper()
	if p.releases > 0 {
		t.Fatalf("release already sent to pid=%d", p.cmd.Process.Pid)
	}
	if _, err := p.releaseFile.Write([]byte{pipelineBarrierReleaseSignal}); err != nil {
		t.Fatalf("release child pid=%d: %v", p.cmd.Process.Pid, err)
	}
	p.releases++
}

func (p *pipelineBarrier) stop() { p.cancel() }

func (p *pipelineBarrier) record() pipelineBarrierRecord {
	return pipelineBarrierRecord{
		Plan:           p.plan,
		Events:         append([]pipelineBarrierNotice(nil), p.events...),
		Observations:   append([]pipelineBarrierObservation(nil), p.observations...),
		Releases:       p.releases,
		WaitConfirmed:  p.waited && p.waits.Load() == 1,
		CleanupRescues: p.rescues,
	}
}

// failure is the lifecycle judgment for one scheduled case. It reads only the
// recorded lifecycle input; it never infers success from a received event.
func (r pipelineBarrierRecord) failure() *pipelineBarrierFailure {
	if r.CleanupRescues > 0 {
		return &pipelineBarrierFailure{Class: pipelineBarrierCleanupRescue, Detail: fmt.Sprintf("cleanup killed a scheduled child: rescues=%d", r.CleanupRescues)}
	}
	if !r.WaitConfirmed {
		return &pipelineBarrierFailure{Class: pipelineBarrierUnreaped, Detail: "lifecycle input has no confirmed single Wait"}
	}
	armed, ready, injected, returned := false, false, false, false
	for _, notice := range r.Events {
		if notice.Case != r.Plan.Case {
			return &pipelineBarrierFailure{Class: pipelineBarrierWrongIdentity, Detail: fmt.Sprintf("event %s case=%q want %q", notice.Event, notice.Case, r.Plan.Case)}
		}
		switch notice.Event {
		case "armed":
			armed = true
		case "ready":
			if notice.Boundary != r.Plan.Boundary || notice.Occurrence != r.Plan.Occurrence {
				return &pipelineBarrierFailure{Class: pipelineBarrierWrongBoundary, Detail: fmt.Sprintf(
					"ready boundary=%q occurrence=%d, want boundary=%q occurrence=%d",
					notice.Boundary, notice.Occurrence, r.Plan.Boundary, r.Plan.Occurrence)}
			}
			ready = true
		case "injected":
			if notice.Boundary != r.Plan.Boundary || notice.Occurrence != r.Plan.Occurrence {
				return &pipelineBarrierFailure{Class: pipelineBarrierWrongBoundary, Detail: fmt.Sprintf(
					"injected boundary=%q occurrence=%d, want boundary=%q occurrence=%d",
					notice.Boundary, notice.Occurrence, r.Plan.Boundary, r.Plan.Occurrence)}
			}
			injected = true
		case "returned":
			returned = true
		}
	}
	if !armed {
		return &pipelineBarrierFailure{Class: pipelineBarrierEarlyReturn, Detail: "armed notice missing"}
	}
	if !ready {
		if returned {
			return &pipelineBarrierFailure{Class: pipelineBarrierUnreached, Detail: "ready boundary " + r.Plan.Boundary + " was never reached"}
		}
		return &pipelineBarrierFailure{Class: pipelineBarrierEarlyReturn, Detail: "child exited before ready and without returned"}
	}
	if r.Plan.Inject && !injected {
		return &pipelineBarrierFailure{Class: pipelineBarrierUninjected, Detail: "no injected notice for boundary " + r.Plan.Boundary}
	}
	if !returned {
		return &pipelineBarrierFailure{Class: pipelineBarrierEarlyReturn, Detail: "returned notice missing"}
	}
	return nil
}

func (p *pipelineBarrier) finish(t *testing.T, want pipelineBarrierExit) pipelineBarrierCleanup {
	t.Helper()
	if p.waited {
		t.Fatalf("barrier lifecycle performed a second Wait for pid=%d", p.cmd.Process.Pid)
	}
	err := <-p.done
	p.waited = true
	<-p.readDone
	status, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("missing Linux WaitStatus for pid=%d: %v", p.cmd.Process.Pid, p.cmd.ProcessState)
	}
	cleanup := pipelineBarrierCleanup{
		PID: p.cmd.Process.Pid, WaitCalls: p.waits.Load(), ExitStatus: status.ExitStatus(),
		Signaled: status.Signaled(), Signal: status.Signal(), Waited: true,
		CleanupRescueKills: p.rescues, ReadEOF: errors.Is(p.readErr, io.EOF),
		ResidualNotices: len(p.notices),
	}
	if cleanup.WaitCalls != 1 {
		t.Fatalf("owned child pid=%d Wait calls = %d, want exactly 1", p.cmd.Process.Pid, cleanup.WaitCalls)
	}
	switch want {
	case pipelineBarrierExitZero:
		if err != nil || !status.Exited() || status.ExitStatus() != 0 {
			t.Fatalf("child pid=%d expected exit 0: wait=%v status=%#v output=%s", p.cmd.Process.Pid, err, status, p.output.Bytes())
		}
	case pipelineBarrierExitKilled:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || !status.Signaled() || status.Signal() != syscall.SIGKILL {
			t.Fatalf("child pid=%d expected actual SIGKILL: wait=%v status=%#v", p.cmd.Process.Pid, err, status)
		}
	case pipelineBarrierExitNonzero:
		if err == nil || !status.Exited() || status.ExitStatus() == 0 {
			t.Fatalf("child pid=%d expected nonzero exit: wait=%v status=%#v", p.cmd.Process.Pid, err, status)
		}
	default:
		t.Fatalf("unknown barrier exit expectation %d", want)
	}
	if !cleanup.ReadEOF {
		t.Fatalf("child pid=%d notice stream did not end in EOF: %v", p.cmd.Process.Pid, p.readErr)
	}
	if cleanup.ResidualNotices != 0 {
		t.Fatalf("child pid=%d produced %d unexpected post-exit notices", p.cmd.Process.Pid, cleanup.ResidualNotices)
	}
	if err := syscall.Kill(p.cmd.Process.Pid, 0); errors.Is(err, syscall.ESRCH) {
		cleanup.PIDAbsent = true
	}
	if err := syscall.Kill(-p.cmd.Process.Pid, 0); errors.Is(err, syscall.ESRCH) {
		cleanup.GroupAbsent = true
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", p.cmd.Process.Pid)); errors.Is(err, os.ErrNotExist) {
		cleanup.ProcAbsent = true
	}
	if !cleanup.PIDAbsent || !cleanup.GroupAbsent || !cleanup.ProcAbsent {
		t.Fatalf("owned child pid=%d survived Wait: pid_absent=%t group_absent=%t proc_absent=%t",
			p.cmd.Process.Pid, cleanup.PIDAbsent, cleanup.GroupAbsent, cleanup.ProcAbsent)
	}
	p.saveChildLog()
	t.Logf("reaped child pid=%d case=%s wait_calls=%d exit=%d signaled=%t signal=%s rescue_kills=%d eof=true pid/group/proc absent",
		p.cmd.Process.Pid, p.plan.Case, cleanup.WaitCalls, cleanup.ExitStatus, cleanup.Signaled, cleanup.Signal, cleanup.CleanupRescueKills)
	return cleanup
}

func (p *pipelineBarrier) cleanup() {
	if !p.waited {
		p.cancel()
		p.waited = true
		if p.cmd != nil && p.cmd.Process != nil {
			if err := syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				p.t.Errorf("barrier cleanup kill pid=%d: %v", p.cmd.Process.Pid, err)
			}
			<-p.done
			p.rescues++
			// A scheduled case must reap its own child. Reaching this path means
			// the case leaked its lifecycle, so the owning test must fail even
			// though the cleanup did perform the real Wait.
			p.failCleanupRescue()
		}
	}
	p.cancel()
	for _, file := range []*os.File{p.noticeFile, p.releaseFile, p.childNotice, p.childRelease} {
		if file == nil {
			continue
		}
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			p.t.Errorf("barrier descriptor close: %v", err)
		}
	}
	p.saveChildLog()
}

// failCleanupRescue reports the facts of the real Wait the cleanup had to
// perform and fails the owning scheduled test with the typed rescue class.
func (p *pipelineBarrier) failCleanupRescue() {
	pid := p.cmd.Process.Pid
	<-p.readDone
	cleanup := pipelineBarrierCleanup{
		PID: pid, WaitCalls: p.waits.Load(), CleanupRescueKills: p.rescues, Waited: true,
		ReadEOF: errors.Is(p.readErr, io.EOF), ResidualNotices: len(p.notices),
	}
	if status, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
		cleanup.ExitStatus = status.ExitStatus()
		cleanup.Signaled = status.Signaled()
		cleanup.Signal = status.Signal()
	}
	cleanup.PIDAbsent = errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	cleanup.GroupAbsent = errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH)
	_, procErr := os.Stat(fmt.Sprintf("/proc/%d", pid))
	cleanup.ProcAbsent = errors.Is(procErr, os.ErrNotExist)
	pipelineBarrierSaveJSON(p.t, "cleanup-rescue.json", pipelineBarrierRescueResult{Case: p.plan.Case, Cleanup: cleanup})
	p.t.Errorf("%s: scheduled case %s relied on registered cleanup: pid=%d wait_calls=%d rescues=%d signaled=%t signal=%s eof=%t residual=%d pid_absent=%t group_absent=%t proc_absent=%t",
		pipelineBarrierCleanupRescue, p.plan.Case, pid, cleanup.WaitCalls, cleanup.CleanupRescueKills,
		cleanup.Signaled, cleanup.Signal, cleanup.ReadEOF, cleanup.ResidualNotices,
		cleanup.PIDAbsent, cleanup.GroupAbsent, cleanup.ProcAbsent)
}

func (p *pipelineBarrier) saveChildLog() {
	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	root := os.Getenv("YAKUORI_PIPELINE_EVIDENCE")
	if root == "" {
		return
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		p.t.Errorf("child log directory: %v", err)
		return
	}
	name := fmt.Sprintf("child-%s-%d.log", p.plan.Case, p.cmd.Process.Pid)
	if err := os.WriteFile(filepath.Join(root, name), p.output.Bytes(), 0600); err != nil {
		p.t.Errorf("child log: %v", err)
	}
}

func pipelineBarrierSaveJSON(t *testing.T, name string, value any) {
	t.Helper()
	root := os.Getenv("YAKUORI_PIPELINE_EVIDENCE")
	if root == "" {
		return
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

// TestPipelineHarnessChild is only a control-wire driver. It never builds
// publication state; the parent only selects it inside this tagged binary.
func TestPipelineHarnessChild(t *testing.T) {
	raw := os.Getenv(pipelineBarrierChildEnv)
	if raw == "" {
		return
	}
	var request pipelineBarrierChildRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatalf("child request: %v", err)
	}
	if request.Case == "" || request.Boundary == "" || request.Occurrence < 1 {
		t.Fatalf("incomplete child request: %+v", request)
	}
	// ExtraFiles arrive blocking; register the release descriptor with the Go
	// poller before imposing its read deadline.
	if err := unix.SetNonblock(pipelineBarrierReleaseFD, true); err != nil {
		t.Fatal(err)
	}
	notice := os.NewFile(pipelineBarrierNoticeFD, "task4-notice")
	release := os.NewFile(pipelineBarrierReleaseFD, "task4-release")
	defer func() {
		if err := errors.Join(notice.Close(), release.Close()); err != nil {
			t.Error(err)
		}
	}()
	if err := release.SetReadDeadline(time.Now().Add(pipelineBarrierReadWindow)); err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(notice)
	send := func(event, boundary string, occurrence int) {
		notice := pipelineBarrierNotice{Event: event, Case: request.Case, Boundary: boundary, Occurrence: occurrence, PID: os.Getpid(), PPID: os.Getppid()}
		if err := encoder.Encode(notice); err != nil {
			t.Fatalf("send %s: %v", event, err)
		}
	}
	awaitRelease := func() {
		var value [1]byte
		if _, err := io.ReadFull(release, value[:]); err != nil || value[0] != pipelineBarrierReleaseSignal {
			t.Fatalf("release=%q error=%v", value, err)
		}
	}
	send("armed", "", 0)
	switch request.Mode {
	case pipelineBarrierModeHappy:
		send("ready", request.Boundary, request.Occurrence)
		awaitRelease()
		send("injected", request.Boundary, request.Occurrence)
		send("returned", "", 0)
	case pipelineBarrierModeWrong:
		wrongOccurrence := request.Occurrence + 1
		send("ready", request.WrongBoundary, wrongOccurrence)
		awaitRelease()
		send("injected", request.WrongBoundary, wrongOccurrence)
		send("returned", "", 0)
	case pipelineBarrierModeNoReady:
		send("returned", "", 0)
	case pipelineBarrierModeNoInject:
		send("ready", request.Boundary, request.Occurrence)
		awaitRelease()
		send("returned", "", 0)
	case pipelineBarrierModeEarlyExit:
	case pipelineBarrierModeHang:
		awaitRelease()
	case pipelineBarrierModePipelineError, pipelineBarrierModePipelineCancel, pipelineBarrierModePipelineRerun, pipelineBarrierModeCommitProbe, pipelineBarrierModeRecoveryProbe, pipelineBarrierModeInvalidRerun, pipelineBarrierModePreparedRerun:
		pipelineFaultChild(t, request, send, awaitRelease)
	default:
		t.Fatalf("unknown child mode %q", request.Mode)
	}
}

func TestPipelineHarnessBarrier(t *testing.T) {
	plan := pipelineBarrierPlan{Case: "barrier-happy", Boundary: "stage.close.after", Occurrence: 2, Inject: true}
	p := startPipelineBarrier(t, plan, pipelineBarrierModeHappy)
	if _, err := p.awaitReady(p.ctx); err != nil {
		t.Fatalf("ready: %v", err)
	}
	observation := p.observeAlive(t)
	if failure := p.record().failure(); failure == nil || failure.Class != pipelineBarrierUnreaped {
		t.Fatalf("guard before Wait = %v, want typed %s", failure, pipelineBarrierUnreaped)
	}
	p.release(t)
	for _, event := range []string{"injected", "returned"} {
		if _, err := p.awaitEvent(p.ctx, event); err != nil {
			t.Fatalf("%s: %v", event, err)
		}
	}
	cleanup := p.finish(t, pipelineBarrierExitZero)
	record := p.record()
	if failure := record.failure(); failure != nil {
		t.Fatalf("barrier lifecycle failure: %v", failure)
	}
	if record.Releases != 1 || !record.WaitConfirmed || record.CleanupRescues != 0 {
		t.Fatalf("barrier lifecycle count mismatch: releases=%d wait_confirmed=%t rescues=%d", record.Releases, record.WaitConfirmed, record.CleanupRescues)
	}
	if len(record.Observations) != 1 || !observation.PIDAlive || !observation.GroupAlive || !observation.ProcPresent {
		t.Fatalf("missing synchronized alive observation: %+v", record.Observations)
	}
	pipelineBarrierSaveJSON(t, "child-events.json", map[string]any{
		"plan": plan, "record": record, "cleanup": cleanup,
		"read_deadline_registered": true, "read_window": pipelineBarrierReadWindow.String(),
		"watchdog": pipelineBarrierWatchdog.String(), "single_wait": cleanup.WaitCalls == 1,
	})
	pipelineBarrierSaveJSON(t, "cleanup.json", cleanup)
	t.Logf("barrier happy: real ready/release/returned, one Wait, EOF, PID/group/proc absent (pid=%d)", cleanup.PID)
}

// pipelineBarrierRunScheduled drives one scheduled case through the real
// control wire and returns the typed lifecycle failure, if any. Every case
// performs its own single real Wait except the rescue control, which by design
// leaves the child to the registered cleanup.
func pipelineBarrierRunScheduled(t *testing.T, spec pipelineBarrierGuardCase) (pipelineBarrierRecord, error) {
	t.Helper()
	plan, mode := spec.Plan, spec.Mode
	if mode == pipelineBarrierModeRescue {
		p := startPipelineBarrier(t, plan, pipelineBarrierModeHappy)
		if _, err := p.awaitReady(p.ctx); err != nil {
			return p.record(), err
		}
		p.observeAlive(t)
		return p.record(), nil
	}
	p := startPipelineBarrierRaw(t, plan, mode, mode != pipelineBarrierModeMalformed)
	var outcome error
	switch mode {
	case pipelineBarrierModeHappy:
		if _, err := p.awaitReady(p.ctx); err != nil {
			outcome = err
			break
		}
		p.observeAlive(t)
		p.release(t)
		for _, event := range []string{"injected", "returned"} {
			if _, err := p.awaitEvent(p.ctx, event); err != nil {
				outcome = err
				break
			}
		}
		if outcome == nil && spec.PreWait {
			// The unreaped judgment must come from the lifecycle input alone:
			// no Wait has happened at this point.
			if failure := p.record().failure(); failure != nil {
				outcome = failure
			}
		}
	case pipelineBarrierModeWrong:
		if _, err := p.awaitReady(p.ctx); err != nil {
			outcome = err
		}
		p.release(t)
		p.drain(t)
	case pipelineBarrierModeNoReady, pipelineBarrierModeEarlyExit, pipelineBarrierModeMalformed:
		_, err := p.awaitReady(p.ctx)
		outcome = err
	case pipelineBarrierModeNoInject:
		if _, err := p.awaitReady(p.ctx); err != nil {
			outcome = err
			break
		}
		p.observeAlive(t)
		p.release(t)
		_, err := p.awaitEvent(p.ctx, "injected")
		outcome = err
	case pipelineBarrierModeHang:
		p.observeAlive(t)
		p.stop()
		_, err := p.awaitReady(p.ctx)
		outcome = err
	default:
		t.Fatalf("unknown scheduled case mode %q", mode)
	}
	exit := pipelineBarrierExitZero
	switch mode {
	case pipelineBarrierModeHang:
		exit = pipelineBarrierExitKilled
	case pipelineBarrierModeMalformed:
		exit = pipelineBarrierExitNonzero
	}
	cleanup := p.finish(t, exit)
	record := p.record()
	if outcome == nil {
		if failure := record.failure(); failure != nil {
			outcome = failure
		}
	}
	pipelineBarrierSaveJSON(t, "case-result.json", pipelineBarrierScheduledResult{Spec: spec, Cleanup: cleanup, Record: record, Outcome: fmt.Sprint(outcome)})
	return record, outcome
}

// TestPipelineHarnessScheduledCase executes one scheduled case in a real child
// process. It applies the same acceptance decision a product case uses: a typed
// lifecycle guard failure fails the scheduled test. The guard test that spawns
// it treats the nonzero exit plus the printed class as detection evidence.
func TestPipelineHarnessScheduledCase(t *testing.T) {
	raw := os.Getenv(pipelineBarrierGuardCaseEnv)
	if raw == "" {
		return
	}
	var spec pipelineBarrierGuardCase
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		t.Fatalf("scheduled case spec: %v", err)
	}
	if spec.Plan.Case == "" || spec.Plan.Boundary == "" || spec.Plan.Occurrence < 1 {
		t.Fatalf("incomplete scheduled case: %+v", spec)
	}
	record, failure := pipelineBarrierRunScheduled(t, spec)
	if failure != nil {
		t.Fatalf("scheduled case %s rejected: %v", spec.Plan.Case, failure)
	}
	if !record.WaitConfirmed {
		// This case left its child to the registered cleanup, which fails this
		// test itself; no acceptance is reported here.
		return
	}
	t.Logf("scheduled case %s accepted; single real Wait confirmed", spec.Plan.Case)
}
