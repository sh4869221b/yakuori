package contextpack

import (
	"fmt"
	"strings"
)

type contextRecord struct {
	id, position string
	protected    bool
}

func packRecords(pack Pack) []contextRecord {
	record := func(id, section string, i int, p Provenance) contextRecord {
		return contextRecord{id, position(section, i), p.Status == "confirmed" || p.UserEdited != nil && *p.UserEdited}
	}
	var records []contextRecord
	for i, r := range pack.World {
		records = append(records, record(r.ID, "world", i, r.Provenance))
	}
	for i, r := range pack.Characters {
		records = append(records, record(r.ID, "characters", i, r.Provenance))
	}
	for i, r := range pack.Scenes {
		records = append(records, record(r.ID, "scenes", i, r.Provenance))
	}
	for i, r := range pack.Units {
		records = append(records, record(r.ID, "units", i, r.Provenance))
	}
	for i, r := range pack.GlossaryRefs {
		records = append(records, record(r.ID, "glossary_refs", i, r.Provenance))
	}
	return records
}

func physicalUnit(u Unit) [4]string {
	return [4]string{u.ArtifactNamespace, u.Adapter, u.FormatSchema, u.StableID}
}

// Collisions reports canonical positions in the two validated packs. It does
// not combine their records or expose names, IDs, or source text in the report.
func Collisions(candidate, against Pack) string {
	var report strings.Builder
	artifacts := make(map[string]int, len(candidate.Scope.Artifacts))
	for i, a := range candidate.Scope.Artifacts {
		artifacts[a.ArtifactNamespace] = i
	}
	for i, a := range against.Scope.Artifacts {
		j, ok := artifacts[a.ArtifactNamespace]
		if ok && a.OriginalArtifactDigest != candidate.Scope.Artifacts[j].OriginalArtifactDigest {
			fmt.Fprintf(&report, "collision: against.scope.artifacts[%d] and candidate.scope.artifacts[%d] have different digests\n", i, j)
		}
	}

	records := make(map[string]string)
	for _, r := range packRecords(candidate) {
		records[r.id] = r.position
	}
	for _, r := range packRecords(against) {
		if at, ok := records[r.id]; ok && r.protected {
			fmt.Fprintf(&report, "collision: protected against.%s and candidate.%s share a record ID\n", r.position, at)
		}
	}

	units := make(map[[4]string]int, len(candidate.Units))
	for i, u := range candidate.Units {
		units[physicalUnit(u)] = i
	}
	for i, u := range against.Units {
		if u.Provenance.Status != "confirmed" && (u.Provenance.UserEdited == nil || !*u.Provenance.UserEdited) {
			continue
		}
		if j, ok := units[physicalUnit(u)]; ok && u.ID != candidate.Units[j].ID {
			fmt.Fprintf(&report, "collision: protected against.units[%d] and candidate.units[%d] share a stable unit\n", i, j)
		}
	}
	return report.String()
}
