//go:build linux

package acceptance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/validate"
)

// ReviewSheet contains private fixture text and human observations. It must not
// be published as a CI artifact without permission from the rights holder.
type ReviewSheet struct {
	Schema           string      `json:"schema"`
	ManifestVersion  string      `json:"manifest_version"`
	Criteria         Criteria    `json:"criteria"`
	FixtureID        string      `json:"fixture_id"`
	Revision         string      `json:"fixture_revision"`
	RunID            string      `json:"run_id"`
	PublicationRunID string      `json:"publication_run_id"`
	RunStatus        string      `json:"run_status"`
	ModelID          string      `json:"model_id"`
	ModelSHA256      string      `json:"model_sha256"`
	Profile          [32]byte    `json:"profile"`
	SourcePath       string      `json:"source_path"`
	SourceSHA256     string      `json:"source_sha256"`
	OutputPath       string      `json:"output_path"`
	OutputSHA256     string      `json:"output_sha256"`
	Rows             []ReviewRow `json:"rows"`
}

type ReviewRow struct {
	ID          string                `json:"unit_id"`
	Kind        string                `json:"kind"`
	Categories  []string              `json:"categories"`
	Findings    []validate.ReviewCode `json:"review_findings"`
	Source      string                `json:"source"`
	Translation string                `json:"translation"`
	Available   bool                  `json:"output_available"`
	Reviewer    string                `json:"reviewer"`
	Date        string                `json:"date"`
	Verdict     string                `json:"verdict"` // pass, fail, hold, or empty
	Rationale   string                `json:"rationale"`
}

type ReviewCount struct {
	Total       int `json:"total"`
	Reviewed    int `json:"reviewed"`
	Pass        int `json:"pass"`
	Fail        int `json:"fail"`
	Hold        int `json:"hold"`
	Unreviewed  int `json:"unreviewed"`
	Unavailable int `json:"unavailable"`
}

type ReviewSummary struct {
	FixtureID       string                 `json:"fixture_id"`
	RunID           string                 `json:"run_id"`
	RunStatus       string                 `json:"run_status"`
	CriteriaVersion string                 `json:"criteria_version"`
	Confirmation    string                 `json:"confirmation"`
	Status          string                 `json:"status"` // pass, fail, or incomplete
	Units           ReviewCount            `json:"units"`
	Categories      map[string]ReviewCount `json:"categories"`
}

// GenerateReview constructs the review scope from the producer's run record.
// root resolves source paths; evidence resolves output paths from run.json.
func GenerateReview(m Manifest, c Criteria, record RunRecord, root, evidence, runID string) (ReviewSheet, Result, error) {
	var sheet ReviewSheet
	if record.Schema != "yakuori-w3-acceptance-run-v1" || record.ManifestVersion != m.Version || record.CriteriaVersion != c.Version || !slices.Equal(record.Selected, c.FixtureIDs) || runID == "" || root == "" || evidence == "" {
		return sheet, Result{}, errors.New("run schema, selection or versions do not match")
	}
	checked, err := Check(root, m, c)
	if err != nil {
		return sheet, Result{}, fmt.Errorf("fixture and criteria check: %w", err)
	}
	var entry *FixtureRun
	for i := range record.Runs {
		if record.Runs[i].RunID == runID {
			if entry != nil {
				return sheet, checked, errors.New("duplicate run ID")
			}
			entry = &record.Runs[i]
		}
	}
	if entry == nil || !slices.Contains(c.FixtureIDs, entry.FixtureID) {
		return sheet, checked, errors.New("run not found in selected fixtures")
	}
	var fixture *Fixture
	for i := range m.Fixtures {
		if m.Fixtures[i].ID == entry.FixtureID {
			fixture = &m.Fixtures[i]
			break
		}
	}
	if fixture == nil || entry.FixtureRevision != fixture.Revision || entry.SourcePath != fixture.LocalPath || entry.SourceSHA256 != fixture.SHA256 {
		return sheet, checked, errors.New("run fixture or source does not match manifest")
	}
	if record.ModelID == "" || record.ModelSHA256 == "" {
		return sheet, checked, errors.New("run model missing")
	}
	if _, err := decodeDigest(record.ModelSHA256); err != nil {
		return sheet, checked, errors.New("run model pin invalid")
	}
	limits := config.DefaultLimits()
	if f := fixture.Limits; f != nil {
		limits.ArtifactBytes, limits.Units, limits.UnitTextBytes, limits.TotalTextBytes, limits.Segments = f.ArtifactBytes, f.Units, f.UnitTextBytes, f.TotalTextBytes, f.Segments
	}
	sourcePath := resolveReviewPath(root, entry.SourcePath)
	sources, sourceAdapter, err := reviewSource(sourcePath, fixture.Language, limits)
	if err != nil {
		return sheet, checked, fmt.Errorf("source: %w", err)
	}
	available := entry.OutputSHA256 != "" && entry.PublicationStatus == "pass"
	var outputs map[string]string
	if available {
		if entry.Details == nil || entry.Details.ModelID != record.ModelID || entry.OutputPath == "" || entry.PublicationRunID == "" {
			return sheet, checked, errors.New("published output metadata missing")
		}
		outputPath := resolveReviewPath(evidence, entry.OutputPath)
		digest, err := fileDigest(context.Background(), outputPath)
		if err != nil || digest != entry.OutputSHA256 {
			return sheet, checked, errors.New("published output hash mismatch or unreadable")
		}
		outputs, err = reviewOutput(sourceAdapter, outputPath)
		if err != nil {
			return sheet, checked, fmt.Errorf("output: %w", err)
		}
		if len(outputs) != len(sources) {
			return sheet, checked, errors.New("source and output unit counts differ")
		}
	}
	sheet = ReviewSheet{Schema: "yakuori-human-review-v1", ManifestVersion: m.Version, Criteria: c, FixtureID: fixture.ID, Revision: fixture.Revision, RunID: entry.RunID, PublicationRunID: entry.PublicationRunID, RunStatus: entry.Status, ModelID: record.ModelID, ModelSHA256: record.ModelSHA256, SourcePath: entry.SourcePath, SourceSHA256: entry.SourceSHA256, OutputPath: entry.OutputPath, OutputSHA256: entry.OutputSHA256, Rows: []ReviewRow{}}
	if entry.Details != nil {
		sheet.Profile = entry.Details.Profile
	}
	byID := map[string]UnitRecord{}
	for _, u := range entry.Units {
		if _, duplicate := byID[u.ID]; duplicate {
			return ReviewSheet{}, checked, errors.New("duplicate unit in run")
		}
		if _, ok := sources[u.ID]; !ok {
			return ReviewSheet{}, checked, errors.New("unknown unit in run")
		}
		byID[u.ID] = u
	}
	if available && len(byID) != len(sources) {
		return ReviewSheet{}, checked, errors.New("published run has missing units")
	}
	ids := make([]string, 0, len(sources))
	for id := range sources {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		names := []string{}
		for _, category := range fixture.Categories {
			if slices.Contains(category.UnitIDs, id) {
				names = append(names, category.Name)
			}
		}
		u, exists := byID[id]
		if exists && !slices.Equal(u.Categories, names) {
			return ReviewSheet{}, checked, errors.New("run unit categories do not match manifest")
		}
		if available {
			if _, ok := outputs[id]; !ok {
				return ReviewSheet{}, checked, errors.New("output unit ID mismatch")
			}
		}
		sheet.Rows = append(sheet.Rows, ReviewRow{ID: id, Kind: u.Kind, Categories: names, Findings: u.Review, Source: sources[id], Translation: outputs[id], Available: available})
	}
	return sheet, checked, nil
}

func resolveReviewPath(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

func reviewSource(path, language string, limits config.Limits) (map[string]string, *w3strings.Adapter, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	a, err := w3strings.NewForSource(language, limits)
	if err != nil {
		return nil, nil, err
	}
	session, err := a.Import(raw, validate.Profile{})
	if err != nil {
		return nil, nil, err
	}
	result := make(map[string]string, len(session.Units()))
	for _, u := range session.Units() {
		result[u.ID().StableID()] = string(u.Source())
	}
	return result, a, nil
}

// Observe applies the format's output bound instead of the source input limits.
func reviewOutput(a *w3strings.Adapter, path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	observed, err := a.Observe(f)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(observed.Units))
	for _, u := range observed.Units {
		if u.TargetLanguage != "ja" {
			return nil, errors.New("output language is not Japanese")
		}
		result[u.ID.StableID()] = u.Text
	}
	return result, nil
}

// SummarizeReview verifies binding before accepting any human observations.
// The returned summary contains no fixture text, private paths or rationale.
func SummarizeReview(expected ReviewSheet, checked Result, actual ReviewSheet) (ReviewSummary, error) {
	summary := ReviewSummary{FixtureID: expected.FixtureID, RunID: expected.RunID, RunStatus: expected.RunStatus, CriteriaVersion: expected.Criteria.Version, Confirmation: checked.Confirmation, Status: "incomplete", Categories: make(map[string]ReviewCount)}
	wantRows, gotRows := expected.Rows, actual.Rows
	expected.Rows, actual.Rows = nil, nil
	if !reflect.DeepEqual(expected, actual) {
		return summary, errors.New("review sheet run, source, output or criteria mismatch")
	}
	want := make(map[string]ReviewRow, len(wantRows))
	for _, row := range wantRows {
		want[row.ID] = row
	}
	seen := make(map[string]bool, len(gotRows))
	for _, row := range gotRows {
		original, ok := want[row.ID]
		if !ok || seen[row.ID] {
			return summary, errors.New("review sheet has unknown or duplicate unit")
		}
		seen[row.ID] = true
		reviewer, date, verdict, rationale := row.Reviewer, row.Date, row.Verdict, row.Rationale
		row.Reviewer, row.Date, row.Verdict, row.Rationale = "", "", "", ""
		if !reflect.DeepEqual(row, original) {
			return summary, errors.New("review unit binding or text mismatch")
		}
		if verdict != "" && verdict != "pass" && verdict != "fail" && verdict != "hold" {
			return summary, errors.New("invalid review verdict")
		}
		if !row.Available && (reviewer != "" || date != "" || verdict != "" || rationale != "") {
			return summary, errors.New("cannot review unavailable output")
		}
		state := "unreviewed"
		if !row.Available {
			state = "unavailable"
		} else if verdict == "hold" {
			state = "hold"
		} else if verdict != "" && strings.TrimSpace(reviewer) != "" && strings.TrimSpace(rationale) != "" {
			if _, err := time.Parse(time.RFC3339, date); err == nil {
				state = verdict
			}
		}
		addReviewCount(&summary.Units, state)
		for _, category := range row.Categories {
			count := summary.Categories[category]
			addReviewCount(&count, state)
			summary.Categories[category] = count
		}
	}
	if len(seen) != len(want) {
		return summary, errors.New("review sheet has missing units")
	}
	for _, name := range categories {
		if _, ok := summary.Categories[name]; !ok {
			summary.Categories[name] = ReviewCount{}
		}
	}
	if checked.AcceptanceReady && summary.Units.Total > 0 && summary.Units.Reviewed == summary.Units.Total {
		if summary.Units.Fail > 0 {
			summary.Status = "fail"
		} else {
			summary.Status = "pass"
		}
	}
	return summary, nil
}

func addReviewCount(c *ReviewCount, state string) {
	c.Total++
	switch state {
	case "pass":
		c.Reviewed++
		c.Pass++
	case "fail":
		c.Reviewed++
		c.Fail++
	case "hold":
		c.Hold++
	case "unavailable":
		c.Unavailable++
	default:
		c.Unreviewed++
	}
}

// ReviewScope counts source units once per fixture, independently of run count.
type ReviewScope struct {
	Units      int            `json:"units"`
	Categories map[string]int `json:"categories"`
}

// ReviewCollection covers the selected fixtures within one producer record.
type ReviewCollection struct {
	Status             string                 `json:"status"`
	CriteriaVersion    string                 `json:"criteria_version"`
	Confirmation       string                 `json:"confirmation"`
	Units              ReviewCount            `json:"units"`
	Categories         map[string]ReviewCount `json:"categories"`
	Runs               []ReviewSummary        `json:"runs"`
	UnreviewedFixtures []string               `json:"unreviewed_fixtures"`
	SelectedScope      ReviewScope            `json:"selected_scope"`
	UnsubmittedScope   ReviewScope            `json:"unsubmitted_scope"`
}

// CollectReviews combines independently verified run summaries and names every
// selected fixture for which no complete human review has been supplied.
func CollectReviews(manifest Manifest, selected []string, checked Result, summaries []ReviewSummary) (ReviewCollection, error) {
	result := ReviewCollection{Status: "incomplete", Confirmation: checked.Confirmation, Categories: map[string]ReviewCount{}, Runs: summaries, UnreviewedFixtures: []string{}}
	seenRuns, completeFixtures, submittedFixtures := map[string]bool{}, map[string]bool{}, map[string]bool{}
	result.SelectedScope.Categories = map[string]int{}
	result.UnsubmittedScope.Categories = map[string]int{}
	allComplete, anyFail := true, false
	for _, summary := range summaries {
		if !slices.Contains(selected, summary.FixtureID) || seenRuns[summary.RunID] || (result.CriteriaVersion != "" && result.CriteriaVersion != summary.CriteriaVersion) || summary.Confirmation != checked.Confirmation {
			return result, errors.New("review collection contains a duplicate or mismatched run")
		}
		seenRuns[summary.RunID] = true
		submittedFixtures[summary.FixtureID] = true
		result.CriteriaVersion = summary.CriteriaVersion
		mergeReviewCount(&result.Units, summary.Units)
		for name, count := range summary.Categories {
			current := result.Categories[name]
			mergeReviewCount(&current, count)
			result.Categories[name] = current
		}
		if summary.Units.Total > 0 && summary.Units.Reviewed == summary.Units.Total {
			completeFixtures[summary.FixtureID] = true
		} else {
			allComplete = false
		}
		if summary.Units.Fail > 0 {
			anyFail = true
		}
	}
	for _, id := range selected {
		var fixture *Fixture
		for i := range manifest.Fixtures {
			if manifest.Fixtures[i].ID == id {
				fixture = &manifest.Fixtures[i]
				break
			}
		}
		if fixture == nil {
			return result, errors.New("selected fixture missing from manifest")
		}
		addFixtureScope(&result.SelectedScope, *fixture)
		if !submittedFixtures[id] {
			addFixtureScope(&result.UnsubmittedScope, *fixture)
		}
		if !completeFixtures[id] {
			result.UnreviewedFixtures = append(result.UnreviewedFixtures, id)
		}
	}
	if checked.AcceptanceReady && len(summaries) > 0 && allComplete && len(result.UnreviewedFixtures) == 0 {
		if anyFail {
			result.Status = "fail"
		} else {
			result.Status = "pass"
		}
	}
	return result, nil
}

func mergeReviewCount(dst *ReviewCount, src ReviewCount) {
	dst.Total += src.Total
	dst.Reviewed += src.Reviewed
	dst.Pass += src.Pass
	dst.Fail += src.Fail
	dst.Hold += src.Hold
	dst.Unreviewed += src.Unreviewed
	dst.Unavailable += src.Unavailable
}

func addFixtureScope(scope *ReviewScope, fixture Fixture) {
	scope.Units += fixture.Units
	for _, category := range fixture.Categories {
		scope.Categories[category.Name] += len(category.UnitIDs)
	}
}
