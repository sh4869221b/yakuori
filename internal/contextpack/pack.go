// Package contextpack reads and validates reusable context separate from translation data.
package contextpack

import (
	"cmp"
	"crypto/sha256"
	"slices"
)

const Schema = "yakuori.context/v1"

// The wire structs' field order is part of the v1 canonical representation.
type Pack struct {
	Schema         string        `json:"schema"`
	Scope          Scope         `json:"scope"`
	SourceLanguage string        `json:"source_language"`
	TargetLanguage string        `json:"target_language"`
	World          []World       `json:"world,omitempty"`
	Characters     []Character   `json:"characters,omitempty"`
	Scenes         []Scene       `json:"scenes,omitempty"`
	Units          []Unit        `json:"units,omitempty"`
	GlossaryRefs   []GlossaryRef `json:"glossary_refs,omitempty"`
}

type Scope struct {
	GameNamespace string     `json:"game_namespace"`
	Artifacts     []Artifact `json:"artifacts,omitempty"`
}

type Artifact struct {
	ArtifactNamespace      string `json:"artifact_namespace"`
	OriginalArtifactDigest string `json:"original_artifact_digest"`
}

type World struct {
	ID         string     `json:"id"`
	Text       string     `json:"text"`
	Artifacts  []string   `json:"artifacts,omitempty"`
	Tags       []string   `json:"tags,omitempty"`
	Provenance Provenance `json:"provenance"`
}

type Character struct {
	ID          string     `json:"id"`
	Name        *string    `json:"name,omitempty"`
	Description *string    `json:"description,omitempty"`
	Artifacts   []string   `json:"artifacts,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
	Provenance  Provenance `json:"provenance"`
}

type Scene struct {
	ID         string     `json:"id"`
	Summary    *string    `json:"summary,omitempty"`
	Artifacts  []string   `json:"artifacts,omitempty"`
	SpeakerIDs []string   `json:"speaker_ids,omitempty"`
	Tags       []string   `json:"tags,omitempty"`
	Provenance Provenance `json:"provenance"`
}

type Unit struct {
	ID                string     `json:"id"`
	ArtifactNamespace string     `json:"artifact_namespace"`
	Adapter           string     `json:"adapter"`
	FormatSchema      string     `json:"format_schema"`
	StableID          string     `json:"stable_id"`
	RawSourceDigest   string     `json:"raw_source_digest"`
	SceneID           *string    `json:"scene_id,omitempty"`
	SpeakerID         *string    `json:"speaker_id,omitempty"`
	PreviousUnitID    *string    `json:"previous_unit_id,omitempty"`
	Tags              []string   `json:"tags,omitempty"`
	Provenance        Provenance `json:"provenance"`
}

type GlossaryRef struct {
	ID         string     `json:"id"`
	Schema     string     `json:"schema"`
	Reference  string     `json:"reference"`
	Provenance Provenance `json:"provenance"`
}

type Provenance struct {
	Method     string            `json:"method"`
	Version    string            `json:"version"`
	Status     string            `json:"status"`
	UserEdited *bool             `json:"user_edited,omitempty"`
	Sources    []Source          `json:"sources,omitempty"`
	Generation *GenerationOrigin `json:"generation,omitempty"`
}

// GenerationOrigin identifies the local model and request that made a proposal.
type GenerationOrigin struct {
	ModelID                 string `json:"model_id"`
	ModelSHA256             string `json:"model_sha256"`
	PromptVersion           string `json:"prompt_version"`
	TemplateFamily          string `json:"template_family"`
	TemplateSource          string `json:"template_source"`
	TemplateRendererVersion string `json:"template_renderer_version"`
	PolicyVersion           string `json:"policy_version"`
}

type Source struct {
	Kind              string  `json:"kind"`
	ArtifactNamespace *string `json:"artifact_namespace,omitempty"`
	UnitID            *string `json:"unit_id,omitempty"`
	MetadataKey       *string `json:"metadata_key,omitempty"`
	Reference         *string `json:"reference,omitempty"`
}

// Document contains the ordered pack, canonical JSON and its SHA-256 fingerprint
// at the time it was read.
type Document struct {
	Pack        Pack
	Canonical   []byte
	Fingerprint [sha256.Size]byte
}

func (p *Pack) sortSets() {
	slices.SortFunc(p.Scope.Artifacts, func(a, b Artifact) int { return cmp.Compare(a.ArtifactNamespace, b.ArtifactNamespace) })
	slices.SortFunc(p.World, func(a, b World) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(p.Characters, func(a, b Character) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(p.Scenes, func(a, b Scene) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(p.Units, func(a, b Unit) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(p.GlossaryRefs, func(a, b GlossaryRef) int { return cmp.Compare(a.ID, b.ID) })
	for i := range p.World {
		w := &p.World[i]
		slices.Sort(w.Artifacts)
		slices.Sort(w.Tags)
		w.Provenance.sortSources()
	}
	for i := range p.Characters {
		c := &p.Characters[i]
		slices.Sort(c.Artifacts)
		slices.Sort(c.Tags)
		c.Provenance.sortSources()
	}
	for i := range p.Scenes {
		s := &p.Scenes[i]
		slices.Sort(s.Artifacts)
		slices.Sort(s.SpeakerIDs)
		slices.Sort(s.Tags)
		s.Provenance.sortSources()
	}
	for i := range p.Units {
		u := &p.Units[i]
		slices.Sort(u.Tags)
		u.Provenance.sortSources()
	}
	for i := range p.GlossaryRefs {
		p.GlossaryRefs[i].Provenance.sortSources()
	}
}

func (p *Provenance) sortSources() {
	slices.SortFunc(p.Sources, func(a, b Source) int {
		ak, bk := a.key(), b.key()
		return slices.Compare(ak[:], bk[:])
	})
}

func (s Source) key() [5]string {
	return [5]string{s.Kind, value(s.ArtifactNamespace), value(s.UnitID), value(s.MetadataKey), value(s.Reference)}
}

func value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
