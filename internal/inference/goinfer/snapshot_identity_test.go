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
