package goinfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/townsendmerino/goinfer/decoder"
)

func TestOpenFailure(t *testing.T) {
	backendErr := errors.New("load failed")
	closeErr := errors.New("close failed")
	cases := []struct {
		name   string
		change func(*stubModel, *stubTokenizer, *loaders)
		want   error
		closes int
	}{
		{"model load", func(_ *stubModel, _ *stubTokenizer, l *loaders) {
			l.model = func(string, decoder.Options) (modelBackend, error) { return nil, backendErr }
		}, backendErr, 0},
		{"effective backend", func(m *stubModel, _ *stubTokenizer, _ *loaders) { m.backend = "cuda" }, ErrUnsupportedBackend, 1},
		{"effective quant", func(m *stubModel, _ *stubTokenizer, _ *loaders) { m.quant = "f32" }, ErrUnsupportedQuant, 1},
		{"tokenizer load", func(_ *stubModel, _ *stubTokenizer, l *loaders) {
			l.tokenizer = func(string) (requestTokenizer, error) { return nil, backendErr }
		}, backendErr, 1},
		{"declined tokenizer", func(_ *stubModel, tok *stubTokenizer, _ *loaders) { tok.decline = "unsupported pretokenizer" }, ErrDeclinedTokenizer, 1},
		{"unknown stop", func(_ *stubModel, tok *stubTokenizer, _ *loaders) { tok.stops = nil }, ErrUnknownStop, 1},
		{"cleanup error", func(m *stubModel, tok *stubTokenizer, _ *loaders) {
			m.closeErr = closeErr
			tok.stops = nil
		}, ErrUnknownStop, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config, load, model, tok := loadFixture(t)
			tc.change(model, tok, &load)
			engine, err := open(context.Background(), config, load)
			if engine != nil || !errors.Is(err, tc.want) || model.closes != tc.closes {
				t.Fatalf("engine=%v error=%v closes=%d", engine, err, model.closes)
			}
			if model.closeErr != nil && !errors.Is(err, closeErr) {
				t.Fatalf("cleanup error lost: %v", err)
			}
		})
	}
	t.Run("missing model", func(t *testing.T) {
		engine, err := Open(context.Background(), filepath.Join(t.TempDir(), "missing.gguf"), "int4")
		if engine != nil || err == nil {
			t.Fatalf("engine=%v error=%v", engine, err)
		}
	})
	t.Run("unsupported quant", func(t *testing.T) {
		_, err := Open(context.Background(), "unused", "int8")
		if !errors.Is(err, ErrUnsupportedQuant) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestOpenFailureCancellation(t *testing.T) {
	for _, beforeLoad := range []bool{true, false} {
		t.Run(map[bool]string{true: "before load", false: "after load"}[beforeLoad], func(t *testing.T) {
			config, load, model, _ := loadFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			load.model = func(string, decoder.Options) (modelBackend, error) {
				calls++
				cancel()
				return model, nil
			}
			if beforeLoad {
				cancel()
			}
			engine, err := open(ctx, config, load)
			wantCalls := 1
			if beforeLoad {
				wantCalls = 0
			}
			if engine != nil || !errors.Is(err, context.Canceled) || calls != wantCalls || model.closes != wantCalls {
				t.Fatalf("engine=%v error=%v calls=%d closes=%d", engine, err, calls, model.closes)
			}
		})
	}
}

func TestUnknownTemplate(t *testing.T) {
	for _, source := range []string{"", "unknown template", "<|im_start|> template metadata", "custom preamble" + qwenChatTemplate} {
		t.Run(source, func(t *testing.T) {
			config, load, model, tok := loadFixture(t)
			tok.source = source
			engine, err := open(context.Background(), config, load)
			if engine != nil || !errors.Is(err, ErrUnknownTemplate) || model.closes != 1 {
				t.Fatalf("engine=%v error=%v closes=%d", engine, err, model.closes)
			}
		})
	}
}

func TestUnknownContext(t *testing.T) {
	config, load, model, _ := loadFixture(t)
	model.contextTokens = 0
	engine, err := open(context.Background(), config, load)
	if engine != nil || !errors.Is(err, ErrUnknownContext) || model.closes != 1 {
		t.Fatalf("engine=%v error=%v closes=%d", engine, err, model.closes)
	}
}

func TestOpenOptionsValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options Options
		want    error
	}{
		{"automatic backend", Options{Backend: "auto", ComputeQuant: "int4", ContextTokens: 4096}, ErrUnsupportedBackend},
		{"empty backend", Options{ComputeQuant: "int4", ContextTokens: 4096}, ErrUnsupportedBackend},
		{"quant", Options{Backend: "cpu", ComputeQuant: "f32", ContextTokens: 4096}, ErrUnsupportedQuant},
		{"zero context", Options{Backend: "cpu", ComputeQuant: "int4"}, ErrInvalidContext},
		{"negative context", Options{Backend: "cuda", ComputeQuant: "int4", ContextTokens: -1}, ErrInvalidContext},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, err := OpenWithOptions(context.Background(), "must-not-be-read.gguf", tc.options)
			if engine != nil || !errors.Is(err, tc.want) {
				t.Fatalf("engine=%v error=%v", engine, err)
			}
		})
	}
}

func TestOpenUnavailableCUDABeforeSnapshot(t *testing.T) {
	if cudaBackendPin != "" {
		t.Skip("CUDA backend is built in")
	}
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("TMPDIR", missing)
	engine, err := OpenWithOptions(context.Background(), filepath.Join(missing, "model.gguf"), Options{
		Backend: "cuda", ComputeQuant: "int4", ContextTokens: 4096,
	})
	if engine != nil || !errors.Is(err, ErrUnsupportedBackend) || !strings.Contains(err.Error(), "CUDA backend not built in") {
		t.Fatalf("engine=%v error=%v", engine, err)
	}
}

func TestOpenExplicitBackend(t *testing.T) {
	for _, backend := range []string{"cpu", "cuda"} {
		t.Run(backend, func(t *testing.T) {
			config, load, model, _ := loadFixture(t)
			config.backend, config.contextTokens = backend, 4096
			model.backend, model.residentActive, model.residentCap = backend, backend == "cuda", 4096
			calls := 0
			load.model = func(_ string, options decoder.Options) (modelBackend, error) {
				calls++
				wantResident := 0
				if backend == "cuda" {
					wantResident = 4096
				}
				if options.Backend != backend || options.Quant != "int4" || options.ResidentContext != wantResident {
					t.Fatalf("options=%+v", options)
				}
				return model, nil
			}
			engine, err := open(context.Background(), config, load)
			if err != nil || calls != 1 {
				t.Fatalf("engine=%v error=%v calls=%d", engine, err, calls)
			}
			if err := engine.Close(); err != nil || model.closes != 1 {
				t.Fatalf("Close error=%v calls=%d", err, model.closes)
			}
		})
	}
}

func TestOpenCUDARejectsFallbackAndResidency(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend string
		active  bool
		cap     int
		decline string
		want    error
		message string
	}{
		{"CPU fallback", "cpu", false, 0, "", ErrUnsupportedBackend, "requested cuda, effective cpu"},
		{"resident decline", "cuda", false, 4096, "driver unavailable", ErrCUDAResidency, "driver unavailable"},
		{"reported decline with active residency", "cuda", true, 4096, "resident declined", ErrCUDAResidency, "resident declined"},
		{"inactive residency", "cuda", false, 4096, "", ErrCUDAResidency, "active=false"},
		{"unknown resident cap", "cuda", true, 0, "", ErrCUDAResidency, "cap=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, load, model, _ := loadFixture(t)
			config.backend, config.contextTokens = "cuda", 4096
			model.backend, model.residentActive, model.residentCap, model.residentDecline = tc.backend, tc.active, tc.cap, tc.decline
			var snapshot string
			load.model = func(path string, _ decoder.Options) (modelBackend, error) {
				snapshot = path
				return model, nil
			}
			load.tokenizer = func(string) (requestTokenizer, error) {
				t.Fatal("rejected CUDA reached tokenizer")
				return nil, nil
			}
			engine, err := open(context.Background(), config, load)
			if engine != nil || !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.message) || model.closes != 1 {
				t.Fatalf("engine=%v error=%v closes=%d", engine, err, model.closes)
			}
			if _, err := os.Stat(snapshot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("snapshot remains after CUDA rejection: %v", err)
			}
		})
	}
}

func TestOpenCUDAUnavailableBuild(t *testing.T) {
	if cudaBackendPin != "" {
		t.Skip("CUDA module is built in")
	}
	config, _, _, _ := loadFixture(t)
	engine, err := OpenWithOptions(context.Background(), config.path, Options{Backend: "cuda", ComputeQuant: "int4", ContextTokens: 4096})
	if engine != nil || !errors.Is(err, ErrUnsupportedBackend) || !strings.Contains(err.Error(), "CUDA backend not built in") {
		t.Fatalf("engine=%v error=%v", engine, err)
	}
}
