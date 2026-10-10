//go:build cuda && cudasmoke

package goinfer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func cudaFixture(path string) (unit.Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return unit.Session{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	if err != nil {
		return unit.Session{}, err
	}
	if len(data) > 2<<20 {
		return unit.Session{}, errors.New("research fixture exceeds 2MiB")
	}
	var texts []string
	if err := json.Unmarshal(data, &texts); err != nil {
		return unit.Session{}, err
	}
	if len(texts) == 0 || len(texts) > 4096 {
		return unit.Session{}, errors.New("research fixture requires 1..4096 units")
	}
	units := make([]unit.TranslationUnit, 0, len(texts))
	total := 0
	for i, text := range texts {
		total += len(text)
		if text == "" || total > 2<<20 {
			return unit.Session{}, errors.New("empty or oversized research text")
		}
		id, err := unit.NewUnitID("cpu-research", "v1", fmt.Sprint(i))
		if err != nil {
			return unit.Session{}, err
		}
		u, err := unit.NewTranslationUnit(id, []byte(text), "en", "ja", nil)
		if err != nil {
			return unit.Session{}, err
		}
		units = append(units, u)
	}
	return unit.NewSession(data, units)
}

func TestCUDAEvaluationFixtures(t *testing.T) {
	root := filepath.Join("..", "..", "..", "docs", "evidence", "cpu-long-context", "index-translate")
	limits := config.DefaultLimits()
	for _, tc := range []struct {
		path                                        string
		units, artifactBytes, unitBytes, totalBytes int
	}{
		{"fixtures/01-fixture.json", 19, 376, 19, 259},
		{"prose/fixtures/00-short.json", 1, 55, 50, 50},
		{"prose/fixtures/01-medium.json", 1, 263, 258, 258},
		{"prose/fixtures/02-long.json", 1, 985, 980, 980},
		{"fixtures/02-x4.json", 76, 1495, 19, 1036},
	} {
		t.Run(tc.path, func(t *testing.T) {
			session, err := cudaFixture(filepath.Join(root, tc.path))
			if err != nil {
				t.Fatal(err)
			}
			units, artifactBytes := len(session.Units()), session.ArtifactBytes()
			unitBytes, totalBytes := 0, 0
			for _, u := range session.Units() {
				unitBytes = max(unitBytes, u.SourceBytes())
				totalBytes += u.SourceBytes()
			}
			if units != tc.units || artifactBytes != tc.artifactBytes || unitBytes != tc.unitBytes || totalBytes != tc.totalBytes {
				t.Fatalf("units=%d artifact=%d max unit=%d total text=%d", units, artifactBytes, unitBytes, totalBytes)
			}
			for _, err := range []error{limits.CheckUnits(units), limits.CheckArtifactBytes(artifactBytes), limits.CheckUnitTextBytes(unitBytes), limits.CheckTotalTextBytes(totalBytes)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("units=%d artifact_bytes=%d max_unit_bytes=%d total_text_bytes=%d within product defaults", units, artifactBytes, unitBytes, totalBytes)
		})
	}
}
