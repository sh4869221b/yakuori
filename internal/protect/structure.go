package protect

import (
	"errors"
	"fmt"
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

// CheckDeclaredSpans checks adapter declarations without building generation tokens
// or allocating an entry for every undeclared source match.
func CheckDeclaredSpans(u unit.TranslationUnit) error {
	m := manifest{source: string(u.Source())}
	if err := m.addExplicitSpans(u.ProtectionSpans()); err != nil {
		return err
	}
	for i, item := range m.literals {
		offset := 0
		for _, want := range m.occurrences {
			if want.literal != i {
				continue
			}
			relative := strings.Index(m.source[offset:], item.text)
			if relative < 0 || offset+relative != want.start {
				return fmt.Errorf("%w: undeclared literal occurrence", ErrInvalidSource)
			}
			// Include overlaps within a single literal in the search.
			offset = want.start + 1
		}
		if strings.Contains(m.source[offset:], item.text) {
			return fmt.Errorf("%w: undeclared literal occurrence", ErrInvalidSource)
		}
	}
	if err := m.checkStructure(m.occurrences); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSource, err)
	}
	return nil
}

func (m *manifest) addExplicitSpans(spans []unit.ProtectionSpan) error {
	for _, span := range spans {
		item := literal{text: m.source[span.Start:span.End], kind: span.Kind, pair: span.Pair, ordered: span.Ordered}
		index := -1
		for i, known := range m.literals {
			if known.text == item.text {
				if known != item {
					return fmt.Errorf("%w: inconsistent literal metadata", ErrInvalidSource)
				}
				index = i
				break
			}
		}
		if index < 0 {
			index = len(m.literals)
			m.literals = append(m.literals, item)
		}
		m.occurrences = append(m.occurrences, occurrence{start: span.Start, end: span.End, literal: index})
		if item.ordered {
			m.ordered = append(m.ordered, index)
		}
	}
	return nil
}

// Every exact occurrence participates, including overlaps within the same literal.
func scanLiterals(text string, literals []literal) ([]occurrence, error) {
	var found []occurrence
	for i, item := range literals {
		if item.kind == "" {
			continue
		}
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
	if len(found) == 0 {
		return text
	}
	var payload strings.Builder
	previous := 0
	for _, occ := range found {
		payload.WriteString(text[previous:occ.start])
		previous = occ.end
	}
	payload.WriteString(text[previous:])
	return payload.String()
}
