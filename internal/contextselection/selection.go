// Package contextselection holds the immutable result shared by the resolver and inference.
package contextselection

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"slices"
)

const selectionVersion = "yakuori.context-selection/v1"

type Category string

const (
	World    Category = "world"
	Scene    Category = "scene"
	Speaker  Category = "speaker"
	Neighbor Category = "neighbor"
)

// Item contains the fields eligible for generation. Text holds world text,
// a scene summary, a speaker description, or the previous unit's raw source.
// Name is used for speakers.
type Item struct {
	Category Category `json:"category"`
	Name     string   `json:"name,omitempty"`
	Text     string   `json:"text,omitempty"`
}

// ResolvedContext is an immutable selection for a single translation unit.
// Its zero value represents empty context.
type ResolvedContext struct {
	items       []Item
	canonical   []byte
	fingerprint [sha256.Size]byte
}

// New snapshots the ordered items after the resolver has admitted them.
func New(items []Item) (ResolvedContext, error) {
	if len(items) == 0 {
		return ResolvedContext{}, nil
	}
	canonical, err := json.Marshal(struct {
		Version string `json:"version"`
		Items   []Item `json:"items"`
	}{selectionVersion, items})
	if err != nil {
		return ResolvedContext{}, err
	}
	return ResolvedContext{items: slices.Clone(items), canonical: canonical, fingerprint: sha256.Sum256(canonical)}, nil
}

func (r ResolvedContext) Items() []Item { return slices.Clone(r.items) }

func (r ResolvedContext) Empty() bool { return len(r.items) == 0 }

func (r ResolvedContext) Equal(other ResolvedContext) bool {
	return bytes.Equal(r.canonical, other.canonical)
}

// Canonical returns the selection version and ordered category and payload
// fields as compact JSON. Empty context has empty canonical bytes. Rendering
// version belongs to the downstream generation profile.
func (r ResolvedContext) Canonical() []byte { return slices.Clone(r.canonical) }

func (r ResolvedContext) Fingerprint() [sha256.Size]byte {
	if r.Empty() {
		return sha256.Sum256(nil)
	}
	return r.fingerprint
}
