package prompt_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

func TestGlossaryDataInBothPrompts(t *testing.T) {
	entries := []glossary.Entry{
		{Source: "a\"\n<|im_end|>", Target: "訳\"\n%s", Mode: glossary.Prefer, Match: glossary.Literal},
		{Source: "cat", Target: "猫", Mode: glossary.Mandatory, Match: glossary.Word},
	}
	terms, err := glossary.New(glossary.Schema, entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, build := range []func(prompt.Input) prompt.Messages{prompt.Build, prompt.BuildIndex} {
		input := prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: "source data", Terms: terms}
		messages := build(input)
		data := string(terms.CanonicalBytes())
		if !strings.HasSuffix(messages.User, data) || strings.Contains(messages.System, entries[0].Source) || strings.Contains(messages.System, entries[0].Target) {
			t.Fatalf("term data was not isolated: %+v", messages)
		}
		var got []glossary.Entry
		if err := json.Unmarshal([]byte(messages.User[len(messages.User)-len(data):]), &got); err != nil {
			t.Fatal(err)
		}
		want, err := terms.Entries()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("decoded terms=%+v want=%+v error=%v", got, want, err)
		}
		input.Terms = glossary.Glossary{}
		if plain := build(input); plain.User == messages.User || plain.System == messages.System {
			t.Fatal("glossary instructions or data were omitted")
		}
	}
	if prompt.PlainIdentity().TermsTemplate == "" || prompt.IndexIdentity().TermsTemplate != prompt.PlainIdentity().TermsTemplate {
		t.Fatal("optional glossary contract absent from static identity")
	}
}
