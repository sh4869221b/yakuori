// Package acceptance checks the locally supplied fixtures and candidate criteria.
package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/validate"
)

var categories = []string{"dialogue", "short_names", "long_text", "terminology", "tags_placeholders", "empty_text"}
var criterionNames = []string{"machine_validation", "mistranslation", "omission", "readability", "terminology", "units", "text_bytes", "artifact_bytes", "duration_seconds", "ram_bytes", "vram_bytes"}

type Manifest struct {
	Version  string    `json:"version"`
	Fixtures []Fixture `json:"fixtures"`
}

type Fixture struct {
	ID            string     `json:"id"`
	Kind          string     `json:"kind"` // mod or synthetic
	SourceURL     string     `json:"source_url"`
	Revision      string     `json:"revision"`
	License       string     `json:"license"`
	LocalPath     string     `json:"local_path"`
	SHA256        string     `json:"sha256"`
	Bytes         int        `json:"bytes"`
	Units         int        `json:"units"`
	TextBytes     int        `json:"text_bytes"`
	MaxTextBytes  int        `json:"max_text_bytes"`
	Format        string     `json:"format"`
	FormatVersion int        `json:"format_version"`
	Language      string     `json:"language"` // canonical en or ja
	Limits        *Limits    `json:"research_limits,omitempty"`
	Categories    []Category `json:"categories"`
}

type Limits struct {
	ArtifactBytes  int `json:"artifact_bytes"`
	Units          int `json:"units"`
	UnitTextBytes  int `json:"unit_text_bytes"`
	TotalTextBytes int `json:"total_text_bytes"`
	Segments       int `json:"segments"`
}

type Category struct {
	Name     string   `json:"name"`
	UnitIDs  []string `json:"unit_ids"`
	Evidence string   `json:"evidence"`
}

type Criteria struct {
	Version         string               `json:"version"`
	ManifestVersion string               `json:"manifest_version"`
	FixtureIDs      []string             `json:"fixture_ids"`
	Values          map[string]Criterion `json:"values"`
	Confirmation    Confirmation         `json:"confirmation"`
}

type Criterion struct {
	Value     string `json:"value"` // empty means not determined
	Rationale string `json:"rationale"`
	Impact    string `json:"impact"`
}

type Confirmation struct {
	Status          string `json:"status"` // pending or confirmed
	Date            string `json:"date"`
	Reviewer        string `json:"reviewer"`
	Decision        string `json:"decision"`
	ManifestVersion string `json:"manifest_version"`
	CriteriaVersion string `json:"criteria_version"`
}

type Result struct {
	AcceptanceReady     bool            `json:"acceptance_ready"`
	MissingReal         []string        `json:"missing_real_categories"`
	MissingAll          []string        `json:"missing_categories"`
	Undetermined        []string        `json:"undetermined_criteria"`
	ProductIncompatible []string        `json:"selected_product_incompatible"`
	Confirmation        string          `json:"confirmation"`
	Fixtures            []FixtureResult `json:"fixtures"`
}

type FixtureResult struct {
	ID                     string `json:"id"`
	InputLimits            string `json:"input_limits"`
	ProductInputCompatible bool   `json:"product_input_compatible"`
}

// Load reads one JSON document without copying fixture text into diagnostics.
func Load(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	const maxDocument = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(f, maxDocument+1))
	if err != nil {
		return err
	}
	if len(raw) > maxDocument {
		return errors.New("evaluation document exceeds 1 MiB")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errors.New("evaluation document must contain one JSON value")
	}
	return nil
}

// Check validates metadata, local originals, and readiness without performing a translation.
func Check(root string, m Manifest, c Criteria) (Result, error) {
	result := Result{MissingReal: []string{}, MissingAll: []string{}, Undetermined: []string{}, Confirmation: c.Confirmation.Status, Fixtures: []FixtureResult{}, ProductIncompatible: []string{}}
	if strings.TrimSpace(root) == "" || strings.TrimSpace(m.Version) == "" || strings.TrimSpace(c.Version) == "" || c.ManifestVersion != m.Version || len(m.Fixtures) == 0 || len(c.FixtureIDs) == 0 {
		return result, errors.New("root, versions, fixture set or matching manifest version missing")
	}
	selected := make(map[string]bool, len(c.FixtureIDs))
	for _, id := range c.FixtureIDs {
		if strings.TrimSpace(id) == "" || selected[id] {
			return result, errors.New("empty or duplicate selected fixture ID")
		}
		selected[id] = true
	}
	seen := make(map[string]bool, len(m.Fixtures))
	covered, realCovered := map[string]bool{}, map[string]bool{}
	selectedUnits, selectedTextBytes, selectedArtifactBytes := 0, 0, 0
	selectedHasMod := false
	for _, f := range m.Fixtures {
		if seen[f.ID] || f.ID == "" {
			return result, errors.New("empty or duplicate fixture ID")
		}
		seen[f.ID] = true
		fixtureResult, err := checkFixture(root, f, selected[f.ID], covered, realCovered)
		if err != nil {
			return result, fmt.Errorf("fixture %s: %w", f.ID, err)
		}
		result.Fixtures = append(result.Fixtures, fixtureResult)
		if selected[f.ID] {
			selectedHasMod = selectedHasMod || f.Kind == "mod"
			selectedUnits += f.Units
			selectedTextBytes += f.TextBytes
			selectedArtifactBytes += f.Bytes
			if !fixtureResult.ProductInputCompatible {
				result.ProductIncompatible = append(result.ProductIncompatible, f.ID)
			}
		}
	}
	for id := range selected {
		if !seen[id] {
			return result, fmt.Errorf("selected fixture %s not in manifest", id)
		}
	}
	for _, name := range categories {
		if !covered[name] {
			result.MissingAll = append(result.MissingAll, name)
		}
		if !realCovered[name] {
			result.MissingReal = append(result.MissingReal, name)
		}
	}
	for _, name := range criterionNames {
		v, ok := c.Values[name]
		if !ok || strings.TrimSpace(v.Rationale) == "" || strings.TrimSpace(v.Impact) == "" {
			return result, fmt.Errorf("criterion %s or its rationale/impact missing", name)
		}
		if strings.TrimSpace(v.Value) == "" {
			result.Undetermined = append(result.Undetermined, name)
			continue
		}
		if slices.Contains([]string{"units", "text_bytes", "artifact_bytes"}, name) {
			n, err := strconv.Atoi(v.Value)
			actual := map[string]int{"units": selectedUnits, "text_bytes": selectedTextBytes, "artifact_bytes": selectedArtifactBytes}[name]
			if err != nil || n <= 0 || n != actual {
				return result, fmt.Errorf("criterion %s must equal the selected fixture total (%d)", name, actual)
			}
		} else if slices.Contains([]string{"duration_seconds", "ram_bytes", "vram_bytes"}, name) {
			n, err := strconv.ParseFloat(v.Value, 64)
			if err != nil || n <= 0 || math.IsInf(n, 0) || math.IsNaN(n) {
				return result, fmt.Errorf("criterion %s must be a positive finite number", name)
			}
		}
	}
	for name := range c.Values {
		if !slices.Contains(criterionNames, name) {
			return result, fmt.Errorf("unknown criterion %s", name)
		}
	}
	switch c.Confirmation.Status {
	case "pending":
		if c.Confirmation.Date != "" || c.Confirmation.Reviewer != "" || c.Confirmation.Decision != "" || c.Confirmation.ManifestVersion != "" || c.Confirmation.CriteriaVersion != "" {
			return result, errors.New("pending confirmation must not contain a decision")
		}
	case "confirmed":
		v := c.Confirmation
		if _, err := time.Parse(time.RFC3339, v.Date); err != nil || strings.TrimSpace(v.Reviewer) == "" || v.Decision != "approved" || v.ManifestVersion != m.Version || v.CriteriaVersion != c.Version {
			return result, errors.New("confirmed decision requires date, reviewer, decision and current versions")
		}
	default:
		return result, errors.New("confirmation status must be pending or confirmed")
	}
	result.AcceptanceReady = selectedHasMod && len(result.MissingAll) == 0 && len(result.Undetermined) == 0 && len(result.ProductIncompatible) == 0 && c.Confirmation.Status == "confirmed"
	return result, nil
}

func checkFixture(root string, f Fixture, selected bool, covered, realCovered map[string]bool) (FixtureResult, error) {
	result := FixtureResult{ID: f.ID, InputLimits: "product"}
	if f.Limits != nil {
		result.InputLimits = "research"
	}
	if (f.Kind != "mod" && f.Kind != "synthetic") || strings.TrimSpace(f.SourceURL) == "" || strings.TrimSpace(f.Revision) == "" || strings.TrimSpace(f.License) == "" || strings.TrimSpace(f.LocalPath) == "" || f.Format != "w3strings" || f.FormatVersion != 162 || (f.Language != "en" && f.Language != "ja") || f.Bytes <= 0 || f.Units < 0 || f.TextBytes < 0 || f.MaxTextBytes < 0 || f.MaxTextBytes > f.TextBytes {
		return result, errors.New("required metadata, size, format or language invalid")
	}
	expected, err := hex.DecodeString(f.SHA256)
	if err != nil || len(expected) != sha256.Size || strings.ToLower(f.SHA256) != f.SHA256 {
		return result, errors.New("invalid original SHA-256")
	}
	limits := config.DefaultLimits()
	if f.Limits != nil {
		v := f.Limits
		limits.ArtifactBytes, limits.Units, limits.UnitTextBytes, limits.TotalTextBytes, limits.Segments = v.ArtifactBytes, v.Units, v.UnitTextBytes, v.TotalTextBytes, v.Segments
	}
	if err := limits.Check(); err != nil || f.Bytes > limits.ArtifactBytes || f.Bytes == math.MaxInt {
		return result, errors.New("invalid input limits or fixture exceeds artifact limit")
	}
	path := f.LocalPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	file, err := os.Open(path)
	if err != nil {
		return result, fmt.Errorf("open local original: %w", err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, int64(f.Bytes)+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return result, err
	}
	if len(raw) != f.Bytes {
		return result, errors.New("original byte count mismatch")
	}
	digest := sha256.Sum256(raw)
	if !slices.Equal(digest[:], expected) {
		return result, errors.New("original SHA-256 mismatch")
	}
	a, err := w3strings.NewForSource(f.Language, limits)
	if err != nil {
		return result, err
	}
	session, err := a.Import(raw, validate.Profile{})
	if err != nil {
		return result, fmt.Errorf("import: %w", err)
	}
	units := session.Units()
	ids := make(map[string]bool, len(units))
	total, maxText := 0, 0
	for _, u := range units {
		ids[u.ID().StableID()] = true
		n := u.SourceBytes()
		total += n
		maxText = max(maxText, n)
	}
	if len(units) != f.Units || total != f.TextBytes || maxText != f.MaxTextBytes {
		return result, errors.New("unit or UTF-8 text byte count mismatch")
	}
	seenCategories := map[string]bool{}
	for _, cat := range f.Categories {
		if !slices.Contains(categories, cat.Name) || seenCategories[cat.Name] || len(cat.UnitIDs) == 0 || strings.TrimSpace(cat.Evidence) == "" {
			return result, errors.New("invalid category, unit references or evidence")
		}
		seenCategories[cat.Name] = true
		refs := map[string]bool{}
		for _, id := range cat.UnitIDs {
			if !ids[id] || refs[id] {
				return result, fmt.Errorf("invalid unit reference in category %s", cat.Name)
			}
			refs[id] = true
		}
		if selected {
			covered[cat.Name] = true
			if f.Kind == "mod" {
				realCovered[cat.Name] = true
			}
		}
	}
	defaults := config.DefaultLimits()
	result.ProductInputCompatible = len(raw) <= defaults.ArtifactBytes && len(units) <= defaults.Units && maxText <= defaults.UnitTextBytes && total <= defaults.TotalTextBytes
	return result, nil
}
