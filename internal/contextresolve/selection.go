package contextresolve

import (
	"cmp"
	"slices"

	"github.com/sh4869221b/yakuori/internal/contextpack"
)

type selection struct {
	items []Item
	seen  map[Item]struct{}
	diag  *Diagnostics
}

func (s *selection) add(id, reason string, item Item) {
	if _, duplicate := s.seen[item]; duplicate {
		s.diag.exclude(id, "duplicate_payload")
		return
	}
	if s.seen == nil {
		s.seen = make(map[Item]struct{})
	}
	s.seen[item] = struct{}{}
	s.items = append(s.items, item)
	s.diag.Matched = append(s.diag.Matched, Decision{ID: id, Reason: reason})
}

func (d *Diagnostics) exclude(id, reason string) {
	d.Excluded = append(d.Excluded, Decision{ID: id, Reason: reason})
}

func selectWorld(world []contextpack.World, artifact string, tags []string, s *selection) {
	const (
		gameScope = iota
		artifactScope
		tagScope
	)
	// Read has ordered each group by full Pack ID. A tagged record
	// requires every tag to be selected, along with its artifact scope.
	var groups [3][]contextpack.World
	for _, entry := range world {
		switch {
		case entry.Provenance.Status != "confirmed":
			s.diag.exclude(entry.ID, "not_confirmed")
		case len(entry.Artifacts) > 0 && !slices.Contains(entry.Artifacts, artifact):
			s.diag.exclude(entry.ID, "outside_artifact_scope")
		case len(entry.Tags) > 0:
			if allTagsMatch(entry.Tags, tags) {
				groups[tagScope] = append(groups[tagScope], entry)
			} else {
				s.diag.exclude(entry.ID, "tags_not_matched")
			}
		case len(entry.Artifacts) > 0:
			groups[artifactScope] = append(groups[artifactScope], entry)
		default:
			groups[gameScope] = append(groups[gameScope], entry)
		}
	}
	reasons := [...]string{"game_world", "artifact_world", "tagged_world"}
	for i, entries := range groups {
		for _, entry := range entries {
			s.add(entry.ID, reasons[i], Item{Category: World, Text: entry.Text})
		}
	}
}

func allTagsMatch(required, selected []string) bool {
	for _, tag := range required {
		if !slices.Contains(selected, tag) {
			return false
		}
	}
	return true
}

func findScene(pack contextpack.Pack, id string) contextpack.Scene {
	for _, entry := range pack.Scenes {
		if entry.ID == id {
			return entry
		}
	}
	return contextpack.Scene{}
}

func selectScene(pack contextpack.Pack, artifact, id string, diag *Diagnostics) (contextpack.Scene, error) {
	if id == "" {
		return contextpack.Scene{}, nil
	}
	entry := findScene(pack, id)
	switch {
	case entry.ID == "":
		return contextpack.Scene{}, ErrSelector
	case !slices.Contains(entry.Artifacts, artifact):
		return contextpack.Scene{}, ErrSelectorScope
	case entry.Provenance.Status != "confirmed":
		diag.exclude(id, "not_confirmed")
		return contextpack.Scene{}, nil
	default:
		return entry, nil
	}
}

func selectSpeaker(pack contextpack.Pack, artifact, id string, diag *Diagnostics) (contextpack.Character, error) {
	if id == "" {
		return contextpack.Character{}, nil
	}
	for _, entry := range pack.Characters {
		if entry.ID != id {
			continue
		}
		if !slices.Contains(entry.Artifacts, artifact) {
			return contextpack.Character{}, ErrSelectorScope
		}
		if entry.Provenance.Status != "confirmed" {
			diag.exclude(id, "not_confirmed")
			return contextpack.Character{}, nil
		}
		return entry, nil
	}
	return contextpack.Character{}, ErrSelector
}

func findPackUnit(units []contextpack.Unit, id string) contextpack.Unit {
	i, found := slices.BinarySearchFunc(units, id, func(entry contextpack.Unit, id string) int {
		return cmp.Compare(entry.ID, id)
	})
	if !found {
		return contextpack.Unit{}
	}
	return units[i]
}

func selectPrevious(req Request, scene contextpack.Scene, current contextpack.Unit, id string, diag *Diagnostics) (contextpack.Unit, error) {
	if id == "" {
		return contextpack.Unit{}, nil
	}
	pack := req.Pack.Pack
	previous := findPackUnit(pack.Units, id)
	switch {
	case previous.ID == "":
		return contextpack.Unit{}, ErrSelector
	case previous.ArtifactNamespace != req.ArtifactNamespace:
		return contextpack.Unit{}, ErrSelectorScope
	case previous.Provenance.Status != "confirmed":
		diag.exclude(id, "not_confirmed")
		return contextpack.Unit{}, nil
	case matchesID(previous, req.UnitID):
		return contextpack.Unit{}, ErrRelationship
	}
	if scene.ID != "" && previous.SceneID != nil && *previous.SceneID != scene.ID {
		return contextpack.Unit{}, ErrRelationship
	}
	// The pack is acyclic. Only an explicit edge can close a new cycle.
	if req.Selectors.PreviousUnitID != "" && current.ID != "" {
		for ancestor := previous; ancestor.Provenance.Status == "confirmed" && ancestor.PreviousUnitID != nil; {
			ancestor = findPackUnit(pack.Units, *ancestor.PreviousUnitID)
			if ancestor.ID == current.ID {
				return contextpack.Unit{}, ErrRelationship
			}
		}
	}
	return previous, nil
}
