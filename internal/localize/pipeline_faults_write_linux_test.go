//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"encoding/binary"
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

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/publication"
	"golang.org/x/sys/unix"
)

// This file owns child-process OS boundaries. All child roots are descendants
// of the caller's registered TMPDIR or explicit unqualified root.
type pipelineTask2Request struct {
	Action, Kind, Mode, Response string
	Options                      publication.Options
	Limits                       config.Limits
}

type pipelineTask2Response struct {
	PID, PPID                                                    int
	Prepared                                                     bool
	InvalidRecord, RecoveryConflict, RecoveryError, OutputExists bool
	Snapshot                                                     []byte
	Error                                                        string
}

func pipelineTask2Child(t *testing.T, request pipelineTask2Request) pipelineTask2Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	notice, childNotice, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := notice.Close(); err != nil {
			t.Error(err)
		}
	}()
	defer func() {
		if err := childNotice.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	}()
	childRelease, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := childRelease.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	}()
	defer func() {
		if err := release.Close(); err != nil {
			t.Error(err)
		}
	}()
	// The read deadline and both pipes exist before the child can run.
	if err := notice.SetReadDeadline(time.Now().Add(25 * time.Second)); err != nil {
		t.Fatal(err)
	}
	request.Response = filepath.Join(t.TempDir(), "child-response.json")
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, executable, "-test.run=^TestPipelineTask2ProcessHelper$", "-test.v")
	command.Env = append(os.Environ(), "YAKUORI_PIPELINE_TASK2_CHILD="+string(raw))
	if root := os.Getenv("YAKUORI_PIPELINE_EVIDENCE"); root != "" {
		command.Env = append(command.Env, "YAKUORI_PIPELINE_EVIDENCE="+filepath.Join(root, t.Name(), request.Action+"-child"))
	}
	command.ExtraFiles = []*os.File{childNotice, childRelease}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Logf("owned child start action=%s pid=%d ppid=%d", request.Action, command.Process.Pid, os.Getpid())
	// The parent must not keep the child's pipe ends alive: an early child exit
	// is an EOF event, not a wait for the readiness watchdog.
	if err := childNotice.Close(); err != nil {
		t.Fatal(err)
	}
	if err := childRelease.Close(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				t.Error(err)
			}
			if err := command.Wait(); err != nil {
				t.Logf("cleanup Wait=%v", err)
			}
		}
	})
	var ready [1]byte
	_, readyErr := io.ReadFull(notice, ready[:])
	if readyErr == nil && ready[0] == 'R' {
		_, err = release.Write([]byte{'G'})
	} else {
		err = fmt.Errorf("child ready=%q: %w", ready, readyErr)
		cancel()
	}
	waitErr := command.Wait()
	waited = true
	pipelineSave(t, request.Action+"-child.log", output.Bytes())
	status, ok := command.ProcessState.Sys().(syscall.WaitStatus)
	t.Logf("owned child action=%s pid=%d ppid=%d Wait=%v status=%#v output=%s", request.Action, command.Process.Pid, os.Getpid(), waitErr, status, output.Bytes())
	if err != nil || waitErr != nil || !ok || !status.Exited() || status.ExitStatus() != 0 {
		t.Fatalf("child action=%s ready/release=%v Wait=%v status=%#v output=%s", request.Action, err, waitErr, status, output.Bytes())
	}
	var response pipelineTask2Response
	if err := json.Unmarshal(fileBytes(t, request.Response), &response); err != nil {
		t.Fatal(err)
	}
	pipelineEqual(t, response.PID, command.Process.Pid)
	pipelineEqual(t, response.PPID, os.Getpid())
	if err := syscall.Kill(command.Process.Pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("reaped child PID still exists: %d %v", command.Process.Pid, err)
	}
	raw, err = json.MarshalIndent(response, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	pipelineSave(t, request.Action+"-process.json", raw)
	return response
}

func TestPipelineTask2ProcessHelper(t *testing.T) {
	raw := os.Getenv("YAKUORI_PIPELINE_TASK2_CHILD")
	if raw == "" {
		return // Ordinary discovery is not a child invocation, not a skipped row.
	}
	var request pipelineTask2Request
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	notice, release := os.NewFile(3, "task2-ready"), os.NewFile(4, "task2-release")
	defer func() {
		if err := notice.Close(); err != nil {
			t.Error(err)
		}
	}()
	defer func() {
		if err := release.Close(); err != nil {
			t.Error(err)
		}
	}()
	ready := func() {
		if _, err := notice.Write([]byte{'R'}); err != nil {
			t.Fatal(err)
		}
		var value [1]byte
		if _, err := io.ReadFull(release, value[:]); err != nil || value[0] != 'G' {
			t.Fatalf("child release=%q error=%v", value, err)
		}
	}
	response := pipelineTask2Response{PID: os.Getpid(), PPID: os.Getppid()}
	switch request.Action {
	case "write":
		pipelinePhysicalWrite(t, request, ready)
	case "rerun":
		ready()
		run, err := publication.PrepareWithLimits(t.Context(), request.Options, request.Limits)
		response.Prepared = err == nil
		response.InvalidRecord = errors.Is(err, publication.ErrInvalidRecord)
		response.OutputExists = errors.Is(err, os.ErrExist)
		response.RecoveryConflict = errors.Is(err, publication.ErrRecoveryConflict)
		var recovery *publication.RecoveryError
		response.RecoveryError = errors.As(err, &recovery)
		if err != nil {
			response.Error = fmt.Sprintf("%+v", err)
		} else {
			response.Snapshot = run.SourceSnapshot()
			if err := run.Close(); err != nil {
				t.Fatal(err)
			}
		}
	default:
		t.Fatalf("unknown child action %q", request.Action)
	}
	encoded, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(request.Response, encoded, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineStageWriteError(t *testing.T) {
	for _, kind := range []string{"text", "w3"} {
		for _, mode := range pipelineFaultModes() {
			t.Run(kind+"/"+mode.name, func(t *testing.T) {
				// Given: an isolated child, ready/release pipes registered before Start.
				// When: the child lowers only its soft limit at the actual stage Write.
				response := pipelineTask2Child(t, pipelineTask2Request{Action: "write", Kind: kind, Mode: mode.name})
				// Then: a real EFBIG was asserted by a normally exited/reaped child.
				pipelineEqual(t, response.PID > 0, true)
			})
		}
	}
}

func pipelinePhysicalWrite(t *testing.T, request pipelineTask2Request, ready func()) {
	t.Helper()
	var mode pipelineFaultMode
	for _, candidate := range pipelineFaultModes() {
		if candidate.name == request.Mode {
			mode = candidate
		}
	}
	if mode.name == "" {
		t.Fatalf("unknown mode %s", request.Mode)
	}
	// Given: model-free Core, initialized real DB and prepared/capability-probed Run.
	h := newPipelineFault(t, request.Kind, mode)
	h.scenario = pipelineFaultScenario{name: "stage-write-EFBIG", row: "M02"}
	h.configureGeneration(t)
	h.configureStage(t)
	h.before = h.assets(t)
	h.want, h.phase = unix.EFBIG, "stage"
	var original, lowered, restored unix.Rlimit
	writes := 0
	h.adapter.export = func(w io.Writer) io.Writer {
		if err := unix.Statx(unix.AT_FDCWD, h.f.paths.Stage, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &h.stageID); err != nil {
			t.Fatal(err)
		}
		return pipelineFaultWriter(func(b []byte) (n int, err error) {
			writes++
			if writes != 1 || len(b) <= 8 {
				t.Fatalf("expected one real adapter export of more than 8 bytes: write=%d len=%d", writes, len(b))
			}
			if err := unix.Getrlimit(unix.RLIMIT_FSIZE, &original); err != nil {
				t.Fatal(err)
			}
			signal.Ignore(syscall.SIGXFSZ) // Only this owned child, never its parent.
			defer signal.Reset(syscall.SIGXFSZ)
			lowered = original
			lowered.Cur = 8
			if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &lowered); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if restoreErr := unix.Setrlimit(unix.RLIMIT_FSIZE, &original); restoreErr != nil {
					t.Fatal(restoreErr)
				}
				if restoreErr := unix.Getrlimit(unix.RLIMIT_FSIZE, &restored); restoreErr != nil {
					t.Fatal(restoreErr)
				}
			}()
			// Two delegating physical writes distinguish the permitted 8-byte
			// prefix from the next actual kernel EFBIG, not an injected errno.
			n, err = w.Write(b[:8])
			if err != nil {
				return n, err
			}
			pipelineEqual(t, n, 8)
			m, physicalErr := w.Write(b[8:])
			var pathErr *os.PathError
			if !errors.Is(physicalErr, unix.EFBIG) || !errors.As(physicalErr, &pathErr) || pathErr.Op != "write" {
				t.Fatalf("physical stage Write EFBIG not reached: n=%d error=%+v", m, physicalErr)
			}
			h.reached["physical-stage-write-EFBIG"]++
			return n + m, physicalErr
		})
	}
	h.f.capture(t, "before-write", pipelineState{Options: h.options})
	// When: ready only after real Prepare has completed, release before Stage.
	result, err := h.f.core.fileWithTM(h.ctx, h.options, h.adapter, h.f.profile, nil,
		&tmExecution{store: h.f.store, scope: h.f.scope},
		func(ctx context.Context, options publication.Options) (publicationRun, error) {
			real, err := publication.PrepareWithLimits(ctx, options, h.f.limits)
			if err != nil {
				return nil, err
			}
			h.run.publicationRun = real
			h.f.paths = real.SetPipelineFaultHooks(h.hooks(t))
			h.reached["prepare"]++
			h.run.beforeStage = ready
			return h.run, nil
		})
	// Then: restored hard/soft limits, real closed stage FD, D0 and exact prefix.
	pipelineEqual(t, lowered.Max, original.Max)
	pipelineEqual(t, restored, original)
	pipelineEqual(t, h.reached["physical-stage-write-EFBIG"], 1)
	pipelineEqual(t, h.adapter.observes, 0)
	pipelineEqual(t, h.reached["stage-sync-real"], 0)
	h.assertResult(t, result, err)
	stage := fileBytes(t, h.f.paths.Stage)
	pipelineEqual(t, len(stage), 8)
	pipelineEqual(t, h.stageFD != nil, true)
	h.f.capture(t, "after-write", pipelineState{Options: h.options, Result: result})
	h.saveReport(t, result, err)
	t.Logf("physical EFBIG pid=%d uid=%d stage=%s prefix_bytes=%d soft=%d hard_before=%d hard_after=%d restored=%+v actual=%T %v", os.Getpid(), os.Getuid(), h.f.paths.Stage, len(stage), lowered.Cur, original.Max, lowered.Max, restored, err, err)
	h.rerun(t)
}

func (h *pipelineFault) rerun(t *testing.T) {
	t.Helper()
	// This fresh-PID check is specifically real Prepare/recovery, not a claim of
	// whole-Core process stops or native inference (those are separate tasks).
	before := h.assets(t)
	response := pipelineTask2Child(t, pipelineTask2Request{Action: "rerun", Options: h.options, Limits: h.f.limits})
	after := h.assets(t)
	refusal := !before["record"].Exists || h.scenario.name == "prepared-record-corrupt" ||
		h.scenario.name == "backup-rename-collision" || h.state == publication.SourceBackedUp
	if refusal {
		if response.Prepared || !response.RecoveryError {
			t.Fatalf("fresh Prepare did not refuse retained assets: %+v", response)
		}
		if !before["record"].Exists || h.scenario.name == "prepared-record-corrupt" {
			pipelineEqual(t, response.InvalidRecord, true)
		} else {
			pipelineEqual(t, response.RecoveryConflict, true)
		}
		pipelineEqual(t, after, before)
	} else {
		if h.state == publication.Published && h.mode.mode == publication.Create {
			pipelineEqual(t, response.Prepared, false)
			pipelineEqual(t, response.OutputExists, true)
		} else {
			pipelineEqual(t, response.Prepared, true)
			pipelineEqual(t, response.Snapshot, after["source"].Bytes)
		}
		for _, name := range []string{"source", "output", "backup", "stage"} {
			pipelineEqual(t, after[name], before[name])
		}
		if after["record"].Exists {
			var record struct {
				Phase string `json:"phase"`
			}
			if err := json.Unmarshal(after["record"].Bytes, &record); err != nil {
				t.Fatal(err)
			}
			expected := "abandoned"
			switch h.state {
			case publication.Published:
				expected = "published"
			case publication.Restored:
				expected = "restored"
			}
			pipelineEqual(t, record.Phase, expected)
		}
	}
	pipelineEqual(t, pipelineReadRows(t, h.f.db), h.rows)
	h.f.capture(t, "after-fresh-prepare", pipelineState{Options: h.options})
}

func TestPipelineUnsupportedFilesystem(t *testing.T) {
	root := os.Getenv("YAKUORI_PIPELINE_UNQUALIFIED_ROOT")
	if root == "" {
		root = "/tmp"
	}
	if !filepath.IsAbs(root) || (root != "/tmp" && !strings.HasPrefix(filepath.Clean(root), "/tmp/")) {
		t.Fatalf("unqualified root must explicitly be under /tmp: %s", root)
	}
	for _, kind := range []string{"text", "w3"} {
		t.Run(kind, func(t *testing.T) {
			// Given: independent /tmp directory, not TMPDIR/T.TempDir inheritance.
			parent, err := os.MkdirTemp(root, "pipeline-unsupported-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(parent); err != nil {
					t.Error(err)
				}
			})
			var fs unix.Statfs_t
			if err := unix.Statfs(parent, &fs); err != nil {
				t.Fatal(err)
			}
			if fs.Type == unix.BTRFS_SUPER_MAGIC || fs.Type == unix.EXT4_SUPER_MAGIC {
				t.Fatalf("runner must supply an actual unqualified /tmp mount; type=%x", fs.Type)
			}
			f := newPipelineFixture(t, kind)
			options := publication.Options{Source: filepath.Join(parent, "source"), Output: filepath.Join(parent, "output"), Mode: publication.Create}
			writeFile(t, options.Source, f.raw)
			var before unix.Statx_t
			if err := unix.Statx(unix.AT_FDCWD, options.Source, 0, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &before); err != nil {
				t.Fatal(err)
			}
			f.capture(t, "before", pipelineState{Options: options})
			// When: actual public Core preflight on the verified unqualified mount.
			result, err := f.core.FileWithTM(t.Context(), options, f.adapter, f.profile, f.store, f.scope)
			// Then: typed Prepare refusal before generation, D0, exact original inode.
			var failure *Error
			if !errors.Is(err, publication.ErrUnsupportedFilesystem) || !errors.As(err, &failure) || failure.Phase != "prepare" {
				t.Fatalf("actual typed Prepare refusal absent: %v", err)
			}
			pipelineEqual(t, result.TMCommitted, false)
			pipelineEqual(t, result.Publication.State, publication.NotPublished)
			pipelineEqual(t, len(f.backend.calls), 0)
			pipelineEqual(t, pipelineReadRows(t, f.db), f.initial)
			pipelineEqual(t, fileBytes(t, options.Source), f.raw)
			var after unix.Statx_t
			if err := unix.Statx(unix.AT_FDCWD, options.Source, 0, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &after); err != nil {
				t.Fatal(err)
			}
			pipelineEqual(t, after.Ino, before.Ino)
			pipelineEqual(t, after.Mnt_id, before.Mnt_id)
			if _, err := os.Lstat(options.Output); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("output created on refused mount: %v", err)
			}
			f.capture(t, "after", pipelineState{Options: options, Result: result})
			t.Logf("actual unqualified fs_type=%x mount_id=%d uid=%d source=%s Generate=0 D0 NotPublished typed=%T", fs.Type, after.Mnt_id, os.Getuid(), options.Source, failure)
		})
	}
}

func (h *pipelineFault) watchTransactionCancel(t *testing.T) {
	t.Helper()
	path := pipelineFaultDBPath(t, h.f.db)
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), "task2-journal-watch")
	h.watchFile = file
	t.Cleanup(func() {
		if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	})
	if _, err := unix.InotifyAddWatch(fd, filepath.Dir(path), unix.IN_CREATE); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	h.watchDone = done
	// Subscription is active before Core can begin its transaction. A held
	// SQLite reader prevents COMMIT while the actual journal event cancels it.
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := file.Read(buffer)
			if err != nil {
				done <- err
				return
			}
			for offset := 0; offset+unix.SizeofInotifyEvent <= n; {
				length := int(binary.LittleEndian.Uint32(buffer[offset+12:]))
				end := offset + unix.SizeofInotifyEvent + length
				if end > n {
					done <- io.ErrUnexpectedEOF
					return
				}
				name := strings.TrimRight(string(buffer[offset+unix.SizeofInotifyEvent:end]), "\x00")
				if name == filepath.Base(path)+"-journal" {
					h.cancel()
					done <- nil
					return
				}
				offset = end
			}
		}
	}()
}
