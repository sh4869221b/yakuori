//go:build cpusmoke && linux

package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/models"
)

func TestPlamoCPUCLI(t *testing.T) {
	source := os.Getenv("YAKUORI_PLAMO_MODEL_DIR")
	if source == "" {
		t.Fatal("YAKUORI_PLAMO_MODEL_DIR must name the pinned local PLaMo 3 Translate 0.8B safetensors directory")
	}
	weights, err := os.Open(filepath.Join(source, "model.safetensors"))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, weights)
	closeErr := weights.Close()
	if copyErr != nil || closeErr != nil || fmt.Sprintf("%x", hash.Sum(nil)) != "7c9f0066d166c075bdbe83ada3d444e08bd7a6ee3f345f47a1f3c442aa6898c5" {
		t.Fatal("unverified PLaMo 0.8B weights")
	}
	root := t.TempDir()
	paths := config.Paths{Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"), State: filepath.Join(root, "state")}
	registry := models.New(paths)
	ctx := context.Background()
	entry, err := registry.Import(ctx, "plamo3-08b", source)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Use(ctx, "plamo3-08b"); err != nil {
		t.Fatal(err)
	}
	decoded, err := hex.DecodeString(entry.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	var modelSHA [32]byte
	copy(modelSHA[:], decoded)
	for _, test := range []struct{ name, from, to, input, want string }{
		{"en-ja", "en", "ja", "The meeting has been moved to next Monday.", "会議は来週の月曜日に変更になりました。"},
		{"ja-en", "ja", "en", "会議は来週の月曜日に変更されました。", "The meeting has been moved to next Monday."},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, test.name+".txt")
			if err := os.WriteFile(path, []byte(test.input), 0600); err != nil {
				t.Fatal(err)
			}
			command, _, err := parseTranslation("translate", []string{"--source-language", test.from, "--target-language", test.to, "--game", "plamo-smoke", "--artifact", test.name, "--backend", "cpu", path})
			if err != nil {
				t.Fatal(err)
			}
			for attempt := range 2 {
				var stdout bytes.Buffer
				observation := &localize.AcceptanceObservation{}
				details := &AcceptanceDetails{ExpectedModelID: "plamo3-08b", ExpectedModelSHA256: modelSHA}
				result, err := command.executeObserved(ctx, &stdout, paths, registry.OpenSelected, observation, details)
				if err != nil || !result.TMCommitted || stdout.String() != test.want {
					t.Fatalf("translation failed: result=%+v err=%v output=%q", result, err, stdout.String())
				}
				if details.Info.Backend != "cpu" || details.Info.ComputeQuant != "int4" || details.Info.Template.Family != "pfml3-translation-v1" || details.Info.Prompt.Variant != "plamo3-translation" {
					t.Fatalf("unexpected model info: %+v", details.Info)
				}
				expectedCalls := 1
				if attempt == 1 {
					expectedCalls = 0
				}
				if observation.GenerateCalls != expectedCalls {
					t.Fatalf("attempt %d: generate calls %d, want %d", attempt, observation.GenerateCalls, expectedCalls)
				}
				kind := "generated"
				if attempt == 1 {
					kind = "tm"
				}
				if len(observation.Units) != 1 || observation.Units[0].Kind != kind {
					t.Fatalf("unexpected acceptance kind: %+v", observation.Units)
				}
				t.Logf("attempt=%d backend=%s quant=%s generation_calls=%d accepted_kind=%s tm_committed=%t", attempt, details.Info.Backend, details.Info.ComputeQuant, observation.GenerateCalls, kind, result.TMCommitted)
			}
		})
	}
}
