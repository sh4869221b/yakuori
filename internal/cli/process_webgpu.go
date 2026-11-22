//go:build gpu && cgo && linux && amd64

package cli

import (
	"os"
	"syscall"

	"github.com/sh4869221b/yakuori/internal/inference/goinfer"
)

// PrepareProcess establishes the startup environment for explicit WebGPU commands.
func PrepareProcess(argv []string) error {
	if len(argv) < 2 || !usesWebGPU(argv[1:]) {
		return nil
	}
	env, changed := goinfer.WebGPUProcessEnv(os.Environ())
	if !changed {
		return nil
	}
	// Re-exec the current executable in place, preserving argv, stdio and PID.
	return syscall.Exec("/proc/self/exe", argv, env)
}

func usesWebGPU(args []string) bool {
	switch args[0] {
	case "translate", "localize":
		cmd, help, err := parseTranslation(args[0], args[1:])
		return err == nil && help == "" && cmd.backend == "webgpu"
	case "doctor":
		cmd, err := parseStorage(args)
		return err == nil && cmd.help == "" && cmd.checkModel && cmd.backend == "webgpu"
	default:
		return false
	}
}
