package identity_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"testing"

	"github.com/sh4869221b/yakuori/internal/identity"
)

func profileInput() identity.ProfileInput {
	return identity.ProfileInput{
		Version: 1, ModelID: "model", ModelDigest: [32]byte{1}, Backend: "cuda", BackendPin: "goinfer-v1", CUDABackendPin: "cuda-v1", ComputeQuant: "q4", KVPrecision: "fp16",
		Tokenizer:    identity.TokenizerInput{ModelDigest: [32]byte{2}, BackendPin: "tokenizer-v1"},
		Template:     identity.TemplateInput{Family: "chat", Source: "embedded", RendererVersion: "renderer-v1"},
		Prompt:       identity.PromptInput{Schema: 1, Variant: "plain", SystemTemplate: []byte("system"), UserTemplate: []byte("user"), LanguageAliases: "aliases-v1", DisabledThinkingPrefix: []byte{0, 255}},
		Generation:   identity.GenerationInput{Schema: 1, MaxOutputTokens: 32, RequestTimeoutNS: 1000000000, SeedFixed: true, StopIDs: []int{2, 3}, Thinking: "disabled", Prefill: identity.PrefillInput{Path: "cuda", Batched: true, BatchedCapability: true, Fast: "both", Floor: 512, Chunk: 256}},
		Segmentation: identity.SegmentationInput{Mode: "enabled", Schema: "segments-v1", BoundaryFixtures: "boundaries-v1", UnitTextBytes: 980, Segments: 76},
		Glossary:     identity.GlossaryInput{Mode: "provided", Schema: "glossary-v1", Entries: []byte{0, 255}},
		Protection:   identity.SchemaInput{Schema: "protection-v1", FixtureSet: "protection-fixtures-v1"}, Validation: identity.SchemaInput{Schema: "validation-v1", FixtureSet: "validation-fixtures-v1"},
		Context: identity.ContextInput{Tokens: 4096, Mode: "provided", Additional: []byte{0, 255}},
	}
}
func makeProfile(t *testing.T, input identity.ProfileInput) identity.ProfileIdentity {
	t.Helper()
	p, err := identity.NewProfileIdentity(input)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestProfileIdentityCanonicalGolden(t *testing.T) {
	want, err := os.ReadFile("testdata/profile-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	p := makeProfile(t, profileInput())
	if !bytes.Equal(p.CanonicalBytes(), want) {
		t.Fatalf("canonical = %s, want %s", p.CanonicalBytes(), want)
	}
	digest := p.Digest()
	if hex.EncodeToString(digest[:]) != "b27d8c749d8a5f860611efece47e544fb8491f148cb4bec64be8439cc428afb8" {
		t.Fatalf("digest = %x", digest)
	}
}
func TestProfileIdentityFieldChanges(t *testing.T) {
	source := makeSource(t, sourceInput())
	base := makeProfile(t, profileInput())
	baseKey, err := identity.NewExactKey(source, base)
	if err != nil {
		t.Fatal(err)
	}
	changes := map[string]func(*identity.ProfileInput){
		"model ID":                 func(i *identity.ProfileInput) { i.ModelID += "2" },
		"model contents":           func(i *identity.ProfileInput) { i.ModelDigest[0]++ },
		"backend":                  func(i *identity.ProfileInput) { i.Backend = "cpu" },
		"release":                  func(i *identity.ProfileInput) { i.BackendPin += "2" },
		"CUDA release":             func(i *identity.ProfileInput) { i.CUDABackendPin += "2" },
		"compute quant":            func(i *identity.ProfileInput) { i.ComputeQuant = "q8" },
		"KV precision":             func(i *identity.ProfileInput) { i.KVPrecision = "fp32" },
		"tokenizer model":          func(i *identity.ProfileInput) { i.Tokenizer.ModelDigest[0]++ },
		"tokenizer release":        func(i *identity.ProfileInput) { i.Tokenizer.BackendPin += "2" },
		"template family":          func(i *identity.ProfileInput) { i.Template.Family += "2" },
		"template source":          func(i *identity.ProfileInput) { i.Template.Source += "2" },
		"template renderer":        func(i *identity.ProfileInput) { i.Template.RendererVersion += "2" },
		"prompt schema":            func(i *identity.ProfileInput) { i.Prompt.Schema++ },
		"prompt variant":           func(i *identity.ProfileInput) { i.Prompt.Variant = "index" },
		"system template bytes":    func(i *identity.ProfileInput) { i.Prompt.SystemTemplate[0]++ },
		"user wrapper bytes":       func(i *identity.ProfileInput) { i.Prompt.UserTemplate[0]++ },
		"language alias rules":     func(i *identity.ProfileInput) { i.Prompt.LanguageAliases += "2" },
		"thinking prefix bytes":    func(i *identity.ProfileInput) { i.Prompt.DisabledThinkingPrefix[0]++ },
		"generation schema":        func(i *identity.ProfileInput) { i.Generation.Schema++ },
		"max output":               func(i *identity.ProfileInput) { i.Generation.MaxOutputTokens++ },
		"timeout ns":               func(i *identity.ProfileInput) { i.Generation.RequestTimeoutNS++ },
		"seed":                     func(i *identity.ProfileInput) { i.Generation.Seed++ },
		"seed fixed":               func(i *identity.ProfileInput) { i.Generation.SeedFixed = false },
		"stop IDs":                 func(i *identity.ProfileInput) { i.Generation.StopIDs[0]++ },
		"stop order":               func(i *identity.ProfileInput) { i.Generation.StopIDs = []int{3, 2} },
		"thinking policy":          func(i *identity.ProfileInput) { i.Generation.Thinking = "enabled" },
		"exact prefill":            func(i *identity.ProfileInput) { i.Generation.Prefill.Exact = true },
		"prefill policy":           func(i *identity.ProfileInput) { i.Generation.Prefill.Path = "none" },
		"batched prefill":          func(i *identity.ProfileInput) { i.Generation.Prefill.Batched = false },
		"batched capability":       func(i *identity.ProfileInput) { i.Generation.Prefill.BatchedCapability = false },
		"fast prefill":             func(i *identity.ProfileInput) { i.Generation.Prefill.Fast = "attn" },
		"fast floor":               func(i *identity.ProfileInput) { i.Generation.Prefill.Floor++ },
		"fast chunk":               func(i *identity.ProfileInput) { i.Generation.Prefill.Chunk++ },
		"segmentation mode":        func(i *identity.ProfileInput) { i.Segmentation = identity.SegmentationInput{Mode: "none"} },
		"segmentation schema":      func(i *identity.ProfileInput) { i.Segmentation.Schema += "2" },
		"boundary fixtures":        func(i *identity.ProfileInput) { i.Segmentation.BoundaryFixtures += "2" },
		"used unit bytes":          func(i *identity.ProfileInput) { i.Segmentation.UnitTextBytes++ },
		"used segments":            func(i *identity.ProfileInput) { i.Segmentation.Segments++ },
		"glossary none":            func(i *identity.ProfileInput) { i.Glossary = identity.GlossaryInput{Mode: "none"} },
		"glossary schema":          func(i *identity.ProfileInput) { i.Glossary.Schema += "2" },
		"glossary entries":         func(i *identity.ProfileInput) { i.Glossary.Entries[0]++ },
		"protection schema":        func(i *identity.ProfileInput) { i.Protection.Schema += "2" },
		"protection fixtures":      func(i *identity.ProfileInput) { i.Protection.FixtureSet += "2" },
		"validation schema":        func(i *identity.ProfileInput) { i.Validation.Schema += "2" },
		"validation fixtures":      func(i *identity.ProfileInput) { i.Validation.FixtureSet += "2" },
		"effective context cap":    func(i *identity.ProfileInput) { i.Context.Tokens++ },
		"additional context none":  func(i *identity.ProfileInput) { i.Context = identity.ContextInput{Tokens: 4096, Mode: "none"} },
		"additional context bytes": func(i *identity.ProfileInput) { i.Context.Additional[0]++ },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			input := profileInput()
			change(&input)
			other := makeProfile(t, input)
			key, err := identity.NewExactKey(source, other)
			if err != nil {
				t.Fatal(err)
			}
			if other == base || other.Digest() == base.Digest() || bytes.Equal(other.CanonicalBytes(), base.CanonicalBytes()) || key == baseKey {
				t.Fatal("changed effective condition retained the identity/key")
			}
		})
	}
}
func TestProfileIdentitySnapshot(t *testing.T) {
	input := profileInput()
	p := makeProfile(t, input)
	before := p.CanonicalBytes()
	input.Prompt.SystemTemplate[0]++
	input.Prompt.UserTemplate[0]++
	input.Prompt.DisabledThinkingPrefix[0]++
	input.Generation.StopIDs[0]++
	input.Glossary.Entries[0]++
	input.Context.Additional[0]++
	returned := p.CanonicalBytes()
	returned[0] = '!'
	if !bytes.Equal(p.CanonicalBytes(), before) || p != makeProfile(t, profileInput()) {
		t.Fatal("canonical profile mutated")
	}
}
func TestProfileIdentityInactiveConditions(t *testing.T) {
	cases := map[string]struct {
		base   func(*identity.ProfileInput)
		change func(*identity.ProfileInput)
	}{
		"CPU ignores CUDA": {func(i *identity.ProfileInput) { i.Backend = "cpu" }, func(i *identity.ProfileInput) {
			i.CUDABackendPin = "unused"
			i.Generation.Prefill.Batched = false
			i.Generation.Prefill.BatchedCapability = false
			i.Generation.Prefill.Fast = "off"
			i.Generation.Prefill.Floor = 9
			i.Generation.Prefill.Chunk = 7
		}},
		"CUDA ignores CPU": {func(i *identity.ProfileInput) {}, func(i *identity.ProfileInput) { i.Generation.Prefill.CPUFastAttention = true }},
		"exact disables fast": {func(i *identity.ProfileInput) { i.Generation.Prefill.Exact = true }, func(i *identity.ProfileInput) {
			i.Generation.Prefill.Fast = "attn"
			i.Generation.Prefill.Floor = 9
		}},
		"off ignores fast floor":              {func(i *identity.ProfileInput) { i.Generation.Prefill.Fast = "off" }, func(i *identity.ProfileInput) { i.Generation.Prefill.Floor = 9 }},
		"no batched policy ignores chunk":     {func(i *identity.ProfileInput) { i.Generation.Prefill.Batched = false }, func(i *identity.ProfileInput) { i.Generation.Prefill.Chunk = 7 }},
		"no batched capability ignores chunk": {func(i *identity.ProfileInput) { i.Generation.Prefill.BatchedCapability = false }, func(i *identity.ProfileInput) { i.Generation.Prefill.Chunk = 7 }},
	}
	for name, tt := range cases {
		t.Run(name, func(t *testing.T) {
			input := profileInput()
			tt.base(&input)
			p := makeProfile(t, input)
			tt.change(&input)
			if p != makeProfile(t, input) {
				t.Fatal("unused condition changed identity")
			}
		})
	}
	t.Run("CPU fast attention", func(t *testing.T) {
		input := profileInput()
		input.Backend = "cpu"
		p := makeProfile(t, input)
		input.Generation.Prefill.CPUFastAttention = true
		if p == makeProfile(t, input) {
			t.Fatal("effective CPU policy did not change identity")
		}
	})
}

func TestProfileIdentityBatchedChunkWithoutFastKernels(t *testing.T) {
	for _, exact := range []bool{false, true} {
		input := profileInput()
		input.Generation.Prefill.Exact, input.Generation.Prefill.Fast = exact, "off"
		base := makeProfile(t, input)
		input.Generation.Prefill.Chunk++
		if base == makeProfile(t, input) {
			t.Fatal("used batched chunk was discarded")
		}
		input.Generation.Prefill.Chunk = 0
		if _, err := identity.NewProfileIdentity(input); !errors.Is(err, identity.ErrInvalidProfile) {
			t.Fatalf("invalid used chunk error=%v", err)
		}
	}
}

func TestProfileIdentityEffectiveProjection(t *testing.T) {
	input := profileInput()
	p := makeProfile(t, input)
	input.ModelID = "caller alias"
	input.Glossary = identity.GlossaryInput{Mode: "none"}
	if ok, err := p.MatchesEffective(input); err != nil || !ok {
		t.Fatalf("caller-only changes: match=%t err=%v", ok, err)
	}
	input.Generation.MaxOutputTokens++
	if ok, err := p.MatchesEffective(input); err != nil || ok {
		t.Fatalf("policy change: match=%t err=%v", ok, err)
	}
	if ok, err := p.MatchesModel(input); err != nil || !ok {
		t.Fatalf("no-generation projection: match=%t err=%v", ok, err)
	}
	input.Context.Tokens++
	if ok, err := p.MatchesModel(input); err != nil || ok {
		t.Fatalf("model context change: match=%t err=%v", ok, err)
	}
}
func TestProfileIdentityModelProjectionRetainsPolicySchema(t *testing.T) {
	input := profileInput()
	p := makeProfile(t, input)
	input.Generation.Schema++
	if ok, err := p.MatchesModel(input); err != nil || ok {
		t.Fatalf("generation schema change: match=%t err=%v", ok, err)
	}
}

func TestProfileIdentityRejectsInvalidInput(t *testing.T) {
	cases := map[string]func(*identity.ProfileInput){
		"unknown version": func(i *identity.ProfileInput) { i.Version = 2 }, "zero version": func(i *identity.ProfileInput) { i.Version = 0 },
		"missing model": func(i *identity.ProfileInput) { i.ModelID = "" }, "missing digest": func(i *identity.ProfileInput) { i.ModelDigest = [32]byte{} },
		"missing CUDA pin": func(i *identity.ProfileInput) { i.CUDABackendPin = "" }, "unknown backend": func(i *identity.ProfileInput) { i.Backend = "auto" },
		"missing tokenizer": func(i *identity.ProfileInput) { i.Tokenizer.BackendPin = "" }, "missing template": func(i *identity.ProfileInput) { i.Template.Family = "" },
		"missing prompt": func(i *identity.ProfileInput) { i.Prompt.UserTemplate = nil }, "missing policy": func(i *identity.ProfileInput) { i.Generation.Schema = 0 },
		"invalid output limit": func(i *identity.ProfileInput) { i.Generation.MaxOutputTokens = 0 }, "invalid timeout": func(i *identity.ProfileInput) { i.Generation.RequestTimeoutNS = 0 },
		"invalid context cap": func(i *identity.ProfileInput) { i.Context.Tokens = 0 }, "missing fixtures": func(i *identity.ProfileInput) { i.Validation.FixtureSet = "" },
		"missing segmentation": func(i *identity.ProfileInput) { i.Segmentation.Schema = "" }, "unknown segmentation mode": func(i *identity.ProfileInput) { i.Segmentation.Mode = "unknown" },
		"none segmentation conditions": func(i *identity.ProfileInput) { i.Segmentation.Mode = "none" }, "none glossary contents": func(i *identity.ProfileInput) { i.Glossary.Mode = "none" },
		"missing glossary contents": func(i *identity.ProfileInput) { i.Glossary.Entries = nil }, "unknown glossary mode": func(i *identity.ProfileInput) { i.Glossary.Mode = "unknown" },
		"none context contents": func(i *identity.ProfileInput) { i.Context.Mode = "none" }, "missing context contents": func(i *identity.ProfileInput) { i.Context.Additional = nil },
		"unknown context mode": func(i *identity.ProfileInput) { i.Context.Mode = "unknown" }, "invalid fast mode": func(i *identity.ProfileInput) { i.Generation.Prefill.Fast = "unknown" },
		"invalid fast chunk": func(i *identity.ProfileInput) { i.Generation.Prefill.Chunk = 0 }, "invalid fast floor": func(i *identity.ProfileInput) { i.Generation.Prefill.Floor = -1 },
		"invalid model UTF8": func(i *identity.ProfileInput) { i.ModelID = "\xff" }, "invalid template UTF8": func(i *identity.ProfileInput) { i.Template.Source = "\xff" },
		"invalid prompt UTF8": func(i *identity.ProfileInput) { i.Prompt.Variant = "\xff" }, "invalid prefill UTF8": func(i *identity.ProfileInput) { i.Generation.Prefill.Path = "\xff" },
		"invalid glossary UTF8": func(i *identity.ProfileInput) { i.Glossary.Schema = "\xff" }, "invalid segmentation UTF8": func(i *identity.ProfileInput) { i.Segmentation.BoundaryFixtures = "\xff" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			input := profileInput()
			change(&input)
			p, err := identity.NewProfileIdentity(input)
			if !errors.Is(err, identity.ErrInvalidProfile) || p != (identity.ProfileIdentity{}) {
				t.Fatalf("profile=%#v, err=%v", p, err)
			}
		})
	}
}
