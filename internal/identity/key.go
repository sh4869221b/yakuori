package identity

import (
	"encoding/json"
	"errors"
)

const KeySchema = 1

var ErrInvalidKey = errors.New("exact key requires canonical source and profile")

// ExactKey equality includes both canonical BLOBs, not just their digests.
type ExactKey struct {
	schema  int
	source  string
	profile string
}

func NewExactKey(source SourceIdentity, profile ProfileIdentity) (ExactKey, error) {
	if source.canonical == "" || profile.canonical == "" {
		return ExactKey{}, ErrInvalidKey
	}
	return ExactKey{KeySchema, source.canonical, profile.canonical}, nil
}

// UnitContext holds the selected context and dictionary effects for a unit.
type UnitContext struct {
	Prompt     PromptInput
	Selection  []byte
	Dictionary UnitDictionary
}

// UnitDictionary holds the exact approved target and canonical added terms.
type UnitDictionary struct {
	Translation string          `json:"translation,omitempty"`
	Terms       json.RawMessage `json:"terms,omitempty"`
}

// NewContextExactKey adds per-unit inputs to the canonical run profile. A unit
// without selected context or dictionary effects uses the original key.
func NewContextExactKey(source SourceIdentity, profile ProfileIdentity, context UnitContext) (ExactKey, error) {
	key, err := NewExactKey(source, profile)
	hasDictionary := context.Dictionary.Translation != "" || len(context.Dictionary.Terms) != 0
	if err != nil || (len(context.Selection) == 0 && !hasDictionary) {
		return key, err
	}
	var record profileRecord
	if err := json.Unmarshal([]byte(key.profile), &record); err != nil {
		return ExactKey{}, err
	}
	if len(context.Selection) != 0 {
		record.Prompt = context.Prompt
		record.Context.Mode = "provided"
		record.Context.Additional = context.Selection
	}
	unitProfile := struct {
		profileRecord
		Dictionary *UnitDictionary `json:"dictionary,omitempty"`
	}{profileRecord: record}
	if hasDictionary {
		unitProfile.Dictionary = &context.Dictionary
	}
	profileBytes, err := json.Marshal(unitProfile)
	if err != nil {
		return ExactKey{}, err
	}
	key.profile = string(profileBytes)
	return key, nil
}

func (k ExactKey) Schema() int          { return k.schema }
func (k ExactKey) SourceBytes() []byte  { return []byte(k.source) }
func (k ExactKey) ProfileBytes() []byte { return []byte(k.profile) }
