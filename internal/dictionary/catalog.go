package dictionary

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/sh4869221b/yakuori/internal/glossary"
)

type Decision string

const (
	Accept Decision = "accept"
	Reject Decision = "reject"
)

type Review struct {
	Decision Decision           `json:"decision"`
	Target   *string            `json:"target,omitempty"`
	Mode     glossary.Mode      `json:"mode,omitempty"`
	Match    glossary.MatchKind `json:"match,omitempty"`
}

// ReviewedEntry binds a local decision to the exact imported entry reviewed.
type ReviewedEntry struct {
	Entry  Entry  `json:"entry"`
	Review Review `json:"review"`
}

const (
	ReasonImportConflict       = "import-conflict"
	ReasonReferenceOnly        = "reference-only"
	ReasonAmbiguousTranslation = "ambiguous-translation"
	ReasonContextMismatch      = "context-mismatch"
)

type Diagnostic struct {
	ID, Reason string
}

// Catalog holds imported entries and local decisions. Its zero value is empty.
// Import and Review must run outside concurrent use of the catalog.
type Catalog struct {
	entries map[string]ReviewedEntry
}

// Import merges an exchange document. A changed entry with a reviewed ID
// leaves its existing entry and decision in place and reports a conflict.
func (c *Catalog) Import(r io.Reader) ([]Diagnostic, error) {
	doc, err := Read(r)
	if err != nil {
		return nil, err
	}
	if c.entries == nil {
		c.entries = make(map[string]ReviewedEntry, len(doc.Entries))
	}
	var diagnostics []Diagnostic
	for _, entry := range doc.Entries {
		old := c.entries[entry.ID]
		if old.Review.Decision != "" {
			if old.Entry != entry {
				diagnostics = append(diagnostics, Diagnostic{ID: entry.ID, Reason: ReasonImportConflict})
			}
			continue
		}
		c.entries[entry.ID] = ReviewedEntry{Entry: entry}
	}
	return diagnostics, nil
}

// Review applies an explicit local decision. Target overrides the imported
// target if supplied. An accepted term requires an explicit glossary mode
// and match kind.
func (c *Catalog) Review(id string, review Review) error {
	record, exists := c.entries[id]
	if !exists {
		return errors.New("unknown dictionary entry ID")
	}
	if err := validateReview(record.Entry, review); err != nil {
		return err
	}
	record.Review = copyReview(review)
	c.entries[id] = record
	return nil
}

// Reviews returns detached entries and decisions for storage by the local caller.
func (c *Catalog) Reviews() []ReviewedEntry {
	var reviews []ReviewedEntry
	for _, id := range slices.Sorted(maps.Keys(c.entries)) {
		record := c.entries[id]
		if record.Review.Decision != "" {
			record.Review = copyReview(record.Review)
			reviews = append(reviews, record)
		}
	}
	return reviews
}

// NewCatalog restores trusted local decisions before exchange data is
// imported again. Exchange documents are read with Import.
func NewCatalog(reviews []ReviewedEntry) (*Catalog, error) {
	c := &Catalog{entries: make(map[string]ReviewedEntry, len(reviews))}
	for i, record := range reviews {
		if err := validateEntry(record.Entry); err != nil {
			return nil, fmt.Errorf("local dictionary review %d: %w", i, err)
		}
		if err := validateReview(record.Entry, record.Review); err != nil {
			return nil, fmt.Errorf("local dictionary review %d: %w", i, err)
		}
		id := record.Entry.ID
		if _, exists := c.entries[id]; exists {
			return nil, errors.New("duplicate ID in local dictionary reviews")
		}
		record.Review = copyReview(record.Review)
		c.entries[id] = record
	}
	return c, nil
}

func validateReview(entry Entry, review Review) error {
	if review.Decision != Accept && review.Decision != Reject {
		return errors.New("unsupported local dictionary decision")
	}
	if review.Target != nil && !validText(*review.Target) {
		return errors.New("invalid local dictionary target")
	}
	if entry.Kind == Term && review.Decision == Accept {
		rule := termRule(entry, review)
		if rule.Mode != glossary.Protect && !nonblank(rule.Target) {
			return errors.New("missing local glossary target")
		}
		if _, err := glossary.New(glossary.Schema, []glossary.Entry{rule}); err != nil {
			return glossary.ErrInvalidGlossary
		}
	} else if review.Mode != "" || review.Match != "" {
		return errors.New("glossary mode and match require an accepted term")
	}
	return nil
}

func termRule(entry Entry, review Review) glossary.Entry {
	return glossary.Entry{
		Source: entry.Source,
		Target: effectiveTarget(entry, review),
		Mode:   review.Mode,
		Match:  review.Match,
	}
}

func effectiveTarget(entry Entry, review Review) string {
	if review.Target != nil {
		return *review.Target
	}
	return entry.Target
}

func copyReview(review Review) Review {
	if review.Target != nil {
		target := *review.Target
		review.Target = &target
	}
	return review
}
