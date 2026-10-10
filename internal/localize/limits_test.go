package localize

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func TestCoreInputBoundaries(t *testing.T) {
	for _, bound := range []string{"artifact", "units", "unit bytes", "total bytes", "segments"} {
		for _, n := range []int{3, 4, 5} {
			t.Run(fmt.Sprintf("%s/%d", bound, n), func(t *testing.T) {
				limits := config.DefaultLimits()
				sources := []string{"a"}
				artifact := []byte("input artifact")
				switch bound {
				case "artifact":
					limits.ArtifactBytes = 4
					artifact = []byte(strings.Repeat("a", n))
				case "units":
					limits.Units = 4
					sources = make([]string, n)
					for i := range sources {
						sources[i] = "a"
					}
				case "unit bytes":
					limits.UnitTextBytes = 4
					sources = []string{"界" + strings.Repeat("a", n-3)}
				case "total bytes":
					limits.TotalTextBytes = 4
					sources = []string{"a", strings.Repeat("b", n-1)}
				case "segments":
					limits.Segments = 4
					sources = make([]string, n)
					for i := range sources {
						sources[i] = ""
					}
				}
				session, profile := generationFixture(t, sources, nil)
				session, err := unit.NewSession(artifact, session.Units())
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				core, err := NewCoreWithLimits(fakeEngine(func(_ context.Context, _ unit.UnitID, text string) (Generation, error) {
					calls++
					return Generation{Text: text, Finish: Stop}, nil
				}), limits)
				if err != nil {
					t.Fatal(err)
				}
				accepted, err := core.Generate(context.Background(), session, profile)
				if n <= 4 {
					if err != nil || len(accepted) != len(sources) {
						t.Fatalf("accepted=%v err=%v", accepted, err)
					}
				} else if !errors.Is(err, config.ErrLimitExceeded) || accepted != nil || calls != 0 {
					t.Fatalf("accepted=%v calls=%d err=%v", accepted, calls, err)
				}
			})
		}
	}
}

type importProbe struct {
	Adapter
	calls int
}

func (a *importProbe) Import(raw []byte, p validate.Profile) (unit.Session, error) {
	a.calls++
	return a.Adapter.Import(raw, p)
}

func TestTextArtifactLimitBeforeImport(t *testing.T) {
	core := NewCore(nil)
	a := &importProbe{Adapter: textadapter.New("en", "ja")}
	sink, tm := &textSink{limit: -1}, &fakeTM{}
	result, err := core.Text(context.Background(), make([]byte, core.limits.ArtifactBytes+1), a, textProfile(t), tm, sink)
	if !errors.Is(err, config.ErrLimitExceeded) || a.calls != 0 || tm.calls != 0 || sink.Len() != 0 || result.TMCommitted {
		t.Fatalf("result=%+v err=%v import=%d commit=%d output=%d", result, err, a.calls, tm.calls, sink.Len())
	}
}

func TestLaterUnitSegmentLimitBeforeAnyGeneration(t *testing.T) {
	for _, n := range []int{2, 3, 4} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			sources := []string{"one", strings.TrimSpace(strings.Repeat("two ", n-1))}
			session, _ := generationFixture(t, sources, nil)
			calls := 0
			core := segmentedFixture(t, byteLength, func(_ context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
				calls++
				return segmentedStop(r), nil
			})
			core.limits.Segments = 3
			accepted, err := core.Generate(context.Background(), session, segmentedProfile(t))
			if n <= 3 {
				if err != nil || len(accepted) != 2 || calls != n {
					t.Fatalf("calls=%d accepted=%v err=%v", calls, accepted, err)
				}
			} else if !errors.Is(err, config.ErrLimitExceeded) || accepted != nil || calls != 0 {
				t.Fatalf("calls=%d accepted=%v err=%v", calls, accepted, err)
			}
		})
	}
}

type sessionAdapter struct {
	Adapter
	session unit.Session
}

func (a sessionAdapter) Import([]byte, validate.Profile) (unit.Session, error) { return a.session, nil }
