package validate

import (
	"strings"
	"unicode"

	"github.com/sh4869221b/yakuori/internal/unit"
)

type ReviewCode string

const (
	SourceIdentical         ReviewCode = "SourceIdentical"
	PossibleProperName      ReviewCode = "PossibleProperName"
	TargetLanguageUncertain ReviewCode = "TargetLanguageUncertain"
)

type ReviewFinding struct {
	Code   ReviewCode
	Reason string
}

type reviewFlags uint8

const (
	sourceIdenticalFlag reviewFlags = 1 << iota
	possibleProperNameFlag
	targetLanguageUncertainFlag
)

// ReviewFindings returns an independent slice of nonblocking quality questions.
func (a AcceptedTranslation) ReviewFindings() []ReviewFinding {
	var findings []ReviewFinding
	for _, finding := range []struct {
		flag reviewFlags
		item ReviewFinding
	}{
		{sourceIdenticalFlag, ReviewFinding{SourceIdentical, "Text with nonprotected payload is identical to the source."}},
		{possibleProperNameFlag, ReviewFinding{PossibleProperName, "The unchanged source may be a proper name."}},
		{targetLanguageUncertainFlag, ReviewFinding{TargetLanguageUncertain, "Nonprotected text has no Hiragana, Katakana, or Han; Japanese target language is uncertain."}},
	} {
		if a.review&finding.flag != 0 {
			findings = append(findings, finding.item)
		}
	}
	return findings
}

func reviewContent(u unit.TranslationUnit, source, candidate string) reviewFlags {
	spans := u.ProtectionSpans()
	if strings.TrimSpace(reviewPayload(source, source, spans)) == "" {
		return 0
	}
	var flags reviewFlags
	if candidate == source {
		flags |= sourceIdenticalFlag
		if possibleProperName(source) {
			flags |= possibleProperNameFlag
		}
	}
	if u.TargetLanguage() == "ja" {
		payload := reviewPayload(candidate, source, spans)
		if !strings.ContainsFunc(payload, func(r rune) bool { return unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Han) }) {
			flags |= targetLanguageUncertainFlag
		}
	}
	return flags
}

func reviewPayload(text, source string, spans []unit.ProtectionSpan) string {
	replacements := make([]string, 0, len(spans)*2)
	for _, span := range spans {
		replacements = append(replacements, source[span.Start:span.End], "")
	}
	return strings.NewReplacer(replacements...).Replace(text)
}

func possibleProperName(source string) bool {
	if strings.ContainsAny(source, "\r\n") {
		return false
	}
	words := strings.Fields(source)
	if len(words) < 1 || len(words) > 2 {
		return false
	}
	for _, word := range words {
		for i, r := range word {
			if i == 0 {
				if !unicode.IsUpper(r) {
					return false
				}
			} else if !unicode.IsLetter(r) && !unicode.IsMark(r) && r != '\'' && r != '-' {
				return false
			}
		}
	}
	return true
}
