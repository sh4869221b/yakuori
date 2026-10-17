//go:build offlinesmoke || modelsmoke

package cli

import "os"

// Keep runtime scratch/cache roots owned by the caller without inheriting its
// HOME, XDG state, model selection or unrelated environment.
func smokeRuntimeEnv() []string {
	env := []string{"PATH=" + os.Getenv("PATH")}
	for _, key := range []string{"TMPDIR", "NODE_COMPILE_CACHE", "BUN_RUNTIME_TRANSPILER_CACHE_PATH", "GOCACHE", "GOPATH"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}
