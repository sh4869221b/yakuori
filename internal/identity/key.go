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

// UnitContext holds the prompt and selected context used by a unit's requests.
type UnitContext struct {
	Prompt    PromptInput
	Selection []byte
}

// NewContextExactKey overrides only the selected context and its prompt in the
// import-wide canonical profile. An empty selection uses the base key.
func NewContextExactKey(source SourceIdentity, profile ProfileIdentity, context UnitContext) (ExactKey, error) {
	key, err := NewExactKey(source, profile)
	if err != nil || len(context.Selection) == 0 {
		return key, err
	}
	var record profileRecord
	if err := json.Unmarshal([]byte(key.profile), &record); err != nil {
		return ExactKey{}, err
	}
	record.Prompt = context.Prompt
	record.Context.Mode = "provided"
	record.Context.Additional = context.Selection
	profileBytes, err := json.Marshal(record)
	if err != nil {
		return ExactKey{}, err
	}
	key.profile = string(profileBytes)
	return key, nil
}

func (k ExactKey) Schema() int          { return k.schema }
func (k ExactKey) SourceBytes() []byte  { return []byte(k.source) }
func (k ExactKey) ProfileBytes() []byte { return []byte(k.profile) }
