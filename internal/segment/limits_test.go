package segment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
)

func TestUnitByteLimitBeforeTokenization(t *testing.T) {
	_, id, p := fixture(t, "界aa")
	info := model(100)
	limits := config.DefaultLimits()
	limits.UnitTextBytes = 4
	builds, counts := 0, 0
	plan, err := BuildWithLimits(context.Background(), id, p, info, func(context.Context, string) (inference.GenerationRequest, error) {
		builds++
		return inference.GenerationRequest{}, nil
	}, func(context.Context, inference.GenerationRequest) (int, error) { counts++; return 0, nil }, limits)
	requireFailure(t, plan, err, config.ErrLimitExceeded)
	if builds != 0 || counts != 0 {
		t.Fatalf("builds=%d counts=%d", builds, counts)
	}
}

func TestPracticalAndPhysicalContextCaps(t *testing.T) {
	for _, tc := range []struct {
		physical, practical int
		want                error
	}{{100, 10, nil}, {100, 9, ErrContextLimit}, {9, 100, ErrContextLimit}} {
		_, id, p := fixture(t, "unbreakable")
		info := model(tc.physical)
		limits := config.DefaultLimits()
		limits.ContextTokens = tc.practical
		limits.MaxOutputTokens = 2
		plan, err := BuildWithLimits(context.Background(), id, p, info, builder(t, info, func(string) int { return 8 }), counter, limits)
		if tc.want != nil {
			requireFailure(t, plan, err, tc.want)
		} else if err != nil || len(plan.Segments()) != 1 {
			t.Fatalf("plan=%v err=%v", plan, err)
		}
	}
}

func TestBuilderCannotExceedProductPolicy(t *testing.T) {
	for _, bound := range []string{"output", "request"} {
		t.Run(bound, func(t *testing.T) {
			_, id, p := fixture(t, "hello")
			info := model(8192)
			limits := config.DefaultLimits()
			output, timeout := limits.MaxOutputTokens, limits.RequestTimeout
			if bound == "output" {
				output++
			} else {
				timeout += time.Nanosecond
			}
			build := func(context.Context, string) (inference.GenerationRequest, error) {
				policy, err := inference.NewGenerationPolicy(1, output, timeout)
				if err != nil {
					t.Fatal(err)
				}
				return inference.NewGenerationRequest(inference.PreparedRequest{TokenIDs: []int{1}, Identity: inference.RequestIdentity{ModelSHA256: info.ModelSHA256, Template: info.Template, Tokenizer: info.Tokenizer, PromptSchema: 1}}, policy)
			}
			plan, err := BuildWithLimits(context.Background(), id, p, info, build, counter, limits)
			if !errors.Is(err, ErrInvalidRequest) || len(plan.Segments()) != 0 {
				t.Fatalf("plan=%v err=%v", plan, err)
			}
		})
	}
}
