package main

import (
	"context"
	"errors"
	"github.com/sh4869221b/yakuori/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/segment"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func TestTranslateFixtureCreatesSeparateUnits(t *testing.T) {
	// Given two labels from a research artifact.
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(path, []byte(`["Cast Aard","Difficulty"]`), 0600); err != nil {
		t.Fatal(err)
	}
	// When the research fixture is imported.
	session, err := translateFixture(path)
	// Then each label is a distinct unit bound to the original artifact.
	if err != nil {
		t.Fatal(err)
	}
	units := session.Units()
	if len(units) != 2 || string(units[0].Source()) != "Cast Aard" || string(units[1].Source()) != "Difficulty" || units[0].ID() == units[1].ID() {
		t.Fatalf("units=%v", units)
	}
}

func TestTranslateFixtureRejectsMalformedInput(t *testing.T) {
	for _, data := range []string{`null`, `[]`, `[""]`, `[1]`, `["unterminated]`} {
		t.Run(data, func(t *testing.T) {
			// Given malformed or empty research input.
			path := filepath.Join(t.TempDir(), "fixture.json")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			// When it is imported, then no session is accepted.
			if _, err := translateFixture(path); err == nil {
				t.Fatal("accepted invalid fixture")
			}
		})
	}
}

func TestTranslateFixtureRejectsOversizedInput(t *testing.T) {
	// Given a file exceeding the bounded research reader.
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(path, make([]byte, (2<<20)+1), 0600); err != nil {
		t.Fatal(err)
	}
	// When imported, then the size error precedes JSON decoding.
	if _, err := translateFixture(path); err == nil || err.Error() != "research fixture exceeds 2 MiB" {
		t.Fatalf("error=%v", err)
	}
}

func TestMechanicalCountsIncludePriorValidatedUnitsOnFailure(t *testing.T) {
	// Given three units and an all-or-nothing failure on the third unit.
	path := filepath.Join(t.TempDir(), "fixture.json")
	if err := os.WriteFile(path, []byte(`["one","two","three"]`), 0600); err != nil {
		t.Fatal(err)
	}
	session, err := translateFixture(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		phase                 string
		failures, denominator int
	}{
		{"validate", 1, 3}, {"restore", 1, 3}, {"generate", 0, 2},
	} {
		t.Run(tc.phase, func(t *testing.T) {
			failure := &localize.Error{Phase: tc.phase, UnitID: session.Units()[2].ID(), Err: context.DeadlineExceeded}
			// When no accepted subset remains, then prior checked units still count.
			failures, denominator := mechanicalCounts(session, 0, failure)
			if failures != tc.failures || denominator != tc.denominator {
				t.Fatalf("counts=%d/%d want=%d/%d", failures, denominator, tc.failures, tc.denominator)
			}
		})
	}
}

func TestOperationalProbeRejectsInputBeforeModelLoad(t *testing.T) {
	// Given inputs over the adopted artifact or per-unit boundary and no model.
	l := config.DefaultLimits()
	for _, data := range []string{strings.Repeat(" ", l.ArtifactBytes+1), `["` + strings.Repeat("a", l.UnitTextBytes+1) + `"]`} {
		path := filepath.Join(t.TempDir(), "input.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		var report translationReport
		// When operational admission runs, then rejection precedes model Open.
		err := translateProbe(&report, "missing-model", path, l.ContextTokens, l.MaxOutputTokens, l.RequestTimeout, l.GenerationTimeout, false, true)
		if !errors.Is(err, config.ErrLimitExceeded) || report.LoadMS != 0 || len(report.Runs) != 0 {
			t.Fatalf("error=%v report=%+v", err, report)
		}
	}
}

type metricsBackend struct {
	inference.Engine
	calls int
}

func (*metricsBackend) Info(context.Context) (inference.ModelInfo, error) {
	return inference.ModelInfo{ContextTokens: 100, PolicySchema: 1}, nil
}

func (*metricsBackend) CountTokens(_ context.Context, r inference.GenerationRequest) (int, error) {
	return len(r.TokenIDs()), nil
}

func (e *metricsBackend) Generate(context.Context, inference.GenerationRequest) (inference.GenerationResult, error) {
	e.calls++
	return inference.GenerationResult{}, errors.New("unexpected generation")
}

func TestMechanicalCountsBeforeGeneration(t *testing.T) {
	for _, phase := range []string{"protect", "plan"} {
		t.Run(phase, func(t *testing.T) {
			var units []unit.TranslationUnit
			for i, text := range []string{"one", "two", "three"} {
				id, err := unit.NewUnitID("metrics", "v1", text)
				if err != nil {
					t.Fatal(err)
				}
				raw := []byte(text)
				if phase == "protect" && i == 2 {
					raw = []byte{0xff}
				}
				u, err := unit.NewTranslationUnit(id, raw, "en", "ja", nil)
				if err != nil {
					t.Fatal(err)
				}
				units = append(units, u)
			}
			session, err := unit.NewSession([]byte("fixture"), units)
			if err != nil {
				t.Fatal(err)
			}
			profile, err := validate.NewProfileWithSegmentation([32]byte{1}, segment.ProfileInput())
			if err != nil {
				t.Fatal(err)
			}
			limits := config.DefaultLimits()
			if phase == "plan" {
				limits.Segments = 2
			}
			backend := &metricsBackend{}
			core, err := localize.NewSegmentedCoreWithLimits(backend, func(_ context.Context, _ unit.UnitID, text string) (inference.GenerationRequest, error) {
				policy, err := inference.NewGenerationPolicy(1, 16, time.Second)
				if err != nil {
					return inference.GenerationRequest{}, err
				}
				return inference.NewGenerationRequest(inference.PreparedRequest{RenderedPrompt: text, TokenIDs: []int{1, 2, 3}, Identity: inference.RequestIdentity{PromptSchema: 1}}, policy)
			}, limits)
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := core.Generate(context.Background(), session, profile)
			if err == nil || backend.calls != 0 || accepted != nil {
				t.Fatalf("accepted=%v calls=%d err=%v", accepted, backend.calls, err)
			}
			failures, denominator := mechanicalCounts(session, 0, err)
			if failures != 0 || denominator != 0 {
				t.Fatalf("counts=%d/%d before generation: %v", failures, denominator, err)
			}
		})
	}
}
