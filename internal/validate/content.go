package validate

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const Schema = "restored-content-v1"

func checkContent(source, candidate string) error {
	sourceText, candidateText := strings.TrimSpace(source), strings.TrimSpace(candidate)
	for _, wrapper := range []func(string) bool{codeFence, translationJSON, translationXML} {
		if wrapper(candidateText) && !wrapper(sourceText) {
			return fmt.Errorf("%w: added translation wrapper", ErrInvalidCandidate)
		}
	}
	for _, prefix := range []string{"Translation:", "Here is the translation:", "翻訳:", "以下が翻訳です:"} {
		if strings.HasPrefix(candidateText, prefix) && !strings.HasPrefix(sourceText, prefix) {
			return fmt.Errorf("%w: added explanation prefix", ErrInvalidCandidate)
		}
	}
	if run := maximumLineRun(candidate); run >= 3 && run > maximumLineRun(source) {
		return fmt.Errorf("%w: added repeated lines", ErrInvalidCandidate)
	}
	return nil
}

func codeFence(text string) bool {
	lines := strings.Split(text, "\n")
	if len(lines) < 2 || lines[len(lines)-1] != "```" {
		return false
	}
	opening := strings.TrimSuffix(lines[0], "\r")
	language, ok := strings.CutPrefix(opening, "```")
	if !ok {
		return false
	}
	for _, r := range language {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func translationJSON(text string) bool {
	decoder := json.NewDecoder(strings.NewReader(text))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') || !decoder.More() {
		return false
	}
	key, err := decoder.Token()
	if err != nil || key != "translation" {
		return false
	}
	value, err := decoder.Token()
	if _, ok := value.(string); err != nil || !ok || decoder.More() {
		return false
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return false
	}
	_, err = decoder.Token()
	return err == io.EOF
}

func translationXML(text string) bool {
	return strings.HasPrefix(text, "<translation>") && strings.HasSuffix(text, "</translation>")
}

func maximumLineRun(text string) int {
	lines := strings.Split(text, "\n")
	var previous string
	run, maximum := 0, 0
	for i, line := range lines {
		if i < len(lines)-1 {
			line = strings.TrimSuffix(line, "\r")
		}
		if strings.TrimSpace(line) == "" {
			run = 0
			continue
		}
		if run > 0 && line == previous {
			run++
		} else {
			run = 1
		}
		previous = line
		if run > maximum {
			maximum = run
		}
	}
	return maximum
}
