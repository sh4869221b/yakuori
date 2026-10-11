package prompt_test

import (
	"fmt"
	"testing"

	"github.com/sh4869221b/yakuori/internal/prompt"
)

func TestPromptIdentityVariants(t *testing.T) {
	input := prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: " \r\nHello\t "}
	plain, index := prompt.PlainIdentity(), prompt.IndexIdentity()
	if plain == index || plain.Schema != index.Schema || plain.LanguageAliases == index.LanguageAliases {
		t.Fatal("prompt variants lost their distinct identity")
	}
	messages := prompt.Build(input)
	if messages.System != fmt.Sprintf(plain.SystemTemplate, input.SourceLanguage, input.TargetLanguage) || messages.User != fmt.Sprintf(plain.UserTemplate, input.Text) {
		t.Fatal("plain identity does not describe used templates")
	}
	messages = prompt.BuildIndex(input)
	if messages.System != fmt.Sprintf(index.SystemTemplate, input.SourceLanguage, input.TargetLanguage) || messages.User != fmt.Sprintf(index.UserTemplate, "英语", "日语", "Hello") {
		t.Fatal("Index identity does not describe aliases and TrimSpace")
	}
}
