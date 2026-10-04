package localize

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

type fakeEngine func(context.Context, unit.UnitID, string) (Generation, error)

func (f fakeEngine) Generate(ctx context.Context, id unit.UnitID, text string) (Generation, error) {
	return f(ctx, id, text)
}

func generationFixture(t *testing.T, sources []string, spans map[int][]unit.ProtectionSpan) (unit.Session, validate.Profile) {
	t.Helper()
	units := make([]unit.TranslationUnit, len(sources))
	for i, source := range sources {
		id, err := unit.NewUnitID("fixture", "v1", string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		units[i], err = unit.NewTranslationUnitWithProtection(id, []byte(source), "en", "ja", nil, spans[i])
		if err != nil {
			t.Fatal(err)
		}
	}
	session, err := unit.NewSession([]byte("input artifact"), units)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := validate.NewProfile([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	return session, profile
}

func TestGenerateAll(t *testing.T) {
	sources := []string{"Hello", "Hello {p}", "", " {p}\r\n", "Alice"}
	session, profile := generationFixture(t, sources, map[int][]unit.ProtectionSpan{
		1: {{Start: 6, End: 9, Kind: unit.Placeholder}},
		3: {{Start: 1, End: 4, Kind: unit.Placeholder}},
	})
	var calls []string
	engine := fakeEngine(func(_ context.Context, id unit.UnitID, text string) (Generation, error) {
		calls = append(calls, id.StableID())
		if id.StableID() == "b" && text != "Hello [[YAKUORI_0_0]]" {
			t.Fatalf("engine input = %q", text)
		}
		return Generation{Text: strings.ReplaceAll(text, "Hello", "こんにちは"), Finish: Stop}, nil
	})
	accepted, err := NewCore(engine).Generate(context.Background(), session, profile)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(calls, []string{"a", "b", "e"}) || len(accepted) != len(sources) {
		t.Fatalf("calls = %v, accepted count = %d", calls, len(accepted))
	}
	want := []string{"こんにちは", "こんにちは {p}", "", " {p}\r\n", "Alice"}
	otherProfile, err := validate.NewProfile([32]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	otherSession, _ := generationFixture(t, sources, map[int][]unit.ProtectionSpan{
		1: {{Start: 6, End: 9, Kind: unit.Placeholder}},
		3: {{Start: 1, End: 4, Kind: unit.Placeholder}},
	})
	for i, translation := range accepted {
		id := session.Units()[i].ID()
		if translation.UnitID() != id || translation.Text() != want[i] {
			t.Fatalf("unit %s = %q, want %q", id.StableID(), translation.Text(), want[i])
		}
		if err := validate.CheckBinding(session, id, profile, translation); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(validate.CheckBinding(session, id, otherProfile, translation), validate.ErrBindingMismatch) ||
			!errors.Is(validate.CheckBinding(otherSession, id, profile, translation), validate.ErrBindingMismatch) {
			t.Fatal("accepted translation lost session/profile binding")
		}
	}
	if len(accepted[4].ReviewFindings()) == 0 {
		t.Fatal("unchanged proper name must keep nonblocking review findings")
	}
	expected, err := artifact.PrepareExport(session, profile, accepted)
	if err != nil || expected.Len() != len(want) {
		t.Fatalf("export count = %d, error = %v", expected.Len(), err)
	}
}

func TestGenerateFailure(t *testing.T) {
	backendError := errors.New("private source and prompt")
	cases := []struct {
		name, source, candidate, phase string
		spans                          []unit.ProtectionSpan
		finish                         Finish
		engineError, want              error
		cancel                         bool
		calls                          int
	}{
		{name: "partial plus error", source: "Secret", candidate: "途中", finish: Stop, engineError: backendError, want: backendError, phase: "generate", calls: 2},
		{name: "broken protection", source: "Hello {p}", candidate: "こんにちは", finish: Stop, spans: []unit.ProtectionSpan{{Start: 6, End: 9, Kind: unit.Placeholder}}, want: protect.ErrInvalidCandidate, phase: "restore", calls: 2},
		{name: "validation failure", source: "Secret", candidate: "Translation: 訳", finish: Stop, want: validate.ErrInvalidCandidate, phase: "validate", calls: 2},
		{name: "invalid source", source: "\xff", want: protect.ErrInvalidSource, phase: "protect", calls: 1},
		{name: "cancel during generation", source: "Secret", candidate: "訳", finish: Stop, cancel: true, want: context.Canceled, phase: "generate", calls: 2},
	}
	for _, finish := range []Finish{"", "unknown", MaxTokens, ContextLimit, Timeout, Canceled, DecodeError, InvalidOutput} {
		cases = append(cases, struct {
			name, source, candidate, phase string
			spans                          []unit.ProtectionSpan
			finish                         Finish
			engineError, want              error
			cancel                         bool
			calls                          int
		}{name: "finish " + string(finish), source: "Secret", candidate: "途中", finish: finish, want: ErrIncompleteGeneration, phase: "generate", calls: 2})
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			session, profile := generationFixture(t, []string{"First", tt.source, "Last"}, map[int][]unit.ProtectionSpan{1: tt.spans})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			engine := fakeEngine(func(_ context.Context, id unit.UnitID, _ string) (Generation, error) {
				calls++
				if id.StableID() == "a" {
					return Generation{Text: "最初", Finish: Stop}, nil
				}
				if tt.cancel {
					cancel()
				}
				return Generation{Text: tt.candidate, Finish: tt.finish}, tt.engineError
			})
			accepted, err := NewCore(engine).Generate(ctx, session, profile)
			var failure *Error
			if !errors.Is(err, tt.want) || !errors.As(err, &failure) || accepted != nil || calls != tt.calls {
				t.Fatalf("accepted = %v, calls = %d, error = %v", accepted, calls, err)
			}
			if failure.Phase != tt.phase || failure.UnitID != session.Units()[1].ID() {
				t.Fatalf("phase = %s, unit = %s", failure.Phase, failure.UnitID.StableID())
			}
			if strings.Contains(err.Error(), "Secret") || strings.Contains(err.Error(), "private source") || strings.Contains(err.Error(), tt.candidate) && tt.candidate != "" {
				t.Fatalf("diagnostic exposed source/candidate/backend text: %s", err)
			}
		})
	}
}
