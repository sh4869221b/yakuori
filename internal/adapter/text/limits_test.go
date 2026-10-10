package text

import (
	"errors"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func TestImportByteLimitsBeforeRetention(t *testing.T) {
	for _, bound := range []string{"artifact", "unit", "total"} {
		for _, n := range []int{3, 4, 5} {
			t.Run(bound+strings.Repeat("x", n), func(t *testing.T) {
				limits := config.DefaultLimits()
				switch bound {
				case "artifact":
					limits.ArtifactBytes = 4
				case "unit":
					limits.UnitTextBytes = 4
				case "total":
					limits.TotalTextBytes = 4
				}
				a, err := NewWithLimits("en", "ja", limits)
				if err != nil {
					t.Fatal(err)
				}
				session, err := a.Import([]byte("界"+strings.Repeat("a", n-3)), validate.Profile{})
				if n <= 4 {
					if err != nil || session.Check() != nil {
						t.Fatalf("N=%d: %v", n, err)
					}
				} else if !errors.Is(err, config.ErrLimitExceeded) || a.session.Check() == nil || session.Check() == nil {
					t.Fatalf("oversized input retained: session=%v err=%v", session, err)
				}
			})
		}
	}
}
