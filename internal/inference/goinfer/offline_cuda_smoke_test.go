//go:build cuda && offlinesmoke

package goinfer

// Offline CUDA matrix for issue-19 task 7. It runs the same pinned local Qwen
// GGUF used by the CPU matrices through the cuda-tagged build, so the CUDA
// build's CPU path, the real GPU path and the driver-free refusal are observed
// by the same ci/offline.py tracer. Nothing here downloads a model or a driver,
// and a CUDA refusal is never reported as a CPU success.
//
// YAKUORI_OFFLINE_MODEL is the pinned fixture path, YAKUORI_OFFLINE_BACKEND is
// cpu or cuda and YAKUORI_OFFLINE_CUDA_DRIVER is present (default; the real
// CUDA load must succeed) or absent (a driver-free host; the real CUDA load
// must be refused for a driver reason and the CPU control must still work).

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

	"github.com/townsendmerino/goinfer/decoder"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

// The fixture constants are the repository pin that ci/cpu-spike.Dockerfile and
// cpu_smoke_test.go also use; the local copy is never re-acquired here.
const (
	offlineCUDASHA256    = "1d9614638d18024d0fbb36575a15f1302a3adf044df10345688ec4f6e1c4ff32"
	offlineCUDASizeBytes = 491400064
	offlineCUDAText      = "Hello."
)

func offlineCUDAFixture(t *testing.T) string {
	t.Helper()
	path := os.Getenv("YAKUORI_OFFLINE_MODEL")
	if path == "" {
		t.Fatal("YAKUORI_OFFLINE_MODEL must name the pinned local Qwen GGUF fixture")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Size() != offlineCUDASizeBytes {
		t.Fatalf("fixture %s must be a regular %d byte file, got mode=%v size=%d", path, offlineCUDASizeBytes, info.Mode(), info.Size())
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	if err := errors.Join(copyErr, file.Close()); err != nil {
		t.Fatal(err)
	}
	if digest := hex.EncodeToString(hash.Sum(nil)); digest != offlineCUDASHA256 {
		t.Fatalf("fixture digest %s does not match the pinned %s", digest, offlineCUDASHA256)
	}
	return path
}

func offlineCUDABackend(t *testing.T) string {
	t.Helper()
	backend := os.Getenv("YAKUORI_OFFLINE_BACKEND")
	if backend != "cpu" && backend != "cuda" {
		t.Fatalf("YAKUORI_OFFLINE_BACKEND must be cpu or cuda, got %q", backend)
	}
	return backend
}

func offlineCUDAOpen(t *testing.T, ctx context.Context, model, backend string, limits config.Limits) *Engine {
	t.Helper()
	engine, err := OpenWithOptions(ctx, model, Options{Backend: backend, ComputeQuant: "int4", ContextTokens: limits.ContextTokens})
	if err != nil {
		t.Fatalf("real %s load: %v", backend, err)
	}
	return engine
}

func offlineCUDARequest(t *testing.T, engine *Engine, text string, maxTokens int) inference.GenerationRequest {
	t.Helper()
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, maxTokens, config.DefaultLimits().RequestTimeout)
	if err != nil {
		t.Fatal(err)
	}
	request, err := engine.NewRequest(context.Background(), prompt.Input{SourceLanguage: "English", TargetLanguage: "Japanese", Text: text}, policy)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestOfflineCUDAMatrix(t *testing.T) {
	model := offlineCUDAFixture(t)
	backend := offlineCUDABackend(t)
	driver := os.Getenv("YAKUORI_OFFLINE_CUDA_DRIVER")
	if driver == "" {
		driver = "present"
	}
	if driver != "present" && driver != "absent" {
		t.Fatalf("YAKUORI_OFFLINE_CUDA_DRIVER must be present or absent, got %q", driver)
	}
	limits := config.DefaultLimits()
	ctx := context.Background()
	var engine *Engine
	var info inference.ModelInfo
	closed := false

	t.Run("real_load", func(t *testing.T) {
		if backend == "cuda" && driver == "absent" {
			if cudaBackendPin == "" {
				t.Fatal("the cuda tag did not compile a backend pin; a not-built-in refusal is not driver evidence")
			}
			opened, err := OpenWithOptions(ctx, model, Options{Backend: "cuda", ComputeQuant: "int4", ContextTokens: limits.ContextTokens})
			if opened != nil || err == nil {
				t.Fatalf("a driver-free host served a CUDA engine: engine=%v err=%v", opened != nil, err)
			}
			if errors.Is(err, ErrUnsupportedBackend) || strings.Contains(err.Error(), "not built in") {
				t.Fatalf("the refusal was not-built-in, not driver initialisation: %v", err)
			}
			t.Logf("case=cuda_driver_absent kind=real reached=cuda_setup_rejection cuda_pin=%s requested_backend=cuda error=%q", cudaBackendPin, err)
			// Control: this binary and fixture still load on CPU, so the refusal
			// above is the missing driver and not an unusable input.
			engine = offlineCUDAOpen(t, ctx, model, "cpu", limits)
			info, err = engine.Info(ctx)
			if err != nil || info.Backend != "cpu" {
				t.Fatalf("cpu control load: info=%+v err=%v", info, err)
			}
			t.Logf("case=cuda_driver_absent_control kind=real reached=cpu_engine_loaded backend=%s", info.Backend)
			return
		}
		engine = offlineCUDAOpen(t, ctx, model, backend, limits)
		loaded, err := engine.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		info = loaded
		if info.RequestedBackend != backend || info.Backend != backend {
			t.Fatalf("requested %s produced requested=%s effective=%s", backend, info.RequestedBackend, info.Backend)
		}
		if fmt.Sprintf("%x", info.ModelSHA256) != offlineCUDASHA256 || cudaBackendPin == "" || info.CUDABackendPin != cudaBackendPin {
			t.Fatalf("engine identity is not the pinned cuda build: digest=%x cuda_pin=%q compiled=%q", info.ModelSHA256, info.CUDABackendPin, cudaBackendPin)
		}
		if info.ContextTokens <= 0 || info.ContextTokens > limits.ContextTokens {
			t.Fatalf("effective context %d is not within the configured %d", info.ContextTokens, limits.ContextTokens)
		}
		if backend == "cuda" && (!info.ResidentActive || info.ResidentContextCap <= 0 || info.ResidentDecline != "" || info.KVPrecision == "") {
			t.Fatalf("cuda load without an active residency: %+v", info)
		}
		t.Logf("case=real_load kind=real requested=%s effective=%s resident=%t cap=%d kv=%s context=%d cuda_pin=%s",
			info.RequestedBackend, info.Backend, info.ResidentActive, info.ResidentContextCap, info.KVPrecision, info.ContextTokens, info.CUDABackendPin)
	})
	if engine == nil {
		t.Fatal("the real load case produced no engine")
	}
	t.Cleanup(func() {
		if !closed {
			_ = engine.Close()
		}
	})

	t.Run("real_request_count_generate", func(t *testing.T) {
		request := offlineCUDARequest(t, engine, offlineCUDAText, 128)
		count, err := engine.CountTokens(ctx, request)
		if err != nil || count <= 0 || count != len(request.TokenIDs()) {
			t.Fatalf("count=%d ids=%d err=%v", count, len(request.TokenIDs()), err)
		}
		result, err := engine.Generate(ctx, request)
		if err != nil || result.Finish != inference.Stop || strings.TrimSpace(result.Text) == "" || result.PromptTokens != count {
			t.Fatalf("generate: finish=%s prompt=%d output=%d error=%v", result.Finish, result.PromptTokens, result.OutputTokens, err)
		}
		t.Logf("case=real_request_count_generate kind=real backend=%s prompt_tokens=%d output_tokens=%d finish=%s", info.Backend, count, result.OutputTokens, result.Finish)
	})

	t.Run("cancel_before_entry_then_reuse", func(t *testing.T) {
		request := offlineCUDARequest(t, engine, offlineCUDAText, 128)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		result, err := engine.Generate(cancelled, request)
		if !errors.Is(err, context.Canceled) || result.Finish != inference.Canceled || result.OutputTokens != 0 {
			t.Fatalf("cancelled generation: finish=%s output=%d error=%v", result.Finish, result.OutputTokens, err)
		}
		// The cancelled request must not leave the engine unusable or fall back
		// to a different backend: the same engine serves a real generation.
		result, err = engine.Generate(ctx, request)
		if err != nil || result.Finish != inference.Stop || strings.TrimSpace(result.Text) == "" {
			t.Fatalf("reuse after cancel: finish=%s output=%d error=%v", result.Finish, result.OutputTokens, err)
		}
		t.Logf("case=cancel_before_entry_then_reuse kind=real backend=%s reused_tokens=%d reused_finish=%s", info.Backend, result.OutputTokens, result.Finish)
	})

	t.Run("close_real_and_info_after_close", func(t *testing.T) {
		if err := engine.Close(); err != nil {
			t.Fatal(err)
		}
		closed = true
		if _, err := engine.Info(ctx); !errors.Is(err, ErrClosed) {
			t.Fatalf("Info after Close: %v", err)
		}
		t.Logf("case=close_real_and_info_after_close kind=real reached=engine_closed")
	})

	t.Run("residency_declined_injected", func(t *testing.T) {
		if cudaBackendPin == "" {
			t.Fatal("the residency gate requires a cuda-tagged build")
		}
		path := filepath.Join(t.TempDir(), "model.gguf")
		if err := os.WriteFile(path, []byte("residency probe bytes"), 0600); err != nil {
			t.Fatal(err)
		}
		declined := &stubModel{contextTokens: 32768, backend: "cuda", quant: "int4", residentDecline: "injected residency decline"}
		declined.start = func(context.Context, generationInput) tokenStream {
			t.Fatal("a residency decline entered the decoder")
			return tokenStream{}
		}
		loads := 0
		load := loaders{
			model: func(gotPath string, opts decoder.Options) (modelBackend, error) {
				loads++
				if opts.Backend != "cuda" || opts.Quant != "int4" || opts.ResidentContext != limits.ContextTokens {
					t.Fatalf("cuda request was rewritten before loading: %+v", opts)
				}
				return declined, nil
			},
			tokenizer: func(string) (requestTokenizer, error) {
				t.Fatal("a residency decline loaded a tokenizer instead of refusing")
				return nil, nil
			},
		}
		opened, err := open(ctx, modelConfig{path: path, backend: "cuda", quant: "int4", contextTokens: limits.ContextTokens, snapshotDir: t.TempDir()}, load)
		if opened != nil || !errors.Is(err, ErrCUDAResidency) || loads != 1 || declined.closes != 1 {
			t.Fatalf("injected residency decline: engine=%v loads=%d closes=%d error=%v", opened != nil, loads, declined.closes, err)
		}
		t.Logf("case=residency_declined_injected kind=injected reached=residency_gate error=%q", err)
	})

	t.Run("model_missing", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "absent.gguf")
		opened, err := OpenWithOptions(ctx, missing, Options{Backend: backend, ComputeQuant: "int4", ContextTokens: limits.ContextTokens})
		if opened != nil || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing model: engine=%v error=%v", opened != nil, err)
		}
		t.Logf("case=model_missing kind=real reached=snapshot_read error=%q", err)
	})

	t.Run("digest_mismatch", func(t *testing.T) {
		wrong := sha256.Sum256([]byte("not the pinned fixture"))
		opened, err := OpenWithOptions(ctx, model, Options{Backend: backend, ComputeQuant: "int4", ContextTokens: limits.ContextTokens, ExpectedModelSHA256: &wrong, SnapshotDir: t.TempDir()})
		if opened != nil || err == nil || !strings.Contains(err.Error(), "model SHA-256 mismatch") {
			t.Fatalf("digest mismatch: engine=%v error=%v", opened != nil, err)
		}
		t.Logf("case=digest_mismatch kind=real reached=digest_gate error=%q", err)
	})

	t.Run("invalid_gguf_loader_rejection", func(t *testing.T) {
		body := []byte(strings.Repeat("not-a-gguf-payload ", 200))
		path := filepath.Join(t.TempDir(), "invalid.gguf")
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(body)
		options := Options{Backend: "cpu", ComputeQuant: "int4", ContextTokens: limits.ContextTokens, SnapshotDir: t.TempDir()}
		pinned := options
		pinned.ExpectedModelSHA256 = &digest
		opened, err := OpenWithOptions(ctx, path, pinned)
		if opened != nil || err == nil {
			t.Fatalf("identity-matched invalid bytes were accepted: engine=%v error=%v", opened != nil, err)
		}
		// Control: the same bytes fail with the identity check disabled, so the
		// rejection is the real loader and not the digest comparison.
		control, controlErr := OpenWithOptions(ctx, path, options)
		if control != nil || controlErr == nil {
			t.Fatalf("unpinned control accepted invalid bytes: engine=%v error=%v", control != nil, controlErr)
		}
		t.Logf("case=invalid_gguf_loader_rejection kind=real_native reached=loader_rejection pinned=%q control=%q", err, controlErr)
	})
}
