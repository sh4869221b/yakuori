package config

import (
	"errors"
	"testing"
	"time"
)

func TestDefaultLimitsMatchAdoptedCandidate(t *testing.T) {
	want := Limits{
		ArtifactBytes: 1495, Units: 76, UnitTextBytes: 980, TotalTextBytes: 1036, Segments: 76,
		ContextTokens: 4096, MaxOutputTokens: 2048, RequestTimeout: 30 * time.Second,
		GenerationTimeout: 300 * time.Second, DBOperationTimeout: time.Second, DBCleanupTimeout: time.Second,
	}
	got := DefaultLimits()
	if err := got.Check(); err != nil || got != want {
		t.Fatalf("default limits = %+v, error = %v; want %+v", got, err, want)
	}
}

func TestLimitsRejectInvalidValues(t *testing.T) {
	for _, value := range []int{0, -1} {
		for _, field := range []string{"artifact", "units", "unit_text", "total_text", "segments", "context", "output", "request", "generation", "operation", "cleanup"} {
			t.Run(field+time.Duration(value).String(), func(t *testing.T) {
				limits := DefaultLimits()
				switch field {
				case "artifact":
					limits.ArtifactBytes = value
				case "units":
					limits.Units = value
				case "unit_text":
					limits.UnitTextBytes = value
				case "total_text":
					limits.TotalTextBytes = value
				case "segments":
					limits.Segments = value
				case "context":
					limits.ContextTokens = value
				case "output":
					limits.MaxOutputTokens = value
				case "request":
					limits.RequestTimeout = time.Duration(value)
				case "generation":
					limits.GenerationTimeout = time.Duration(value)
				case "operation":
					limits.DBOperationTimeout = time.Duration(value)
				case "cleanup":
					limits.DBCleanupTimeout = time.Duration(value)
				}
				if err := limits.Check(); !errors.Is(err, ErrInvalidLimits) {
					t.Fatalf("invalid %s = %d: %v", field, value, err)
				}
			})
		}
	}
	for _, contextTokens := range []int{2047, 2048} {
		limits := DefaultLimits()
		limits.ContextTokens = contextTokens
		if err := limits.Check(); !errors.Is(err, ErrInvalidLimits) {
			t.Fatalf("context %d <= output accepted: %v", contextTokens, err)
		}
	}
}
