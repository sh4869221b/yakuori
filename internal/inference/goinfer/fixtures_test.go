package goinfer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/townsendmerino/goinfer/decoder"
	"github.com/townsendmerino/goinfer/tokenizer"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

type stubModel struct {
	contextTokens int
	backend       string
	quant         string
	closes        int
	closeErr      error
	start         func(context.Context, generationInput) tokenStream
	onClose       func() error
}

func (m *stubModel) Config() *decoder.Config  { return &decoder.Config{MaxPositions: m.contextTokens} }
func (m *stubModel) EffectiveBackend() string { return m.backend }
func (m *stubModel) Quant() string            { return m.quant }
func (m *stubModel) Close() error {
	m.closes++
	if m.onClose != nil {
		return m.onClose()
	}
	return m.closeErr
}

func (m *stubModel) generate(ctx context.Context, input generationInput) tokenStream {
	return m.start(ctx, input)
}

type stubTokenizer struct {
	source  string
	decline string
	stops   map[string]int
	encode  func([]tokenizer.Segment, bool) ([]int, error)
	decode  func([]int) (string, error)
}

func (t *stubTokenizer) ChatTemplate() string        { return t.source }
func (t *stubTokenizer) PreTokenizerDecline() string { return t.decline }
func (t *stubTokenizer) TokenID(text string) (int, bool) {
	id, ok := t.stops[text]
	return id, ok
}
func (t *stubTokenizer) EncodeSegments(segments []tokenizer.Segment, bos bool) ([]int, error) {
	return t.encode(segments, bos)
}

func (t *stubTokenizer) Decode(ids []int) (string, error) { return t.decode(ids) }

const fixtureModel = "test model bytes"

func loadFixture(t *testing.T) (modelConfig, loaders, *stubModel, *stubTokenizer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(path, []byte(fixtureModel), 0600); err != nil {
		t.Fatal(err)
	}
	model := &stubModel{contextTokens: 32768, backend: "cpu", quant: "int4"}
	model.start = func(_ context.Context, input generationInput) tokenStream {
		tokens := make(chan int, 1)
		tokens <- 20
		close(tokens)
		return tokenStream{tokens: tokens, outcome: func() generationState { return generationState{budget: input.max} }}
	}
	tok := &stubTokenizer{
		source: qwenChatTemplate, stops: map[string]int{"<|im_end|>": 42},
		encode: func([]tokenizer.Segment, bool) ([]int, error) { return []int{10, 11, 12}, nil },
		decode: func([]int) (string, error) { return "translated", nil },
	}
	load := loaders{
		model: func(gotPath string, opts decoder.Options) (modelBackend, error) {
			if gotPath == path || opts.Backend != "cpu" || opts.Quant != "int4" {
				t.Fatalf("load model: path=%q options=%+v", gotPath, opts)
			}
			return model, nil
		},
		tokenizer: func(gotPath string) (requestTokenizer, error) {
			if gotPath == path {
				t.Fatalf("tokenizer used mutable model path %q", path)
			}
			return tok, nil
		},
	}
	return modelConfig{path: path, quant: "int4"}, load, model, tok
}

func generationFixture(t *testing.T) (*Engine, *stubModel, *stubTokenizer, inference.GenerationRequest) {
	t.Helper()
	config, load, model, tok := loadFixture(t)
	engine, err := open(context.Background(), config, load)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request, err := engine.NewRequest(context.Background(), prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: "Hello"}, policy)
	if err != nil {
		t.Fatal(err)
	}
	return engine, model, tok, request
}
