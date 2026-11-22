package contextpack

import (
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/unit"
	"golang.org/x/text/language"
)

func validate(p Pack) error {
	if p.Schema != Schema {
		return errors.New("unsupported context pack schema")
	}
	if !nonblank(p.Scope.GameNamespace) {
		return errors.New("invalid scope.game_namespace")
	}
	if !validLanguage(p.SourceLanguage) {
		return errors.New("invalid source_language")
	}
	if !validLanguage(p.TargetLanguage) {
		return errors.New("invalid target_language")
	}
	artifacts := make(map[string]struct{}, len(p.Scope.Artifacts))
	for i, a := range p.Scope.Artifacts {
		at := position("scope.artifacts", i)
		if !nonblank(a.ArtifactNamespace) || !validDigest(a.OriginalArtifactDigest) {
			return invalid(at, "invalid artifact namespace or digest")
		}
		if !addUnique(artifacts, a.ArtifactNamespace) {
			return invalid(at, "duplicate artifact namespace")
		}
	}

	ids := make(map[string]struct{})
	units := make(map[string]int, len(p.Units))
	type unitKey struct {
		artifact string
		id       unit.UnitID
	}
	unitKeys := make(map[unitKey]struct{}, len(p.Units))
	for i, u := range p.Units {
		at := position("units", i)
		if err := registerID(ids, u.ID, at); err != nil {
			return err
		}
		if _, ok := artifacts[u.ArtifactNamespace]; !ok {
			return invalid(at, "unresolved artifact namespace")
		}
		id, err := unit.NewUnitID(u.Adapter, u.FormatSchema, u.StableID)
		if err != nil || !validDigest(u.RawSourceDigest) {
			return invalid(at, "invalid stable unit identity or digest")
		}
		if !addUnique(unitKeys, unitKey{u.ArtifactNamespace, id}) {
			return invalid(at, "duplicate stable unit in artifact")
		}
		units[u.ID] = i
	}

	for i, w := range p.World {
		at := position("world", i)
		if err := registerID(ids, w.ID, at); err != nil {
			return err
		}
		if !nonblank(w.Text) {
			return invalid(at, "missing world text")
		}
		if err := validateEntry(w.Artifacts, w.Tags, w.Provenance, artifacts, units, at); err != nil {
			return err
		}
	}
	characters := make(map[string]Character, len(p.Characters))
	for i, c := range p.Characters {
		at := position("characters", i)
		if err := registerID(ids, c.ID, at); err != nil {
			return err
		}
		if c.Name != nil && !nonblank(*c.Name) {
			return invalid(at, "invalid character name")
		}
		if c.Description != nil && !nonblank(*c.Description) {
			return invalid(at, "invalid character description")
		}
		if err := validateEntry(c.Artifacts, c.Tags, c.Provenance, artifacts, units, at); err != nil {
			return err
		}
		characters[c.ID] = c
	}
	scenes := make(map[string]Scene, len(p.Scenes))
	for i, s := range p.Scenes {
		at := position("scenes", i)
		if err := registerID(ids, s.ID, at); err != nil {
			return err
		}
		if s.Summary != nil && !nonblank(*s.Summary) {
			return invalid(at, "invalid scene summary")
		}
		if err := validateEntry(s.Artifacts, s.Tags, s.Provenance, artifacts, units, at); err != nil {
			return err
		}
		if !uniqueStrings(s.SpeakerIDs) {
			return invalid(at, "invalid or duplicate scene speaker reference")
		}
		for _, id := range s.SpeakerIDs {
			c, ok := characters[id]
			if !ok {
				return invalid(at, "unresolved scene speaker reference")
			}
			for _, artifact := range s.Artifacts {
				if !slices.Contains(c.Artifacts, artifact) {
					return invalid(at, "scene scope exceeds speaker scope")
				}
			}
		}
		scenes[s.ID] = s
	}
	for i, u := range p.Units {
		at := position("units", i)
		if err := validateEntry(nil, u.Tags, u.Provenance, artifacts, units, at); err != nil {
			return err
		}
		if err := validateUnitLinks(u, p.Units, units, characters, scenes, at); err != nil {
			return err
		}
	}
	if err := validatePreviousChains(p.Units, units); err != nil {
		return err
	}
	for i, g := range p.GlossaryRefs {
		at := position("glossary_refs", i)
		if err := registerID(ids, g.ID, at); err != nil {
			return err
		}
		if g.Schema != glossary.Schema || !nonblank(g.Reference) {
			return invalid(at, "invalid glossary schema or reference")
		}
		if err := validateProvenance(g.Provenance, artifacts, units, at); err != nil {
			return err
		}
	}
	return nil
}

func validateUnitLinks(u Unit, all []Unit, units map[string]int, characters map[string]Character, scenes map[string]Scene, at string) error {
	if u.SpeakerID != nil {
		c, ok := characters[*u.SpeakerID]
		if !ok || !slices.Contains(c.Artifacts, u.ArtifactNamespace) {
			return invalid(at, "unresolved or out-of-scope unit speaker")
		}
	}
	if u.SceneID != nil {
		s, ok := scenes[*u.SceneID]
		if !ok || !slices.Contains(s.Artifacts, u.ArtifactNamespace) {
			return invalid(at, "unresolved or out-of-scope unit scene")
		}
		if u.SpeakerID != nil && len(s.SpeakerIDs) > 0 && !slices.Contains(s.SpeakerIDs, *u.SpeakerID) {
			return invalid(at, "unit speaker conflicts with scene speakers")
		}
	}
	if u.PreviousUnitID != nil {
		i, ok := units[*u.PreviousUnitID]
		if !ok {
			return invalid(at, "unresolved previous unit reference")
		}
		previous := all[i]
		if previous.ArtifactNamespace != u.ArtifactNamespace ||
			(u.SceneID != nil && previous.SceneID != nil && *u.SceneID != *previous.SceneID) {
			return invalid(at, "previous unit crosses artifact or scene scope")
		}
	}
	return nil
}

func validatePreviousChains(all []Unit, units map[string]int) error {
	const (
		visiting = 1
		checked  = 2
	)
	state := make([]uint8, len(all))
	var path []int
	// Each unit has at most one outgoing edge. Visit it once and reuse
	// checked paths so that a long valid chain costs linear time.
	for i := range all {
		path = path[:0]
		for j := i; state[j] != checked; j = units[*all[j].PreviousUnitID] {
			if state[j] == visiting {
				return invalid(position("units", j), "previous unit cycle")
			}
			state[j] = visiting
			path = append(path, j)
			if all[j].PreviousUnitID == nil {
				break
			}
		}
		for _, j := range path {
			state[j] = checked
		}
	}
	return nil
}

func validateEntry(scope, tags []string, p Provenance, artifacts map[string]struct{}, units map[string]int, at string) error {
	if !uniqueStrings(scope) {
		return invalid(at, "invalid or duplicate artifact reference")
	}
	for _, artifact := range scope {
		if _, ok := artifacts[artifact]; !ok {
			return invalid(at, "unresolved artifact reference")
		}
	}
	if !uniqueStrings(tags) {
		return invalid(at, "invalid or duplicate tag")
	}
	return validateProvenance(p, artifacts, units, at)
}

func validateProvenance(p Provenance, artifacts map[string]struct{}, units map[string]int, at string) error {
	loc := at + ".provenance"
	switch p.Method {
	case "manual", "extraction", "llm":
	default:
		return invalid(loc, "unknown creation method")
	}
	if !nonblank(p.Version) || p.UserEdited == nil {
		return invalid(loc, "missing version or user_edited")
	}
	switch p.Status {
	case "confirmed":
		if len(p.Sources) == 0 {
			return invalid(loc, "confirmed record requires a source")
		}
	case "candidate":
	default:
		return invalid(loc, "unknown confirmation status")
	}
	if p.Generation != nil {
		if p.Method != "llm" || !p.Generation.valid() || !slices.ContainsFunc(p.Sources, func(s Source) bool { return s.Kind == "unit" }) {
			return invalid(loc, "generation requires valid identity and unit evidence for an llm record")
		}
	}
	seen := make(map[[5]string]struct{}, len(p.Sources))
	for i, s := range p.Sources {
		at := position(loc+".sources", i)
		if !s.validFields() {
			return invalid(at, "invalid source kind or fields")
		}
		if s.ArtifactNamespace != nil {
			if _, ok := artifacts[*s.ArtifactNamespace]; !ok {
				return invalid(at, "unresolved source artifact")
			}
		}
		if s.UnitID != nil {
			if _, ok := units[*s.UnitID]; !ok {
				return invalid(at, "unresolved source unit")
			}
		}
		if !addUnique(seen, s.key()) {
			return invalid(at, "duplicate source")
		}
	}
	return nil
}

func (g GenerationOrigin) valid() bool {
	return config.ValidateModelID(g.ModelID) == nil && validDigest(g.ModelSHA256) &&
		nonblank(g.PromptVersion) && nonblank(g.TemplateFamily) && nonblank(g.TemplateSource) &&
		nonblank(g.TemplateRendererVersion) && nonblank(g.PolicyVersion)
}

func (s Source) validFields() bool {
	switch s.Kind {
	case "artifact":
		return nonblank(value(s.ArtifactNamespace)) && s.UnitID == nil && s.MetadataKey == nil && s.Reference == nil
	case "unit":
		return s.ArtifactNamespace == nil && nonblank(value(s.UnitID)) && s.MetadataKey == nil && s.Reference == nil
	case "metadata":
		return nonblank(value(s.ArtifactNamespace)) && s.UnitID == nil && nonblank(value(s.MetadataKey)) && s.Reference == nil
	case "manual":
		return s.ArtifactNamespace == nil && s.UnitID == nil && s.MetadataKey == nil && nonblank(value(s.Reference))
	default:
		return false
	}
}

func validLanguage(s string) bool {
	// V1 accepts registered language, script and region tags. Restrict the
	// subtags explicitly because Parse can accept and rewrite other forms.
	tag, err := language.Parse(s)
	return err == nil && len(tag.Variants()) == 0 && len(tag.Extensions()) == 0 && strings.EqualFold(s, tag.String())
}

func validDigest(s string) bool {
	var digest [32]byte
	if len(s) != hex.EncodedLen(len(digest)) || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.Decode(digest[:], []byte(s))
	return err == nil && digest != [32]byte{}
}

func nonblank(s string) bool { return strings.TrimSpace(s) != "" && !strings.ContainsRune(s, 0) }

func uniqueStrings(items []string) bool {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if !nonblank(item) || !addUnique(seen, item) {
			return false
		}
	}
	return true
}

func addUnique[K comparable](seen map[K]struct{}, key K) bool {
	if _, ok := seen[key]; ok {
		return false
	}
	seen[key] = struct{}{}
	return true
}

func registerID(ids map[string]struct{}, id, at string) error {
	if !nonblank(id) || !addUnique(ids, id) {
		return invalid(at, "invalid or duplicate record ID")
	}
	return nil
}

func position(section string, i int) string { return fmt.Sprintf("%s[%d]", section, i) }

func invalid(at, reason string) error { return fmt.Errorf("%s: %s", at, reason) }
