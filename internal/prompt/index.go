package prompt

import (
	"fmt"
	"strings"
)

// BuildIndex uses Index-Translate's canonical instTrans constraint format.
// Language aliases cover the existing English-to-Japanese translation path.
func BuildIndex(input Input) Messages {
	language := func(name string) string {
		switch name {
		case "en", "English":
			return "英语"
		case "ja", "Japanese":
			return "日语"
		default:
			return name
		}
	}
	return Messages{
		Schema: SchemaV1,
		System: fmt.Sprintf(systemV1, input.SourceLanguage, input.TargetLanguage),
		User: fmt.Sprintf("请将以下%s文本翻译成%s，并且严格遵循所有约束要求。\n\n【源文】\n%s\n\n【约束要求】\n1. 【硬性要求】原样保留所有受保护的占位符。\n\n只输出译文，不要有任何额外说明。",
			language(input.SourceLanguage), language(input.TargetLanguage), strings.TrimSpace(input.Text)),
	}
}
