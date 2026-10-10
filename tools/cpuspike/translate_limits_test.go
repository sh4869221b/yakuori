package main

import (
	"context"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
)

func TestTranslationResearchLimitsPreserveExplicitConditions(t *testing.T) {
	policy, err := inference.NewGenerationPolicy(1, 4096, 300*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Second)
	defer cancel()
	limits := translationLimits(ctx, inference.ModelInfo{ContextTokens: 16384}, policy)
	if err := limits.Check(); err != nil {
		t.Fatal(err)
	}
	if limits.ArtifactBytes != 2<<20 || limits.Units != 4096 || limits.UnitTextBytes != 2<<20 || limits.TotalTextBytes != 2<<20 || limits.Segments != 2<<20 || limits.ContextTokens != 16384 || limits.MaxOutputTokens != 4096 || limits.RequestTimeout != 300*time.Second || limits.GenerationTimeout <= config.DefaultLimits().GenerationTimeout {
		t.Fatalf("limits=%+v", limits)
	}
}
