//go:build linux

package acceptance

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var pciAddress = regexp.MustCompile(`^[[:xdigit:]]{4}:[[:xdigit:]]{2}:[[:xdigit:]]{2}\.[0-7]$`)

type drmClient struct {
	vram   int64
	engine map[string]uint64
}

func readAMDProcess(pid int, pci string) (map[string]drmClient, error) {
	return readAMDClients(filepath.Join("/proc", strconv.Itoa(pid), "fdinfo"), pci)
}

func readAMDClients(dir, pci string) (map[string]drmClient, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	clients := make(map[string]drmClient)
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if errors.Is(err, fs.ErrNotExist) {
			// A worker can close an fd between the directory listing and read.
			continue
		}
		if err != nil {
			return nil, err
		}
		fields := make(map[string]string)
		for _, line := range strings.Split(string(data), "\n") {
			if key, value, ok := strings.Cut(line, ":"); ok && strings.HasPrefix(key, "drm-") {
				fields[key] = strings.TrimSpace(value)
			}
		}
		if fields["drm-driver"] != "amdgpu" || fields["drm-pdev"] != pci {
			continue
		}
		id := fields["drm-client-id"]
		if _, err := strconv.ParseUint(id, 10, 64); err != nil {
			return nil, errors.New("invalid AMD DRM client ID")
		}
		if _, seen := clients[id]; seen {
			continue
		}
		value := fields["drm-resident-vram"]
		if value == "" {
			value = fields["drm-memory-vram"] // amdgpu's older name for the same resident region
		}
		vram, err := drmVRAMBytes(value)
		if err != nil {
			return nil, err
		}
		engines := make(map[string]uint64)
		for _, key := range []string{"drm-engine-gfx", "drm-engine-compute", "drm-cycles-gfx", "drm-cycles-compute"} {
			fields := strings.Fields(fields[key])
			// drm-engine values are cumulative nanoseconds. drm-cycles values count compute cycles.
			if (strings.HasPrefix(key, "drm-engine-") && (len(fields) != 2 || fields[1] != "ns")) ||
				(strings.HasPrefix(key, "drm-cycles-") && len(fields) != 1) {
				continue
			}
			if n, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
				engines[key] = n
			}
		}
		clients[id] = drmClient{vram: vram, engine: engines}
	}
	return clients, nil
}

func drmVRAMBytes(value string) (int64, error) {
	fields := strings.Fields(value)
	factor := int64(1)
	switch {
	case len(fields) == 1:
	case len(fields) == 2 && fields[1] == "KiB":
		factor = 1 << 10
	case len(fields) == 2 && fields[1] == "MiB":
		factor = 1 << 20
	default:
		return 0, errors.New("invalid AMD DRM resident VRAM unit")
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || n < 0 || n > math.MaxInt64/factor {
		return 0, errors.New("invalid AMD DRM resident VRAM")
	}
	return n * factor, nil
}

func amdEngineActivity(before, after map[string]drmClient) (observed, advanced bool) {
	for id, current := range after {
		old, ok := before[id]
		if !ok {
			continue
		}
		for engine, now := range current.engine {
			prev, listed := old.engine[engine]
			// amdgpu omits unused drm-engine-* counters, so the first positive
			// reading for an existing DRM client can prove activity as well.
			if (!listed && !strings.HasPrefix(engine, "drm-engine-")) || now < prev {
				continue
			}
			observed, advanced = true, advanced || now > prev
		}
	}
	return observed, advanced
}

func readSysfsBytes(path string) *int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || n < 0 {
		return nil
	}
	return &n
}

func (s *Supervisor) captureAMD(ctx context.Context, env *SupervisorEnvironment) {
	dir := filepath.Join("/sys/bus/pci/devices", s.opts.GPUPCI)
	driver, err := os.Readlink(filepath.Join(dir, "driver"))
	vendor, _ := os.ReadFile(filepath.Join(dir, "vendor"))
	if err == nil && filepath.Base(driver) == "amdgpu" && strings.TrimSpace(string(vendor)) == "0x1002" {
		env.GPUMemoryBytes = readSysfsBytes(filepath.Join(dir, "mem_info_vram_total"))
		if env.GPUMemoryBytes != nil && *env.GPUMemoryBytes > 0 {
			env.GPUStatus = "complete"
		}
	}
	baseline := s.sampleAMD(ctx, 0, time.Now())
	env.Baseline = &baseline
	env.QueryCleanup = s.queryCleanup()
}

func (s *Supervisor) sampleAMD(ctx context.Context, pid int, start time.Time) GPUSample {
	started := time.Now()
	sample := GPUSample{AtSeconds: started.Sub(start).Seconds(), ProcessStatus: "absent", DeviceStatus: "error"}
	if ctx.Err() != nil {
		sample.ProcessStatus, sample.DeviceStatus = "interrupted", "not_sampled"
		return sample
	}
	if pid != 0 {
		clients, err := readAMDProcess(pid, s.opts.GPUPCI)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// A reaped worker no longer has an fdinfo directory.
		case err != nil:
			sample.ProcessStatus = "error"
		case len(clients) > 0:
			var bytes int64
			for _, client := range clients {
				if client.vram > math.MaxInt64-bytes {
					sample.ProcessStatus = "error"
					break
				}
				bytes += client.vram
			}
			if sample.ProcessStatus != "error" {
				sample.ProcessBytes, sample.ProcessStatus = &bytes, "observed"
			}
		}
	}
	sample.DeviceBytes = readSysfsBytes(filepath.Join("/sys/bus/pci/devices", s.opts.GPUPCI, "mem_info_vram_used"))
	if sample.DeviceBytes != nil {
		sample.DeviceStatus = "observed"
	}
	sample.QuerySeconds = time.Since(started).Seconds()
	return sample
}
