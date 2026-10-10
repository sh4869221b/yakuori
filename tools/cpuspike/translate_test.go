package main

import (
	"context"
	"errors"
	"github.com/sh4869221b/yakuori/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/localize"
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
