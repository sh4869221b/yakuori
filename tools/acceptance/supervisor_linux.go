//go:build linux

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sh4869221b/yakuori/internal/localize"
)

// SupervisorOptions controls observation and process recovery, not acceptance limits.
type SupervisorOptions struct {
	GPUUUID                                        string
	Interval, QueryTimeout, StopGrace, ReapTimeout time.Duration
	SMIBinary                                      string
}

type SupervisorEnvironment struct {
	GPUUUID        string     `json:"gpu_uuid"`
	GPUName        string     `json:"gpu_name,omitempty"`
	Driver         string     `json:"driver,omitempty"`
	GPUMemoryBytes *int64     `json:"gpu_memory_bytes"`
	CPUModel       string     `json:"cpu_model,omitempty"`
	LogicalCPUs    int        `json:"logical_cpus"`
	RAMBytes       *int64     `json:"ram_bytes"`
	OS             string     `json:"os"`
	Kernel         string     `json:"kernel,omitempty"`
	Architecture   string     `json:"architecture"`
	GoVersion      string     `json:"go_version"`
	GoMaxProcs     int        `json:"gomaxprocs"`
	GoMemLimit     string     `json:"gomemlimit,omitempty"`
	GPUStatus      string     `json:"gpu_status"`
	QueryCleanup   string     `json:"query_cleanup"`
	Baseline       *GPUSample `json:"baseline"`
}

type GPUProcess struct {
	PID       int    `json:"pid"`
	UsedBytes *int64 `json:"used_bytes"`
}

type GPUSample struct {
	AtSeconds      float64      `json:"at_seconds"`
	QuerySeconds   float64      `json:"query_seconds"`
	ProcessStatus  string       `json:"process_status"` // observed, absent, ambiguous, interrupted, error
	ProcessBytes   *int64       `json:"process_bytes"`
	DeviceStatus   string       `json:"device_status"`
	DeviceBytes    *int64       `json:"device_bytes"`
	OtherProcesses []GPUProcess `json:"other_processes,omitempty"`
}

type Measurement struct {
	RunID               string                     `json:"run_id"`
	ResourceStatus      string                     `json:"resource_status"` // complete, partial, unavailable, error
	ChildOutcome        string                     `json:"child_outcome"`
	StopReason          string                     `json:"stop_reason"`
	ExitCode            *int                       `json:"exit_code"`
	Signal              string                     `json:"signal,omitempty"`
	ResultStatus        string                     `json:"result_status"`
	ReapStatus          string                     `json:"reap_status"`
	GroupStop           string                     `json:"group_stop"`
	GroupStatus         string                     `json:"group_status"`
	WallSeconds         *float64                   `json:"wall_seconds"`
	QueryCleanup        string                     `json:"query_cleanup"`
	RSSBytes            *int64                     `json:"rss_bytes"`
	ProcessVRAMBytes    *int64                     `json:"process_vram_sampled_max_bytes"`
	DeviceVRAMBytes     *int64                     `json:"device_vram_sampled_max_bytes"`
	MonitoringStatus    string                     `json:"monitoring_status"`
	IntervalSeconds     float64                    `json:"interval_seconds"`
	QueryTimeoutSeconds float64                    `json:"query_timeout_seconds"`
	Samples             []GPUSample                `json:"samples"`
	LoadSeconds         *float64                   `json:"load_seconds"`
	LoadStatus          string                     `json:"load_status"`
	GenerationSeconds   *float64                   `json:"generation_seconds"`
	GenerationStatus    string                     `json:"generation_status"`
	GenerationCalls     *int                       `json:"generation_calls"`
	GenerateCompleted   *int                       `json:"generate_completed"`
	GenerateFailed      *int                       `json:"generate_failed"`
	Validation          *localize.ValidationCounts `json:"validation"`
	TMValidation        *localize.ValidationCounts `json:"tm_validation"`
}

type Supervisor struct {
	executable   string
	opts         SupervisorOptions
	Results      map[string]*Measurement
	Environment  SupervisorEnvironment
	queryStarted atomic.Bool
	queryBlocked atomic.Bool
	queryMu      sync.Mutex
	queryPID     int
}

func NewSupervisor(executable string, opts SupervisorOptions) (*Supervisor, error) {
	if executable == "" || opts.GPUUUID == "" || opts.Interval <= 0 || opts.QueryTimeout <= 0 || opts.StopGrace <= 0 || opts.ReapTimeout <= 0 {
		return nil, errors.New("invalid supervisor options")
	}
	if opts.SMIBinary == "" {
		opts.SMIBinary = "nvidia-smi"
	}
	return &Supervisor{executable: executable, opts: opts, Results: make(map[string]*Measurement)}, nil
}

func (s *Supervisor) query(ctx context.Context, args ...string) (string, error) {
	if s.queryBlocked.Load() {
		return "", errors.New("GPU query cleanup unknown")
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.QueryTimeout)
	defer cancel()
	cmd := exec.Command(s.opts.SMIBinary, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	reader, writer, err := os.Pipe()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	cmd.Stdout, cmd.Stderr = writer, io.Discard
	s.queryMu.Lock()
	if s.queryBlocked.Load() {
		s.queryMu.Unlock()
		writer.Close()
		return "", errors.New("GPU query cleanup unknown")
	}
	if err := ctx.Err(); err != nil {
		s.queryMu.Unlock()
		writer.Close()
		return "", err
	}
	if err := cmd.Start(); err != nil {
		s.queryMu.Unlock()
		writer.Close()
		return "", err
	}
	s.queryPID = cmd.Process.Pid
	s.queryMu.Unlock()
	writer.Close()
	s.queryStarted.Store(true)
	type outputResult struct {
		data []byte
		err  error
	}
	out := make(chan outputResult, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
		reader.Close()
		out <- outputResult{data, err}
	}()
	state, waitErr, group := waitOracle(ctx, cmd, s.opts.StopGrace, s.opts.ReapTimeout)
	s.queryMu.Lock()
	if state == nil || group != "cleared" {
		s.queryBlocked.Store(true)
		s.queryPID = 0
		s.queryMu.Unlock()
		return "", errors.New("GPU query cleanup unknown")
	}
	s.queryPID = 0
	s.queryMu.Unlock()
	if waitErr != nil {
		return "", waitErr
	}
	select {
	case output := <-out:
		if output.err != nil || len(output.data) > 1<<20 {
			return "", errors.New("invalid GPU query output")
		}
		return string(output.data), nil
	case <-time.After(s.opts.ReapTimeout):
		s.queryBlocked.Store(true)
		return "", errors.New("GPU query output incomplete")
	}
}

func (s *Supervisor) queryCleanup() string {
	if s.queryBlocked.Load() {
		return "unknown"
	}
	if s.queryStarted.Load() {
		return "cleared"
	}
	return "not_started"
}

func mibBytes(value string) (*int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || n < 0 || n > int64(^uint64(0)>>1)/(1024*1024) {
		return nil, errors.New("invalid GPU memory")
	}
	n *= 1024 * 1024
	return &n, nil
}

// CaptureEnvironment records host conditions and the selected device. Missing
// information stays empty or null; it is never inferred from the model name.
func (s *Supervisor) CaptureEnvironment(ctx context.Context) {
	e := SupervisorEnvironment{GPUUUID: s.opts.GPUUUID, GPUStatus: "unavailable", OS: runtime.GOOS, Architecture: runtime.GOARCH, GoVersion: runtime.Version(), LogicalCPUs: runtime.NumCPU(), GoMaxProcs: runtime.GOMAXPROCS(0), GoMemLimit: os.Getenv("GOMEMLIMIT")}
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		e.Kernel = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if key, value, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(key) == "model name" {
				e.CPUModel = strings.TrimSpace(value)
				break
			}
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) >= 3 && f[0] == "MemTotal:" && f[2] == "kB" {
				if n, err := strconv.ParseInt(f[1], 10, 64); err == nil && n > 0 {
					n *= 1024
					e.RAMBytes = &n
				}
				break
			}
		}
	}
	out, err := s.query(ctx, "--query-gpu=uuid,name,driver_version,memory.total", "--format=csv,noheader,nounits")
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			f := strings.Split(line, ",")
			if len(f) == 4 && strings.TrimSpace(f[0]) == s.opts.GPUUUID {
				e.GPUName, e.Driver = strings.TrimSpace(f[1]), strings.TrimSpace(f[2])
				e.GPUMemoryBytes, err = mibBytes(f[3])
				if err == nil && e.GPUName != "" && e.GPUName != "N/A" && e.Driver != "" && e.Driver != "N/A" {
					e.GPUStatus = "complete"
				}
				break
			}
		}
	}
	baseline := s.sample(ctx, 0, time.Now())
	e.Baseline = &baseline
	e.QueryCleanup = s.queryCleanup()
	s.Environment = e
}

func (s *Supervisor) sample(ctx context.Context, pid int, start time.Time) GPUSample {
	started := time.Now()
	sample := GPUSample{AtSeconds: started.Sub(start).Seconds(), ProcessStatus: "error", DeviceStatus: "error"}
	out, err := s.query(ctx, "--query-compute-apps=gpu_uuid,pid,used_gpu_memory", "--format=csv,noheader,nounits")
	if err != nil && ctx.Err() != nil {
		sample.ProcessStatus, sample.DeviceStatus = "interrupted", "not_sampled"
		sample.QuerySeconds = time.Since(started).Seconds()
		return sample
	}
	if err == nil {
		sample.ProcessStatus = "absent"
		otherDevice := false
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			f := strings.Split(line, ",")
			if len(f) != 3 {
				sample.ProcessStatus = "error"
				break
			}
			p, parseErr := strconv.Atoi(strings.TrimSpace(f[1]))
			if parseErr != nil {
				sample.ProcessStatus = "error"
				break
			}
			if strings.TrimSpace(f[0]) != s.opts.GPUUUID {
				if p == pid {
					otherDevice = true
				}
				continue
			}
			v, valueErr := mibBytes(f[2])
			if p == pid {
				if valueErr != nil || sample.ProcessBytes != nil {
					sample.ProcessStatus = "error"
					break
				}
				sample.ProcessBytes, sample.ProcessStatus = v, "observed"
			} else {
				sample.OtherProcesses = append(sample.OtherProcesses, GPUProcess{PID: p, UsedBytes: v})
			}
		}
		if otherDevice && sample.ProcessStatus != "error" {
			sample.ProcessStatus, sample.ProcessBytes = "ambiguous", nil
		}
	}
	out, err = s.query(ctx, "--query-gpu=uuid,memory.used", "--format=csv,noheader,nounits")
	if err != nil && ctx.Err() != nil {
		sample.DeviceStatus = "interrupted"
	}
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			f := strings.Split(line, ",")
			if len(f) == 2 && strings.TrimSpace(f[0]) == s.opts.GPUUUID {
				if v, e := mibBytes(f[1]); e == nil {
					sample.DeviceBytes, sample.DeviceStatus = v, "observed"
				}
				break
			}
		}
	}
	sample.QuerySeconds = time.Since(started).Seconds()
	return sample
}

func summarizeSamples(m *Measurement, interrupted bool) {
	bad := interrupted
	for _, sample := range m.Samples {
		if sample.ProcessStatus == "error" || sample.ProcessStatus == "ambiguous" || sample.DeviceStatus == "error" {
			bad = true
		}
		if v := sample.ProcessBytes; sample.ProcessStatus == "observed" && v != nil && (m.ProcessVRAMBytes == nil || *v > *m.ProcessVRAMBytes) {
			n := *v
			m.ProcessVRAMBytes = &n
		}
		if v := sample.DeviceBytes; sample.DeviceStatus == "observed" && v != nil && (m.DeviceVRAMBytes == nil || *v > *m.DeviceVRAMBytes) {
			n := *v
			m.DeviceVRAMBytes = &n
		}
	}
	if interrupted {
		m.MonitoringStatus = "interrupted"
	} else if bad {
		m.MonitoringStatus = "error"
	} else {
		m.MonitoringStatus = "complete"
	}
	switch {
	case m.RSSBytes == nil || m.ReapStatus != "reaped" || m.GroupStatus != "cleared" || m.QueryCleanup == "unknown":
		m.ResourceStatus = "error"
	case len(m.Samples) == 0 || (m.ProcessVRAMBytes == nil && m.DeviceVRAMBytes == nil):
		m.ResourceStatus = "unavailable"
	case bad || m.ProcessVRAMBytes == nil || m.DeviceVRAMBytes == nil:
		m.ResourceStatus = "partial"
	default:
		m.ResourceStatus = "complete"
	}
}

// Launch owns one direct worker child and calls Wait exactly once. Measurement
// failures do not replace a valid worker result or prevent the oracle from running.
func (s *Supervisor) Launch(ctx context.Context, req WorkerRequest) (WorkerResult, error) {
	return s.launch(ctx, req, []string{"worker"})
}

func (s *Supervisor) launch(ctx context.Context, req WorkerRequest, args []string) (result WorkerResult, resultErr error) {
	m := &Measurement{RunID: req.RunID, ResourceStatus: "error", ChildOutcome: "not_started", StopReason: "none", ResultStatus: "not_available", ReapStatus: "not_started", GroupStop: "not_started", GroupStatus: "not_started", QueryCleanup: s.queryCleanup(), MonitoringStatus: "not_started", GenerationStatus: "not_run", IntervalSeconds: s.opts.Interval.Seconds(), QueryTimeoutSeconds: s.opts.QueryTimeout.Seconds(), Samples: []GPUSample{}}
	s.Results[req.RunID] = m
	data, err := json.Marshal(req)
	if err != nil {
		return result, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return result, err
	}
	defer reader.Close()
	cmd := exec.Command(s.executable, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.ExtraFiles = []*os.File{writer}
	started := time.Now()
	if err := ctx.Err(); err != nil {
		writer.Close()
		m.StopReason = stopReason(context.Cause(ctx))
		return result, err
	}
	if err := cmd.Start(); err != nil {
		writer.Close()
		m.ChildOutcome = "start_failed"
		return result, err
	}
	writer.Close()
	m.ReapStatus = "pending"
	type readResult struct {
		data []byte
		err  error
	}
	reads := make(chan readResult, 1)
	go func() { b, e := io.ReadAll(io.LimitReader(reader, (8<<20)+1)); reads <- readResult{b, e} }()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	sampleCtx, cancelSamples := context.WithCancel(context.Background())
	var samples []GPUSample
	var samplesMu sync.Mutex
	samplesDone := make(chan struct{})
	go func() {
		defer close(samplesDone)
		appendSample := func(sample GPUSample) {
			samplesMu.Lock()
			samples = append(samples, sample)
			samplesMu.Unlock()
		}
		for {
			sample := s.sample(sampleCtx, cmd.Process.Pid, started)
			if sampleCtx.Err() != nil {
				if sample.ProcessStatus != "interrupted" || sample.DeviceStatus == "observed" {
					appendSample(sample)
				}
				break
			}
			appendSample(sample)
			if s.queryBlocked.Load() {
				break
			}
			timer := time.NewTimer(s.opts.Interval)
			select {
			case <-sampleCtx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
		}
	}()
	var waitErr error
	reaped := false
	select {
	case waitErr = <-waited:
		reaped = true
	case <-ctx.Done():
		m.StopReason = stopReason(context.Cause(ctx))
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(s.opts.StopGrace)
		select {
		case waitErr = <-waited:
			reaped = true
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			select {
			case waitErr = <-waited:
				reaped = true
			case <-time.After(s.opts.ReapTimeout):
			}
		}
	}
	wall := time.Since(started).Seconds()
	m.WallSeconds = &wall
	// The original process is gone or has been killed; close writers inherited by
	// its group so they cannot retain the private result pipe indefinitely.
	groupErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	switch {
	case groupErr == nil:
		m.GroupStop = "sent"
	case errors.Is(groupErr, syscall.ESRCH):
		m.GroupStop = "not_needed"
	default:
		m.GroupStop = "failed"
	}
	cancelSamples()
	// A query can be inside its TERM grace when the worker exits. The command
	// may return directly to os.Exit, so send KILL before waiting for sampling.
	// The lock also prevents a canceled sampler from starting a new query here.
	s.queryMu.Lock()
	if s.queryPID != 0 {
		if err := syscall.Kill(-s.queryPID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			s.queryBlocked.Store(true)
		}
	}
	s.queryMu.Unlock()
	select {
	case <-samplesDone:
	case <-time.After(2*s.opts.StopGrace + 2*s.opts.ReapTimeout):
		m.MonitoringStatus = "interrupted"
		s.queryBlocked.Store(true)
	}
	samplesMu.Lock()
	m.Samples = append(m.Samples, samples...)
	samplesMu.Unlock()
	m.QueryCleanup = s.queryCleanup()
	if reaped && cmd.ProcessState == nil {
		reaped = false
	}
	if reaped {
		m.ReapStatus = "reaped"
		state := cmd.ProcessState
		if code := state.ExitCode(); code >= 0 {
			m.ChildOutcome = "exited"
			m.ExitCode = &code
		} else if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			m.ChildOutcome = "signaled"
			m.Signal = status.Signal().String()
		} else {
			m.ChildOutcome = "wait_failed"
		}
		if usage, ok := state.SysUsage().(*syscall.Rusage); ok && usage.Maxrss >= 0 {
			n := usage.Maxrss * 1024
			m.RSSBytes = &n
		}
	} else {
		m.ReapStatus = "unreaped"
		m.ChildOutcome = "wait_failed"
	}
	m.GroupStatus = "unknown"
	if groupErr == nil || errors.Is(groupErr, syscall.ESRCH) {
		deadline := time.Now().Add(s.opts.StopGrace)
		for {
			checkErr := syscall.Kill(-cmd.Process.Pid, 0)
			if errors.Is(checkErr, syscall.ESRCH) {
				m.GroupStatus = "cleared"
				break
			}
			if checkErr != nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	interrupted := m.MonitoringStatus == "interrupted" || !reaped || m.StopReason != "none"
	summarizeSamples(m, interrupted)
	var read readResult
	select {
	case read = <-reads:
	case <-time.After(s.opts.ReapTimeout):
		reader.Close()
		m.ResultStatus = "incomplete"
		return result, errors.New("worker result pipe did not close")
	}
	if read.err != nil || len(read.data) > 8<<20 || json.Unmarshal(read.data, &result) != nil {
		m.ResultStatus = "missing_or_invalid"
		return WorkerResult{}, errors.New("worker result unavailable")
	}
	if err := result.CheckRequest(req); err != nil {
		m.ResultStatus = "mismatch"
		return WorkerResult{}, err
	}
	m.ResultStatus = "available"
	m.LoadSeconds, m.LoadStatus = result.LoadSeconds, result.LoadStatus
	generation := result.Observation.GenerationDuration.Seconds()
	calls, completed, failed := result.Observation.GenerateCalls, result.Observation.GenerateCompleted, result.Observation.GenerateFailed
	m.GenerationCalls, m.GenerateCompleted, m.GenerateFailed = &calls, &completed, &failed
	if calls > 0 || result.FailurePhase == "" {
		m.GenerationSeconds, m.GenerationStatus = &generation, "complete"
		if failed > 0 || calls != completed || result.FailurePhase != "" {
			m.GenerationStatus = "partial"
		}
	}
	validation, tmValidation := result.Observation.Validation, result.Observation.TMValidation
	m.Validation, m.TMValidation = &validation, &tmValidation
	if !reaped {
		return result, errors.New("worker not reaped")
	}
	if m.StopReason != "none" {
		return result, ctx.Err()
	}
	return result, waitErr
}

func stopReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "wall_limit"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "supervisor_error"
}
