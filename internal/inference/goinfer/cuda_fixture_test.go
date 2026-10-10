//go:build cuda && cudasmoke

package goinfer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

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
