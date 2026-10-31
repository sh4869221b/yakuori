package acceptance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func candidate(t *testing.T) (string, Manifest, Criteria) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	var c Criteria
	if err := Load(filepath.Join(root, "docs/evidence/acceptance/fixtures.json"), &m); err != nil {
		t.Fatal(err)
	}
	if err := Load(filepath.Join(root, "docs/evidence/acceptance/criteria.json"), &c); err != nil {
		t.Fatal(err)
	}
	return root, m, c
}

func readyCandidate(t *testing.T, m *Manifest, c *Criteria) {
	t.Helper()
	// Synthetic references test the record mechanics, not real-world representativeness.
	f := &m.Fixtures[1]
	f.Categories = nil
	for _, name := range categories {
		f.Categories = append(f.Categories, Category{Name: name, UnitIDs: []string{"2119001011"}, Evidence: "test-only category"})
	}
	c.Confirmation = Confirmation{Status: "confirmed", Date: "2026-10-31T12:00:00+09:00", Reviewer: "test", Decision: "approved", ManifestVersion: m.Version, CriteriaVersion: c.Version}
}

func TestCandidateFixturesAndReadiness(t *testing.T) {
	root, m, c := candidate(t)
	result, err := Check(root, m, c)
	if err != nil {
		t.Fatal(err)
	}
	if result.AcceptanceReady || result.Confirmation != "pending" || !slices.Contains(result.MissingReal, "dialogue") || !slices.Contains(result.MissingReal, "long_text") || len(result.Undetermined) != 0 {
		t.Fatalf("candidate must remain unready: %+v", result)
	}
	if len(result.Fixtures) != 4 || !result.Fixtures[0].ProductInputCompatible || result.Fixtures[2].ProductInputCompatible || result.Fixtures[2].InputLimits != "research" {
		t.Fatalf("product and research fixture conditions: %+v", result.Fixtures)
	}
	// Merely selecting a synthetic long fixture cannot establish real-MOD coverage.
	c.FixtureIDs = append(c.FixtureIDs, "authored-en")
	setCount(&c, "units", "53")
	setCount(&c, "text_bytes", "6676")
	setCount(&c, "artifact_bytes", "6330")
	result, err = Check(root, m, c)
	if err != nil || slices.Contains(result.MissingAll, "long_text") || !slices.Contains(result.MissingReal, "long_text") {
		t.Fatalf("synthetic category must not fill real-MOD coverage: %+v, %v", result, err)
	}
	c.FixtureIDs = c.FixtureIDs[:2]
	setCount(&c, "units", "49")
	setCount(&c, "text_bytes", "482")
	setCount(&c, "artifact_bytes", "2074")
	readyCandidate(t, &m, &c)
	result, err = Check(root, m, c)
	if err != nil || !result.AcceptanceReady {
		t.Fatalf("test-only complete confirmed case should be ready: %+v, %v", result, err)
	}
}

func TestMixedCategoriesAndSelectedMOD(t *testing.T) {
	root, m, c := candidate(t)
	// Reuse a product-sized original as a synthetic fixture for record mechanics.
	// These test-only labels do not assert the semantic category of its text.
	synthetic, mod := &m.Fixtures[0], &m.Fixtures[1]
	synthetic.Kind = "synthetic"
	synthetic.Categories = nil
	for _, name := range categories {
		if name != "short_names" {
			synthetic.Categories = append(synthetic.Categories, Category{Name: name, UnitIDs: []string{"2114573011"}, Evidence: "test-only category"})
		}
	}

	result, err := Check(root, m, c)
	if err != nil || result.AcceptanceReady || len(result.MissingAll) != 0 || len(result.MissingReal) == 0 {
		t.Fatalf("pending mixed set must be unready, while reporting real gaps: %+v, %v", result, err)
	}
	c.Confirmation = Confirmation{Status: "confirmed", Date: "2026-10-31T12:00:00+09:00", Reviewer: "test", Decision: "approved", ManifestVersion: m.Version, CriteriaVersion: c.Version}
	result, err = Check(root, m, c)
	if err != nil || !result.AcceptanceReady || len(result.MissingAll) != 0 || len(result.MissingReal) == 0 {
		t.Fatalf("confirmed mixed set should be ready with real-MOD gaps reported: %+v, %v", result, err)
	}

	all := synthetic.Categories
	synthetic.Categories = all[:len(all)-1]
	result, err = Check(root, m, c)
	if err != nil || result.AcceptanceReady || len(result.MissingAll) == 0 {
		t.Fatalf("a missing set-wide category must prevent readiness: %+v, %v", result, err)
	}
	synthetic.Categories = all
	mod.Kind = "synthetic"
	result, err = Check(root, m, c)
	if err != nil || result.AcceptanceReady || len(result.MissingAll) != 0 {
		t.Fatalf("a synthetic-only set must not be ready: %+v, %v", result, err)
	}
}

func setCount(c *Criteria, name, value string) {
	v := c.Values[name]
	v.Value = value
	c.Values[name] = v
}

func TestUndeterminedPerformanceCannotBeReady(t *testing.T) {
	root, m, c := candidate(t)
	readyCandidate(t, &m, &c)
	v := c.Values["vram_bytes"]
	v.Value = ""
	c.Values["vram_bytes"] = v
	result, err := Check(root, m, c)
	if err != nil || result.AcceptanceReady || !slices.Equal(result.Undetermined, []string{"vram_bytes"}) {
		t.Fatalf("missing performance threshold must prevent readiness: %+v, %v", result, err)
	}
}

func TestResearchOnlySelectedCannotBeReady(t *testing.T) {
	root, m, c := candidate(t)
	readyCandidate(t, &m, &c)
	f := &m.Fixtures[2]
	f.Kind = "mod" // Test-only: exercise the product limit independently of category gaps.
	f.Categories = nil
	for _, name := range categories {
		f.Categories = append(f.Categories, Category{Name: name, UnitIDs: []string{"1001"}, Evidence: "test-only category"})
	}
	c.FixtureIDs = []string{f.ID}
	setCount(&c, "units", "4")
	setCount(&c, "text_bytes", "6194")
	setCount(&c, "artifact_bytes", "4256")
	result, err := Check(root, m, c)
	if err != nil || result.AcceptanceReady || !slices.Equal(result.ProductIncompatible, []string{"authored-en"}) || len(result.MissingReal) != 0 {
		t.Fatalf("research input must not pass product readiness: %+v, %v", result, err)
	}
}

func TestInvalidCandidate(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Manifest, *Criteria)
	}{
		{"missing source", func(m *Manifest, _ *Criteria) { m.Fixtures[0].SourceURL = "" }},
		{"duplicate ID", func(m *Manifest, _ *Criteria) { m.Fixtures[1].ID = m.Fixtures[0].ID }},
		{"wrong hash", func(m *Manifest, _ *Criteria) { m.Fixtures[0].SHA256 = strings.Repeat("0", 64) }},
		{"wrong bytes", func(m *Manifest, _ *Criteria) { m.Fixtures[0].Bytes++ }},
		{"wrong count", func(m *Manifest, _ *Criteria) { m.Fixtures[0].Units++ }},
		{"wrong text bytes", func(m *Manifest, _ *Criteria) { m.Fixtures[0].TextBytes++ }},
		{"wrong language", func(m *Manifest, _ *Criteria) { m.Fixtures[0].Language = "ja" }},
		{"wrong format version", func(m *Manifest, _ *Criteria) { m.Fixtures[0].FormatVersion = 161 }},
		{"missing category unit", func(m *Manifest, _ *Criteria) { m.Fixtures[1].Categories[0].UnitIDs = []string{"0"} }},
		{"product limit", func(m *Manifest, _ *Criteria) { m.Fixtures[2].Limits = nil }},
		{"invalid research limit", func(m *Manifest, _ *Criteria) { m.Fixtures[2].Limits.UnitTextBytes = 0 }},
		{"mismatched version", func(_ *Manifest, c *Criteria) { c.ManifestVersion = "old" }},
		{"missing criterion", func(_ *Manifest, c *Criteria) { delete(c.Values, "omission") }},
		{"fractional count", func(_ *Manifest, c *Criteria) { setCount(c, "units", "49.5") }},
		{"wrong selected count", func(_ *Manifest, c *Criteria) { setCount(c, "units", "48") }},
		{"wrong selected text bytes", func(_ *Manifest, c *Criteria) { setCount(c, "text_bytes", "1") }},
		{"wrong selected artifact bytes", func(_ *Manifest, c *Criteria) { setCount(c, "artifact_bytes", "1") }},
		{"invalid finite bound", func(_ *Manifest, c *Criteria) { v := c.Values["ram_bytes"]; v.Value = "NaN"; c.Values["ram_bytes"] = v }},
		{"stale confirmation", func(m *Manifest, c *Criteria) { readyCandidate(t, m, c); c.Confirmation.CriteriaVersion = "old" }},
		{"rejected decision", func(m *Manifest, c *Criteria) { readyCandidate(t, m, c); c.Confirmation.Decision = "rejected" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, m, c := candidate(t)
			tt.edit(&m, &c)
			if _, err := Check(root, m, c); err == nil {
				t.Fatal("invalid candidate accepted")
			}
		})
	}
}

func TestPrivateLocalPathAndNoTextInResult(t *testing.T) {
	root, m, c := candidate(t)
	original, err := os.ReadFile(filepath.Join(root, m.Fixtures[0].LocalPath))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "private.w3strings")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	m.Fixtures[0].LocalPath = path
	result, err := Check(root, m, c)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "Discard Target") || strings.Contains(string(encoded), path) {
		t.Fatal("result contains source text or private fixture path")
	}
}
