package protect

import (
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"
)

var reservedTokenPattern = regexp.MustCompile(`\[\[YAKUORI_[0-9]+_[0-9]+\]\]`)

func reservedTokens(text string) map[string]int {
	counts := make(map[string]int)
	for _, token := range reservedTokenPattern.FindAllString(text, -1) {
		counts[token]++
	}
	return counts
}

func (m *manifest) scanTokens(text string) ([]occurrence, error) {
	var found []occurrence
	seen := make([]bool, len(m.occurrences))
	var ordered []int
	for offset := 0; offset < len(text); {
		relative := strings.Index(text[offset:], m.prefix)
		if relative < 0 {
			break
		}
		start := offset + relative
		ending := strings.Index(text[start+len(m.prefix):], "]]")
		if ending < 0 {
			return nil, errors.New("unterminated selected token")
		}
		end := start + len(m.prefix) + ending + 2
		index, exists := m.tokens[text[start:end]]
		if !exists {
			return nil, errors.New("unknown or malformed selected token")
		}
		if seen[index] {
			return nil, errors.New("duplicate selected token")
		}
		seen[index] = true
		literalIndex := m.occurrences[index].literal
		found = append(found, occurrence{start: start, end: end, literal: literalIndex})
		if m.literals[literalIndex].ordered {
			ordered = append(ordered, index)
		}
		offset = end
	}
	for _, present := range seen {
		if !present {
			return nil, errors.New("missing selected token")
		}
	}
	var wantOrdered []int
	for i, occ := range m.occurrences {
		if m.literals[occ.literal].ordered {
			wantOrdered = append(wantOrdered, i)
		}
	}
	if !slices.Equal(ordered, wantOrdered) {
		return nil, errors.New("ordered token sequence differs")
	}
	if err := m.checkStructure(found); err != nil {
		return nil, err
	}
	counts := reservedTokens(text)
	for token := range m.tokens {
		delete(counts, token)
	}
	if !maps.Equal(counts, m.tokenizedReserved) {
		return nil, errors.New("unknown or missing reserved token")
	}
	return found, nil
}
