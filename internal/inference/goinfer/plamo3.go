package goinfer

import (
	"errors"
	"regexp"
	"strings"

	"github.com/townsendmerino/goinfer/tokenizer"

	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

const plamo3Family = "pfml3-translation-v1"

// Hash of the reviewed official chat_template.jinja at the pinned PLaMo 3 0.8B revision.
const plamo3TemplateSHA256 = "cd49e7e0dfa8ec847c163db88dfca89b280dddbb73ddc60ba38c62a420eaced2"

var plamo3Language = regexp.MustCompile(`^[a-z]{2,3}(-[a-z]{4})?$`)

func plamo3Lang(value string) (string, error) {
	switch strings.ToLower(value) {
	case "english":
		value = "en"
	case "japanese":
		value = "ja"
	}
	value = strings.ToLower(value)
	if value != "ja(easy)" && !plamo3Language.MatchString(value) {
		return "", errors.New("unsupported PLaMo language code")
	}
	return value, nil
}

func plamo3Segments(input prompt.Input) ([]tokenizer.Segment, error) {
	source, err := plamo3Lang(input.SourceLanguage)
	if err != nil {
		return nil, err
	}
	target, err := plamo3Lang(input.TargetLanguage)
	if err != nil {
		return nil, err
	}
	if strings.Contains(input.Text, "<|plamo:") {
		return nil, errors.New("source contains a reserved PFML marker")
	}
	segments := []tokenizer.Segment{{Text: prompt.Plamo3Identity().SystemTemplate, Special: true}}
	entries, err := input.Terms.Entries()
	if err != nil {
		return nil, err
	}
	glossaryStarted := false
	for _, entry := range entries {
		if entry.Mode == glossary.Protect {
			continue
		}
		if strings.Contains(entry.Source, "<|plamo:") || strings.Contains(entry.Target, "<|plamo:") {
			return nil, errors.New("glossary contains a reserved PFML marker")
		}
		if !glossaryStarted {
			segments = append(segments, tokenizer.Segment{Text: "<|plamo:tag|>glossary", Special: true})
			glossaryStarted = true
		}
		segments = append(segments, tokenizer.Segment{Text: "<|plamo:index|>", Special: true}, tokenizer.Segment{Text: entry.Source}, tokenizer.Segment{Text: "<|plamo:item|>", Special: true}, tokenizer.Segment{Text: entry.Target})
	}
	segments = append(segments,
		tokenizer.Segment{Text: "<|plamo:tag|>input<|plamo:key|>lang<|plamo:val|>" + source + "<|plamo:msg|>", Special: true},
		tokenizer.Segment{Text: input.Text},
		tokenizer.Segment{Text: "<|plamo:tag|>output<|plamo:key|>lang<|plamo:val|>" + target + "<|plamo:msg|>", Special: true})
	return segments, nil
}
