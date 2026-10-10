package protect

import (
	"fmt"
	"maps"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sh4869221b/yakuori/internal/unit"
)

// ByteRange is a half-open byte range in source or prepared text.
type ByteRange struct{ Start, End int }

// Cut ends a body at End and preserves text[End:Next] as its separator.
type Cut struct{ End, Next int }

// Piece binds a safe range to the Prepared that produced it.
type Piece struct {
	owner            *manifest
	source, prepared ByteRange
}

func (s Piece) SourceRange() ByteRange   { return s.source }
func (s Piece) PreparedRange() ByteRange { return s.prepared }
func (s Piece) Source() string {
	if s.owner == nil {
		return ""
	}
	return s.owner.source[s.source.Start:s.source.End]
}
func (s Piece) Text() string {
	if s.owner == nil {
		return ""
	}
	return s.owner.text[s.prepared.Start:s.prepared.End]
}
func (s Piece) GenerationRequired() bool {
	if s.owner == nil {
		return false
	}
	m := s.subset()
	return m.generationRequired
}

// Cuts returns whitespace runs and sentence ends outside protected structures.
// Leading and trailing whitespace runs are included for separator preservation.
func (p Prepared) Cuts() []Cut {
	if p.data == nil {
		return nil
	}
	text := p.data.text
	var cuts []Cut
	for offset := 0; offset < len(text); {
		r, size := utf8.DecodeRuneInString(text[offset:])
		next := offset + size
		if unicode.IsSpace(r) {
			for next < len(text) {
				r, size = utf8.DecodeRuneInString(text[next:])
				if !unicode.IsSpace(r) {
					break
				}
				next += size
			}
			if _, ok := p.sourceOffset(offset); ok {
				if _, ok := p.sourceOffset(next); ok {
					cuts = append(cuts, Cut{offset, next})
				}
			}
		} else if strings.ContainsRune(".!?。！？", r) {
			end := next
			for next < len(text) {
				r, size = utf8.DecodeRuneInString(text[next:])
				if !unicode.IsSpace(r) {
					break
				}
				next += size
			}
			if _, ok := p.sourceOffset(end); ok {
				if _, ok := p.sourceOffset(next); ok {
					cuts = append(cuts, Cut{end, next})
				}
			}
		}
		offset = next
	}
	return cuts
}

// Slice accepts only rune-aligned boundaries outside tokens and open tags.
func (p Prepared) Slice(start, end int) (Piece, error) {
	if p.data == nil || start < 0 || end < start || end > len(p.data.text) {
		return Piece{}, ErrInvalidPrepared
	}
	sourceStart, validStart := p.sourceOffset(start)
	sourceEnd, validEnd := p.sourceOffset(end)
	if !validStart || !validEnd {
		return Piece{}, fmt.Errorf("%w: unsafe segment range", ErrInvalidPrepared)
	}
	return Piece{p.data, ByteRange{sourceStart, sourceEnd}, ByteRange{start, end}}, nil
}

func (p Prepared) sourceOffset(offset int) (int, bool) {
	m := p.data
	if offset < 0 || offset > len(m.text) || (offset < len(m.text) && !utf8.RuneStart(m.text[offset])) {
		return 0, false
	}
	if offset > 0 && offset < len(m.text) && m.text[offset-1] == '\r' && m.text[offset] == '\n' {
		return 0, false
	}
	for _, span := range reservedTokenPattern.FindAllStringIndex(m.text, -1) {
		if span[0] < offset && offset < span[1] {
			return 0, false
		}
	}
	previousSource, previousPrepared, depth := 0, 0, 0
	for i, occ := range m.occurrences {
		start := previousPrepared + occ.start - previousSource
		end := start + len(fmt.Sprintf("%s%d]]", m.prefix, i))
		if offset <= start {
			return previousSource + offset - previousPrepared, depth == 0
		}
		if offset < end {
			return 0, false
		}
		switch m.literals[occ.literal].kind {
		case unit.OpenTag:
			depth++
		case unit.CloseTag:
			depth--
		case unit.Placeholder:
		}
		previousSource, previousPrepared = occ.end, end
	}
	return previousSource + offset - previousPrepared, depth == 0
}

func (s Piece) subset() *manifest {
	parent := s.owner
	m := &manifest{source: s.Source(), text: s.Text(), prefix: parent.prefix, literals: parent.literals, tokens: make(map[string]int)}
	for i, occ := range parent.occurrences {
		if occ.start < s.source.Start || occ.end > s.source.End {
			continue
		}
		m.tokens[fmt.Sprintf("%s%d]]", parent.prefix, i)] = len(m.occurrences)
		m.occurrences = append(m.occurrences, occurrence{occ.start - s.source.Start, occ.end - s.source.Start, occ.literal})
		if parent.literals[occ.literal].ordered {
			m.ordered = append(m.ordered, occ.literal)
		}
	}
	m.sourceReserved = reservedTokens(m.source)
	m.tokenizedReserved = reservedTokens(m.text)
	for token := range m.tokens {
		delete(m.tokenizedReserved, token)
	}
	m.payloadRequired = strings.TrimSpace(withoutOccurrences(m.source, m.occurrences)) != ""
	m.generationRequired = m.source != "" && (len(m.occurrences) == 0 || m.payloadRequired)
	return m
}

// CheckCandidate applies the parent's protection contract to one bound piece.
// It requires exactly the piece's tokens, while allowing unordered placeholders.
func (p Prepared) CheckCandidate(piece Piece, candidate string) error {
	if p.data == nil || piece.owner != p.data {
		return ErrInvalidPrepared
	}
	if err := checkText(candidate); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCandidate, err)
	}
	m := piece.subset()
	found, err := m.scanTokens(candidate)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCandidate, err)
	}
	var restored strings.Builder
	previous := 0
	for _, occ := range found {
		restored.WriteString(candidate[previous:occ.start])
		restored.WriteString(m.literals[occ.literal].text)
		previous = occ.end
	}
	restored.WriteString(candidate[previous:])
	text := restored.String()
	if strings.Contains(text, m.prefix) || !maps.Equal(reservedTokens(text), m.sourceReserved) {
		return fmt.Errorf("%w: unrestored or unknown reserved token", ErrInvalidCandidate)
	}
	observed, err := scanLiterals(text, m.literals)
	if err == nil {
		err = m.checkStructure(observed)
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCandidate, err)
	}
	if (m.source == "") != (text == "") || (!m.generationRequired && text != m.source) || (m.payloadRequired && strings.TrimSpace(withoutOccurrences(text, observed)) == "") {
		return fmt.Errorf("%w: segment payload differs", ErrInvalidCandidate)
	}
	return nil
}
