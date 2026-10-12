package models

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/inference/goinfer"
)

type testEngine struct {
	inference.Engine
	closed bool
}

func (e *testEngine) Close() error { e.closed = true; return nil }

func fixture(t *testing.T) (*Registry, string) {
	t.Helper()
	root := t.TempDir()
	r := New(config.Paths{Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"), State: filepath.Join(root, "state")})
	r.open = func(ctx context.Context, path string, options goinfer.Options) (inference.Engine, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(data)
		if options.ExpectedModelSHA256 == nil || digest != *options.ExpectedModelSHA256 {
			return nil, errors.New("digest mismatch")
		}
		if options.ComputeQuant != "int4" || options.ContextTokens != config.DefaultLimits().ContextTokens {
			t.Fatal("incorrect engine defaults")
		}
		return &testEngine{}, nil
	}
	source := filepath.Join(root, "source.gguf")
	write(t, source, []byte("managed model"))
	return r, source
}
func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
func read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func importOne(t *testing.T, r *Registry, source string) Entry {
	t.Helper()
	entry, err := r.Import(context.Background(), "one", source)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func TestManagedImportSelection(t *testing.T) {
	r, source := fixture(t)
	entry := importOne(t, r, source)
	if id, err := r.Selected(); err != nil || id != "" {
		t.Fatalf("import selected %q: %v", id, err)
	}
	write(t, source, []byte("changed source"))
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	got, err := r.Verify(context.Background(), "one")
	if err != nil || got != entry {
		t.Fatalf("verify after source deletion: %+v %v", got, err)
	}
	if err := r.Use(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	id, engine, err := r.OpenSelected(context.Background(), "cpu")
	if err != nil || id != "one" {
		t.Fatalf("open selected: %q %v", id, err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{r.paths.Data, r.paths.Cache, r.paths.Config, r.storage.ModelsDir, filepath.Dir(r.modelPath("one")), r.storage.SnapshotDir} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("directory %s: %v %v", path, info, err)
		}
	}
	for _, path := range []string{r.storage.RegistryFile, r.storage.ConfigFile, r.storage.RegistryLock, r.modelPath("one")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("file %s: %v %v", path, info, err)
		}
	}
}

func TestManagedReadOnlyMissing(t *testing.T) {
	r, _ := fixture(t)
	if _, err := r.Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("load missing: %v", err)
	}
	if _, err := r.Verify(context.Background(), "one"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verify missing: %v", err)
	}
	if id, err := r.Selected(); id != "" || err != nil {
		t.Fatalf("selected missing: %q %v", id, err)
	}
	for _, path := range []string{r.paths.Data, r.paths.Config, r.paths.Cache, r.paths.State} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read created %s", path)
		}
	}
}

func TestRegistryEngineBinding(t *testing.T) {
	for _, backend := range []string{"cpu", "cuda"} {
		t.Run(backend, func(t *testing.T) {
			r, source := fixture(t)
			entry := importOne(t, r, source)
			if err := r.Use(context.Background(), "one"); err != nil {
				t.Fatal(err)
			}
			open := r.open
			r.open = func(ctx context.Context, path string, options goinfer.Options) (inference.Engine, error) {
				if options.Backend != backend || options.SnapshotDir != r.storage.SnapshotDir || options.ExpectedModelSHA256 == nil {
					t.Fatal("unbound engine options")
				}
				write(t, path, []byte("replaced after Verify"))
				return open(ctx, path, options)
			}
			if _, engine, err := r.OpenSelected(context.Background(), backend); err == nil || engine != nil {
				t.Fatal("changed model was loaded")
			}
			entries, err := r.Load()
			if err != nil || entries["one"] != entry {
				t.Fatalf("registry changed: %v", err)
			}
		})
	}
}

func TestImportPublishPreservesConcurrentDestination(t *testing.T) {
	r, source := fixture(t)
	open := r.open
	destination := filepath.Join(r.storage.ModelsDir, "one")
	r.open = func(ctx context.Context, path string, options goinfer.Options) (inference.Engine, error) {
		engine, err := open(ctx, path, options)
		if err := os.Mkdir(destination, 0o700); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(destination, "owner-file"), []byte("external owner"))
		return engine, err
	}
	if _, err := r.Import(context.Background(), "one", source); !errors.Is(err, ErrCollision) {
		t.Fatalf("publish collision: %v", err)
	}
	if string(read(t, filepath.Join(destination, "owner-file"))) != "external owner" {
		t.Fatal("changed external destination")
	}
	if _, err := os.Stat(r.storage.RegistryFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed import wrote registry: %v", err)
	}
}

func TestImportRealLoaderRejectsInvalidModel(t *testing.T) {
	r, source := fixture(t)
	r.open = New(r.paths).open
	if _, err := r.Import(context.Background(), "one", source); err == nil {
		t.Fatal("real loader accepted invalid model")
	}
	children, err := os.ReadDir(r.storage.ModelsDir)
	if err != nil || len(children) != 0 {
		t.Fatalf("invalid import retained stage: %v %v", children, err)
	}
}
