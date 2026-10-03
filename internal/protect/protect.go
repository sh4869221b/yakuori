// Package protect preserves explicitly declared source literals through generation.
package protect

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sh4869221b/yakuori/internal/unit"
)

const Schema = "explicit-spans-v1"

var (
	ErrInvalidSource    = errors.New("invalid source text")
	ErrInvalidCandidate = errors.New("invalid candidate text")
	ErrInvalidPrepared  = errors.New("invalid prepared protection")
)

// Prepared holds a private immutable manifest bound to one imported unit.
// The zero value cannot restore or check a candidate.
type Prepared struct{ data *manifest }

type manifest struct {
	session            unit.Session
	id                 unit.UnitID
	source             string
	text               string
	prefix             string
	literals           []literal
	occurrences        []occurrence
	ordered            []int
	tokens             map[string]int
	sourceReserved     map[string]int
	tokenizedReserved  map[string]int
	generationRequired bool
	payloadRequired    bool
}

func Prepare(session unit.Session, id unit.UnitID) (Prepared, error) {
	u, err := session.Unit(id)
	if err != nil {
		return Prepared{}, err
	}
	source := string(u.Source())
	if err := checkText(source); err != nil {
		return Prepared{}, fmt.Errorf("%w: %v", ErrInvalidSource, err)
	}
	m := &manifest{session: session, id: id, source: source, tokens: make(map[string]int)}
	spans := u.ProtectionSpans()
	for _, span := range spans {
		item := literal{text: source[span.Start:span.End], kind: span.Kind, pair: span.Pair, ordered: span.Ordered}
		index := -1
		for i, known := range m.literals {
			if known.text == item.text {
				if known != item {
					return Prepared{}, fmt.Errorf("%w: inconsistent literal metadata", ErrInvalidSource)
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
	observed, err := scanLiterals(source, m.literals)
	if err != nil {
		return Prepared{}, fmt.Errorf("%w: %v", ErrInvalidSource, err)
	}
	if !slices.Equal(observed, m.occurrences) {
		return Prepared{}, fmt.Errorf("%w: undeclared literal occurrence", ErrInvalidSource)
	}
	if err := m.checkStructure(observed); err != nil {
		return Prepared{}, fmt.Errorf("%w: %v", ErrInvalidSource, err)
	}
	payload := withoutOccurrences(source, observed)
	m.payloadRequired = strings.TrimSpace(payload) != ""
	m.generationRequired = source != "" && (len(spans) == 0 || m.payloadRequired)
	for namespace := 0; ; namespace++ {
		m.prefix = fmt.Sprintf("[[YAKUORI_%d_", namespace)
		if !strings.Contains(source, m.prefix) {
			break
		}
	}
	var text strings.Builder
	previous := 0
	for i, occ := range m.occurrences {
		token := fmt.Sprintf("%s%d]]", m.prefix, i)
		m.tokens[token] = i
		text.WriteString(source[previous:occ.start])
		text.WriteString(token)
		previous = occ.end
	}
	text.WriteString(source[previous:])
	m.text = text.String()
	m.sourceReserved = reservedTokens(source)
	m.tokenizedReserved = reservedTokens(m.text)
	for token := range m.tokens {
		delete(m.tokenizedReserved, token)
	}
	return Prepared{data: m}, nil
}

func (p Prepared) Text() string {
	if p.data == nil {
		return ""
	}
	return p.data.text
}

func (p Prepared) GenerationRequired() bool { return p.data != nil && p.data.generationRequired }

func (p Prepared) checkBinding(session unit.Session, id unit.UnitID) error {
	if p.data == nil || !p.data.session.SameImport(session) || p.data.id != id {
		return ErrInvalidPrepared
	}
	u, err := session.Unit(id)
	if err != nil {
		return err
	}
	if string(u.Source()) != p.data.source {
		return ErrInvalidPrepared
	}
	return nil
}

// Restore validates the complete token stream before constructing restored text.
func (p Prepared) Restore(session unit.Session, id unit.UnitID, candidate string) (string, error) {
	if err := p.checkBinding(session, id); err != nil {
		return "", err
	}
	if err := checkText(candidate); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidCandidate, err)
	}
	occurrences, err := p.data.scanTokens(candidate)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidCandidate, err)
	}
	var restored strings.Builder
	previous := 0
	for _, occ := range occurrences {
		restored.WriteString(candidate[previous:occ.start])
		restored.WriteString(p.data.literals[occ.literal].text)
		previous = occ.end
	}
	restored.WriteString(candidate[previous:])
	text := restored.String()
	if err := p.CheckRestored(session, id, text); err != nil {
		return "", err
	}
	return text, nil
}

// CheckRestored applies the same source structure contract to a restored candidate.
func (p Prepared) CheckRestored(session unit.Session, id unit.UnitID, text string) error {
	if err := p.checkBinding(session, id); err != nil {
		return err
	}
	if err := checkText(text); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCandidate, err)
	}
	m := p.data
	if strings.Contains(text, m.prefix) || !maps.Equal(reservedTokens(text), m.sourceReserved) {
		return fmt.Errorf("%w: unrestored or unknown reserved token", ErrInvalidCandidate)
	}
	observed, err := scanLiterals(text, m.literals)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCandidate, err)
	}
	if err := m.checkStructure(observed); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCandidate, err)
	}
	if (m.source == "") != (text == "") {
		return fmt.Errorf("%w: source and candidate emptiness differ", ErrInvalidCandidate)
	}
	if !m.generationRequired && text != m.source {
		return fmt.Errorf("%w: generation-free unit differs from source", ErrInvalidCandidate)
	}
	if m.payloadRequired && strings.TrimSpace(withoutOccurrences(text, observed)) == "" {
		return fmt.Errorf("%w: nonprotected payload missing", ErrInvalidCandidate)
	}
	return nil
}

func checkText(text string) error {
	if !utf8.ValidString(text) {
		return errors.New("invalid UTF-8")
	}
	if strings.IndexByte(text, 0) >= 0 {
		return errors.New("NUL")
	}
	return nil
}
