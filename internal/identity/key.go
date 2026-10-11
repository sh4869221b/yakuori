package identity

import "errors"

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
func (k ExactKey) Schema() int          { return k.schema }
func (k ExactKey) SourceBytes() []byte  { return []byte(k.source) }
func (k ExactKey) ProfileBytes() []byte { return []byte(k.profile) }
