package protect

import (
	"errors"
	"slices"
	"strings"

	"github.com/sh4869221b/yakuori/internal/unit"
)

type literal struct {
	text    string
	kind    unit.ProtectionKind
	pair    string
	ordered bool
}

type occurrence struct{ start, end, literal int }

// Every exact occurrence participates, including overlaps within the same literal.
func scanLiterals(text string, literals []literal) ([]occurrence, error) {
	var found []occurrence
	for i, item := range literals {
		for offset := 0; offset < len(text); {
			relative := strings.Index(text[offset:], item.text)
			if relative < 0 {
				break
			}
			start := offset + relative
			found = append(found, occurrence{start: start, end: start + len(item.text), literal: i})
			offset = start + 1
		}
	}
	slices.SortFunc(found, func(a, b occurrence) int { return a.start - b.start })
	for i := 1; i < len(found); i++ {
		if found[i].start < found[i-1].end {
			return nil, errors.New("ambiguous overlapping literals")
		}
	}
	return found, nil
}

func (m *manifest) checkStructure(found []occurrence) error {
	wantCounts := make([]int, len(m.literals))
	for _, occ := range m.occurrences {
		wantCounts[occ.literal]++
	}
	counts := make([]int, len(m.literals))
	var ordered []int
	var stack []string
	for _, occ := range found {
		counts[occ.literal]++
		item := m.literals[occ.literal]
		if item.ordered {
			ordered = append(ordered, occ.literal)
		}
		switch item.kind {
		case unit.Placeholder:
		case unit.OpenTag:
			stack = append(stack, item.pair)
		case unit.CloseTag:
			if len(stack) == 0 || stack[len(stack)-1] != item.pair {
				return errors.New("unbalanced tag nesting")
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) != 0 {
		return errors.New("unclosed tag")
	}
	if !slices.Equal(counts, wantCounts) {
		return errors.New("protected literal count differs")
	}
	if !slices.Equal(ordered, m.ordered) {
		return errors.New("ordered literal sequence differs")
	}
	return nil
}

func withoutOccurrences(text string, found []occurrence) string {
	var payload strings.Builder
	previous := 0
	for _, occ := range found {
		payload.WriteString(text[previous:occ.start])
		previous = occ.end
	}
	payload.WriteString(text[previous:])
	return payload.String()
}
