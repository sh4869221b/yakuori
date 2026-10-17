//go:build modelsmoke

package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func (l *pullSmokeLayout) childEnv(caFile string) []string {
	env := append(smokeRuntimeEnv(), "SSL_CERT_FILE="+caFile)
	for key, value := range l.env {
		env = append(env, key+"="+value)
	}
	sort.Strings(env)
	return env
}

func TestPullSmokeLayoutPreservesOwnedRuntimeEnv(t *testing.T) {
	// Given
	for _, key := range []string{"TMPDIR", "NODE_COMPILE_CACHE", "BUN_RUNTIME_TRANSPILER_CACHE_PATH", "GOCACHE", "GOPATH"} {
		t.Setenv(key, filepath.Join(t.TempDir(), key))
	}
	t.Setenv("PULL_SMOKE_UNRELATED", "secret")
	layout := newPullSmokeLayout(t)

	// When
	values := make(map[string]string)
	for _, entry := range layout.childEnv("fixture-ca.pem") {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}

	// Then
	for _, key := range []string{"TMPDIR", "NODE_COMPILE_CACHE", "BUN_RUNTIME_TRANSPILER_CACHE_PATH", "GOCACHE", "GOPATH"} {
		if values[key] != os.Getenv(key) {
			t.Fatalf("%s = %q, want %q", key, values[key], os.Getenv(key))
		}
	}
	if values["HOME"] != layout.root || values["XDG_DATA_HOME"] != layout.env["XDG_DATA_HOME"] || values["SSL_CERT_FILE"] != "fixture-ca.pem" {
		t.Fatalf("fixture environment = %v", values)
	}
	if _, ok := values["PULL_SMOKE_UNRELATED"]; ok {
		t.Fatal("unrelated caller environment leaked into the child")
	}
}
