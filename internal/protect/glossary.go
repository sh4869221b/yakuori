package protect

import (
	"fmt"
	"slices"

	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func (m *manifest) addGlossary(matches []glossary.Match, spans []unit.ProtectionSpan) error {
	var entries []glossary.Entry
	for _, match := range matches {
		m.blocked = append(m.blocked, ByteRange{match.Start, match.End})
		adapter := false
		for _, span := range spans {
			if match.Start >= span.End || span.Start >= match.End {
				continue
			}
			if match.Start != span.Start || match.End != span.End || match.Output() != m.source[span.Start:span.End] {
				return fmt.Errorf("%w: %w: glossary overlaps adapter protection", ErrInvalidSource, glossary.ErrConflict)
			}
			adapter = true
		}
		if match.Entry.Mode != glossary.Protect {
			continue
		}
		m.glossaryMatches = append(m.glossaryMatches, match)
		entries = append(entries, match.Entry)
		if adapter {
			continue
		}
		index := -1
		for i, item := range m.literals {
			if item.kind == "" && item.text == match.Entry.Source {
				index = i
				break
			}
		}
		if index < 0 {
			index = len(m.literals)
			// Empty kind is a glossary literal, without adapter tag or order semantics.
			m.literals = append(m.literals, literal{text: match.Entry.Source})
		}
		occ := occurrence{match.Start, match.End, index}
		if !slices.Contains(m.occurrences, occ) {
			m.occurrences = append(m.occurrences, occ)
		}
	}
	var err error
	m.glossary, err = glossary.New(glossary.Schema, entries)
	if err != nil {
		return err
	}
	slices.SortFunc(m.occurrences, func(a, b occurrence) int { return a.start - b.start })
	return nil
}

// Adapter literals retain all-occurrence scanning. Glossary literals use the
// glossary matcher so word boundaries stay identical during restoration.
func (m *manifest) scanLiterals(text string) ([]occurrence, error) {
	found, err := scanLiterals(text, m.literals)
	if err != nil {
		return nil, err
	}
	matches, err := m.glossary.Match(text)
	if err != nil {
		return nil, err
	}
	counts := make(map[glossary.Entry]int)
	for _, match := range m.glossaryMatches {
		counts[match.Entry]++
	}
	for _, match := range matches {
		counts[match.Entry]--
	}
	for _, count := range counts {
		if count != 0 {
			return nil, fmt.Errorf("%w: protected glossary entry count differs", ErrInvalidCandidate)
		}
	}
	for _, match := range matches {
		duplicate := false
		for _, occ := range found {
			if occ.start == match.Start && occ.end == match.End {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		for i, item := range m.literals {
			if item.kind == "" && item.text == match.Entry.Source {
				found = append(found, occurrence{match.Start, match.End, i})
				break
			}
		}
	}
	slices.SortFunc(found, func(a, b occurrence) int { return a.start - b.start })
	for i := 1; i < len(found); i++ {
		if found[i].start < found[i-1].end {
			return nil, fmt.Errorf("%w: overlapping protected literals", glossary.ErrConflict)
		}
	}
	return found, nil
}

func (m *manifest) safeGlossaryOffset(offset int) bool {
	for _, span := range m.blocked {
		if span.Start < offset && offset < span.End {
			return false
		}
	}
	return true
}

// GlossaryRanges returns detached raw-source ranges for all applied terms,
// including prefer and mandatory terms that do not produce protection tokens.
func (p Prepared) GlossaryRanges() []ByteRange {
	if p.data == nil {
		return nil
	}
	return slices.Clone(p.data.blocked)
}

func (m *manifest) safeGlossarySeparator(start, end int) bool {
	for _, span := range m.blocked {
		if start < span.End && span.Start < end {
			return false
		}
	}
	return true
}
