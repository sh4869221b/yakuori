package goinfer

import "strings"

// WebGPUProcessEnv gives a new process a headless environment for the native
// Vulkan/GL adapter probes and fixes the pinned WebGPU attention kernel policy
// before the dependency reads it at package initialization.
func WebGPUProcessEnv(env []string) ([]string, bool) {
	next := make([]string, 0, len(env)+1)
	hasSurfaceless := false
	changed := false
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		switch name {
		case "DISPLAY", "WAYLAND_DISPLAY", "WAYLAND_SOCKET", "XDG_RUNTIME_DIR", "GOINFER_ATTN_KEYS":
			changed = true
			continue
		case "EGL_PLATFORM":
			if value != "surfaceless" {
				changed = true
				continue
			}
			hasSurfaceless = true
		}
		next = append(next, entry)
	}
	if !hasSurfaceless {
		next = append(next, "EGL_PLATFORM=surfaceless")
		changed = true
	}
	return next, changed
}
