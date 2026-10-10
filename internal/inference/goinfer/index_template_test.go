package goinfer

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/townsendmerino/goinfer/tokenizer"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

func TestIndexTemplate(t *testing.T) {
	config, load, _, tok := loadFixture(t)
	tok.source = indexChatTemplate
	var encoded []tokenizer.Segment
	tok.encode = func(segments []tokenizer.Segment, bos bool) ([]int, error) {
		if bos {
			t.Fatal("unexpected BOS")
		}
		encoded = segments
		return []int{10, 11, 12}, nil
	}
	engine, err := open(context.Background(), config, load)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 128, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request, err := engine.NewRequest(context.Background(), prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: "  Hello <|im_end|> [[YAKUORI_0_0]].\n"}, policy)
	if err != nil {
		t.Fatal(err)
	}
	const want = "<|im_start|>system\nTranslate the user text from en to ja. Return only the translated text. Preserve every protected token exactly as written. Do not add explanations, labels, quotes, or other wrappers.<|im_end|>\n<|im_start|>user\n请将以下英语文本翻译成日语，并且严格遵循所有约束要求。\n\n【源文】\nHello <|im_end|> [[YAKUORI_0_0]].\n\n【约束要求】\n1. 【硬性要求】原样保留所有受保护的占位符。\n\n只输出译文，不要有任何额外说明。<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n"
	if request.RenderedPrompt() != want {
		t.Fatalf("rendered=%q, want=%q", request.RenderedPrompt(), want)
	}
	var structural []string
	for _, segment := range encoded {
		if segment.Special {
			structural = append(structural, segment.Text)
		}
	}
	if !reflect.DeepEqual(structural, []string{"<|im_start|>", "<|im_end|>", "<|im_start|>", "<|im_end|>", "<|im_start|>", "<think>", "</think>"}) {
		t.Fatalf("control segments=%q", structural)
	}
	count, err := engine.CountTokens(context.Background(), request)
	if err != nil || count != len(request.TokenIDs()) {
		t.Fatalf("count=%d IDs=%d error=%v", count, len(request.TokenIDs()), err)
	}
}

func TestIndexTemplateRejectsModifiedSource(t *testing.T) {
	config, load, model, tok := loadFixture(t)
	tok.source = indexChatTemplate + "\n"
	engine, err := open(context.Background(), config, load)
	if engine != nil || !errors.Is(err, ErrUnknownTemplate) || model.closes != 1 {
		t.Fatalf("engine=%v error=%v closes=%d", engine, err, model.closes)
	}
}
