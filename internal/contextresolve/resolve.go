// Package contextresolve selects confirmed, unit-scoped context from Context Pack v1.
package contextresolve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sh4869221b/yakuori/internal/contextpack"
	"github.com/sh4869221b/yakuori/internal/contextselection"
	"github.com/sh4869221b/yakuori/internal/unit"
)

var (
	ErrGameScope      = errors.New("context game scope mismatch")
	ErrArtifactScope  = errors.New("unknown context artifact scope")
	ErrArtifactDigest = errors.New("context artifact snapshot mismatch")
	ErrLanguages      = errors.New("context language pair mismatch")
	ErrSourceDigest   = errors.New("context unit source digest mismatch")
	ErrSelector       = errors.New("unknown context selector ID or type")
	ErrSelectorScope  = errors.New("context selector outside artifact scope")
	ErrRelationship   = errors.New("conflicting context relationship")
	ErrTags           = errors.New("invalid context tag")
	ErrQuest          = errors.New("conflicting context quest tags")
	ErrPreviousUnit   = errors.New("previous context unit missing from import")
	ErrPreviousSource = errors.New("previous context source is not UTF-8")
	ErrBudget         = errors.New("invalid context budget")
	ErrItemLimit      = errors.New("context item limit exceeded")
	ErrTokenLimit     = errors.New("context token budget exceeded")
	ErrTokenCount     = errors.New("context token count unavailable")
)

// Categories enables the categories held for evaluation in #25 independently.
// Confirmed world context is eligible regardless of these options.
type Categories struct {
	Scene    bool
	Speaker  bool
	Neighbor bool
}

// Selectors supplies explicit full Pack IDs. A nonempty ID overrides the
// confirmed unit default. Nil Tags inherits the confirmed unit's tags. A
// non-nil slice replaces the entire tag set, including when it is empty.
type Selectors struct {
	SceneID        string
	SpeakerID      string
	PreviousUnitID string
	Tags           []string
}

// Budget limits optional context. The caller supplies the tokens remaining
// after reserving source, mandatory instructions and glossary, and output.
// Zero allows zero items or tokens, respectively.
type Budget struct {
	MaxItems  int
	MaxTokens int
}

type Request struct {
	// Pack must come from contextpack.Read and remain unchanged. Nil means
	// there is no pack.
	Pack *contextpack.Document

	Session       unit.Session
	UnitID        unit.UnitID
	GameNamespace string
	// An empty ArtifactNamespace selects game scope only, regardless of
	// whether the pack lists artifacts.
	ArtifactNamespace string
	Selectors         Selectors
	Categories        Categories
	Budget            Budget
}

type Category = contextselection.Category

const (
	World    = contextselection.World
	Scene    = contextselection.Scene
	Speaker  = contextselection.Speaker
	Neighbor = contextselection.Neighbor
)

type Item = contextselection.Item

// CountTokens counts the context cost of the entire ordered selection. The
// caller can account for rendering and token boundaries in one invocation.
type CountTokens func(context.Context, []Item) (int, error)

type ResolvedContext = contextselection.ResolvedContext

type Decision struct {
	ID     string
	Reason string
}

// Diagnostics contains IDs and fixed reasons, without source or context text.
// Matched describes selection before budget admission. A match is applied only
// on success. Fingerprint is set on success, including for empty context.
type Diagnostics struct {
	Matched     []Decision
	Excluded    []Decision
	Budget      Budget
	Tokens      int
	Fingerprint [sha256.Size]byte
}

// Resolve selects optional context without truncation. Glossary references are
// external, unscoped data in Pack v1. The caller handles glossary input and
// mandatory/protect validation independently of this selection.
func Resolve(ctx context.Context, req Request, count CountTokens) (ResolvedContext, Diagnostics, error) {
	diag := Diagnostics{Budget: req.Budget}
	if req.Budget.MaxItems < 0 || req.Budget.MaxTokens < 0 {
		return ResolvedContext{}, diag, ErrBudget
	}
	current, err := req.Session.Unit(req.UnitID)
	if err != nil {
		return ResolvedContext{}, diag, err
	}

	var items []Item
	if req.Pack != nil {
		items, err = selectItems(req, current, &diag)
		if err != nil {
			return ResolvedContext{}, diag, err
		}
	}
	if len(items) == 0 {
		diag.Fingerprint = ResolvedContext{}.Fingerprint()
		return ResolvedContext{}, diag, nil
	}
	if len(items) > req.Budget.MaxItems {
		return ResolvedContext{}, diag, ErrItemLimit
	}
	if count == nil {
		return ResolvedContext{}, diag, ErrTokenCount
	}
	tokens, err := count(ctx, slices.Clone(items))
	if err != nil || tokens < 0 {
		return ResolvedContext{}, diag, ErrTokenCount
	}
	diag.Tokens = tokens
	if tokens > req.Budget.MaxTokens {
		return ResolvedContext{}, diag, ErrTokenLimit
	}

	selection, err := contextselection.New(items)
	if err != nil {
		return ResolvedContext{}, diag, errors.New("encode context selection failed")
	}
	diag.Fingerprint = selection.Fingerprint()
	return selection, diag, nil
}

func selectItems(req Request, current unit.TranslationUnit, diag *Diagnostics) ([]Item, error) {
	pack := req.Pack.Pack
	if pack.Scope.GameNamespace != req.GameNamespace {
		return nil, ErrGameScope
	}
	if !languagesMatch(pack, current) {
		return nil, ErrLanguages
	}
	if req.ArtifactNamespace != "" {
		i := slices.IndexFunc(pack.Scope.Artifacts, func(artifact contextpack.Artifact) bool {
			return artifact.ArtifactNamespace == req.ArtifactNamespace
		})
		if i == -1 {
			return nil, ErrArtifactScope
		}
		if !digestMatches(pack.Scope.Artifacts[i].OriginalArtifactDigest, req.Session.ArtifactDigest()) {
			return nil, ErrArtifactDigest
		}
	}

	selectors := req.Selectors
	var defaults contextpack.Unit
	if req.ArtifactNamespace != "" {
		for _, entry := range pack.Units {
			if entry.ArtifactNamespace != req.ArtifactNamespace || !matchesID(entry, req.UnitID) {
				continue
			}
			if entry.Provenance.Status != "confirmed" {
				diag.exclude(entry.ID, "not_confirmed")
				break
			}
			if !digestMatches(entry.RawSourceDigest, current.SourceDigest()) {
				return nil, ErrSourceDigest
			}
			defaults = entry
			break
		}
	}
	tags := selectors.Tags
	if tags == nil {
		tags = defaults.Tags
	}
	if err := validateTags(tags, selectors.Tags != nil); err != nil {
		return nil, err
	}

	sceneID, sceneReason := reference(selectors.SceneID, defaults.SceneID)
	scene, err := selectScene(pack, req.ArtifactNamespace, sceneID, diag)
	if err != nil {
		return nil, err
	}
	speakerID, speakerReason := reference(selectors.SpeakerID, defaults.SpeakerID)
	speaker, err := selectSpeaker(pack, req.ArtifactNamespace, speakerID, diag)
	if err != nil {
		return nil, err
	}
	if speaker.ID != "" && len(scene.SpeakerIDs) > 0 && !slices.Contains(scene.SpeakerIDs, speaker.ID) {
		return nil, ErrRelationship
	}

	previousID, previousReason := reference(selectors.PreviousUnitID, defaults.PreviousUnitID)
	previous, err := selectPrevious(req, scene, defaults, previousID, diag)
	if err != nil {
		return nil, err
	}

	s := selection{diag: diag}
	selectWorld(pack.World, req.ArtifactNamespace, tags, &s)
	switch {
	case scene.ID == "":
	case !req.Categories.Scene:
		diag.exclude(scene.ID, "category_disabled")
	case scene.Summary == nil:
		diag.exclude(scene.ID, "no_payload")
	default:
		s.add(scene.ID, sceneReason, Item{Category: Scene, Text: *scene.Summary})
	}
	switch {
	case speaker.ID == "":
	case !req.Categories.Speaker:
		diag.exclude(speaker.ID, "category_disabled")
	case speaker.Name == nil && speaker.Description == nil:
		diag.exclude(speaker.ID, "no_payload")
	default:
		s.add(speaker.ID, speakerReason, Item{Category: Speaker, Name: value(speaker.Name), Text: value(speaker.Description)})
	}
	if previous.ID != "" {
		if !req.Categories.Neighbor {
			diag.exclude(previous.ID, "category_disabled")
			return s.items, nil
		}
		id, err := unit.NewUnitID(previous.Adapter, previous.FormatSchema, previous.StableID)
		if err != nil {
			return nil, err
		}
		entry, err := req.Session.Unit(id)
		if err != nil {
			return nil, ErrPreviousUnit
		}
		if !languagesMatch(pack, entry) {
			return nil, ErrLanguages
		}
		if !digestMatches(previous.RawSourceDigest, entry.SourceDigest()) {
			return nil, ErrSourceDigest
		}
		source := entry.Source()
		switch {
		case !utf8.Valid(source):
			return nil, ErrPreviousSource
		case len(source) == 0:
			diag.exclude(previous.ID, "no_payload")
		default:
			s.add(previous.ID, previousReason, Item{Category: Neighbor, Text: string(source)})
		}
	}
	return s.items, nil
}

func languagesMatch(pack contextpack.Pack, entry unit.TranslationUnit) bool {
	return pack.SourceLanguage == entry.SourceLanguage() && pack.TargetLanguage == entry.TargetLanguage()
}

func digestMatches(expected string, digest [sha256.Size]byte) bool {
	return expected == hex.EncodeToString(digest[:])
}

func matchesID(entry contextpack.Unit, id unit.UnitID) bool {
	return entry.Adapter == id.Adapter() && entry.FormatSchema == id.FormatSchema() && entry.StableID == id.StableID()
}

func validateTags(tags []string, explicit bool) error {
	var quest string
	for _, tag := range tags {
		if explicit && (strings.TrimSpace(tag) == "" || strings.ContainsRune(tag, 0) || !utf8.ValidString(tag)) {
			return ErrTags
		}
		if value, ok := strings.CutPrefix(tag, "quest:"); ok {
			if strings.TrimSpace(value) == "" {
				return ErrTags
			}
			if quest != "" && quest != value {
				return ErrQuest
			}
			quest = value
		}
	}
	return nil
}

func reference(explicit string, inherited *string) (id, reason string) {
	if explicit != "" {
		return explicit, "explicit_selector"
	}
	return value(inherited), "confirmed_unit"
}

func value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
