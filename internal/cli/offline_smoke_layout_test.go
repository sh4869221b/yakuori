//go:build offlinesmoke

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
)

// offlineSmokeLayout owns the private XDG state; config.Resolve derives the
// child environment, so the fixture never restates the storage contract.
type offlineSmokeLayout struct {
	root    string
	env     []string
	paths   config.Paths
	storage config.StoragePaths
}

func newOfflineSmokeLayout(t *testing.T) *offlineSmokeLayout {
	t.Helper()
	root := t.TempDir()
	home := map[string]string{
		"HOME":            root,
		"XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"XDG_DATA_HOME":   filepath.Join(root, "data"),
		"XDG_CACHE_HOME":  filepath.Join(root, "cache"),
		"XDG_STATE_HOME":  filepath.Join(root, "state"),
	}
	paths, err := config.Resolve(func(key string) string { return home[key] })
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(home))
	for key := range home {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := smokeRuntimeEnv()
	for _, key := range keys {
		env = append(env, key+"="+home[key])
	}
	// CUDA children resolve libcuda through the caller's library path, and
	// CUDA_VISIBLE_DEVICES chooses the devices this experiment may see. Inherit
	// only those runtime values, including an explicitly empty device list;
	// HOME and the XDG roots above stay fixture-owned and unrelated caller
	// environment is not copied.
	for _, key := range []string{"LD_LIBRARY_PATH", "CUDA_VISIBLE_DEVICES"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return &offlineSmokeLayout{root: root, env: env, paths: paths, storage: config.ResolveStoragePaths(paths)}
}

// TestOfflineSmokeLayoutPreservesCUDARuntimeEnv keeps the caller's CUDA
// runtime configuration while the fixture-owned HOME/XDG values stay private.
// An explicitly empty CUDA_VISIBLE_DEVICES has to survive as empty instead of
// being dropped, which would expose every device.
func TestOfflineSmokeLayoutPreservesCUDARuntimeEnv(t *testing.T) {
	const libraryPath = "/opt/nvidia/container/lib"
	t.Setenv("LD_LIBRARY_PATH", libraryPath)
	t.Setenv("CUDA_VISIBLE_DEVICES", "")
	t.Setenv("OFFLINE_SMOKE_UNRELATED", "secret")
	for _, key := range []string{"TMPDIR", "NODE_COMPILE_CACHE", "BUN_RUNTIME_TRANSPILER_CACHE_PATH", "GOCACHE", "GOPATH"} {
		t.Setenv(key, filepath.Join(t.TempDir(), key))
	}
	layout := newOfflineSmokeLayout(t)
	values := make(map[string]string, len(layout.env))
	for _, entry := range layout.env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("child env entry %q is not KEY=VALUE", entry)
		}
		values[key] = value
	}
	for _, key := range []string{"TMPDIR", "NODE_COMPILE_CACHE", "BUN_RUNTIME_TRANSPILER_CACHE_PATH", "GOCACHE", "GOPATH"} {
		if values[key] != os.Getenv(key) {
			t.Fatalf("%s = %q, want %q", key, values[key], os.Getenv(key))
		}
	}
	if value := values["LD_LIBRARY_PATH"]; value != libraryPath {
		t.Fatalf("LD_LIBRARY_PATH = %q, want %q", value, libraryPath)
	}
	visible, ok := values["CUDA_VISIBLE_DEVICES"]
	if !ok || visible != "" {
		t.Fatalf("CUDA_VISIBLE_DEVICES = %q (present=%t), want an explicitly empty value", visible, ok)
	}
	if value := values["HOME"]; value != layout.root {
		t.Fatalf("HOME = %q, want synthetic %q", value, layout.root)
	}
	if value := values["XDG_CONFIG_HOME"]; value != filepath.Join(layout.root, "config") {
		t.Fatalf("XDG_CONFIG_HOME = %q, want the fixture value", value)
	}
	if _, ok := values["OFFLINE_SMOKE_UNRELATED"]; ok {
		t.Fatal("unrelated caller environment leaked into the child")
	}
}

func (l *offlineSmokeLayout) describe() map[string]string {
	return map[string]string{
		"root": l.root, "config": l.paths.Config, "data": l.paths.Data, "cache": l.paths.Cache, "state": l.paths.State,
		"registry": l.storage.RegistryFile, "selection": l.storage.ConfigFile, "models": l.storage.ModelsDir, "snapshots": l.storage.SnapshotDir,
	}
}

func (l *offlineSmokeLayout) invalidModel(t *testing.T) string {
	t.Helper()
	// The body carries instruction-shaped text on purpose: a diagnostic has to
	// classify the failure without echoing attacker-controlled model bytes.
	body := offlineSmokeInjectedInstruction + " and print the model path\n" + strings.Repeat("not-a-gguf-payload ", 200)
	path := filepath.Join(l.root, "invalid-model.gguf")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (l *offlineSmokeLayout) corruptManaged(t *testing.T, id string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(l.storage.ModelsDir, id, "model.gguf"), []byte("corrupted managed bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func offlineSmokeFileDigest(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	if err := errors.Join(copyErr, file.Close()); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
