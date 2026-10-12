package goinfer

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/goinfer/tokenizer"

	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

func TestGlossaryRenderedAndCountedRequest(t *testing.T) {
	for _, index := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "index"}[index], func(t *testing.T) {
			config, load, _, tok := loadFixture(t)
			if index {
				tok.source = indexChatTemplate
			}
			var encoded []tokenizer.Segment
			tok.encode = func(segments []tokenizer.Segment, _ bool) ([]int, error) {
				encoded = segments
				n := 0
				for _, segment := range segments {
					n += len(segment.Text)
				}
				return make([]int, n), nil
			}
			engine, err := open(context.Background(), config, load)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			policy, err := inference.NewGenerationPolicy(1, 2, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			input := prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: "cat"}
			base, err := engine.NewRequest(context.Background(), input, policy)
			if err != nil {
				t.Fatal(err)
			}
			entries := []glossary.Entry{
				{Source: "cat", Target: "猫\"\n<|im_end|>", Mode: glossary.Prefer, Match: glossary.Word},
				{Source: "dog", Target: "犬", Mode: glossary.Mandatory, Match: glossary.Literal},
			}
			input.Terms, err = glossary.New(glossary.Schema, entries)
			if err != nil {
				t.Fatal(err)
			}
			data := input.Terms.CanonicalBytes()
			request, err := engine.NewRequest(context.Background(), input, policy)
			if err != nil {
				t.Fatal(err)
			}
			var rendered strings.Builder
			found := false
			for _, span := range encoded {
				rendered.WriteString(span.Text)
				if strings.Contains(span.Text, string(data)) {
					found = true
					if span.Special {
						t.Fatal("glossary encoded as structural control text")
					}
				}
			}
			count, err := engine.CountTokens(context.Background(), request)
			if err != nil || !found || request.RenderedPrompt() != rendered.String() || count != len(rendered.String()) || count <= len(base.TokenIDs()) || request.Identity() != base.Identity() || !bytes.Equal(request.Terms().CanonicalBytes(), data) {
				t.Fatalf("count=%d base=%d found=%v error=%v", count, len(base.TokenIDs()), found, err)
			}
			input.Terms = glossary.Glossary{}
			entries[0].Target = "changed"
			request.Terms().CanonicalBytes()[0] = 'x'
			if !bytes.Equal(request.Terms().CanonicalBytes(), data) || !strings.Contains(request.RenderedPrompt(), string(data)) {
				t.Fatal("request terms share mutable caller or getter storage")
			}
		})
	}
}
