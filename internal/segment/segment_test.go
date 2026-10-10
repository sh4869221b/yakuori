package segment

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func fixture(t *testing.T, source string, spans ...unit.ProtectionSpan) (unit.Session, unit.UnitID, protect.Prepared) {
	t.Helper()
	id, err := unit.NewUnitID("fixture", "v1", "parent")
	if err != nil {
		t.Fatal(err)
	}
	u, err := unit.NewTranslationUnitWithProtection(id, []byte(source), "en", "ja", nil, spans)
	if err != nil {
		t.Fatal(err)
	}
	session, err := unit.NewSession(nil, []unit.TranslationUnit{u})
	if err != nil {
		t.Fatal(err)
	}
	p, err := protect.Prepare(session, id)
	if err != nil {
		t.Fatal(err)
	}
	return session, id, p
}
func model(limit int) inference.ModelInfo {
	return inference.ModelInfo{ContextTokens: limit, PolicySchema: 1, BackendPin: "fixture", Template: inference.TemplateIdentity{Family: "test"}, Tokenizer: inference.TokenizerIdentity{BackendPin: "fixture"}}
}
func request(t *testing.T, info inference.ModelInfo, text string, n, reserve int) inference.GenerationRequest {
	t.Helper()
	policy, err := inference.NewGenerationPolicy(1, reserve, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int, n)
	for i := range ids {
		ids[i] = i + 1
	}
	r, err := inference.NewGenerationRequest(inference.PreparedRequest{RenderedPrompt: "<system>translate</system>" + text, Spans: []inference.PromptSpan{{Text: "<system>", Special: true}, {Text: text}}, TokenIDs: ids, Identity: inference.RequestIdentity{ModelSHA256: info.ModelSHA256, Template: info.Template, Tokenizer: info.Tokenizer, PromptSchema: 1}, StopIDs: []int{7}}, policy)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func counter(_ context.Context, r inference.GenerationRequest) (int, error) {
	return len(r.TokenIDs()), nil
}
func builder(t *testing.T, info inference.ModelInfo, size func(string) int) func(context.Context, string) (inference.GenerationRequest, error) {
	return func(_ context.Context, text string) (inference.GenerationRequest, error) {
		return request(t, info, text, size(text), 2), nil
	}
}
func originalResults(plan Plan) []Result {
	var results []Result
	for _, s := range plan.Segments() {
		results = append(results, Result{s, s.Text()})
	}
	return results
}
func requireFailure(t *testing.T, p Plan, err, target error) {
	t.Helper()
	if !errors.Is(err, target) || p.data != nil || len(p.Segments()) != 0 {
		t.Fatalf("plan=%+v error=%v want=%v", p, err, target)
	}
}

func TestBudgetBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		context int
		want    error
	}{{"limit minus one", 9, ErrContextLimit}, {"exact", 10, nil}, {"limit plus one", 11, nil}} {
		t.Run(tc.name, func(t *testing.T) {
			_, id, p := fixture(t, "literal <|control|>")
			info := model(tc.context)
			plan, err := Build(context.Background(), id, p, info, builder(t, info, func(string) int { return 8 }), counter)
			if tc.want != nil {
				requireFailure(t, plan, err, tc.want)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			s := plan.Segments()
			if len(s) != 1 || s[0].Text() != p.Text() || s[0].PromptTokens() != 8 || s[0].Request().RenderedPrompt() != "<system>translate</system>"+p.Text() {
				t.Fatalf("segments=%+v", s)
			}
			ids := s[0].Request().TokenIDs()
			ids[0] = 999
			policy := s[0].Request().Policy()
			policy.StopIDs[0] = 999
			if s[0].Request().TokenIDs()[0] != 1 || s[0].Request().Policy().StopIDs[0] != 7 {
				t.Fatal("mutable request")
			}
		})
	}
	if got := ProfileInput(); got.Schema != Schema || got.BoundaryFixtures != BoundaryFixtures {
		t.Fatalf("profile=%+v", got)
	}
}

func TestSafeBoundaries(t *testing.T) {
	cases := []struct {
		name, source string
		budget       int
		want         []string
		leading      string
		separators   []string
	}{
		{"CRLF and runs", " \r\none.\r\n  two!\t", 4, []string{"one.", "two!"}, " \r\n", []string{"\r\n  ", "\t"}},
		{"Japanese", "一。二！三？", 6, []string{"一。", "二！", "三？"}, "", []string{"", "", ""}},
		{"whitespace", "alpha  beta\tgamma", 5, []string{"alpha", "beta", "gamma"}, "", []string{"  ", "\t", ""}},
		{"literal reserved marker", "hello [[YAKUORI_0_9]] world", 15, []string{"hello", "[[YAKUORI_0_9]]", "world"}, "", []string{" ", " ", ""}},
		{"whole fits", "  one. two!\r\n", 100, []string{"  one. two!\r\n"}, "", []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session, id, p := fixture(t, tc.source)
			info := model(tc.budget + 2)
			plan, err := Build(context.Background(), id, p, info, builder(t, info, func(text string) int { return len(text) }), counter)
			if err != nil {
				t.Fatal(err)
			}
			segments := plan.Segments()
			var bodies, separators []string
			var source strings.Builder
			source.WriteString(plan.data.leading.Source())
			for i, s := range segments {
				bodies = append(bodies, s.Text())
				separators = append(separators, s.Separator())
				source.WriteString(s.piece.Source())
				source.WriteString(s.separator.Source())
				if s.Ordinal() != i || s.ParentID() != id || s.PromptTokens()+s.Request().Policy().MaxOutputTokens > info.ContextTokens {
					t.Fatalf("invalid segment %+v", s)
				}
			}
			if !reflect.DeepEqual(bodies, tc.want) || !reflect.DeepEqual(separators, tc.separators) || plan.LeadingSeparator() != tc.leading || source.String() != tc.source {
				t.Fatalf("bodies=%q separators=%q leading=%q original=%q", bodies, separators, plan.LeadingSeparator(), source.String())
			}
			joined, err := Join(plan, originalResults(plan))
			if err != nil || joined != p.Text() {
				t.Fatalf("joined=%q err=%v", joined, err)
			}
			restored, err := p.Restore(session, id, joined)
			if err != nil || restored != tc.source {
				t.Fatalf("restored=%q err=%v", restored, err)
			}
			segments[0] = Segment{}
			if plan.Segments()[0].owner == nil {
				t.Fatal("mutable segments")
			}
		})
	}
}

func TestSegmentJoin(t *testing.T) {
	source := "  hi {p}.\r\n{q} bye!\t"
	session, id, p := fixture(t, source, unit.ProtectionSpan{Start: 5, End: 8, Kind: unit.Placeholder}, unit.ProtectionSpan{Start: 11, End: 14, Kind: unit.Placeholder})
	info := model(7)
	var built []string
	build := func(_ context.Context, text string) (inference.GenerationRequest, error) {
		built = append(built, text)
		n := 5
		if strings.Contains(text, "\r\n") {
			n = 30
		}
		return request(t, info, text, n, 2), nil
	}
	plan, err := Build(context.Background(), id, p, info, build, counter)
	if err != nil {
		t.Fatal(err)
	}
	results := originalResults(plan)
	if len(results) != 2 {
		t.Fatalf("segments=%d built=%q", len(results), built)
	}
	results[0].Text = strings.ReplaceAll(results[0].Text, "hi", "訳一")
	results[1].Text = strings.ReplaceAll(results[1].Text, "bye", "訳二")
	joined, err := Join(plan, results)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := p.Restore(session, id, joined)
	if err != nil || restored != "  訳一 {p}.\r\n{q} 訳二!\t" {
		t.Fatalf("restored=%q error=%v", restored, err)
	}
	for _, tc := range []struct {
		name    string
		results []Result
	}{{"missing", results[:1]}, {"duplicate", []Result{results[0], results[0]}}, {"out of order", []Result{results[1], results[0]}}, {"empty", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			if output, err := Join(plan, tc.results); !errors.Is(err, ErrInvalidResult) || output != "" {
				t.Fatalf("output=%q err=%v", output, err)
			}
		})
	}
	other, err := Build(context.Background(), id, p, info, build, counter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Join(other, results); !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("stale plan err=%v", err)
	}
	for _, bad := range []string{"", "\xff", "\x00", results[0].Text + results[1].Text} {
		corrupt := append([]Result(nil), results...)
		corrupt[0].Text = bad
		if output, err := Join(plan, corrupt); err == nil || output != "" {
			t.Fatalf("dirty=%q output=%q err=%v", bad, output, err)
		}
	}
}

func TestSafeBoundariesProtectedOnly(t *testing.T) {
	_, id, p := fixture(t, "alpha {p} omega", unit.ProtectionSpan{Start: 6, End: 9, Kind: unit.Placeholder})
	info := model(7)
	var built []string
	build := func(_ context.Context, text string) (inference.GenerationRequest, error) {
		built = append(built, text)
		n := len(text)
		return request(t, info, text, n, 2), nil
	}
	plan, err := Build(context.Background(), id, p, info, build, counter)
	if err != nil {
		t.Fatal(err)
	}
	segments := plan.Segments()
	if len(segments) != 3 || segments[1].GenerationRequired() || segments[1].PromptTokens() != 0 || len(segments[1].Request().TokenIDs()) != 0 {
		t.Fatalf("segments=%+v", segments)
	}
	for _, text := range built {
		if text == segments[1].Text() {
			t.Fatal("built protected-only request")
		}
	}
	if joined, err := Join(plan, originalResults(plan)); err != nil || joined != p.Text() {
		t.Fatalf("joined=%q err=%v", joined, err)
	}
}

func TestNonMonotonicCounts(t *testing.T) {
	_, id, p := fixture(t, "a b c d")
	info := model(7)
	counts := map[string]int{"a b c d": 10, "a b c": 5, "a b": 9, "a": 2, "d": 2}
	var attempts []string
	build := func(_ context.Context, text string) (inference.GenerationRequest, error) {
		attempts = append(attempts, text)
		n, ok := counts[text]
		if !ok {
			t.Fatalf("unexpected %q", text)
		}
		return request(t, info, text, n, 2), nil
	}
	plan, err := Build(context.Background(), id, p, info, build, counter)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{plan.Segments()[0].Text(), plan.Segments()[1].Text()}; !reflect.DeepEqual(got, []string{"a b c", "d"}) || !reflect.DeepEqual(attempts, []string{"a b c d", "a b c", "d"}) {
		t.Fatalf("got=%q attempts=%q", got, attempts)
	}
	// Greedy selection is final, even when a shorter first choice could work.
	counts = map[string]int{"a b c d": 10, "a b c": 5, "d": 10}
	attempts = nil
	plan, err = Build(context.Background(), id, p, info, build, counter)
	requireFailure(t, plan, err, ErrContextLimit)
	if !reflect.DeepEqual(attempts, []string{"a b c d", "a b c", "d"}) {
		t.Fatalf("backtracked: %q", attempts)
	}
}

func TestNoSafeBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		spans        []unit.ProtectionSpan
	}{{"word", "unbreakable", nil}, {"tag", "<b>long text. more text</b>", []unit.ProtectionSpan{{Start: 0, End: 3, Kind: unit.OpenTag, Pair: "b", Ordered: true}, {Start: 23, End: 27, Kind: unit.CloseTag, Pair: "b", Ordered: true}}}, {"whitespace", " \r\n\t", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			_, id, p := fixture(t, tc.source, tc.spans...)
			info := model(3)
			plan, err := Build(context.Background(), id, p, info, builder(t, info, func(string) int { return 9 }), counter)
			requireFailure(t, plan, err, ErrContextLimit)
		})
	}
}

func TestSegmentRejects(t *testing.T) {
	_, id, p := fixture(t, "one two")
	info := model(7)
	base := builder(t, info, func(text string) int { return len(text) })
	boom := errors.New("fixture error")
	tests := []struct {
		name  string
		info  inference.ModelInfo
		build func(context.Context, string) (inference.GenerationRequest, error)
		count func(context.Context, inference.GenerationRequest) (int, error)
		want  error
	}{
		{"unknown context", model(0), base, counter, ErrContextLimit},
		{"template overhead", info, builder(t, info, func(string) int { return 100 }), counter, ErrContextLimit},
		{"output reserve", info, func(_ context.Context, text string) (inference.GenerationRequest, error) {
			return request(t, info, text, 1, 100), nil
		}, counter, ErrContextLimit},
		{"zero count", info, base, func(context.Context, inference.GenerationRequest) (int, error) { return 0, nil }, ErrInvalidRequest},
		{"negative count", info, base, func(context.Context, inference.GenerationRequest) (int, error) { return -1, nil }, ErrInvalidRequest},
		{"count mismatch", info, base, func(context.Context, inference.GenerationRequest) (int, error) { return 1, nil }, ErrInvalidRequest},
		{"builder error", info, func(context.Context, string) (inference.GenerationRequest, error) {
			return inference.GenerationRequest{}, boom
		}, counter, boom},
		{"counter error", info, base, func(context.Context, inference.GenerationRequest) (int, error) { return 0, boom }, boom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := Build(context.Background(), id, p, tc.info, tc.build, tc.count)
			requireFailure(t, plan, err, tc.want)
		})
	}
	for _, kind := range []string{"model", "tokenizer", "template", "prompt schema", "policy", "stop IDs"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			build := func(_ context.Context, text string) (inference.GenerationRequest, error) {
				calls++
				r := request(t, info, text, len(text), 2)
				if calls > 1 {
					prepared := inference.PreparedRequest{RenderedPrompt: r.RenderedPrompt(), TokenIDs: r.TokenIDs(), Identity: r.Identity(), StopIDs: r.Policy().StopIDs}
					reserve := 2
					switch kind {
					case "model":
						prepared.Identity.ModelSHA256[0] = 1
					case "tokenizer":
						prepared.Identity.Tokenizer.BackendPin = "other"
					case "template":
						prepared.Identity.Template.Family = "other"
					case "prompt schema":
						prepared.Identity.PromptSchema++
					case "policy":
						reserve = 3
					case "stop IDs":
						prepared.StopIDs = []int{8}
					}
					policy, err := inference.NewGenerationPolicy(1, reserve, time.Second)
					if err != nil {
						t.Fatal(err)
					}
					return inference.NewGenerationRequest(prepared, policy)
				}
				return r, nil
			}
			plan, err := Build(context.Background(), id, p, info, build, counter)
			requireFailure(t, plan, err, ErrInvalidRequest)
		})
	}
	plan, err := Build(context.Background(), id, p, info, base, counter)
	if err != nil {
		t.Fatal(err)
	}
	plan.data.segments[0].ordinal = 4
	if output, err := Join(plan, originalResults(plan)); !errors.Is(err, ErrInvalidPlan) || output != "" {
		t.Fatalf("partition output=%q err=%v", output, err)
	}
}

func TestSegmentPlanningCancel(t *testing.T) {
	_, id, p := fixture(t, "one two")
	info := model(7)
	for _, phase := range []string{"before", "builder", "counter"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			if phase == "before" {
				cancel()
			}
			build := func(_ context.Context, text string) (inference.GenerationRequest, error) {
				calls++
				if phase == "builder" && calls == 2 {
					cancel()
				}
				return request(t, info, text, len(text), 2), nil
			}
			countCalls := 0
			count := func(_ context.Context, r inference.GenerationRequest) (int, error) {
				countCalls++
				if phase == "counter" && countCalls == 2 {
					cancel()
				}
				return len(r.TokenIDs()), nil
			}
			plan, err := Build(ctx, id, p, info, build, count)
			requireFailure(t, plan, err, context.Canceled)
			if (phase == "before" && calls != 0) || (phase != "before" && calls != 2) {
				t.Fatalf("continued planning calls=%d", calls)
			}
		})
	}
}

func TestSegmentRejectsPartition(t *testing.T) {
	_, id, p := fixture(t, "one  two three")
	info := model(7)
	build := builder(t, info, func(text string) int { return len(text) })
	for _, kind := range []string{"gap", "overlap", "separator mismatch", "foreign piece"} {
		t.Run(kind, func(t *testing.T) {
			plan, err := Build(context.Background(), id, p, info, build, counter)
			if err != nil {
				t.Fatal(err)
			}
			results := originalResults(plan)
			segments := plan.data.segments
			switch kind {
			case "gap":
				segments[0].separator, err = p.Slice(3, 3)
			case "overlap":
				segments[1].piece = segments[0].piece
			case "separator mismatch":
				segments[0].separator = segments[1].separator
			case "foreign piece":
				_, _, foreign := fixture(t, "one  two three")
				segments[0].piece, err = foreign.Slice(0, 3)
			}
			if err != nil {
				t.Fatal(err)
			}
			if output, err := Join(plan, results); !errors.Is(err, ErrInvalidPlan) || output != "" {
				t.Fatalf("output=%q error=%v", output, err)
			}
		})
	}
}
