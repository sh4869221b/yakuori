package models

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/inference/goinfer"
)

func TestManagedRegistryFailures(t *testing.T) {
	for _, scenario := range []string{"duplicate", "existing directory", "corruption", "missing copy", "invalid ID", "load failure", "copy failure", "lock busy", "registry save failure", "config save failure"} {
		t.Run(scenario, func(t *testing.T) {
			r, source := fixture(t)
			importOne(t, r, source)
			if err := r.Use(context.Background(), "one"); err != nil {
				t.Fatal(err)
			}
			oldRegistry := read(t, r.storage.RegistryFile)
			oldConfig := read(t, r.storage.ConfigFile)
			oldModel := read(t, r.modelPath("one"))
			oldSource := read(t, source)
			var err error
			switch scenario {
			case "duplicate":
				_, err = r.Import(context.Background(), "one", source)
			case "existing directory":
				if err := os.Mkdir(filepath.Join(r.storage.ModelsDir, "two"), 0o700); err != nil {
					t.Fatal(err)
				}
				_, err = r.Import(context.Background(), "two", source)
			case "corruption":
				write(t, r.modelPath("one"), []byte("bad model"))
				oldModel = []byte("bad model")
				err = r.Use(context.Background(), "one")
			case "missing copy":
				if err := os.Remove(r.modelPath("one")); err != nil {
					t.Fatal(err)
				}
				_, err = r.Verify(context.Background(), "one")
			case "invalid ID":
				_, err = r.Import(context.Background(), "../escape", source)
			case "load failure":
				r.open = func(context.Context, string, goinfer.Options) (inference.Engine, error) {
					return nil, errors.New("unsupported model")
				}
				_, err = r.Import(context.Background(), "two", source)
			case "copy failure":
				_, err = r.Import(context.Background(), "two", filepath.Join(filepath.Dir(source), "missing"))
			case "lock busy":
				lock, lockErr := r.lock()
				if lockErr != nil {
					t.Fatal(lockErr)
				}
				defer lock.Close()
				err = r.Use(context.Background(), "one")
				if !errors.Is(err, ErrBusy) {
					t.Fatalf("busy: %v", err)
				}
			case "registry save failure":
				if os.Geteuid() == 0 {
					t.Skip("permission failure requires non-root")
				}
				open := r.open
				r.open = func(ctx context.Context, path string, options goinfer.Options) (inference.Engine, error) {
					engine, err := open(ctx, path, options)
					if err := os.Chmod(r.paths.Data, 0o500); err != nil {
						t.Fatal(err)
					}
					return engine, err
				}
				defer os.Chmod(r.paths.Data, 0o700)
				_, err = r.Import(context.Background(), "two", source)
			case "config save failure":
				if os.Geteuid() == 0 {
					t.Skip("permission failure requires non-root")
				}
				if err := os.Chmod(r.paths.Config, 0o500); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(r.paths.Config, 0o700)
				err = r.Use(context.Background(), "one")
			}
			if err == nil {
				t.Fatal("operation unexpectedly succeeded")
			}
			if string(read(t, r.storage.RegistryFile)) != string(oldRegistry) || string(read(t, r.storage.ConfigFile)) != string(oldConfig) || string(read(t, source)) != string(oldSource) {
				t.Fatal("failure changed existing registry/config/source")
			}
			if scenario != "missing copy" && string(read(t, r.modelPath("one"))) != string(oldModel) {
				t.Fatal("failure changed existing model")
			}
			children, err := os.ReadDir(r.storage.ModelsDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, child := range children {
				if child.Name() != "one" && !(scenario == "existing directory" && child.Name() == "two") {
					t.Fatalf("unowned or staged copy retained: %s", child.Name())
				}
			}
		})
	}
}

func TestManagedUnsafeStorage(t *testing.T) {
	for _, scenario := range []string{"public directory", "symlink directory", "symlink model", "public model", "invalid registry", "symlink registry", "invalid config"} {
		t.Run(scenario, func(t *testing.T) {
			r, source := fixture(t)
			importOne(t, r, source)
			switch scenario {
			case "public directory":
				if err := os.Chmod(r.storage.ModelsDir, 0o755); err != nil {
					t.Fatal(err)
				}
			case "symlink directory":
				original := r.storage.ModelsDir + "-original"
				if err := os.Rename(r.storage.ModelsDir, original); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(original, r.storage.ModelsDir); err != nil {
					t.Fatal(err)
				}
			case "symlink model":
				if err := os.Remove(r.modelPath("one")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(source, r.modelPath("one")); err != nil {
					t.Fatal(err)
				}
			case "public model":
				if err := os.Chmod(r.modelPath("one"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "invalid registry":
				write(t, r.storage.RegistryFile, []byte(`{"schema":2,"models":{}}`))
			case "symlink registry":
				if err := os.Remove(r.storage.RegistryFile); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(source, r.storage.RegistryFile); err != nil {
					t.Fatal(err)
				}
			case "invalid config":
				if err := privateDir(r.paths.Config, true); err != nil {
					t.Fatal(err)
				}
				write(t, r.storage.ConfigFile, []byte(`{"schema":2}`))
				if err := r.Use(context.Background(), "one"); err == nil {
					t.Fatal("repaired corrupt config")
				}
				return
			}
			if _, err := r.Verify(context.Background(), "one"); err == nil {
				t.Fatal("accepted unsafe storage")
			}
		})
	}
}

func TestRegistrySchema(t *testing.T) {
	for _, data := range []string{`{"schema":1}`, `{"schema":1,"models":null}`, `{"schema":1,"models":{},"extra":true}`, `{"schema":1,"models":{}} {}`, `{"schema":1,"models":{"../one":{"sha256":"abc","size_bytes":1}}}`, `{"schema":1,"models":{"one":{"sha256":"abc","size_bytes":1}}}`, `{"schema":1,"models":{"one":{"sha256":"0000000000000000000000000000000000000000000000000000000000000000","size_bytes":0}}}`} {
		t.Run(data, func(t *testing.T) {
			r, source := fixture(t)
			importOne(t, r, source)
			write(t, r.storage.RegistryFile, []byte(data))
			if _, err := r.Load(); err == nil {
				t.Fatal("accepted invalid registry")
			}
		})
	}
}
