package contextpack

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
)

// Approve merges packs normalized by Read. Only selected records in reviewed
// can gain confirmed status. The result goes through the same v1 reader.
func Approve(base, reviewed Pack, accept []string) (*Document, error) {
	if base.Scope.GameNamespace != reviewed.Scope.GameNamespace ||
		base.SourceLanguage != reviewed.SourceLanguage || base.TargetLanguage != reviewed.TargetLanguage {
		return nil, errors.New("game namespace or languages do not match")
	}
	if len(accept) == 0 {
		return nil, errors.New("at least one accepted record ID is required")
	}
	selection := make(map[string]bool, len(accept))
	for _, id := range accept {
		if _, exists := selection[id]; exists {
			return nil, errors.New("duplicate accepted record ID")
		}
		selection[id] = false
	}
	merged := base
	merged.Scope.Artifacts = slices.Clone(base.Scope.Artifacts)
	digests := make(map[string]string, len(base.Scope.Artifacts))
	for _, a := range base.Scope.Artifacts {
		digests[a.ArtifactNamespace] = a.OriginalArtifactDigest
	}
	for _, a := range reviewed.Scope.Artifacts {
		if digest, exists := digests[a.ArtifactNamespace]; exists {
			if digest != a.OriginalArtifactDigest {
				return nil, errors.New("artifact namespace refers to different original snapshots")
			}
		} else {
			merged.Scope.Artifacts = append(merged.Scope.Artifacts, a)
		}
	}

	ids := make(map[string]struct{})
	var err error
	merged.World, err = mergeApprovalRecords(base.World, reviewed.World, ids, selection, func(r *World) (string, *Provenance) { return r.ID, &r.Provenance })
	if err != nil {
		return nil, err
	}
	merged.Characters, err = mergeApprovalRecords(base.Characters, reviewed.Characters, ids, selection, func(r *Character) (string, *Provenance) { return r.ID, &r.Provenance })
	if err != nil {
		return nil, err
	}
	merged.Scenes, err = mergeApprovalRecords(base.Scenes, reviewed.Scenes, ids, selection, func(r *Scene) (string, *Provenance) { return r.ID, &r.Provenance })
	if err != nil {
		return nil, err
	}
	merged.Units, err = mergeApprovalRecords(base.Units, reviewed.Units, ids, selection, func(r *Unit) (string, *Provenance) { return r.ID, &r.Provenance })
	if err != nil {
		return nil, err
	}
	merged.GlossaryRefs, err = mergeApprovalRecords(base.GlossaryRefs, reviewed.GlossaryRefs, ids, selection, func(r *GlossaryRef) (string, *Provenance) { return r.ID, &r.Provenance })
	if err != nil {
		return nil, err
	}
	for _, matched := range selection {
		if !matched {
			return nil, errors.New("accepted record ID is absent from reviewed pack")
		}
	}
	data, err := json.Marshal(merged)
	if err != nil {
		return nil, errors.New("encode merged context pack failed")
	}
	return Read(bytes.NewReader(data))
}

func mergeApprovalRecords[T any](base, reviewed []T, ids map[string]struct{}, selection map[string]bool, fields func(*T) (string, *Provenance)) ([]T, error) {
	merged := slices.Clone(base)
	byID := make(map[string]int, len(base))
	for i := range merged {
		id, _ := fields(&merged[i])
		if !addUnique(ids, id) {
			return nil, errors.New("record ID collides across record types")
		}
		byID[id] = i
	}
	for _, r := range reviewed {
		id, provenance := fields(&r)
		i, exists := byID[id]
		_, accepted := selection[id]
		provenance.Status = "candidate"
		if exists {
			_, existing := fields(&merged[i])
			provenance.Status = existing.Status
			// The wire encoding treats empty and omitted optional sets alike.
			before, beforeErr := json.Marshal(merged[i])
			after, afterErr := json.Marshal(r)
			if beforeErr != nil || afterErr != nil {
				return nil, errors.New("encode record for comparison failed")
			}
			if !bytes.Equal(before, after) {
				return nil, errors.New("record ID refers to different record content")
			}
		} else if !addUnique(ids, id) {
			return nil, errors.New("record ID collides across record types")
		}
		if accepted {
			provenance.Status = "confirmed"
			selection[id] = true
		}
		if exists {
			merged[i] = r
		} else {
			merged = append(merged, r)
		}
	}
	return merged, nil
}
