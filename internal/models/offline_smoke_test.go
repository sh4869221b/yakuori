//go:build offlinesmoke

package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/inference/goinfer"
)

// The fixture constants are the repository pin that ci/cpu-spike.Dockerfile and
// internal/inference/goinfer/cpu_smoke_test.go also use. The owned copy is taken
// from the preexisting local image: this matrix never acquires a model.
const (
	offlineSmokeSHA256    = "1d9614638d18024d0fbb36575a15f1302a3adf044df10345688ec4f6e1c4ff32"
	offlineSmokeSizeBytes = 491400064
	offlineSmokeRevision  = "ebb2015119c907b064c512bf053e945850b5875f"
)

func offlineSmokeFixture(t *testing.T) string {
	t.Helper()
	path := os.Getenv("YAKUORI_OFFLINE_MODEL")
	if path == "" {
		t.Fatal("YAKUORI_OFFLINE_MODEL must name the pinned local Qwen GGUF fixture")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Size() != offlineSmokeSizeBytes {
		t.Fatalf("fixture %s must be a regular %d byte file, got mode=%v size=%d", path, offlineSmokeSizeBytes, info.Mode(), info.Size())
	}
	if digest := offlineSmokeFileDigest(t, path); digest != offlineSmokeSHA256 {
		t.Fatalf("fixture digest %s does not match the pinned %s", digest, offlineSmokeSHA256)
	}
	return path
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

func offlineSmokeBackend(t *testing.T) string {
	t.Helper()
	backend := os.Getenv("YAKUORI_OFFLINE_BACKEND")
	if backend == "" {
		t.Fatal("YAKUORI_OFFLINE_BACKEND must name the requested backend")
	}
	return backend
}

func offlineSmokeRegistry(t *testing.T) *Registry {
	t.Helper()
	root := t.TempDir()
	return New(config.Paths{Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"), State: filepath.Join(root, "state")})
}

func offlineSmokeSmallSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.gguf")
	if err := os.WriteFile(path, []byte("small model bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// offlineSmokeCloser is an injected engine: Import only closes the engine its
// open seam returned, so the rest of the interface is never reached.
type offlineSmokeCloser struct {
	inference.Engine
	err error
}

func (c offlineSmokeCloser) Close() error { return c.err }

// TestOfflineRegistryMatrix runs the real registry over private XDG storage with
// the real goinfer loader. Each case logs the stage it reached; the injected
// failures use the existing open seam and are labelled injected, so none of them
// is reported as a native loader rejection.
func TestOfflineRegistryMatrix(t *testing.T) {
	model := offlineSmokeFixture(t)
	backend := offlineSmokeBackend(t)
	ctx := context.Background()

	t.Run("real_import_use_open_close", func(t *testing.T) {
		r := offlineSmokeRegistry(t)
		entry, err := r.Import(ctx, "qwen", model)
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if entry.SHA256 != offlineSmokeSHA256 || entry.SizeBytes != offlineSmokeSizeBytes {
			t.Fatalf("imported entry %+v does not match the pin", entry)
		}
		if selected, err := r.Selected(); err != nil || selected != "" {
			t.Fatalf("import changed the selection: %q %v", selected, err)
		}
		if err := r.Use(ctx, "qwen"); err != nil {
			t.Fatalf("use: %v", err)
		}
		if _, err := r.Verify(ctx, "qwen"); err != nil {
			t.Fatalf("verify: %v", err)
		}
		id, engine, err := r.OpenSelected(ctx, backend)
		if err != nil || engine == nil || id != "qwen" {
			t.Fatalf("open selected: id=%q engine=%v err=%v", id, engine != nil, err)
		}
		info, err := engine.Info(ctx)
		if err != nil {
			t.Fatalf("info: %v", err)
		}
		if fmt.Sprintf("%x", info.ModelSHA256) != offlineSmokeSHA256 || info.Backend != backend ||
			info.PolicySchema != inference.PolicySchemaV1 || info.ContextTokens <= 0 {
			t.Fatalf("engine info %+v does not match the pin and backend", info)
		}
		if err := engine.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		t.Logf("case=real_import_use_open_close kind=real reached=engine_closed model_sha256=%x backend=%s context_tokens=%d", info.ModelSHA256, info.Backend, info.ContextTokens)
	})

	t.Run("model_missing", func(t *testing.T) {
		r := offlineSmokeRegistry(t)
		id, engine, err := r.OpenSelected(ctx, backend)
		if err == nil || engine != nil || id != "" || !strings.Contains(err.Error(), "no model selected") {
			t.Fatalf("unselected registry: id=%q engine=%v err=%v", id, engine != nil, err)
		}
		if _, err := os.Lstat(filepath.Join(r.storage.ModelsDir, "qwen")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing model left a managed directory: %v", err)
		}
		t.Logf("case=model_missing kind=real reached=registry_setup error=%q", err)
	})

	t.Run("registered_digest_mismatch", func(t *testing.T) {
		r := offlineSmokeRegistry(t)
		entry, err := r.Import(ctx, "qwen", model)
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		if err := r.Use(ctx, "qwen"); err != nil {
			t.Fatalf("use: %v", err)
		}
		if err := os.WriteFile(r.modelPath("qwen"), []byte("corrupted managed bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Verify(ctx, "qwen"); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
			t.Fatalf("verify on changed bytes: %v", err)
		}
		id, engine, err := r.OpenSelected(ctx, backend)
		if err == nil || engine != nil || id != "" || !strings.Contains(err.Error(), "identity mismatch") {
			t.Fatalf("open changed model: id=%q engine=%v err=%v", id, engine != nil, err)
		}
		rejected := err
		entries, err := r.Load()
		if err != nil || entries["qwen"] != entry {
			t.Fatalf("registry was changed: %+v %v", entries, err)
		}
		if selected, err := r.Selected(); err != nil || selected != "qwen" {
			t.Fatalf("selection was changed: %q %v", selected, err)
		}
		t.Logf("case=registered_digest_mismatch kind=real reached=registry_verify error=%q", rejected)
	})

	t.Run("invalid_gguf_identity_matched", func(t *testing.T) {
		r := offlineSmokeRegistry(t)
		invalid := filepath.Join(t.TempDir(), "invalid.gguf")
		if err := os.WriteFile(invalid, []byte(strings.Repeat("not-a-gguf-payload ", 200)), 0o600); err != nil {
			t.Fatal(err)
		}
		// The registered entry is the digest of these bytes, so the identity check
		// cannot be what rejects them: the real loader is.
		_, err := r.Import(ctx, "bad", invalid)
		if err == nil || !strings.Contains(err.Error(), "validate imported model") {
			t.Fatalf("real loader accepted invalid bytes: %v", err)
		}
		rejected := err
		if _, err := r.Load(); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed import wrote a registry: %v", err)
		}
		children, err := os.ReadDir(r.storage.ModelsDir)
		if err != nil || len(children) != 0 {
			t.Fatalf("failed import left %v behind: %v", children, err)
		}
		t.Logf("case=invalid_gguf_identity_matched kind=real_native reached=loader_rejection error=%q", rejected)
	})

	t.Run("injected_open_error", func(t *testing.T) {
		r := offlineSmokeRegistry(t)
		labelled := errors.New("injected loader failure")
		r.open = func(context.Context, string, goinfer.Options) (inference.Engine, error) { return nil, labelled }
		_, err := r.Import(ctx, "one", offlineSmokeSmallSource(t))
		if !errors.Is(err, labelled) {
			t.Fatalf("injected loader error was not surfaced: %v", err)
		}
		if children, listErr := os.ReadDir(r.storage.ModelsDir); listErr != nil || len(children) != 0 {
			t.Fatalf("failed import left %v behind: %v", children, listErr)
		}
		t.Logf("case=injected_open_error kind=injected reached=import_validation error=%q", err)
	})

	t.Run("injected_close_error", func(t *testing.T) {
		r := offlineSmokeRegistry(t)
		labelled := errors.New("injected close failure")
		r.open = func(context.Context, string, goinfer.Options) (inference.Engine, error) {
			return offlineSmokeCloser{err: labelled}, nil
		}
		_, err := r.Import(ctx, "one", offlineSmokeSmallSource(t))
		if !errors.Is(err, labelled) {
			t.Fatalf("injected close error was not surfaced: %v", err)
		}
		t.Logf("case=injected_close_error kind=injected reached=import_close error=%q", err)
	})
}
