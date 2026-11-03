//go:build linux

package acceptance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/cli"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/validate"
)

// The fixture, criteria and review decisions are synthetic test data only.
func reviewFixture(t *testing.T) (Manifest, Criteria, RunRecord, string, string) {
	t.Helper()
	root := t.TempDir()
	source, err := os.ReadFile("testdata/small.w3strings")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.w3strings"), source, 0600); err != nil {
		t.Fatal(err)
	}
	adapter := w3strings.New()
	profile, err := validate.NewProfile([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	session, err := adapter.Import(source, profile)
	if err != nil {
		t.Fatal(err)
	}
	var accepted []validate.AcceptedTranslation
	for _, u := range session.Units() {
		item, err := validate.Validate(session, u.ID(), profile, string(u.Source()))
		if err != nil {
			t.Fatal(err)
		}
		accepted = append(accepted, item)
	}
	var buf bytes.Buffer
	if _, err := adapter.Export(&buf, session, profile, accepted); err != nil {
		t.Fatal(err)
	}
	output := buf.Bytes()
	if err := os.WriteFile(filepath.Join(root, "output.w3strings"), output, 0600); err != nil {
		t.Fatal(err)
	}
	sourceDigest, outputDigest := sha256.Sum256(source), sha256.Sum256(output)
	fixture := Fixture{ID: "synthetic", Kind: "mod", SourceURL: "test-only", Revision: "v1", License: "project-authored", LocalPath: "source.w3strings", SHA256: hex.EncodeToString(sourceDigest[:]), Bytes: len(source), Units: 3, TextBytes: 16, MaxTextBytes: 11, Format: "w3strings", FormatVersion: 162, Language: "en"}
	for _, name := range categories {
		id := "10"
		if name == "empty_text" {
			id = "20"
		}
		if name == "long_text" {
			id = "30"
		}
		fixture.Categories = append(fixture.Categories, Category{Name: name, UnitIDs: []string{id}, Evidence: "test-only"})
	}
	m := Manifest{Version: "test-manifest", Fixtures: []Fixture{fixture}}
	c := Criteria{Version: "test-criteria", ManifestVersion: m.Version, FixtureIDs: []string{fixture.ID}, Values: map[string]Criterion{}, Confirmation: Confirmation{Status: "confirmed", Date: "2026-01-01T00:00:00Z", Reviewer: "test-only", Decision: "approved", ManifestVersion: m.Version, CriteriaVersion: "test-criteria"}}
	for _, name := range criterionNames {
		c.Values[name] = Criterion{Value: "1", Rationale: "test-only", Impact: "test-only"}
	}
	for name, value := range map[string]string{"units": "3", "text_bytes": "16", "artifact_bytes": strconv.Itoa(len(source))} {
		v := c.Values[name]
		v.Value = value
		c.Values[name] = v
	}
	texts, _, err := reviewSource(filepath.Join(root, "source.w3strings"), "en", config.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	units := []UnitRecord{}
	for id := range texts {
		u := UnitRecord{ID: id, Kind: "generated", Categories: []string{}}
		for _, category := range fixture.Categories {
			if category.UnitIDs[0] == id {
				u.Categories = append(u.Categories, category.Name)
			}
		}
		if id == "10" {
			u.Review = []validate.ReviewCode{validate.SourceIdentical}
		}
		units = append(units, u)
	}
	r := RunRecord{Schema: "yakuori-w3-acceptance-run-v1", ManifestVersion: m.Version, CriteriaVersion: c.Version, Selected: c.FixtureIDs, ModelID: "test-model", ModelSHA256: strings.Repeat("1", 64), Runs: []FixtureRun{{RunID: "synthetic-fresh", FixtureID: fixture.ID, FixtureRevision: fixture.Revision, SourcePath: fixture.LocalPath, SourceSHA256: fixture.SHA256, OutputPath: "output.w3strings", OutputSHA256: hex.EncodeToString(outputDigest[:]), Status: "pass", PublicationStatus: "pass", PublicationRunID: strings.Repeat("a", 32), Details: &cli.AcceptanceDetails{ModelID: "test-model", Profile: [32]byte{1}}, Units: units}}}
	return m, c, r, root, root
}

func cloneReview(t *testing.T, sheet ReviewSheet) ReviewSheet {
	t.Helper()
	b, err := json.Marshal(sheet)
	if err != nil {
		t.Fatal(err)
	}
	var result ReviewSheet
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestReviewRoundTripAndScope(t *testing.T) {
	m, c, run, root, evidence := reviewFixture(t)
	want, checked, err := GenerateReview(m, c, run, root, evidence, "synthetic-fresh")
	if err != nil {
		t.Fatal(err)
	}
	if len(want.Rows) != 3 || want.Rows[0].Source != "Hello" || want.Rows[0].Translation != "Hello" || len(want.Rows[0].Findings) != 1 {
		t.Fatalf("incorrect producer binding: %+v", want.Rows)
	}
	actual := cloneReview(t, want)
	actual.Rows[0].Reviewer, actual.Rows[0].Date, actual.Rows[0].Verdict, actual.Rows[0].Rationale = "test reviewer", "2026-01-01T12:00:00Z", "pass", "test observation"
	actual.Rows[1].Verdict = "hold"
	result, err := SummarizeReview(want, checked, actual)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "incomplete" || result.Units.Reviewed != 1 || result.Units.Hold != 1 || result.Units.Unreviewed != 1 || result.Categories["short_names"].Reviewed != 1 || result.Categories["empty_text"].Reviewed != 0 {
		t.Fatalf("bad partial summary: %+v", result)
	}
	b, _ := json.Marshal(result)
	if strings.Contains(string(b), "Hello") || strings.Contains(string(b), "test observation") || strings.Contains(string(b), root) {
		t.Fatal("summary exposed private data")
	}
	for i := range actual.Rows {
		actual.Rows[i].Reviewer, actual.Rows[i].Date, actual.Rows[i].Verdict, actual.Rows[i].Rationale = "test reviewer", "2026-01-01T12:00:00Z", "pass", "test observation"
	}
	result, err = SummarizeReview(want, checked, actual)
	if err != nil || result.Status != "pass" || result.Units.Reviewed != 3 {
		t.Fatalf("completed review: %+v %v", result, err)
	}
	actual.Rows[1].Verdict = "fail"
	result, err = SummarizeReview(want, checked, actual)
	if err != nil || result.Status != "fail" {
		t.Fatalf("failed review: %+v %v", result, err)
	}
	checked.AcceptanceReady, checked.Confirmation = false, "pending"
	result, err = SummarizeReview(want, checked, actual)
	if err != nil || result.Status != "incomplete" {
		t.Fatalf("unconfirmed criteria: %+v %v", result, err)
	}
}

func TestReviewRejectsBindingAndMissingRows(t *testing.T) {
	m, c, run, root, evidence := reviewFixture(t)
	want, checked, err := GenerateReview(m, c, run, root, evidence, "synthetic-fresh")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		edit func(*ReviewSheet)
	}{
		{"run", func(s *ReviewSheet) { s.RunID = "other" }},
		{"profile", func(s *ReviewSheet) { s.Profile[0]++ }},
		{"output hash", func(s *ReviewSheet) { s.OutputSHA256 = strings.Repeat("0", 64) }},
		{"fixture", func(s *ReviewSheet) { s.FixtureID = "other" }},
		{"criteria content", func(s *ReviewSheet) {
			v := s.Criteria.Values["omission"]
			v.Value = "changed"
			s.Criteria.Values["omission"] = v
		}},
		{"duplicate", func(s *ReviewSheet) { s.Rows[1] = s.Rows[0] }},
		{"missing", func(s *ReviewSheet) { s.Rows = s.Rows[:2] }},
		{"category", func(s *ReviewSheet) { s.Rows[0].Categories = nil }},
		{"text", func(s *ReviewSheet) { s.Rows[0].Translation = "changed" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := cloneReview(t, want)
			tc.edit(&actual)
			if _, err := SummarizeReview(want, checked, actual); err == nil {
				t.Fatal("accepted mismatched sheet")
			}
		})
	}
	run.Runs[0].OutputSHA256 = strings.Repeat("0", 64)
	if _, _, err := GenerateReview(m, c, run, root, evidence, "synthetic-fresh"); err == nil {
		t.Fatal("accepted changed output")
	}
}

func TestReviewUnavailableOutput(t *testing.T) {
	m, c, run, root, evidence := reviewFixture(t)
	run.Runs[0].Status, run.Runs[0].PublicationStatus, run.Runs[0].OutputSHA256 = "not_run", "not_run", ""
	run.Runs[0].Units, run.Runs[0].Details = nil, nil
	want, checked, err := GenerateReview(m, c, run, root, evidence, "synthetic-fresh")
	if err != nil {
		t.Fatal(err)
	}
	got, err := SummarizeReview(want, checked, cloneReview(t, want))
	if err != nil || got.Status != "incomplete" || got.Units.Unavailable != 3 {
		t.Fatalf("unavailable scope: %+v %v", got, err)
	}
}

func TestCollectReviewsShowsMissingSelectedFixture(t *testing.T) {
	m, c, run, root, evidence := reviewFixture(t)
	want, checked, err := GenerateReview(m, c, run, root, evidence, "synthetic-fresh")
	if err != nil {
		t.Fatal(err)
	}
	for i := range want.Rows {
		want.Rows[i].Reviewer, want.Rows[i].Date, want.Rows[i].Verdict, want.Rows[i].Rationale = "reviewer", "2026-01-01T12:00:00Z", "pass", "test"
	}
	expected, _, err := GenerateReview(m, c, run, root, evidence, "synthetic-fresh")
	if err != nil {
		t.Fatal(err)
	}
	summary, err := SummarizeReview(expected, checked, want)
	if err != nil {
		t.Fatal(err)
	}
	other := m.Fixtures[0]
	other.ID = "other"
	m.Fixtures = append(m.Fixtures, other)
	got, err := CollectReviews(m, []string{"synthetic", "other"}, checked, []ReviewSummary{summary})
	if err != nil || got.Status != "incomplete" || len(got.UnreviewedFixtures) != 1 || got.UnreviewedFixtures[0] != "other" || got.Units.Reviewed != 3 || got.SelectedScope.Units != 6 || got.UnsubmittedScope.Units != 3 || got.SelectedScope.Categories["short_names"] != 2 || got.UnsubmittedScope.Categories["short_names"] != 1 {
		t.Fatalf("collection: %+v %v", got, err)
	}
	if _, err := CollectReviews(m, c.FixtureIDs, checked, []ReviewSummary{summary, summary}); err == nil {
		t.Fatal("duplicate run accepted")
	}
}

func TestReviewCommandRoundTrip(t *testing.T) {
	m, c, run, root, _ := reviewFixture(t)
	for name, data := range map[string]any{"manifest.json": m, "criteria.json": c, "run.json": run} {
		b, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	common := []string{"--manifest", filepath.Join(root, "manifest.json"), "--criteria", filepath.Join(root, "criteria.json"), "--run", filepath.Join(root, "run.json"), "--root", root, "--sheet", filepath.Join(root, "review.json")}
	args := append([]string{"run", "./cmd/review", "generate", "--run-id", "synthetic-fresh"}, common...)
	if out, err := exec.Command("go", args...).CombinedOutput(); err != nil {
		t.Fatalf("generate: %s: %v", out, err)
	}
	info, err := os.Stat(filepath.Join(root, "review.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private sheet permissions: %v %v", info, err)
	}
	args = append([]string{"run", "./cmd/review", "summarize"}, common...)
	out, err := exec.Command("go", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("summarize: %s: %v", out, err)
	}
	var result ReviewCollection
	if err := json.Unmarshal(out, &result); err != nil || result.Status != "incomplete" || result.Units.Unreviewed != 3 {
		t.Fatalf("summary: %s %v", out, err)
	}
}

func TestReviewAcceptsExpandedPublishedOutput(t *testing.T) {
	m, c, run, root, evidence := reviewFixture(t)
	source, err := os.ReadFile(filepath.Join(root, "source.w3strings"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := w3strings.New()
	profile, err := validate.NewProfile([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	session, err := adapter.Import(source, profile)
	if err != nil {
		t.Fatal(err)
	}
	longText := strings.Repeat("長", 1000)
	var accepted []validate.AcceptedTranslation
	for _, u := range session.Units() {
		candidate := string(u.Source())
		if u.ID().StableID() == "10" {
			candidate = longText
		}
		item, err := validate.Validate(session, u.ID(), profile, candidate)
		if err != nil {
			t.Fatal(err)
		}
		accepted = append(accepted, item)
	}
	var output bytes.Buffer
	if _, err := adapter.Export(&output, session, profile, accepted); err != nil {
		t.Fatal(err)
	}
	if output.Len() <= config.DefaultLimits().ArtifactBytes || len(longText) <= config.DefaultLimits().UnitTextBytes {
		t.Fatal("test did not exceed input limits")
	}
	if err := os.WriteFile(filepath.Join(root, "output.w3strings"), output.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(output.Bytes())
	run.Runs[0].OutputSHA256 = hex.EncodeToString(digest[:])
	sheet, _, err := GenerateReview(m, c, run, root, evidence, "synthetic-fresh")
	if err != nil || sheet.Rows[0].Translation != longText {
		t.Fatalf("legal expanded output: %v", err)
	}
}

func TestReviewDistinguishesPublicationRuns(t *testing.T) {
	m, c, run, root, evidence := reviewFixture(t)
	original, _, err := GenerateReview(m, c, run, root, evidence, "synthetic-fresh")
	if err != nil {
		t.Fatal(err)
	}
	// All metadata and output are identical, apart from the existing producer's
	// publication run ID, as can happen on two separate identical runs.
	run.Runs[0].PublicationRunID = strings.Repeat("b", 32)
	other, checked, err := GenerateReview(m, c, run, root, evidence, "synthetic-fresh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SummarizeReview(other, checked, cloneReview(t, original)); err == nil {
		t.Fatal("accepted review from another publication run")
	}
	run.Runs[0].PublicationRunID = ""
	if _, _, err := GenerateReview(m, c, run, root, evidence, "synthetic-fresh"); err == nil {
		t.Fatal("accepted published output without publication run ID")
	}
}
