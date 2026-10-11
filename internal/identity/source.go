package identity

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"
)

var ErrInvalidSource = errors.New("invalid source identity")

// ProtectionSpan preserves source order and the adapter's protection semantics.
type ProtectionSpan struct {
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Kind    string `json:"kind"`
	Pair    string `json:"pair"`
	Ordered bool   `json:"ordered"`
}

// SourceInput uses existing raw-source and whole-original-artifact digests.
// Keys are opaque bytes: order is ignored, but empty keys and duplicates matter.
type SourceInput struct {
	Version                int
	GameNamespace          string
	ArtifactNamespace      string
	OriginalArtifactDigest [32]byte
	Adapter                string
	FormatSchema           string
	StableID               string
	Keys                   [][]byte
	RawSourceDigest        [32]byte
	SourceLanguage         string
	TargetLanguage         string
	ProtectionSpans        []ProtectionSpan
}

// Field order and JSON encoding are part of the v1 persistence contract.
type sourceRecord struct {
	Version                int              `json:"version"`
	GameNamespace          string           `json:"game_namespace"`
	ArtifactNamespace      string           `json:"artifact_namespace"`
	OriginalArtifactDigest string           `json:"original_artifact_digest"`
	Adapter                string           `json:"adapter"`
	FormatSchema           string           `json:"format_schema"`
	StableID               string           `json:"stable_id"`
	Keys                   [][]byte         `json:"keys"`
	RawSourceDigest        string           `json:"raw_source_digest"`
	SourceLanguage         string           `json:"source_language"`
	TargetLanguage         string           `json:"target_language"`
	ProtectionSpans        []ProtectionSpan `json:"protection_spans"`
}

// SourceIdentity is immutable and comparable, including its canonical bytes.
// The zero value has no canonical identity.
type SourceIdentity struct {
	canonical string
	digest    [32]byte
}

func NewSourceIdentity(input SourceInput) (SourceIdentity, error) {
	if input.Version != SourceVersion {
		return SourceIdentity{}, fmt.Errorf("%w: unsupported version %d", ErrInvalidSource, input.Version)
	}
	if input.GameNamespace == "" || input.ArtifactNamespace == "" || input.Adapter == "" || input.FormatSchema == "" || input.StableID == "" || input.SourceLanguage == "" || input.TargetLanguage == "" {
		return SourceIdentity{}, fmt.Errorf("%w: missing scope, unit identity or language", ErrInvalidSource)
	}
	// JSON replaces invalid UTF-8 in strings; reject it before distinct IDs collapse.
	for _, field := range []string{input.GameNamespace, input.ArtifactNamespace, input.Adapter, input.FormatSchema, input.StableID, input.SourceLanguage, input.TargetLanguage} {
		if !utf8.ValidString(field) {
			return SourceIdentity{}, fmt.Errorf("%w: invalid UTF-8 identity field", ErrInvalidSource)
		}
	}
	for _, span := range input.ProtectionSpans {
		if !utf8.ValidString(span.Kind) || !utf8.ValidString(span.Pair) {
			return SourceIdentity{}, fmt.Errorf("%w: invalid UTF-8 protection field", ErrInvalidSource)
		}
	}
	keys := make([][]byte, len(input.Keys))
	for i, key := range input.Keys {
		// A nil key and an empty key both represent the same empty byte sequence.
		keys[i] = append([]byte{}, key...)
	}
	slices.SortFunc(keys, bytes.Compare)
	spans := append([]ProtectionSpan{}, input.ProtectionSpans...)
	canonical, err := json.Marshal(sourceRecord{
		Version: input.Version, GameNamespace: input.GameNamespace,
		ArtifactNamespace: input.ArtifactNamespace, OriginalArtifactDigest: digestHex(input.OriginalArtifactDigest),
		Adapter: input.Adapter, FormatSchema: input.FormatSchema, StableID: input.StableID,
		Keys: keys, RawSourceDigest: digestHex(input.RawSourceDigest),
		SourceLanguage: input.SourceLanguage, TargetLanguage: input.TargetLanguage,
		ProtectionSpans: spans,
	})
	if err != nil {
		return SourceIdentity{}, fmt.Errorf("marshal source identity: %w", err)
	}
	return SourceIdentity{canonical: string(canonical), digest: sha256.Sum256(canonical)}, nil
}

func (s SourceIdentity) CanonicalBytes() []byte { return []byte(s.canonical) }
func (s SourceIdentity) Digest() [32]byte       { return s.digest }
