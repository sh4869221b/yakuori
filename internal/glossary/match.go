package glossary

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrConflict = errors.New("conflicting glossary matches")

// Match identifies a source occurrence using half-open byte offsets.
type Match struct {
	Start, End int
	Entry      Entry
}

func (m Match) Output() string {
	switch m.Entry.Mode {
	case Protect:
		return m.Entry.Source
	case Prefer, Mandatory:
		return m.Entry.Target
	}
	return ""
}

// Match collects every occurrence before checking ambiguity. No longest-match
// or entry-order winner is selected; compatible exact spans retain all entries.
func (g Glossary) Match(source string) ([]Match, error) {
	if !utf8.ValidString(source) || strings.ContainsRune(source, 0) {
		return nil, fmt.Errorf("%w: invalid source UTF-8 or NUL", ErrInvalidGlossary)
	}
	entries, err := g.Entries()
	if err != nil {
		return nil, err
	}
	var matches []Match
	for _, e := range entries {
		for offset := 0; offset < len(source); {
			found := strings.Index(source[offset:], e.Source)
			if found < 0 {
				break
			}
			start := offset + found
			end := start + len(e.Source)
			if e.Match == Literal || wordBoundary(source, start, end) {
				matches = append(matches, Match{Start: start, End: end, Entry: e})
			}
			// Advance one rune, rather than the full match, to expose self-overlap.
			_, width := utf8.DecodeRuneInString(source[start:])
			offset = start + width
		}
	}
	slices.SortFunc(matches, func(a, b Match) int {
		if n := cmp.Compare(a.Start, b.Start); n != 0 {
			return n
		}
		if n := cmp.Compare(a.End, b.End); n != 0 {
			return n
		}
		return compareEntries(a.Entry, b.Entry)
	})
	for i, a := range matches {
		for _, b := range matches[i+1:] {
			if b.Start >= a.End {
				break
			}
			if a.Start != b.Start || a.End != b.End || a.Output() != b.Output() {
				return nil, fmt.Errorf("%w: [%d,%d) %q and [%d,%d) %q", ErrConflict, a.Start, a.End, a.Entry.Source, b.Start, b.End, b.Entry.Source)
			}
		}
	}
	return matches, nil
}

func wordBoundary(source string, start, end int) bool {
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(source[:start])
		if wordRune(r) {
			return false
		}
	}
	if end < len(source) {
		r, _ := utf8.DecodeRuneInString(source[end:])
		if wordRune(r) {
			return false
		}
	}
	return true
}

func wordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsNumber(r)
}
