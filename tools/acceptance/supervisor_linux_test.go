//go:build linux

package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary is the real worker process, including fd 3 and Wait.
func TestSupervisorChild(t *testing.T) {
	if os.Getenv("YAKUORI_SUPERVISOR_HELPER") != "1" {
		return
	}
	var req WorkerRequest
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		os.Exit(11)
	}
	if req.Mode == "timeout" || req.Mode == "cancel" {
		signal.Ignore(syscall.SIGTERM)
	}
	if err := os.WriteFile(req.TMData, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		os.Exit(12)
	}
	switch req.Mode {
	case "heldpipe":
		child := exec.Command("sh", "-c", "trap '' TERM; exec sleep 10")
		child.ExtraFiles = []*os.File{os.NewFile(3, "result")}
		if err := child.Start(); err != nil {
			os.Exit(14)
		}
		if err := os.WriteFile(req.Source, []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
			os.Exit(15)
		}
		f := os.NewFile(3, "result")
		if err := json.NewEncoder(f).Encode(WorkerResult{RunID: req.RunID, FixtureID: req.FixtureID, Mode: req.Mode}); err != nil {
			os.Exit(16)
		}
		f.Close()
		os.Exit(0)
	case "timeout", "cancel":
		time.Sleep(10 * time.Second)
	case "failed":
		os.Exit(7)
	case "missing":
		return
	case "after-sample":
		for {
			if _, err := os.Stat(req.Source); err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		fallthrough
	default:
		time.Sleep(250 * time.Millisecond)
		f := os.NewFile(3, "result")
		seconds := 0.01
		if err := json.NewEncoder(f).Encode(WorkerResult{RunID: req.RunID, FixtureID: req.FixtureID, Mode: req.Mode, LoadSeconds: &seconds, LoadStatus: "complete"}); err != nil {
			os.Exit(13)
		}
		f.Close()
	}
	os.Exit(0)
}

func TestSupervisorClosesInheritedResultPipe(t *testing.T) {
	t.Setenv("YAKUORI_SUPERVISOR_HELPER", "1")
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "pid")
	descendantPath := filepath.Join(dir, "descendant")
	s, err := NewSupervisor(os.Args[0], SupervisorOptions{GPUUUID: "GPU-test", Interval: 20 * time.Millisecond, QueryTimeout: 80 * time.Millisecond, StopGrace: 40 * time.Millisecond, ReapTimeout: time.Second, SMIBinary: fakeSMI(t, pidPath, "missing")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err = s.launch(ctx, WorkerRequest{RunID: "held", FixtureID: "test", Mode: "heldpipe", TMData: pidPath, Source: descendantPath}, []string{"-test.run=^TestSupervisorChild$"})
	if err != nil || time.Since(start) > 2*time.Second {
		t.Fatalf("worker did not finish: err=%v", err)
	}
	if m := s.Results["held"]; m.ReapStatus != "reaped" || m.ResultStatus != "available" {
		t.Fatalf("worker result: %+v", m)
	}
	p, err := os.ReadFile(descendantPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(p))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if errors.Is(err, os.ErrNotExist) || (err == nil && strings.Contains(string(stat), ") Z ")) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant %d remains running", pid)
}

func fakeSMI(t *testing.T, pidPath, behavior string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-smi")
	var script string
	switch behavior {
	case "valid":
		script = fmt.Sprintf(`#!/bin/sh
case "$1" in
 --query-compute-apps=*) if test -f '%s'; then p=$(cat '%s'); printf 'GPU-test, %%s, 8\n' "$p"; fi ;;
 --query-gpu=uuid,memory.used) printf 'GPU-test, 10\n' ;;
 --query-gpu=uuid,name,driver_version,memory.total) printf 'GPU-test, test gpu, 1.0, 100\n' ;;
esac
`, pidPath, pidPath)
	case "missing":
		script = "#!/bin/sh\ncase \"$1\" in --query-gpu=uuid,memory.used) printf 'GPU-test, 10\\n';; esac\n"
	case "hang":
		script = "#!/bin/sh\nsleep 10\n"
	case "hangdevice":
		script = fmt.Sprintf("#!/bin/sh\ncase \"$1\" in --query-compute-apps=*) while ! test -f '%s'; do sleep .01; done; p=$(cat '%s'); printf 'GPU-test, %%s, 8\\n' \"$p\";; --query-gpu=uuid,memory.used) sleep 10;; esac\n", pidPath, pidPath)
	}
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSupervisorKeepsSampleWhenWorkerEndsDuringQuery(t *testing.T) {
	t.Setenv("YAKUORI_SUPERVISOR_HELPER", "1")
	pidPath := filepath.Join(t.TempDir(), "pid")
	s, err := NewSupervisor(os.Args[0], SupervisorOptions{GPUUUID: "GPU-test", Interval: 20 * time.Millisecond, QueryTimeout: time.Second, StopGrace: 40 * time.Millisecond, ReapTimeout: time.Second, SMIBinary: fakeSMI(t, pidPath, "hangdevice")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = s.launch(ctx, WorkerRequest{RunID: "during-query", FixtureID: "test", Mode: "success", TMData: pidPath}, []string{"-test.run=^TestSupervisorChild$"})
	if err != nil {
		t.Fatal(err)
	}
	m := s.Results["during-query"]
	if m.ProcessVRAMBytes == nil || *m.ProcessVRAMBytes != 8*1024*1024 || m.DeviceVRAMBytes != nil || m.ResourceStatus != "partial" || len(m.Samples) != 1 || m.Samples[0].DeviceStatus != "interrupted" {
		t.Fatalf("lost partial query: %+v", m)
	}
}

func TestSupervisorKeepsEarlierSamplesWhenQueryCleanupExceedsReapTimeout(t *testing.T) {
	t.Setenv("YAKUORI_SUPERVISOR_HELPER", "1")
	dir := t.TempDir()
	pidPath, marker, queryPID := filepath.Join(dir, "worker-pid"), filepath.Join(dir, "query-hanging"), filepath.Join(dir, "query-pid")
	scriptPath := filepath.Join(dir, "fake-smi")
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
 --query-compute-apps=*)
  if test -f '%s'; then
   trap '' TERM
   printf '%%s' $$ > '%s'
   : > '%s'
   while :; do :; done
  fi
  while ! test -f '%s'; do sleep .01; done
  p=$(cat '%s')
  : > '%s'
  printf 'GPU-test, %%s, 8\n' "$p" ;;
 --query-gpu=uuid,memory.used) printf 'GPU-test, 10\n' ;;
esac
`, filepath.Join(dir, "first"), queryPID, marker, pidPath, pidPath, filepath.Join(dir, "first"))
	if err := os.WriteFile(scriptPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s, err := NewSupervisor(os.Args[0], SupervisorOptions{GPUUUID: "GPU-test", Interval: 10 * time.Millisecond, QueryTimeout: 5 * time.Second, StopGrace: 500 * time.Millisecond, ReapTimeout: 30 * time.Millisecond, SMIBinary: scriptPath})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_, err = s.launch(ctx, WorkerRequest{RunID: "snapshot", FixtureID: "test", Mode: "after-sample", TMData: pidPath, Source: marker}, []string{"-test.run=^TestSupervisorChild$"})
	if err != nil {
		t.Fatal(err)
	}
	m := s.Results["snapshot"]
	if (m.MonitoringStatus != "complete" && m.MonitoringStatus != "interrupted") || (m.QueryCleanup != "cleared" && m.QueryCleanup != "unknown") || len(m.Samples) == 0 || m.ProcessVRAMBytes == nil || *m.ProcessVRAMBytes != 8*1024*1024 || m.DeviceVRAMBytes == nil || *m.DeviceVRAMBytes != 10*1024*1024 {
		t.Fatalf("lost earlier sample during delayed cleanup: %+v", m)
	}
	p, err := os.ReadFile(queryPID)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(p))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if errors.Is(err, os.ErrNotExist) || (err == nil && strings.Contains(string(stat), ") Z ")) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("query %d remained running", pid)
}

// This process exits immediately after Launch, like the real CLI main. The
// parent test checks that query cleanup did not depend on its goroutines
// continuing after that exit.
func TestSupervisorImmediateExitHelper(t *testing.T) {
	if os.Getenv("YAKUORI_SUPERVISOR_EXIT_HELPER") != "1" {
		return
	}
	if err := os.Setenv("YAKUORI_SUPERVISOR_HELPER", "1"); err != nil {
		os.Exit(21)
	}
	dir := os.Getenv("YAKUORI_SUPERVISOR_EXIT_DIR")
	s, err := NewSupervisor(os.Args[0], SupervisorOptions{GPUUUID: "GPU-test", Interval: 10 * time.Millisecond, QueryTimeout: 5 * time.Second, StopGrace: 500 * time.Millisecond, ReapTimeout: 30 * time.Millisecond, SMIBinary: filepath.Join(dir, "fake-smi")})
	if err != nil {
		os.Exit(22)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_, err = s.launch(ctx, WorkerRequest{RunID: "exit", FixtureID: "test", Mode: "after-sample", TMData: filepath.Join(dir, "worker-pid"), Source: filepath.Join(dir, "query-hanging")}, []string{"-test.run=^TestSupervisorChild$"})
	if err != nil {
		os.Exit(23)
	}
	if err := os.WriteFile(filepath.Join(dir, "returned"), []byte(s.Results["exit"].QueryCleanup), 0600); err != nil {
		os.Exit(24)
	}
	os.Exit(0)
}

func TestSupervisorImmediateExitStopsQuery(t *testing.T) {
	dir := t.TempDir()
	pidPath, marker, queryPID := filepath.Join(dir, "worker-pid"), filepath.Join(dir, "query-hanging"), filepath.Join(dir, "query-pid")
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
 --query-compute-apps=*)
  if test -f '%s'; then
   trap '' TERM
   printf '%%s' $$ > '%s'
   : > '%s'
   while :; do :; done
  fi
  while ! test -f '%s'; do sleep .01; done
  p=$(cat '%s')
  : > '%s'
  printf 'GPU-test, %%s, 8\n' "$p" ;;
 --query-gpu=uuid,memory.used) printf 'GPU-test, 10\n' ;;
esac
`, filepath.Join(dir, "first"), queryPID, marker, pidPath, pidPath, filepath.Join(dir, "first"))
	if err := os.WriteFile(filepath.Join(dir, "fake-smi"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSupervisorImmediateExitHelper$")
	cmd.Env = append(os.Environ(), "YAKUORI_SUPERVISOR_EXIT_HELPER=1", "YAKUORI_SUPERVISOR_EXIT_DIR="+dir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("supervisor helper: %v: %s", err, output)
	}
	if _, err := os.Stat(filepath.Join(dir, "returned")); err != nil {
		t.Fatal(err)
	}
	p, err := os.ReadFile(queryPID)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(p))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("query %d still exists after supervisor exited", pid)
}

func TestSupervisorAmbiguousGPUProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake-smi")
	script := "#!/bin/sh\ncase \"$1\" in --query-compute-apps=*) printf 'GPU-other, 99, 20\\nGPU-test, 99, 8\\n';; --query-gpu=uuid,memory.used) printf 'GPU-test, 10\\n';; esac\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s, err := NewSupervisor(os.Args[0], SupervisorOptions{GPUUUID: "GPU-test", Interval: time.Millisecond, QueryTimeout: time.Second, StopGrace: 20 * time.Millisecond, ReapTimeout: time.Second, SMIBinary: path})
	if err != nil {
		t.Fatal(err)
	}
	sample := s.sample(context.Background(), 99, time.Now())
	if sample.ProcessStatus != "ambiguous" || sample.ProcessBytes != nil || sample.DeviceStatus != "observed" || sample.DeviceBytes == nil || *sample.DeviceBytes != 10*1024*1024 {
		t.Fatalf("ambiguous PID not rejected: %+v", sample)
	}
}

func TestSupervisorUnstartedWallIsNull(t *testing.T) {
	for _, canceled := range []bool{true, false} {
		t.Run(strconv.FormatBool(canceled), func(t *testing.T) {
			s, err := NewSupervisor(filepath.Join(t.TempDir(), "missing"), SupervisorOptions{GPUUUID: "GPU-test", Interval: time.Millisecond, QueryTimeout: time.Second, StopGrace: 20 * time.Millisecond, ReapTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			if canceled {
				cancel()
			}
			defer cancel()
			_, err = s.Launch(ctx, WorkerRequest{RunID: "unstarted"})
			if err == nil || s.Results["unstarted"].WallSeconds != nil {
				t.Fatalf("unstarted wall time: %+v, %v", s.Results["unstarted"], err)
			}
			dir := t.TempDir()
			if err := saveRecord(dir, RunRecord{Runs: []FixtureRun{{RunID: "unstarted", Measurement: s.Results["unstarted"]}}}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "run.json"))
			if err != nil || !strings.Contains(string(data), `"wall_seconds": null`) {
				t.Fatalf("saved wall: %s %v", data, err)
			}
		})
	}
}

func TestSupervisorQueryCleansExitedParentsGroup(t *testing.T) {
	dir := t.TempDir()
	pidPath, callsPath, scriptPath := filepath.Join(dir, "pid"), filepath.Join(dir, "calls"), filepath.Join(dir, "smi")
	script := fmt.Sprintf("#!/bin/sh\nprintf x >> '%s'\nsleep 10 </dev/null >/dev/null 2>&1 &\nprintf '%%s' $! > '%s'\nprintf 'GPU-test, 10\\n'\n", callsPath, pidPath)
	if err := os.WriteFile(scriptPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s, err := NewSupervisor(os.Args[0], SupervisorOptions{GPUUUID: "GPU-test", Interval: time.Millisecond, QueryTimeout: time.Second, StopGrace: 40 * time.Millisecond, ReapTimeout: time.Second, SMIBinary: scriptPath})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, queryErr := s.query(context.Background(), "query")
	if time.Since(start) > 2*time.Second {
		t.Fatal("query cleanup was unbounded")
	}
	b, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatal(err)
	}
	stat, statErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if statErr == nil && !strings.Contains(string(stat), ") Z ") {
		t.Fatalf("query descendant %d still running: %s", pid, stat)
	}
	if s.queryCleanup() == "unknown" {
		if queryErr == nil {
			t.Fatal("unknown query cleanup succeeded")
		}
		if _, err := s.query(context.Background(), "query"); err == nil {
			t.Fatal("query restarted after unknown cleanup")
		}
		calls, err := os.ReadFile(callsPath)
		if err != nil || string(calls) != "x" {
			t.Fatalf("query ran again: %q %v", calls, err)
		}
	} else if queryErr != nil || s.queryCleanup() != "cleared" {
		t.Fatalf("query cleanup %q: %v", s.queryCleanup(), queryErr)
	}
}

func TestSupervisorProcessOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, mode, smi, wantReason, wantOutcome, wantResult string
		cancel                                               bool
	}{
		{"success", "success", "valid", "none", "exited", "available", false},
		{"failure", "failed", "missing", "none", "exited", "missing_or_invalid", false},
		{"missing", "missing", "missing", "none", "exited", "missing_or_invalid", false},
		{"deadline", "timeout", "hang", "wall_limit", "signaled", "missing_or_invalid", false},
		{"cancellation", "cancel", "hang", "canceled", "signaled", "missing_or_invalid", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("YAKUORI_SUPERVISOR_HELPER", "1")
			pidPath := filepath.Join(t.TempDir(), "pid")
			s, err := NewSupervisor(os.Args[0], SupervisorOptions{GPUUUID: "GPU-test", Interval: 20 * time.Millisecond, QueryTimeout: 80 * time.Millisecond, StopGrace: 40 * time.Millisecond, ReapTimeout: time.Second, SMIBinary: fakeSMI(t, pidPath, tc.smi)})
			if err != nil {
				t.Fatal(err)
			}
			req := WorkerRequest{RunID: tc.name, FixtureID: "test", Mode: tc.mode, TMData: pidPath}
			guard, cancelGuard := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelGuard()
			var ctx context.Context
			var cancel context.CancelFunc
			if tc.mode == "timeout" {
				ctx, cancel = context.WithTimeout(guard, 2*time.Second)
			} else {
				ctx, cancel = context.WithCancel(guard)
			}
			defer cancel()
			ready := make(chan struct{})
			if tc.mode == "timeout" || tc.mode == "cancel" {
				go func() {
					for {
						if _, err := os.Stat(pidPath); err == nil {
							break
						}
						if ctx.Err() != nil {
							return
						}
						time.Sleep(5 * time.Millisecond)
					}
					close(ready)
					if tc.cancel {
						cancel()
					}
				}()
			}
			start := time.Now()
			_, gotErr := s.launch(ctx, req, []string{"-test.run=^TestSupervisorChild$"})
			if elapsed := time.Since(start); elapsed > 4*time.Second {
				t.Fatalf("supervisor did not stop in time: %v", elapsed)
			}
			m := s.Results[tc.name]
			if tc.mode == "timeout" || tc.mode == "cancel" {
				select {
				case <-ready:
				default:
					t.Fatal("worker did not become ready before stop")
				}
				if tc.mode == "timeout" && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
					t.Fatalf("deadline context did not expire: %v", ctx.Err())
				}
				if tc.mode == "cancel" && !errors.Is(ctx.Err(), context.Canceled) {
					t.Fatalf("cancel context did not cancel: %v", ctx.Err())
				}
			}
			if tc.mode == "success" && gotErr != nil {
				t.Fatalf("success: %v, record %+v", gotErr, m)
			}
			if tc.mode != "success" && gotErr == nil {
				t.Fatal("expected failure")
			}
			if m.StopReason != tc.wantReason || m.ChildOutcome != tc.wantOutcome || m.ResultStatus != tc.wantResult || m.ReapStatus != "reaped" {
				t.Fatalf("outcome: %+v", m)
			}
			if m.RSSBytes == nil || *m.RSSBytes <= 0 {
				t.Fatalf("missing child ru_maxrss: %+v", m)
			}
			if tc.name == "success" && (m.ResourceStatus != "complete" || m.ProcessVRAMBytes == nil || *m.ProcessVRAMBytes != 8*1024*1024 || m.DeviceVRAMBytes == nil) {
				t.Fatalf("missing measurement: %+v", m)
			}
			if tc.mode == "timeout" || tc.mode == "cancel" {
				pidBytes, err := os.ReadFile(pidPath)
				if err != nil {
					t.Fatal(err)
				}
				pid, _ := strconv.Atoi(string(pidBytes))
				if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
					t.Fatalf("worker still exists: %v", err)
				}
			}
			evidence := t.TempDir()
			if err := saveRecord(evidence, RunRecord{Runs: []FixtureRun{{RunID: req.RunID, Measurement: m}}}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(evidence, "run.json"))
			if err != nil {
				t.Fatal(err)
			}
			var saved RunRecord
			if err := json.Unmarshal(data, &saved); err != nil || len(saved.Runs) != 1 || saved.Runs[0].Measurement == nil {
				t.Fatalf("saved: %v %s", err, data)
			}
			got := saved.Runs[0].Measurement
			if got.StopReason != m.StopReason || got.ChildOutcome != m.ChildOutcome || got.Signal != m.Signal || got.ReapStatus != m.ReapStatus || got.GroupStatus != m.GroupStatus || got.ResultStatus != m.ResultStatus || got.RSSBytes == nil || *got.RSSBytes != *m.RSSBytes || got.WallSeconds == nil || m.WallSeconds == nil || *got.WallSeconds != *m.WallSeconds || (got.ExitCode == nil) != (m.ExitCode == nil) || (got.ProcessVRAMBytes == nil) != (m.ProcessVRAMBytes == nil) {
				t.Fatalf("saved outcome mismatch: %+v want %+v", got, m)
			}
			if got.ExitCode != nil && *got.ExitCode != *m.ExitCode {
				t.Fatal("saved exit differs")
			}
			exit := "null"
			if got.ExitCode != nil {
				exit = strconv.Itoa(*got.ExitCode)
			}
			if (tc.name == "failure" && exit != "7") || ((tc.name == "success" || tc.name == "missing") && exit != "0") || ((tc.name == "deadline" || tc.name == "cancellation") && (exit != "null" || got.Signal != "killed")) {
				t.Fatalf("incorrect exit: %+v", got)
			}
			t.Logf("control=%s wall=%.6fs rss=%dB outcome=%s exit=%s signal=%s stop=%s result=%s reap=%s group=%s query=%s resource=%s", tc.name, *got.WallSeconds, *got.RSSBytes, got.ChildOutcome, exit, got.Signal, got.StopReason, got.ResultStatus, got.ReapStatus, got.GroupStatus, got.QueryCleanup, got.ResourceStatus)
		})
	}
}

func TestSupervisorSampleCompleteness(t *testing.T) {
	n := int64(42)
	for _, tc := range []struct {
		name          string
		samples       []GPUSample
		interrupted   bool
		want, monitor string
	}{
		{"startup absent", []GPUSample{{ProcessStatus: "absent", DeviceStatus: "observed", DeviceBytes: &n}, {ProcessStatus: "observed", ProcessBytes: &n, DeviceStatus: "observed", DeviceBytes: &n}}, false, "complete", "complete"},
		{"all absent", []GPUSample{{ProcessStatus: "absent", DeviceStatus: "observed", DeviceBytes: &n}}, false, "partial", "complete"},
		{"query failure", []GPUSample{{ProcessStatus: "observed", ProcessBytes: &n, DeviceStatus: "observed", DeviceBytes: &n}, {ProcessStatus: "error", DeviceStatus: "error"}}, false, "partial", "error"},
		{"interrupted", []GPUSample{{ProcessStatus: "observed", ProcessBytes: &n, DeviceStatus: "observed", DeviceBytes: &n}}, true, "partial", "interrupted"},
		{"no samples", nil, false, "unavailable", "complete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Measurement{Samples: tc.samples, RSSBytes: &n, ReapStatus: "reaped", GroupStatus: "cleared"}
			summarizeSamples(&m, tc.interrupted)
			if m.ResourceStatus != tc.want || m.MonitoringStatus != tc.monitor {
				t.Fatalf("got %+v", m)
			}
		})
	}
}
