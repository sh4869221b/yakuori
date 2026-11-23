package dictionary

import (
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/sh4869221b/yakuori/internal/glossary"
)

var ErrAmbiguousTranslation = errors.New("conflicting dictionary translations")

type Query struct {
	Game, Artifact, SourceLanguage, TargetLanguage, Adapter, FormatSchema, StableID, Source, Scene, Speaker string
}

type Selection struct {
	Target      string
	Terms       []glossary.Entry
	Diagnostics []Diagnostic
}

// References returns detached candidates for presentation, including rejected
// entries and entries with incomplete identity or context. It matches language,
// declared scope and known source text.
func (c *Catalog) References(q Query) []ReviewedEntry {
	var references []ReviewedEntry
	for _, id := range slices.Sorted(maps.Keys(c.entries)) {
		record := c.entries[id]
		if scopeMatches(record.Entry, q) && sourceMatches(record.Entry, q) {
			record.Review = copyReview(record.Review)
			references = append(references, record)
		}
	}
	return references
}

// Select finds the unique exact target candidate for current-unit validation
// and the applicable local terminology rules. The caller owns acceptance.
func (c *Catalog) Select(q Query) (Selection, error) {
	var selection Selection
	var targetIDs []string
	ambiguous := false
	for _, candidate := range c.References(q) {
		entry, review := candidate.Entry, candidate.Review
		if review.Decision == Reject {
			continue
		}
		if review.Decision != Accept || !eligible(entry) {
			selection.Diagnostics = append(selection.Diagnostics, Diagnostic{entry.ID, ReasonReferenceOnly})
			continue
		}
		if entry.Kind == Translation {
			target := effectiveTarget(entry, review)
			if !exactTranslation(entry, q) || !nonblank(target) {
				selection.Diagnostics = append(selection.Diagnostics, Diagnostic{entry.ID, ReasonReferenceOnly})
				continue
			}
			if selection.Target == "" {
				selection.Target = target
			}
			ambiguous = ambiguous || selection.Target != target
			targetIDs = append(targetIDs, entry.ID)
			continue
		}
		if !nonblank(entry.Source) {
			selection.Diagnostics = append(selection.Diagnostics, Diagnostic{entry.ID, ReasonReferenceOnly})
			continue
		}
		rule := termRule(entry, review)
		term, err := glossary.New(glossary.Schema, []glossary.Entry{rule})
		if err != nil {
			return Selection{}, glossary.ErrInvalidGlossary
		}
		matches, err := term.Match(q.Source)
		if err != nil && !errors.Is(err, glossary.ErrConflict) {
			return Selection{}, glossary.ErrInvalidGlossary
		}
		if len(matches) == 0 && err == nil {
			continue
		}
		if !selectorsMatch(entry, q) {
			selection.Diagnostics = append(selection.Diagnostics, Diagnostic{entry.ID, ReasonContextMismatch})
			continue
		}
		if err != nil {
			return Selection{}, glossary.ErrConflict
		}
		selection.Terms = append(selection.Terms, rule)
	}

	if ambiguous {
		selection.Target = ""
		for _, id := range targetIDs {
			selection.Diagnostics = append(selection.Diagnostics, Diagnostic{id, ReasonAmbiguousTranslation})
		}
		return selection, ErrAmbiguousTranslation
	}
	if len(selection.Terms) > 1 {
		terms, err := glossary.New(glossary.Schema, selection.Terms)
		if err != nil {
			return Selection{}, glossary.ErrInvalidGlossary
		}
		if _, err := terms.Match(q.Source); err != nil {
			if errors.Is(err, glossary.ErrConflict) {
				return Selection{}, glossary.ErrConflict
			}
			return Selection{}, glossary.ErrInvalidGlossary
		}
		selection.Terms, err = terms.Entries()
		if err != nil {
			return Selection{}, glossary.ErrInvalidGlossary
		}
	}
	return selection, nil
}

func eligible(entry Entry) bool {
	return entry.Confirmation == Confirmed && entry.License.Permission == Allowed &&
		nonblank(entry.Provenance) && nonblank(entry.License.Name)
}

func exactTranslation(entry Entry, q Query) bool {
	return nonblank(entry.Scope.Game) && nonblank(entry.Scope.Artifact) &&
		nonblank(entry.Unit.Adapter) && nonblank(entry.Unit.FormatSchema) && nonblank(entry.Unit.StableID) &&
		nonblank(entry.Context.Scene) && nonblank(entry.Source) && entry.Source == q.Source &&
		selectorsMatch(entry, q)
}

func sourceMatches(entry Entry, q Query) bool {
	if !nonblank(entry.Source) {
		return true
	}
	if entry.Kind == Term {
		return strings.Contains(q.Source, entry.Source)
	}
	return entry.Source == q.Source
}

func scopeMatches(entry Entry, q Query) bool {
	return entry.SourceLanguage == q.SourceLanguage && entry.TargetLanguage == q.TargetLanguage &&
		matchesOptional(entry.Scope.Game, q.Game) && matchesOptional(entry.Scope.Artifact, q.Artifact)
}

func selectorsMatch(entry Entry, q Query) bool {
	return matchesOptional(entry.Unit.Adapter, q.Adapter) &&
		matchesOptional(entry.Unit.FormatSchema, q.FormatSchema) &&
		matchesOptional(entry.Unit.StableID, q.StableID) &&
		matchesOptional(entry.Context.Scene, q.Scene) &&
		matchesOptional(entry.Context.Speaker, q.Speaker)
}

func matchesOptional(constraint, current string) bool {
	return constraint == "" || constraint == current
}
