package identity

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

const ProfileVersion = 1

var ErrInvalidProfile = errors.New("invalid canonical profile")

// ProfileInput contains only effective, import-wide translation conditions.
type ProfileInput struct {
	Version        int
	ModelID        string
	ModelDigest    [32]byte
	Backend        string
	BackendPin     string
	CUDABackendPin string
	ComputeQuant   string
	KVPrecision    string
	Tokenizer      TokenizerInput
	Template       TemplateInput
	Prompt         PromptInput
	Generation     GenerationInput
	Segmentation   SegmentationInput
	Glossary       GlossaryInput
	Protection     SchemaInput
	Validation     SchemaInput
	Context        ContextInput
}

type TokenizerInput struct {
	ModelDigest [32]byte
	BackendPin  string
}
type TemplateInput struct {
	Family          string `json:"family"`
	Source          string `json:"source"`
	RendererVersion string `json:"renderer_version"`
}
type PromptInput struct {
	Schema                 int    `json:"schema"`
	Variant                string `json:"variant"`
	SystemTemplate         []byte `json:"system_template"`
	UserTemplate           []byte `json:"user_template"`
	LanguageAliases        string `json:"language_aliases"`
	DisabledThinkingPrefix []byte `json:"disabled_thinking_prefix"`
}
type GenerationInput struct {
	Schema           int          `json:"schema"`
	MaxOutputTokens  int          `json:"max_output_tokens"`
	RequestTimeoutNS int64        `json:"request_timeout_ns"`
	Seed             int64        `json:"seed"`
	SeedFixed        bool         `json:"seed_fixed"`
	StopIDs          []int        `json:"stop_ids"`
	Thinking         string       `json:"thinking"`
	Prefill          PrefillInput `json:"prefill"`
}

// PrefillInput is a model-load policy snapshot. Path is the static cpu/cuda/none
// policy ID; it is neither a diagnostic reason nor a per-request execution path.
type PrefillInput struct {
	Exact             bool   `json:"exact"`
	Path              string `json:"path"`
	CPUFastAttention  bool   `json:"cpu_fast_attention"`
	Batched           bool   `json:"batched"`
	BatchedCapability bool   `json:"batched_capability"`
	Fast              string `json:"fast"`
	Floor             int    `json:"floor"`
	Chunk             int    `json:"chunk"`
}
type SegmentationInput struct {
	Mode             string `json:"mode"`
	Schema           string `json:"schema"`
	BoundaryFixtures string `json:"boundary_fixtures"`
	UnitTextBytes    int    `json:"unit_text_bytes"`
	Segments         int    `json:"segments"`
}
type GlossaryInput struct {
	Mode    string `json:"mode"`
	Schema  string `json:"schema"`
	Entries []byte `json:"entries"`
}
type SchemaInput struct {
	Schema     string `json:"schema"`
	FixtureSet string `json:"fixture_set"`
}

// Mode applies to Additional; Tokens is always the effective context cap.
type ContextInput struct {
	Tokens     int    `json:"tokens"`
	Mode       string `json:"mode"`
	Additional []byte `json:"additional"`
}
type tokenizerRecord struct {
	ModelDigest string `json:"model_digest"`
	BackendPin  string `json:"backend_pin"`
}
type profileRecord struct {
	Version        int               `json:"version"`
	ModelID        string            `json:"model_id"`
	ModelDigest    string            `json:"model_digest"`
	Backend        string            `json:"backend"`
	BackendPin     string            `json:"backend_pin"`
	CUDABackendPin string            `json:"cuda_backend_pin"`
	ComputeQuant   string            `json:"compute_quant"`
	KVPrecision    string            `json:"kv_precision"`
	Tokenizer      tokenizerRecord   `json:"tokenizer"`
	Template       TemplateInput     `json:"template"`
	Prompt         PromptInput       `json:"prompt"`
	Generation     GenerationInput   `json:"generation"`
	Segmentation   SegmentationInput `json:"segmentation"`
	Glossary       GlossaryInput     `json:"glossary"`
	Protection     SchemaInput       `json:"protection"`
	Validation     SchemaInput       `json:"validation"`
	Context        ContextInput      `json:"context"`
}

// ProfileIdentity is immutable and comparable, including its canonical bytes.
type ProfileIdentity struct {
	canonical                                     string
	effective                                     string
	model                                         string
	digest                                        [32]byte
	segmentationSchema, boundaryFixtures          string
	segmentation                                  SegmentationInput
	protection, validation                        SchemaInput
	glossaryMode, glossarySchema, glossaryEntries string
}

func NewProfileIdentity(input ProfileInput) (ProfileIdentity, error) {
	if input.Version != ProfileVersion {
		return ProfileIdentity{}, fmt.Errorf("%w: unsupported version %d", ErrInvalidProfile, input.Version)
	}
	if err := checkProfile(input); err != nil {
		return ProfileIdentity{}, err
	}
	// Inactive backend settings cannot change the effective identity.
	prefill := &input.Generation.Prefill
	if input.Backend == "cpu" {
		input.CUDABackendPin = ""
		prefill.Batched, prefill.BatchedCapability = false, false
		prefill.Fast, prefill.Floor, prefill.Chunk = "off", 0, 0
	} else {
		prefill.CPUFastAttention = false
	}
	if prefill.Exact {
		prefill.CPUFastAttention = false
		prefill.Fast, prefill.Floor = "off", 0
	}
	if prefill.Fast == "off" {
		prefill.Floor = 0
	}
	if input.Backend == "cuda" && (!prefill.Batched || !prefill.BatchedCapability) {
		prefill.Fast, prefill.Floor, prefill.Chunk = "off", 0, 0
	}
	input.Prompt.SystemTemplate = append([]byte{}, input.Prompt.SystemTemplate...)
	input.Prompt.UserTemplate = append([]byte{}, input.Prompt.UserTemplate...)
	input.Prompt.DisabledThinkingPrefix = append([]byte{}, input.Prompt.DisabledThinkingPrefix...)
	input.Generation.StopIDs = append([]int{}, input.Generation.StopIDs...)
	input.Glossary.Entries = append([]byte{}, input.Glossary.Entries...)
	input.Context.Additional = append([]byte{}, input.Context.Additional...)
	record := profileRecord{
		input.Version, input.ModelID, digestHex(input.ModelDigest), input.Backend, input.BackendPin, input.CUDABackendPin, input.ComputeQuant, input.KVPrecision,
		tokenizerRecord{digestHex(input.Tokenizer.ModelDigest), input.Tokenizer.BackendPin}, input.Template, input.Prompt, input.Generation,
		input.Segmentation, input.Glossary, input.Protection, input.Validation, input.Context,
	}
	canonical, err := json.Marshal(record)
	if err != nil {
		return ProfileIdentity{}, fmt.Errorf("marshal profile identity: %w", err)
	}
	record.ModelID, record.Glossary = "", GlossaryInput{Mode: "none", Entries: []byte{}}
	effective, err := json.Marshal(record)
	if err != nil {
		return ProfileIdentity{}, fmt.Errorf("marshal effective profile conditions: %w", err)
	}
	record.Generation = GenerationInput{Schema: record.Generation.Schema, Thinking: record.Generation.Thinking, Prefill: record.Generation.Prefill}
	model, err := json.Marshal(record)
	if err != nil {
		return ProfileIdentity{}, fmt.Errorf("marshal model profile conditions: %w", err)
	}
	return ProfileIdentity{canonical: string(canonical), effective: string(effective), model: string(model), digest: sha256.Sum256(canonical), segmentationSchema: input.Segmentation.Schema, boundaryFixtures: input.Segmentation.BoundaryFixtures, segmentation: input.Segmentation, protection: input.Protection, validation: input.Validation, glossaryMode: input.Glossary.Mode, glossarySchema: input.Glossary.Schema, glossaryEntries: string(input.Glossary.Entries)}, nil
}

// MatchesEntry checks common Core conditions even when no model is connected.
func (p ProfileIdentity) MatchesEntry(segmentation SegmentationInput, protection, validation SchemaInput) bool {
	return p.canonical != "" && p.segmentation == segmentation && p.protection == protection && p.validation == validation
}

// MatchesEffective compares runtime-owned conditions without decoding canonical
// bytes. ModelID and Glossary remain caller-owned and are excluded here only.
func (p ProfileIdentity) MatchesEffective(input ProfileInput) (bool, error) {
	input.ModelID, input.Glossary = "effective", GlossaryInput{Mode: "none"}
	other, err := NewProfileIdentity(input)
	if err != nil {
		return false, err
	}
	return p.effective != "" && p.effective == other.effective, nil
}

// MatchesModel excludes request-specific generation budgets, seed and stops for
// imports with no generation, while retaining thinking and model-load prefill.
func (p ProfileIdentity) MatchesModel(input ProfileInput) (bool, error) {
	input.ModelID, input.Glossary = "effective", GlossaryInput{Mode: "none"}
	input.Generation = GenerationInput{Schema: input.Generation.Schema, MaxOutputTokens: 1, RequestTimeoutNS: 1, Thinking: input.Generation.Thinking, Prefill: input.Generation.Prefill}
	other, err := NewProfileIdentity(input)
	if err != nil {
		return false, err
	}
	return p.model != "" && p.model == other.model, nil
}

// Glossary returns the exact caller-owned definition with detached entry bytes.
func (p ProfileIdentity) Glossary() GlossaryInput {
	return GlossaryInput{Mode: p.glossaryMode, Schema: p.glossarySchema, Entries: []byte(p.glossaryEntries)}
}

func (p ProfileIdentity) CanonicalBytes() []byte { return []byte(p.canonical) }
func (p ProfileIdentity) Digest() [32]byte       { return p.digest }
func (p ProfileIdentity) Segmentation() (schema, boundaryFixtures string) {
	return p.segmentationSchema, p.boundaryFixtures
}

func checkProfile(i ProfileInput) error {
	required := []string{i.ModelID, i.BackendPin, i.ComputeQuant, i.KVPrecision, i.Tokenizer.BackendPin, i.Template.Family, i.Template.Source, i.Template.RendererVersion, i.Prompt.Variant, i.Prompt.LanguageAliases, i.Generation.Thinking, i.Generation.Prefill.Path, i.Protection.Schema, i.Protection.FixtureSet, i.Validation.Schema, i.Validation.FixtureSet}
	for _, field := range required {
		if field == "" {
			return fmt.Errorf("%w: missing effective identity field", ErrInvalidProfile)
		}
	}
	strings := append(required, i.Backend, i.CUDABackendPin, i.Generation.Prefill.Fast, i.Segmentation.Mode, i.Segmentation.Schema, i.Segmentation.BoundaryFixtures, i.Glossary.Mode, i.Glossary.Schema, i.Context.Mode)
	for _, field := range strings {
		if !utf8.ValidString(field) {
			return fmt.Errorf("%w: invalid UTF-8 identity field", ErrInvalidProfile)
		}
	}
	if i.ModelDigest == ([32]byte{}) || i.Tokenizer.ModelDigest == ([32]byte{}) || i.Prompt.Schema <= 0 || len(i.Prompt.SystemTemplate) == 0 || len(i.Prompt.UserTemplate) == 0 || i.Generation.Schema <= 0 || i.Generation.MaxOutputTokens <= 0 || i.Generation.RequestTimeoutNS <= 0 || i.Context.Tokens <= 0 {
		return fmt.Errorf("%w: missing model, prompt or generation conditions", ErrInvalidProfile)
	}
	switch i.Generation.Prefill.Path {
	case "cpu", "cuda", "none":
	default:
		return fmt.Errorf("%w: invalid prefill policy ID", ErrInvalidProfile)
	}
	switch i.Backend {
	case "cpu":
	case "cuda":
		if i.CUDABackendPin == "" {
			return fmt.Errorf("%w: missing CUDA release", ErrInvalidProfile)
		}
		switch i.Generation.Prefill.Fast {
		case "off", "attn", "gemm", "both":
		default:
			return fmt.Errorf("%w: invalid fast prefill mode", ErrInvalidProfile)
		}
		if i.Generation.Prefill.Batched && i.Generation.Prefill.BatchedCapability && i.Generation.Prefill.Chunk <= 0 {
			return fmt.Errorf("%w: invalid batched prefill chunk", ErrInvalidProfile)
		}
		if i.Generation.Prefill.Batched && i.Generation.Prefill.BatchedCapability && !i.Generation.Prefill.Exact && i.Generation.Prefill.Fast != "off" && i.Generation.Prefill.Floor < 0 {
			return fmt.Errorf("%w: invalid fast prefill limits", ErrInvalidProfile)
		}
	default:
		return fmt.Errorf("%w: unsupported backend", ErrInvalidProfile)
	}
	switch i.Segmentation.Mode {
	case "none":
		if i.Segmentation != (SegmentationInput{Mode: "none"}) {
			return fmt.Errorf("%w: disabled segmentation has conditions", ErrInvalidProfile)
		}
	case "enabled":
		if i.Segmentation.Schema == "" || i.Segmentation.BoundaryFixtures == "" || i.Segmentation.UnitTextBytes <= 0 || i.Segmentation.Segments <= 0 {
			return fmt.Errorf("%w: missing segmentation conditions", ErrInvalidProfile)
		}
	default:
		return fmt.Errorf("%w: invalid segmentation mode", ErrInvalidProfile)
	}
	switch i.Glossary.Mode {
	case "none":
		if i.Glossary.Schema != "" || len(i.Glossary.Entries) != 0 {
			return fmt.Errorf("%w: unused glossary has contents", ErrInvalidProfile)
		}
	case "provided":
		if i.Glossary.Schema == "" || len(i.Glossary.Entries) == 0 {
			return fmt.Errorf("%w: missing glossary contents", ErrInvalidProfile)
		}
	default:
		return fmt.Errorf("%w: invalid glossary mode", ErrInvalidProfile)
	}
	switch i.Context.Mode {
	case "none":
		if len(i.Context.Additional) != 0 {
			return fmt.Errorf("%w: unused context has contents", ErrInvalidProfile)
		}
	case "provided":
		if len(i.Context.Additional) == 0 {
			return fmt.Errorf("%w: missing additional context", ErrInvalidProfile)
		}
	default:
		return fmt.Errorf("%w: invalid context mode", ErrInvalidProfile)
	}
	return nil
}
