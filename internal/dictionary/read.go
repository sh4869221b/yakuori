// Package dictionary imports neutral translation and terminology reference data.
package dictionary

import (
	"bytes"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	Version        = 1
	maxBytes       = 2 << 20
	maxEntries     = 4096
	maxStringBytes = 16 << 10
	maxDepth       = 8
)

type Document struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

type Kind string

const (
	Translation Kind = "translation"
	Term        Kind = "term"
)

type Confirmation string

const (
	Confirmed   Confirmation = "confirmed"
	Unconfirmed Confirmation = "unconfirmed"
	Unknown     Confirmation = "unknown"
)

type Permission string

const (
	Allowed           Permission = "allowed"
	Denied            Permission = "denied"
	UnknownPermission Permission = "unknown"
)

type Entry struct {
	ID             string       `json:"id"`
	Kind           Kind         `json:"kind"`
	SourceLanguage string       `json:"source_language"`
	TargetLanguage string       `json:"target_language"`
	Source         string       `json:"source,omitempty"`
	Target         string       `json:"target,omitempty"`
	Provenance     string       `json:"provenance,omitempty"`
	License        License      `json:"license,omitzero"`
	Confirmation   Confirmation `json:"confirmation,omitempty"`
	Scope          Scope        `json:"scope,omitzero"`
	Unit           Unit         `json:"unit,omitzero"`
	Context        Context      `json:"context,omitzero"`
}

type License struct {
	Name       string     `json:"name,omitempty"`
	Permission Permission `json:"permission,omitempty"`
}

type Scope struct {
	Game     string `json:"game,omitempty"`
	Artifact string `json:"artifact,omitempty"`
}

type Unit struct {
	Adapter      string `json:"adapter,omitempty"`
	FormatSchema string `json:"format_schema,omitempty"`
	StableID     string `json:"stable_id,omitempty"`
}

type Context struct {
	Scene   string `json:"scene,omitempty"`
	Speaker string `json:"speaker,omitempty"`
}

// Read validates a complete exchange document. Errors contain fixed reasons
// and positions, allowing callers to report them without exposing input text.
func Read(r io.Reader) (Document, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return Document{}, errors.New("dictionary read failed")
	}
	if len(data) > maxBytes {
		return Document{}, errors.New("dictionary exceeds byte limit")
	}
	if err := checkJSON(data); err != nil {
		return Document{}, err
	}
	var doc Document
	if err := jsonv2.Unmarshal(data, &doc, jsonv2.RejectUnknownMembers(true)); err != nil {
		return Document{}, errors.New("invalid dictionary member or value type")
	}
	if doc.Version != Version {
		return Document{}, errors.New("unsupported dictionary version")
	}
	if doc.Entries == nil {
		return Document{}, errors.New("missing dictionary entries")
	}
	seen := make(map[string]Entry, len(doc.Entries))
	distinct := doc.Entries[:0]
	for i, entry := range doc.Entries {
		if err := validateEntry(entry); err != nil {
			return Document{}, fmt.Errorf("dictionary entries[%d]: %w", i, err)
		}
		if previous, exists := seen[entry.ID]; exists {
			if previous != entry {
				return Document{}, fmt.Errorf("dictionary entries[%d]: ID has conflicting entries", i)
			}
			continue
		}
		seen[entry.ID] = entry
		distinct = append(distinct, entry)
	}
	doc.Entries = distinct
	return doc, nil
}

func checkJSON(data []byte) error {
	// The strict token decoder rejects duplicate names, invalid UTF-8 and
	// unpaired surrogates before the typed decode allocates the entry array.
	decoder := jsontext.NewDecoder(bytes.NewReader(data))
	root, err := decoder.ReadToken()
	if err != nil || root.Kind() != jsontext.KindBeginObject {
		return errors.New("expected dictionary JSON object")
	}
	count := 0
	for decoder.StackDepth() > 0 {
		parent, _ := decoder.StackIndex(decoder.StackDepth())
		token, err := decoder.ReadToken()
		if err != nil {
			return fmt.Errorf("invalid dictionary JSON at byte %d", decoder.InputOffset())
		}
		kind := token.Kind()
		if parent == jsontext.KindBeginArray && kind != jsontext.KindEndArray {
			count++
			if count > maxEntries {
				return errors.New("dictionary exceeds entry limit")
			}
		}
		if decoder.StackDepth() > maxDepth {
			return errors.New("dictionary exceeds nesting limit")
		}
		if kind == jsontext.KindNull {
			return fmt.Errorf("unexpected dictionary null at byte %d", decoder.InputOffset())
		}
		if kind == jsontext.KindString && len(token.String()) > maxStringBytes {
			return errors.New("dictionary exceeds string byte limit")
		}
	}
	if _, err := decoder.ReadToken(); err != io.EOF {
		return errors.New("trailing data after dictionary")
	}
	return nil
}

func validateEntry(entry Entry) error {
	if !nonblank(entry.ID) || strings.ContainsFunc(entry.ID, unicode.IsControl) {
		return errors.New("invalid entry ID")
	}
	if entry.Kind != Translation && entry.Kind != Term {
		return errors.New("unsupported entry kind")
	}
	if !nonblank(entry.SourceLanguage) || !nonblank(entry.TargetLanguage) {
		return errors.New("missing language pair")
	}
	switch entry.Confirmation {
	case "", Confirmed, Unconfirmed, Unknown:
	default:
		return errors.New("unsupported confirmation state")
	}
	switch entry.License.Permission {
	case "", Allowed, Denied, UnknownPermission:
	default:
		return errors.New("unsupported license permission")
	}
	for _, value := range []string{
		entry.ID, entry.SourceLanguage, entry.TargetLanguage, entry.Source, entry.Target,
		entry.Provenance, entry.License.Name, entry.Scope.Game, entry.Scope.Artifact,
		entry.Unit.Adapter, entry.Unit.FormatSchema, entry.Unit.StableID, entry.Context.Scene, entry.Context.Speaker,
	} {
		if !validText(value) {
			return errors.New("invalid entry string")
		}
	}
	return nil
}

func validText(s string) bool {
	return len(s) <= maxStringBytes && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

func nonblank(s string) bool { return strings.TrimSpace(s) != "" }
