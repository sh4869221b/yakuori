package identity_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"testing"

	"github.com/sh4869221b/yakuori/internal/identity"
)

func sourceInput() identity.SourceInput {
	return identity.SourceInput{
		Version: 1, GameNamespace: "game", ArtifactNamespace: "dialogue",
		OriginalArtifactDigest: sha256.Sum256([]byte("original artifact")),
		Adapter:                "fixture", FormatSchema: "v1", StableID: "record-1",
		Keys:            [][]byte{{255}, []byte("a"), {}, []byte("a")},
		RawSourceDigest: sha256.Sum256([]byte(" e\u0301\r\n{name}")),
		SourceLanguage:  "en", TargetLanguage: "ja",
		ProtectionSpans: []identity.ProtectionSpan{{Start: 6, End: 12, Kind: "placeholder"}},
	}
}

func makeSource(t *testing.T, input identity.SourceInput) identity.SourceIdentity {
	t.Helper()
	result, err := identity.NewSourceIdentity(input)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSourceIdentityCanonicalGolden(t *testing.T) {
	got := makeSource(t, sourceInput())
	want, err := os.ReadFile("testdata/source-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.CanonicalBytes(), want) {
		t.Fatalf("canonical = %s, want %s", got.CanonicalBytes(), want)
	}
	if got.Digest() != sha256.Sum256(want) {
		t.Fatal("digest does not identify canonical bytes")
	}
}

func TestSourceIdentityFieldChanges(t *testing.T) {
	base := makeSource(t, sourceInput())
	changes := map[string]func(*identity.SourceInput){
		"game namespace":     func(i *identity.SourceInput) { i.GameNamespace += "2" },
		"artifact namespace": func(i *identity.SourceInput) { i.ArtifactNamespace += "2" },
		"whole artifact":     func(i *identity.SourceInput) { i.OriginalArtifactDigest[0] ^= 1 },
		"adapter":            func(i *identity.SourceInput) { i.Adapter += "2" },
		"schema":             func(i *identity.SourceInput) { i.FormatSchema += "2" },
		"stable ID":          func(i *identity.SourceInput) { i.StableID += "2" },
		"key bytes":          func(i *identity.SourceInput) { i.Keys[0][0] = 254 },
		"key multiplicity":   func(i *identity.SourceInput) { i.Keys = i.Keys[:3] },
		"empty key":          func(i *identity.SourceInput) { i.Keys = append(i.Keys[:2], i.Keys[3:]...) },
		"source":             func(i *identity.SourceInput) { i.RawSourceDigest[0] ^= 1 },
		"source language":    func(i *identity.SourceInput) { i.SourceLanguage = "fr" },
		"target language":    func(i *identity.SourceInput) { i.TargetLanguage = "de" },
		"span start":         func(i *identity.SourceInput) { i.ProtectionSpans[0].Start++ },
		"span end":           func(i *identity.SourceInput) { i.ProtectionSpans[0].End++ },
		"span kind":          func(i *identity.SourceInput) { i.ProtectionSpans[0].Kind = "open_tag" },
		"span pair":          func(i *identity.SourceInput) { i.ProtectionSpans[0].Pair = "pair" },
		"span ordered":       func(i *identity.SourceInput) { i.ProtectionSpans[0].Ordered = true },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			input := sourceInput()
			change(&input)
			got := makeSource(t, input)
			if got == base || bytes.Equal(got.CanonicalBytes(), base.CanonicalBytes()) || got.Digest() == base.Digest() {
				t.Fatal("changed source field reused identity")
			}
		})
	}
}

func TestSourceIdentityKeysAndSnapshot(t *testing.T) {
	input := sourceInput()
	got := makeSource(t, input)
	original := got.CanonicalBytes()
	reordered := sourceInput()
	reordered.Keys = [][]byte{nil, []byte("a"), {255}, []byte("a")}
	if other := makeSource(t, reordered); other != got {
		t.Fatal("key order or nil empty representation changed identity")
	}
	input.Keys[0][0] = 0
	input.Keys[1] = []byte("changed")
	input.ProtectionSpans[0].Start = 0
	got.CanonicalBytes()[0] = '!'
	if !bytes.Equal(got.CanonicalBytes(), original) || got != makeSource(t, sourceInput()) {
		t.Fatal("input or accessor mutation changed identity")
	}
	empty := sourceInput()
	empty.Keys, empty.ProtectionSpans = nil, nil
	canonical := makeSource(t, empty).CanonicalBytes()
	if !bytes.Contains(canonical, []byte(`"keys":[]`)) || !bytes.Contains(canonical, []byte(`"protection_spans":[]`)) {
		t.Fatalf("empty collections must be arrays: %s", canonical)
	}
}

func TestSourceIdentityInvalidInput(t *testing.T) {
	changes := map[string]func(*identity.SourceInput){
		"zero version":    func(i *identity.SourceInput) { i.Version = 0 },
		"unknown version": func(i *identity.SourceInput) { i.Version = 2 },
		"game":            func(i *identity.SourceInput) { i.GameNamespace = "" },
		"artifact":        func(i *identity.SourceInput) { i.ArtifactNamespace = "" },
		"adapter":         func(i *identity.SourceInput) { i.Adapter = "" },
		"schema":          func(i *identity.SourceInput) { i.FormatSchema = "" },
		"stable ID":       func(i *identity.SourceInput) { i.StableID = "" },
		"source language": func(i *identity.SourceInput) { i.SourceLanguage = "" },
		"target language": func(i *identity.SourceInput) { i.TargetLanguage = "" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			input := sourceInput()
			change(&input)
			got, err := identity.NewSourceIdentity(input)
			if !errors.Is(err, identity.ErrInvalidSource) || got != (identity.SourceIdentity{}) {
				t.Fatalf("identity = %v, error = %v", got, err)
			}
		})
	}
}

func TestSourceIdentityProtectionOrder(t *testing.T) {
	input := sourceInput()
	input.ProtectionSpans = append(input.ProtectionSpans, identity.ProtectionSpan{Start: 13, End: 17, Kind: "open_tag", Pair: "tag", Ordered: true})
	base := makeSource(t, input)
	input.ProtectionSpans[0], input.ProtectionSpans[1] = input.ProtectionSpans[1], input.ProtectionSpans[0]
	if got := makeSource(t, input); got == base {
		t.Fatal("protection source order was discarded")
	}
}

func TestSourceIdentityRejectsLossyJSONString(t *testing.T) {
	changes := map[string]func(*identity.SourceInput){
		"game":            func(i *identity.SourceInput) { i.GameNamespace = "\xff" },
		"artifact":        func(i *identity.SourceInput) { i.ArtifactNamespace = "\xff" },
		"adapter":         func(i *identity.SourceInput) { i.Adapter = "\xff" },
		"schema":          func(i *identity.SourceInput) { i.FormatSchema = "\xff" },
		"stable ID":       func(i *identity.SourceInput) { i.StableID = "\xff" },
		"source language": func(i *identity.SourceInput) { i.SourceLanguage = "\xff" },
		"target language": func(i *identity.SourceInput) { i.TargetLanguage = "\xff" },
		"span kind":       func(i *identity.SourceInput) { i.ProtectionSpans[0].Kind = "\xff" },
		"span pair":       func(i *identity.SourceInput) { i.ProtectionSpans[0].Pair = "\xff" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			input := sourceInput()
			change(&input)
			got, err := identity.NewSourceIdentity(input)
			if !errors.Is(err, identity.ErrInvalidSource) || got != (identity.SourceIdentity{}) {
				t.Fatalf("lossy JSON string accepted: identity %v, error %v", got, err)
			}
		})
	}
}
