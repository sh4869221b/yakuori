package goinfer

import (
	"slices"
	"testing"
)

func TestWebGPUProcessEnv(t *testing.T) {
	inherited := []string{
		"DISPLAY=:0", "WAYLAND_DISPLAY=wayland-1", "WAYLAND_SOCKET=5",
		"XDG_RUNTIME_DIR=/run/user/1000", "EGL_PLATFORM=wayland", "GOINFER_ATTN_KEYS=0",
		"VK_DRIVER_FILES=/path/to/icd.json", "XDG_DATA_HOME=/path/to/data",
	}
	want := []string{
		"VK_DRIVER_FILES=/path/to/icd.json", "XDG_DATA_HOME=/path/to/data", "EGL_PLATFORM=surfaceless",
	}
	original := slices.Clone(inherited)
	env, changed := WebGPUProcessEnv(inherited)
	if !changed || !slices.Equal(env, want) || !slices.Equal(inherited, original) {
		t.Fatalf("prepared environment = %q, changed = %t, original = %q", env, changed, inherited)
	}
	env, changed = WebGPUProcessEnv(env)
	if changed || !slices.Equal(env, want) {
		t.Fatalf("prepared environment would re-exec again: %q, changed = %t", env, changed)
	}
}
