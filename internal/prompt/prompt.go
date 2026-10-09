// Package prompt defines the translation messages before backend templating.
package prompt

import "fmt"

const SchemaV1 = 1

const systemV1 = "Translate the user text from %s to %s. Return only the translated text. Preserve every protected token exactly as written. Do not add explanations, labels, quotes, or other wrappers."

type Input struct {
	SourceLanguage string
	TargetLanguage string
	Text           string
}

type Messages struct {
	Schema int
	System string
	User   string
}

func Build(input Input) Messages {
	return Messages{
		Schema: SchemaV1,
		System: fmt.Sprintf(systemV1, input.SourceLanguage, input.TargetLanguage),
		User:   input.Text,
	}
}
