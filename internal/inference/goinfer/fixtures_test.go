package goinfer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/townsendmerino/goinfer/decoder"
	"github.com/townsendmerino/goinfer/tokenizer"
)

type stubModel struct {
	contextTokens int
	backend       string
	quant         string
	closes        int
	closeErr      error
}

func (m *stubModel) Config() *decoder.Config  { return &decoder.Config{MaxPositions: m.contextTokens} }
func (m *stubModel) EffectiveBackend() string { return m.backend }
func (m *stubModel) Quant() string            { return m.quant }
func (m *stubModel) Close() error {
	m.closes++
	return m.closeErr
}

type stubTokenizer struct {
	source  string
	decline string
	stops   map[string]int
	encode  func([]tokenizer.Segment, bool) ([]int, error)
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

const fixtureModel = "test model bytes"

func loadFixture(t *testing.T) (modelConfig, loaders, *stubModel, *stubTokenizer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(path, []byte(fixtureModel), 0600); err != nil {
		t.Fatal(err)
	}
	model := &stubModel{contextTokens: 32768, backend: "cpu", quant: "int4"}
	tok := &stubTokenizer{
		source: "<|im_start|> template metadata", stops: map[string]int{"<|im_end|>": 42},
		encode: func([]tokenizer.Segment, bool) ([]int, error) { return []int{10, 11, 12}, nil },
	}
	load := loaders{
		model: func(gotPath string, opts decoder.Options) (modelBackend, error) {
			if gotPath != path || opts.Backend != "cpu" || opts.Quant != "int4" {
				t.Fatalf("load model: path=%q options=%+v", gotPath, opts)
			}
			return model, nil
		},
		tokenizer: func(gotPath string) (requestTokenizer, error) {
			if gotPath != path {
				t.Fatalf("tokenizer path=%q, want same GGUF %q", gotPath, path)
			}
			return tok, nil
		},
	}
	return modelConfig{path: path, quant: "int4"}, load, model, tok
}
