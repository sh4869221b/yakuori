// Package glossary defines exact, versioned translation requirements.
package glossary

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

const Schema = "glossary-v1"

type Mode string

const (
	Prefer    Mode = "prefer"
	Protect   Mode = "protect"
	Mandatory Mode = "mandatory"
)

type MatchKind string

const (
	Literal MatchKind = "literal"
	Word    MatchKind = "word"
)

var ErrInvalidGlossary = errors.New("invalid glossary")

// Entry is constructor input. An empty Target means absent, required for Protect.
type Entry struct {
	Source string    `json:"source"`
	Target string    `json:"target,omitempty"`
	Mode   Mode      `json:"mode"`
	Match  MatchKind `json:"match"`
}

// Glossary is immutable and comparable by content. Its zero value is empty.
type Glossary struct{ canonical string }

func New(schema string, entries []Entry) (Glossary, error) {
	if schema != Schema {
		return Glossary{}, fmt.Errorf("%w: unsupported schema %q", ErrInvalidGlossary, schema)
	}
	entries = slices.Clone(entries)
	for i, e := range entries {
		if err := checkEntry(e); err != nil {
			return Glossary{}, fmt.Errorf("entry %d: %w", i, err)
		}
	}
	slices.SortFunc(entries, compareEntries)
	entries = slices.Compact(entries)
	if len(entries) == 0 {
		return Glossary{}, nil
	}
	canonical, err := json.Marshal(entries)
	if err != nil {
		return Glossary{}, fmt.Errorf("marshal glossary: %w", err)
	}
	return Glossary{canonical: string(canonical)}, nil
}

// Parse reads a JSON entry array using the separately supplied schema. Protect
// entries must omit target, including an explicitly empty or null target.
func Parse(schema string, data []byte) (Glossary, error) {
	if !utf8.Valid(data) {
		return Glossary{}, fmt.Errorf("%w: invalid UTF-8 JSON", ErrInvalidGlossary)
	}
	var records []struct {
		Source string          `json:"source"`
		Target json.RawMessage `json:"target"`
		Mode   Mode            `json:"mode"`
		Match  MatchKind       `json:"match"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&records); err != nil {
		return Glossary{}, fmt.Errorf("%w: decode entries: %v", ErrInvalidGlossary, err)
	}
	if records == nil {
		return Glossary{}, fmt.Errorf("%w: expected entry array", ErrInvalidGlossary)
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return Glossary{}, fmt.Errorf("%w: trailing JSON", ErrInvalidGlossary)
	}
	entries := make([]Entry, len(records))
	for i, r := range records {
		var target string
		if r.Target != nil {
			if r.Mode == Protect || bytes.Equal(r.Target, []byte("null")) {
				return Glossary{}, fmt.Errorf("%w: entry %d has unused or null target", ErrInvalidGlossary, i)
			}
			if err := json.Unmarshal(r.Target, &target); err != nil {
				return Glossary{}, fmt.Errorf("%w: entry %d target: %v", ErrInvalidGlossary, i, err)
			}
		}
		entries[i] = Entry{Source: r.Source, Target: target, Mode: r.Mode, Match: r.Match}
	}
	return New(schema, entries)
}

func (g Glossary) Empty() bool { return g.canonical == "" }
func (g Glossary) Schema() string {
	if g.Empty() {
		return ""
	}
	return Schema
}
func (g Glossary) CanonicalBytes() []byte {
	if g.Empty() {
		return []byte("[]")
	}
	return []byte(g.canonical)
}

// Entries returns a detached copy of the canonical entry sequence.
func (g Glossary) Entries() ([]Entry, error) {
	if g.Empty() {
		return nil, nil
	}
	var entries []Entry
	if err := json.Unmarshal([]byte(g.canonical), &entries); err != nil {
		return nil, fmt.Errorf("decode canonical glossary: %w", err)
	}
	return entries, nil
}

func checkEntry(e Entry) error {
	if e.Source == "" {
		return fmt.Errorf("%w: empty source", ErrInvalidGlossary)
	}
	for _, s := range []string{e.Source, e.Target} {
		if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
			return fmt.Errorf("%w: invalid UTF-8 or NUL", ErrInvalidGlossary)
		}
	}
	switch e.Mode {
	case Protect:
		if e.Target != "" {
			return fmt.Errorf("%w: protect target must be absent", ErrInvalidGlossary)
		}
	case Prefer, Mandatory:
		if e.Target == "" {
			return fmt.Errorf("%w: missing target", ErrInvalidGlossary)
		}
	default:
		return fmt.Errorf("%w: unsupported mode %q", ErrInvalidGlossary, e.Mode)
	}
	switch e.Match {
	case Literal, Word:
	default:
		return fmt.Errorf("%w: unsupported match %q", ErrInvalidGlossary, e.Match)
	}
	return nil
}

func compareEntries(a, b Entry) int {
	if n := cmp.Compare(a.Source, b.Source); n != 0 {
		return n
	}
	if n := cmp.Compare(a.Target, b.Target); n != 0 {
		return n
	}
	if n := cmp.Compare(a.Mode, b.Mode); n != 0 {
		return n
	}
	return cmp.Compare(a.Match, b.Match)
}
