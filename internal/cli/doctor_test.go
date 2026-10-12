package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/inference/goinfer"
)

func TestDoctorStorage(t *testing.T) {
	t.Run("fresh namespace", func(t *testing.T) {
		root := t.TempDir()
		paths := config.Paths{Config: filepath.Join(root, "c"), Data: filepath.Join(root, "d"), Cache: filepath.Join(root, "k"), State: filepath.Join(root, "s")}
		code, out, diag := storageRun([]string{"doctor"}, paths)
		if code != ExitOK || diag != "" || !strings.Contains(out, "[missing]") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, diag)
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("writes=%v err=%v", entries, err)
		}
	})
	t.Run("lightweight and private", func(t *testing.T) {
		paths := modelFixture(t)
		storage := config.ResolveStoragePaths(paths)
		writeFixture(t, storage.TMFile, "private-text invalid SQLite")
		writeFixture(t, filepath.Join(storage.ModelsDir, "test-model", "model.gguf"), "private-text corrupted model")
		code, out, diag := storageRun([]string{"doctor"}, paths)
		if code != ExitOK || diag != "" || strings.Contains(out, "private-text") || !strings.Contains(out, "model file test-model:") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, diag)
		}
		if _, err := os.Stat(storage.RegistryLock); !os.IsNotExist(err) {
			t.Fatalf("doctor created lock: %v", err)
		}
		if err := os.Chmod(storage.TMFile, 0o644); err != nil {
			t.Fatal(err)
		}
		code, out, diag = storageRun([]string{"doctor"}, paths)
		if code != ExitFailure || !strings.Contains(out, "insecure mode or type") || !strings.Contains(diag, "TM file") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out, diag)
		}
		if err := os.Chmod(storage.TMFile, 0o200); err != nil {
			t.Fatal(err)
		}
		code, _, diag = storageRun([]string{"doctor"}, paths)
		if code != ExitFailure || !strings.Contains(diag, "owner read access") {
			t.Fatalf("code=%d stderr=%q", code, diag)
		}
	})
	for _, target := range []string{"config", "registry", "symlink"} {
		t.Run(target, func(t *testing.T) {
			paths := modelFixture(t)
			storage := config.ResolveStoragePaths(paths)
			file := storage.ConfigFile
			if target == "registry" {
				file = storage.RegistryFile
			}
			if target == "symlink" {
				if err := os.Symlink(storage.RegistryFile, file); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFixture(t, file, `{"private-text":"translation-prompt"}`)
			}
			code, out, diag := storageRun([]string{"doctor"}, paths)
			if code != ExitFailure || diag == "" || strings.Contains(out+diag, "private-text") || strings.Contains(out+diag, "translation-prompt") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out, diag)
			}
		})
	}
}

type doctorEngine struct {
	inference.Engine
	info              inference.ModelInfo
	infoErr, closeErr error
	closed            bool
}

func (e *doctorEngine) Info(context.Context) (inference.ModelInfo, error) { return e.info, e.infoErr }
func (e *doctorEngine) Close() error                                      { e.closed = true; return e.closeErr }

func TestDoctorModelCheck(t *testing.T) {
	for _, backend := range []string{"cpu", "cuda"} {
		t.Run(backend, func(t *testing.T) {
			engine := &doctorEngine{info: inference.ModelInfo{
				RequestedBackend: backend, Backend: backend, ModelSHA256: [32]byte{1}, Tokenizer: inference.TokenizerIdentity{ModelSHA256: [32]byte{2}, BackendPin: "tokenizer-pin"},
				ModelContextTokens: 8192, ConfiguredContextTokens: 4096, ContextTokens: 2048, ComputeQuant: "int4", BackendPin: "backend-pin", CUDABackendPin: "cuda-pin", ResidentActive: backend == "cuda", ResidentContextCap: 2048,
				Template: inference.TemplateIdentity{Source: "private-text"}, DisabledThinkingPrefix: "translation-prompt",
			}}
			out, err := checkModel(context.Background(), backend, func(_ context.Context, requested string) (string, inference.Engine, error) {
				if requested != backend {
					t.Fatalf("requested=%s", requested)
				}
				return "test-model", engine, nil
			})
			if err != nil || !engine.closed || strings.Contains(out, "private-text") || strings.Contains(out, "translation-prompt") {
				t.Fatalf("out=%q err=%v closed=%t", out, err, engine.closed)
			}
			for _, want := range []string{"model_id: test-model", "model_sha256: 01", "tokenizer_sha256: 02", "requested_backend: " + backend, "effective_backend: " + backend, "model_context_tokens: 8192", "configured_context_tokens: 4096", "effective_context_tokens: 2048", "backend_pin: backend-pin", "cuda_backend_pin: cuda-pin", "tokenizer_backend_pin: tokenizer-pin", "compute_quant: int4"} {
				if !strings.Contains(out, want) {
					t.Fatalf("missing %q: %q", want, out)
				}
			}
		})
	}
	for _, stage := range []string{"info", "close"} {
		engine := &doctorEngine{}
		if stage == "info" {
			engine.infoErr = errors.New("private-text")
		} else {
			engine.closeErr = errors.New("private-text")
		}
		out, err := checkModel(context.Background(), "cpu", func(context.Context, string) (string, inference.Engine, error) { return "test-model", engine, nil })
		if err == nil || out != "" || !engine.closed || strings.Contains(err.Error(), "private-text") {
			t.Fatalf("out=%q err=%v closed=%t", out, err, engine.closed)
		}
	}
	for _, failure := range []error{fmt.Errorf("%w: CUDA backend not built in", goinfer.ErrUnsupportedBackend), fmt.Errorf("%w: active=false cap=0 decline=cuda: libcuda.so.1 unavailable", goinfer.ErrCUDAResidency), errors.New("load model tokenizer: private-text")} {
		out, err := checkModel(context.Background(), "cuda", func(context.Context, string) (string, inference.Engine, error) { return "", nil, failure })
		if err == nil || out != "" || strings.Contains(err.Error(), "private-text") || !strings.Contains(err.Error(), "requested_backend=cuda") {
			t.Fatalf("out=%q err=%v", out, err)
		}
		if strings.Contains(failure.Error(), "libcuda") && !strings.Contains(err.Error(), "libcuda.so.1") {
			t.Fatalf("lost driver reason: %v", err)
		}
	}
	t.Run("missing cache and corrupt model", func(t *testing.T) {
		paths := modelFixture(t)
		writeFixture(t, filepath.Join(paths.Config, "config.json"), `{"schema":1,"selected_model":"test-model"}`)
		if err := os.RemoveAll(paths.Cache); err != nil {
			t.Fatal(err)
		}
		code, _, diag := storageRun([]string{"doctor", "--check-model"}, paths)
		if code != ExitFailure || diag == "" {
			t.Fatalf("code=%d stderr=%q", code, diag)
		}
		if _, err := os.Stat(paths.Cache); !os.IsNotExist(err) {
			t.Fatalf("cache created: %v", err)
		}
		writeFixture(t, filepath.Join(paths.Data, "models", "test-model", "model.gguf"), "private-text changed")
		code, _, diag = storageRun([]string{"doctor", "--check-model"}, paths)
		if code != ExitFailure || !strings.Contains(diag, "identity mismatch") || strings.Contains(diag, "private-text") {
			t.Fatalf("code=%d stderr=%q", code, diag)
		}
	})
	var diag bytes.Buffer
	if code := storageOutput(brokenWriter{short: true}, &diag, "metadata", nil); code != ExitFailure {
		t.Fatalf("writer code=%d", code)
	}
}
