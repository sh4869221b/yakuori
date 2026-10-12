package goinfer

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/townsendmerino/goinfer/decoder"
	"golang.org/x/sys/unix"
)

func TestSnapshotRejectsIdentityBeforeLoad(t *testing.T) {
	for _, missingCache := range []bool{false, true} {
		t.Run(map[bool]string{false: "digest mismatch", true: "missing cache"}[missingCache], func(t *testing.T) {
			config, load, model, _ := loadFixture(t)
			cache := t.TempDir()
			config.snapshotDir = cache
			config.expectedSHA256 = sha256.Sum256([]byte("registered model"))
			config.checkSHA256 = true
			if missingCache {
				config.snapshotDir = filepath.Join(cache, "missing")
			}
			calls := 0
			load.model = func(string, decoder.Options) (modelBackend, error) {
				calls++
				return model, nil
			}
			load.tokenizer = func(string) (requestTokenizer, error) {
				t.Fatal("rejected snapshot reached tokenizer")
				return nil, nil
			}
			model.start = func(context.Context, generationInput) tokenStream {
				t.Fatal("rejected snapshot generated tokens")
				return tokenStream{}
			}

			engine, err := open(context.Background(), config, load)

			if engine != nil || err == nil || calls != 0 || model.closes != 0 {
				t.Fatalf("engine=%v error=%v loads=%d closes=%d", engine, err, calls, model.closes)
			}
			if missingCache {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("error=%v", err)
				}
			} else if !strings.Contains(err.Error(), "SHA-256 mismatch") {
				t.Fatalf("error=%v", err)
			}
			entries, err := os.ReadDir(cache)
			if err != nil || len(entries) != 0 {
				t.Fatalf("cache entries=%v error=%v", entries, err)
			}
		})
	}
}

func TestSnapshotRejectsSpecialFiles(t *testing.T) {
	for _, kind := range []string{"fifo", "symlink", "device"} {
		t.Run(kind, func(t *testing.T) {
			config, load, _, _ := loadFixture(t)
			config.snapshotDir = t.TempDir()
			config.checkSHA256 = true
			config.expectedSHA256 = sha256.Sum256([]byte(fixtureModel))
			if err := os.Remove(config.path); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "fifo":
				if err := unix.Mkfifo(config.path, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := config.path + ".target"
				if err := os.WriteFile(target, []byte(fixtureModel), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, config.path); err != nil {
					t.Fatal(err)
				}
			case "device":
				config.path = "/dev/null"
			}
			load.model = func(string, decoder.Options) (modelBackend, error) {
				t.Fatal("special file reached decoder")
				return nil, nil
			}
			engine, err := open(context.Background(), config, load)
			if err == nil || engine != nil {
				t.Fatalf("special file opened: engine=%v error=%v", engine, err)
			}
			if kind == "symlink" && !errors.Is(err, unix.ELOOP) {
				t.Fatalf("managed symlink was followed: %v", err)
			}
			entries, err := os.ReadDir(config.snapshotDir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("snapshot entries=%v error=%v", entries, err)
			}
		})
	}
}

func TestOpenOptionsBindExpectedSnapshot(t *testing.T) {
	config, _, _, _ := loadFixture(t)
	cache := t.TempDir()
	expected := sha256.Sum256([]byte("registered model"))

	engine, err := OpenWithOptions(context.Background(), config.path, Options{
		Backend: "cpu", ComputeQuant: "int4", ContextTokens: 4096,
		ExpectedModelSHA256: &expected, SnapshotDir: cache,
	})

	if engine != nil || err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("engine=%v error=%v", engine, err)
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cache entries=%v error=%v", entries, err)
	}
}
