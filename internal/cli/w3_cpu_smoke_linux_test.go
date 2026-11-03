//go:build linux && cpusmoke

package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/models"
	"github.com/sh4869221b/yakuori/internal/publication"
)

func TestW3CPURegression(t *testing.T) {
	const (
		modelID   = "cpu-regression"
		modelSHA  = "1d9614638d18024d0fbb36575a15f1302a3adf044df10345688ec4f6e1c4ff32"
		modelSize = 491400064
		game      = "witcher3"
		artifact  = "cpu-regression"
	)
	model := os.Getenv("YAKUORI_CPU_MODEL")
	if model == "" {
		t.Fatal("YAKUORI_CPU_MODEL must name the local pinned GGUF fixture")
	}
	ctx := context.Background()
	dir := t.TempDir()
	paths := config.Paths{Data: filepath.Join(dir, "data"), Config: filepath.Join(dir, "config"), Cache: filepath.Join(dir, "cache"), State: filepath.Join(dir, "state")}
	raw, err := os.ReadFile(filepath.Join("..", "..", "tools", "acceptance", "testdata", "small.w3strings"))
	if err != nil {
		t.Fatal(err)
	}
	source, output := filepath.Join(dir, "source.w3strings"), filepath.Join(dir, "output.w3strings")
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	registry := models.New(paths)
	entry, err := registry.Import(ctx, modelID, model)
	if err != nil {
		t.Fatalf("import CPU fixture: %v", err)
	}
	if entry.SizeBytes != modelSize || entry.SHA256 != modelSHA {
		t.Fatal("CPU fixture does not match the pinned model size and SHA-256")
	}
	if err := registry.Use(ctx, modelID); err != nil {
		t.Fatal(err)
	}
	var modelDigest [32]byte
	digest, err := hex.DecodeString(modelSHA)
	if err != nil {
		t.Fatal(err)
	}
	copy(modelDigest[:], digest)
	observation := localize.AcceptanceObservation{SourceSHA256: sha256.Sum256(raw)}
	t.Logf("fixture=tools/acceptance/testdata/small.w3strings source_sha256=%x go=%s os=%s arch=%s", observation.SourceSHA256, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	details := AcceptanceDetails{ExpectedModelID: modelID, ExpectedModelSHA256: modelDigest}
	args := []string{"--format", "w3strings", "--source-language", "en", "--target-language", "ja", "--game", game, "--artifact", artifact, "--backend", "cpu", "--output", output, source}
	result, err := RunW3Acceptance(ctx, args, paths, registry.OpenSelected, &observation, &details)
	t.Logf("CPU W3 evidence: model=%s sha256=%s size=%d backend_requested=%s backend=%s quant=%s model_context=%d effective_context=%d goinfer=%s policy=%+v generated=%d completed=%d failed=%d validation=%+v final_validated=%t tm_committed=%t publication=%s", details.ModelID, modelSHA, modelSize, details.Info.RequestedBackend, details.Info.Backend, details.Info.ComputeQuant, details.Info.ModelContextTokens, details.Info.ContextTokens, details.Info.BackendPin, details.Policy, observation.GenerateCalls, observation.GenerateCompleted, observation.GenerateFailed, observation.Validation, observation.FinalValidated, result.TMCommitted, result.Publication.State)
	if err != nil {
		t.Fatalf("CPU W3 run failed: %v", err)
	}
	if details.Info.RequestedBackend != "cpu" || details.Info.Backend != "cpu" || details.ModelID != modelID || details.Info.ModelSHA256 != modelDigest {
		t.Fatal("CPU model identity or backend mismatch")
	}
	if observation.GenerateCalls != 2 || observation.GenerateCompleted != 2 || observation.GenerateFailed != 0 || observation.Validation != (localize.ValidationCounts{Attempted: 3, Passed: 3}) || observation.TMValidation != (localize.ValidationCounts{}) || !observation.FinalValidated || !result.TMCommitted || result.Publication.State != publication.Published || result.Publication.OutputPath != output {
		t.Fatal("CPU generation, validation, commit or publication did not complete")
	}
	texts := make(map[string]string, 3)
	wantKinds := map[string]string{"10": "generated", "20": "unchanged", "30": "generated"}
	for _, u := range observation.Units {
		if kind, ok := wantKinds[u.ID]; !ok || kind != u.Kind {
			t.Fatalf("unexpected accepted unit %s kind %s", u.ID, u.Kind)
		}
		if _, exists := texts[u.ID]; exists {
			t.Fatalf("duplicate accepted unit %s", u.ID)
		}
		texts[u.ID] = u.Text
	}
	if len(texts) != 3 || texts["20"] != "" {
		t.Fatal("accepted units or empty entry mismatch")
	}
	exp := w3Expectation{Texts: texts, UnitOrder: []string{"20", "30", "10"}, KeyOrder: []string{"00000001", "00000002", "00000003"}, KeyIDs: map[string]string{"00000001": "10", "00000002": "20", "00000003": "30"}}
	w3AssertPublished(t, raw, "en", output, exp)
	id, engine, err := registry.OpenSelected(ctx, "cpu")
	if err != nil {
		t.Fatal(err)
	}
	expected := expectedW3TranslationRows(t, w3TMInput{Raw: raw, SourceLanguage: "en", ModelID: id, Game: game, Artifact: artifact, Keys: exp.keys(t), Texts: texts}, engine)
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	rows := readTranslationTM(t, paths)
	if len(rows) != len(expected) {
		t.Fatalf("committed SQLite rows=%d, want %d", len(rows), len(expected))
	}
	for _, row := range expected {
		found := false
		for _, actual := range rows {
			if actual.Schema == row.Schema && string(actual.Source) == string(row.Source) && string(actual.Profile) == string(row.Profile) && actual.Text == row.Text {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("committed SQLite row does not match accepted W3 translation")
		}
	}
}
