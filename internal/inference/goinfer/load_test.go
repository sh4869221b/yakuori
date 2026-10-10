package goinfer

import (
	"context"
	"errors"
	"path/filepath"
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
