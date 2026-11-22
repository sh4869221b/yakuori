package contextpack

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/unit"
)

const (
	InferTimeout      = 300 * time.Second
	inferRequestTime  = 30 * time.Second
	inferContextLimit = 4096
	inferOutputTokens = 2048
	inferBytes        = 16 << 10
	inferPolicy       = "context-infer-policy-v1"
)

var errInvalidInference = errors.New("invalid context inference response")

// ContextEngine prepares a local context request independently of translation.
type ContextEngine interface {
	inference.Engine
	NewContextRequest(context.Context, string, inference.GenerationPolicy) (inference.GenerationRequest, error)
}

// Infer enriches the validated structural build with candidates from its import.
// The caller owns the engine and closes it before publishing the result.
func Infer(ctx context.Context, doc *Document, session unit.Session, modelID string, engine ContextEngine) (*Document, error) {
	pack := doc.Pack
	evidence, data, err := selectInferenceMaterial(pack, session)
	if err != nil {
		return nil, err
	}
	info, err := engine.Info(ctx)
	if err != nil || info.RemoteModel != "" || info.Template.Family != "chatml" || !nonblank(info.Template.Source) {
		return nil, errors.New("context inference requires a supported local ChatML model")
	}
	origin := GenerationOrigin{
		ModelID: modelID, ModelSHA256: hex.EncodeToString(info.ModelSHA256[:]),
		PromptVersion: prompt.ContextVersion, TemplateFamily: info.Template.Family,
		TemplateSource: "gguf:tokenizer.chat_template", TemplateRendererVersion: info.Template.RendererVersion,
		PolicyVersion: inferPolicy,
	}
	if !origin.valid() {
		return nil, errors.New("invalid context inference model identity")
	}

	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, inferOutputTokens, inferRequestTime)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, inferRequestTime)
	defer cancel()
	request, err := engine.NewContextRequest(requestCtx, string(data), policy)
	if err != nil {
		return nil, errors.New("cannot prepare context inference request")
	}
	n, err := engine.CountTokens(requestCtx, request)
	if err != nil || n <= 0 {
		return nil, errors.New("cannot count context inference tokens")
	}
	if n > min(info.ContextTokens, inferContextLimit)-inferOutputTokens {
		return nil, errors.New("context inference request exceeds token budget")
	}
	result, err := engine.Generate(requestCtx, request)
	if requestCtx.Err() != nil {
		return nil, errors.New("context inference canceled or timed out")
	}
	switch result.Finish {
	case inference.Timeout, inference.Canceled, inference.MaxTokens, inference.ContextLimit:
		return nil, fmt.Errorf("context inference stopped: %s", result.Finish)
	}
	if err != nil || result.Finish != inference.Stop || result.OutputTokens <= 0 || result.OutputTokens >= inferOutputTokens {
		return nil, errors.New("context inference did not complete")
	}
	return decodeInference(pack, evidence, origin, result.Text)
}

type inferenceUnit struct {
	unit         Unit
	Handle       int    `json:"unit"`
	Source       string `json:"source"`
	Speaker      int    `json:"speaker,omitempty"`
	SpeakerLabel string `json:"speaker_label,omitempty"`
	Scene        int    `json:"scene,omitempty"`
	SceneLabel   string `json:"scene_label,omitempty"`
}

func selectInferenceMaterial(pack Pack, session unit.Session) ([]inferenceUnit, []byte, error) {
	speakers := make(map[string]int, len(pack.Characters))
	for i, c := range pack.Characters {
		speakers[c.ID] = i + 1
	}
	scenes := make(map[string]int, len(pack.Scenes))
	for i, s := range pack.Scenes {
		scenes[s.ID] = i + 1
	}
	evidence := make([]inferenceUnit, 0, len(pack.Units))
	for _, record := range pack.Units {
		id, err := unit.NewUnitID(record.Adapter, record.FormatSchema, record.StableID)
		if err != nil {
			return nil, nil, errors.New("invalid build unit identity")
		}
		translation, err := session.Unit(id)
		if err != nil {
			return nil, nil, errors.New("missing build unit in import")
		}
		text := string(translation.Source())
		if strings.TrimSpace(text) == "" {
			continue
		}
		origin := translation.Origin()
		evidence = append(evidence, inferenceUnit{
			unit: record, Handle: len(evidence) + 1, Source: text,
			Speaker: speakers[value(record.SpeakerID)], SpeakerLabel: origin["speaker"],
			Scene: scenes[value(record.SceneID)], SceneLabel: origin["scene"],
		})
	}
	// Encode HTML-sensitive characters too, keeping template markers inside
	// the literal user data. Embedded newlines cannot close the data delimiter.
	data, err := json.Marshal(struct {
		SourceLanguage string          `json:"source_language"`
		TargetLanguage string          `json:"target_language"`
		Units          []inferenceUnit `json:"units"`
	}{pack.SourceLanguage, pack.TargetLanguage, evidence})
	if err != nil || len(data) > inferBytes {
		return nil, nil, errors.New("context inference data exceeds byte budget")
	}
	return evidence, data, nil
}

type inferredCandidate struct {
	Kind     string  `json:"kind"`
	Entity   *int    `json:"entity"`
	Name     *string `json:"name"`
	Text     *string `json:"text"`
	Source   *string `json:"source"`
	Target   *string `json:"target"`
	Evidence []int   `json:"evidence"`
}

func decodeInference(pack Pack, evidence []inferenceUnit, origin GenerationOrigin, text string) (*Document, error) {
	if len(text) > inferBytes {
		return nil, errors.New("context inference response exceeds byte budget")
	}
	raw := []byte(text)
	var response struct {
		Candidates []inferredCandidate `json:"candidates"`
	}
	// The pack scanner rejects nulls, duplicate keys, and incomplete JSON.
	if checkJSON(raw) != nil || jsonv2.Unmarshal(raw, &response, jsonv2.RejectUnknownMembers(true)) != nil || response.Candidates == nil {
		return nil, errInvalidInference
	}

	// The new pack can enrich structural records without mutating the input
	// document or removing their original metadata sources and links.
	pack.Characters, pack.Scenes = slices.Clone(pack.Characters), slices.Clone(pack.Scenes)
	pack.World, pack.GlossaryRefs = slices.Clone(pack.World), slices.Clone(pack.GlossaryRefs)
	characters := make(map[int]struct{})
	scenes := make(map[int]struct{})
	for i, c := range response.Candidates {
		if !c.validFields() {
			return nil, invalid(position("candidates", i), "invalid fields")
		}
		sources, anchor, ok := c.resolveEvidence(evidence)
		if !ok {
			return nil, invalid(position("candidates", i), "invalid unit evidence")
		}
		edited := false
		provenance := Provenance{Method: "llm", Version: prompt.ContextVersion, Status: "candidate", UserEdited: &edited, Sources: sources, Generation: &origin}
		switch c.Kind {
		case "world":
			pack.World = append(pack.World, World{
				ID: "world-inferred/" + strings.TrimPrefix(anchor.ID, "unit/"), Text: *c.Text,
				Artifacts: []string{anchor.ArtifactNamespace}, Provenance: provenance,
			})
		case "character":
			if !addUnique(characters, *c.Entity) {
				return nil, invalid(position("candidates", i), "duplicate character proposal")
			}
			record := &pack.Characters[*c.Entity-1]
			record.Name, record.Description = c.Name, c.Text
			provenance.Sources = append(provenance.Sources, record.Provenance.Sources...)
			record.Provenance = provenance
		case "scene":
			if !addUnique(scenes, *c.Entity) {
				return nil, invalid(position("candidates", i), "duplicate scene proposal")
			}
			record := &pack.Scenes[*c.Entity-1]
			record.Summary = c.Text
			provenance.Sources = append(provenance.Sources, record.Provenance.Sources...)
			record.Provenance = provenance
		case "glossary":
			term := url.PathEscape(*c.Source)
			pack.GlossaryRefs = append(pack.GlossaryRefs, GlossaryRef{
				ID:     "glossary-candidate/" + url.PathEscape(anchor.ArtifactNamespace) + "/" + term,
				Schema: glossary.Schema, Reference: "urn:yakuori:glossary-candidate:v1:" + term + "/" + url.PathEscape(*c.Target),
				Provenance: provenance,
			})
		}
	}
	raw, err := json.Marshal(pack)
	if err != nil {
		return nil, errInvalidInference
	}
	return Read(bytes.NewReader(raw))
}

func (c inferredCandidate) validFields() bool {
	if len(c.Evidence) == 0 {
		return false
	}
	for _, field := range []*string{c.Text, c.Name, c.Source, c.Target} {
		if field != nil && !nonblank(*field) {
			return false
		}
	}
	switch c.Kind {
	case "world":
		return c.Text != nil && c.Entity == nil && c.Name == nil && c.Source == nil && c.Target == nil
	case "character":
		return c.Entity != nil && (c.Name != nil || c.Text != nil) && c.Source == nil && c.Target == nil
	case "scene":
		return c.Entity != nil && c.Text != nil && c.Name == nil && c.Source == nil && c.Target == nil
	case "glossary":
		return c.Source != nil && c.Target != nil && c.Entity == nil && c.Name == nil && c.Text == nil
	}
	return false
}

func (c inferredCandidate) resolveEvidence(evidence []inferenceUnit) ([]Source, Unit, bool) {
	seen := make(map[int]struct{}, len(c.Evidence))
	sources := make([]Source, 0, len(c.Evidence))
	anchor := len(evidence) + 1
	glossaryMatch := false
	for _, handle := range c.Evidence {
		if handle < 1 || handle > len(evidence) || !addUnique(seen, handle) {
			return nil, Unit{}, false
		}
		e := evidence[handle-1]
		if (c.Kind == "character" && (e.Speaker == 0 || e.Speaker != *c.Entity)) ||
			(c.Kind == "scene" && (e.Scene == 0 || e.Scene != *c.Entity)) {
			return nil, Unit{}, false
		}
		if c.Source != nil && strings.Contains(e.Source, *c.Source) {
			glossaryMatch = true
		}
		anchor = min(anchor, handle)
		id := e.unit.ID
		sources = append(sources, Source{Kind: "unit", UnitID: &id})
	}
	if c.Source != nil && !glossaryMatch {
		return nil, Unit{}, false
	}
	return sources, evidence[anchor-1].unit, true
}
