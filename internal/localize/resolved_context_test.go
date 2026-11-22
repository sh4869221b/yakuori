package localize

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/contextpack"
	"github.com/sh4869221b/yakuori/internal/contextresolve"
	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/segment"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

const evaluationOutputTokens = 8
const evaluationGuardScene = "Soldiers return to the gate. In this scene, cat is rendered as 犬."

func resolveDialogue(t *testing.T, session unit.Session, categories contextresolve.Categories) map[unit.UnitID]contextresolve.ResolvedContext {
	t.Helper()
	const game, artifact = "test-game", "test-artifact"
	confirmed := contextpack.Provenance{
		Method: "manual", Version: "v1", Status: "confirmed", UserEdited: new(false),
		Sources: []contextpack.Source{{Kind: "manual", Reference: new("dialogue fixture")}},
	}
	digest := session.ArtifactDigest()
	pack := contextpack.Pack{
		Schema: contextpack.Schema, Scope: contextpack.Scope{GameNamespace: game, Artifacts: []contextpack.Artifact{
			{ArtifactNamespace: artifact, OriginalArtifactDigest: hex.EncodeToString(digest[:])},
		}},
		SourceLanguage: "en", TargetLanguage: "ja",
		Characters: []contextpack.Character{
			{ID: "keeper", Name: new("Innkeeper"), Provenance: confirmed, Artifacts: []string{artifact}},
			{ID: "guard", Name: new("Guard"), Provenance: confirmed, Artifacts: []string{artifact}},
		},
		Scenes: []contextpack.Scene{
			{ID: "inn", Summary: new("Guests enter the inn."), SpeakerIDs: []string{"keeper"}, Provenance: confirmed, Artifacts: []string{artifact}},
			{ID: "gate", Summary: new(evaluationGuardScene), SpeakerIDs: []string{"guard"}, Provenance: confirmed, Artifacts: []string{artifact}},
		},
	}
	raw, err := json.Marshal(pack)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := contextpack.Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	selectors := []contextresolve.Selectors{{SceneID: "inn", SpeakerID: "keeper"}, {SceneID: "gate", SpeakerID: "guard"}}
	resolved := make(map[unit.UnitID]contextresolve.ResolvedContext)
	for i, entry := range session.Units() {
		selection, _, err := contextresolve.Resolve(t.Context(), contextresolve.Request{
			Pack: doc, Session: session, UnitID: entry.ID(), GameNamespace: game, ArtifactNamespace: artifact,
			Selectors: selectors[i], Categories: categories, Budget: contextresolve.Budget{MaxItems: 2, MaxTokens: 12},
		}, func(_ context.Context, items []contextresolve.Item) (int, error) {
			return 2 * len(items), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		resolved[entry.ID()] = selection
	}
	return resolved
}

func evaluationDialogue(t *testing.T) (unit.Session, []unit.TranslationUnit, glossary.Glossary) {
	t.Helper()
	session, _ := generationFixture(t, []string{"Welcome cat {p}", "Welcome cat {p}"}, map[int][]unit.ProtectionSpan{
		0: {{Start: 12, End: 15, Kind: unit.Placeholder}}, 1: {{Start: 12, End: 15, Kind: unit.Placeholder}},
	})
	terms, err := glossary.New(glossary.Schema, []glossary.Entry{{Source: "cat", Target: "猫", Mode: glossary.Mandatory, Match: glossary.Word}})
	if err != nil {
		t.Fatal(err)
	}
	return session, session.Units(), terms
}

type evaluationHarness struct {
	core     Core
	profile  validate.Profile
	info     inference.ModelInfo
	build    TermsRequestBuilder
	requests []inference.GenerationRequest
	rewrite  func(unit.UnitID, *inference.PreparedRequest)
	count    func(inference.GenerationRequest) int
	finish   func(context.Context, inference.GenerationRequest, inference.GenerationResult) (inference.GenerationResult, error)
}

func contextEvaluationCore(t *testing.T, segmented bool, limits config.Limits, modelContext int, terms glossary.Glossary) *evaluationHarness {
	t.Helper()
	var unused int
	base, _ := canonicalCoreFixture(t, false, "", &unused)
	info, err := base.inference.engine.Info(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	info.ContextTokens = modelContext
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, evaluationOutputTokens, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	h := &evaluationHarness{info: info}
	h.build = func(_ context.Context, id unit.UnitID, input RequestInput) (inference.GenerationRequest, error) {
		p := info.Prompt
		payload := prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: input.Text, Terms: input.Terms, ResolvedContext: input.ResolvedContext}
		messages := prompt.Build(payload)
		if !input.ResolvedContext.Empty() {
			p, _ = prompt.ResolvedContextIdentity(info.Prompt)
			var err error
			messages, err = prompt.BuildResolvedContext(payload)
			if err != nil {
				return inference.GenerationRequest{}, err
			}
		}
		rendered := messages.System + "\n" + messages.User
		prepared := inference.PreparedRequest{
			RenderedPrompt: rendered, TokenIDs: make([]int, len(rendered)), StopIDs: []int{42}, ProtectedSource: input.Text,
			Terms: input.Terms, ResolvedContext: input.ResolvedContext,
			Identity: inference.RequestIdentity{ModelSHA256: info.ModelSHA256, Tokenizer: info.Tokenizer, Template: info.Template,
				PromptSchema: p.Schema, Prompt: p, Thinking: info.Thinking},
		}
		if h.rewrite != nil {
			h.rewrite(id, &prepared)
		}
		return inference.NewGenerationRequest(prepared, policy)
	}
	baseRequest, err := h.build(t.Context(), unit.UnitID{}, RequestInput{Text: "base"})
	if err != nil {
		t.Fatal(err)
	}
	h.profile, err = NewCanonicalInferenceProfile(info, baseRequest, InferenceProfileInput{
		ModelID: "model", Glossary: validate.GlossaryInput(terms), Limits: limits, Segmentation: segmented,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := segmentedBackend{info: info, commonEngine: commonEngine{
		count: func(_ context.Context, r inference.GenerationRequest) (int, error) {
			if h.count != nil {
				return h.count(r), nil
			}
			return len(r.TokenIDs()), nil
		},
		generate: func(ctx context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
			h.requests = append(h.requests, r)
			result := inference.GenerationResult{Text: strings.ReplaceAll(r.ProtectedSource(), "cat", "猫"), Finish: inference.Stop, PromptTokens: len(r.TokenIDs())}
			if h.finish != nil {
				return h.finish(ctx, r, result)
			}
			return result, nil
		},
	}}
	if segmented {
		h.core, err = NewGlossarySegmentedCoreWithLimits(engine, h.build, limits)
	} else {
		h.core, err = NewCoreWithLimits(NewGlossaryInferenceEngine(engine, h.build), limits)
	}
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestResolvedContextEvaluationUsesConfirmedUnitContext(t *testing.T) {
	session, units, terms := evaluationDialogue(t)
	limits := config.DefaultLimits()
	selection := resolveDialogue(t, session, contextresolve.Categories{Speaker: true, Scene: true})
	disabled := resolveDialogue(t, session, contextresolve.Categories{})
	for _, segmented := range []bool{false, true} {
		t.Run(stringBool(segmented), func(t *testing.T) {
			h := contextEvaluationCore(t, segmented, limits, limits.ContextTokens, terms)
			observation := &AcceptanceObservation{}
			core := h.core.WithAcceptanceObservation(observation)
			h.finish = func(_ context.Context, r inference.GenerationRequest, result inference.GenerationResult) (inference.GenerationResult, error) {
				translation := "こんにちは"
				if !r.ResolvedContext().Empty() {
					_, data, _ := strings.Cut(r.RenderedPrompt(), "\n")
					var payload struct {
						Source   string                `json:"source"`
						Context  []contextresolve.Item `json:"context"`
						Glossary json.RawMessage       `json:"glossary"`
					}
					if err := json.Unmarshal([]byte(data), &payload); err != nil {
						t.Fatal(err)
					}
					if payload.Source != "Welcome cat [[YAKUORI_0_0]]" || !bytes.Equal(payload.Glossary, terms.CanonicalBytes()) || len(payload.Context) != 2 {
						t.Fatalf("incorrect translation input: %s", data)
					}
					scene, speaker := payload.Context[0], payload.Context[1]
					switch {
					case scene.Category == contextresolve.Scene && scene.Text == "Guests enter the inn." && speaker.Category == contextresolve.Speaker && speaker.Name == "Innkeeper":
						translation = "いらっしゃい"
					case scene.Category == contextresolve.Scene && scene.Text == evaluationGuardScene && speaker.Category == contextresolve.Speaker && speaker.Name == "Guard":
						translation = "ご苦労"
					default:
						t.Fatalf("wrong speaker or scene in context: %v", payload.Context)
					}
				}
				result.Text = strings.Replace(result.Text, "Welcome", translation, 1)
				return result, nil
			}
			got, err := core.GenerateWithResolvedContext(t.Context(), session, h.profile, selection)
			want := map[unit.UnitID]string{units[0].ID(): "いらっしゃい 猫 {p}", units[1].ID(): "ご苦労 猫 {p}"}
			if err != nil || !maps.Equal(got, want) || len(h.requests) != 2 || len(observation.Units) != 0 || observation.GenerateCalls != 0 {
				t.Fatalf("translation=%v calls=%d observation=%+v error=%v", got, len(h.requests), observation, err)
			}
			empty, err := core.GenerateWithResolvedContext(t.Context(), session, h.profile, disabled)
			if err != nil {
				t.Fatal(err)
			}
			baseline, err := h.core.Generate(t.Context(), session, h.profile)
			if err != nil || len(h.requests) != 6 {
				t.Fatalf("baseline calls=%d error=%v", len(h.requests), err)
			}
			for i, a := range baseline {
				eval, standard := h.requests[i+2], h.requests[i+4]
				if empty[a.UnitID()] != a.Text() || a.Text() != "こんにちは 猫 {p}" || !eval.ResolvedContext().Empty() ||
					eval.Identity() != standard.Identity() || eval.RenderedPrompt() != standard.RenderedPrompt() || !slices.Equal(eval.TokenIDs(), standard.TokenIDs()) {
					t.Fatalf("disabled selection changed the baseline for %s", a.UnitID().StableID())
				}
			}
		})
	}
}

func TestResolvedContextEvaluationPreflightsFullTokenBudget(t *testing.T) {
	session, units, terms := evaluationDialogue(t)
	resolved := resolveDialogue(t, session, contextresolve.Categories{Speaker: true, Scene: true})
	delete(resolved, units[0].ID())
	messages, err := prompt.BuildResolvedContext(prompt.Input{
		SourceLanguage: "en", TargetLanguage: "ja", Text: "Welcome cat [[YAKUORI_0_0]]",
		Terms: terms, ResolvedContext: resolved[units[1].ID()],
	})
	if err != nil {
		t.Fatal(err)
	}
	exact := len(messages.System) + 1 + len(messages.User) + evaluationOutputTokens
	for _, smaller := range []string{"configuration", "model"} {
		t.Run(smaller, func(t *testing.T) {
			for _, cap := range []int{exact, exact - 1} {
				limits := config.DefaultLimits()
				limits.MaxOutputTokens = evaluationOutputTokens
				modelCap := limits.ContextTokens
				if smaller == "configuration" {
					limits.ContextTokens = cap
				} else {
					modelCap = cap
				}
				h := contextEvaluationCore(t, false, limits, modelCap, terms)
				result, err := h.core.GenerateWithResolvedContext(t.Context(), session, h.profile, resolved)
				if cap == exact {
					if err != nil || len(result) != 2 || len(h.requests) != 2 {
						t.Fatalf("exact fit failed: results=%v calls=%d error=%v", result, len(h.requests), err)
					}
				} else if !errors.Is(err, segment.ErrContextLimit) || result != nil || len(h.requests) != 0 {
					t.Fatalf("over-budget request ran: results=%v calls=%d error=%v", result, len(h.requests), err)
				}
			}
		})
	}
}

func TestResolvedContextEvaluationKeepsContextOnSegments(t *testing.T) {
	session, _ := generationFixture(t, []string{"cat KEEP cat"}, nil)
	terms, err := glossary.New(glossary.Schema, []glossary.Entry{{Source: "cat", Target: "猫", Mode: glossary.Mandatory, Match: glossary.Word}, {Source: "KEEP", Mode: glossary.Protect, Match: glossary.Word}})
	if err != nil {
		t.Fatal(err)
	}
	applicable, err := glossary.New(glossary.Schema, []glossary.Entry{{Source: "cat", Target: "猫", Mode: glossary.Mandatory, Match: glossary.Word}})
	if err != nil {
		t.Fatal(err)
	}
	id := session.Units()[0].ID()
	resolved := resolveDialogue(t, session, contextresolve.Categories{Speaker: true, Scene: true})
	messages, err := prompt.BuildResolvedContext(prompt.Input{
		SourceLanguage: "en", TargetLanguage: "ja", Text: "cat", Terms: applicable, ResolvedContext: resolved[id],
	})
	if err != nil {
		t.Fatal(err)
	}
	cap := len(messages.System) + 1 + len(messages.User) + evaluationOutputTokens
	h := contextEvaluationCore(t, true, config.DefaultLimits(), cap, terms)
	result, err := h.core.GenerateWithResolvedContext(t.Context(), session, h.profile, resolved)
	if err != nil || result[id] != "猫 KEEP 猫" || len(h.requests) != 2 {
		t.Fatalf("segmented result=%v calls=%d error=%v", result, len(h.requests), err)
	}
	for _, r := range h.requests {
		if r.ProtectedSource() != "cat" || !r.ResolvedContext().Equal(resolved[id]) || r.Terms() != applicable || len(r.TokenIDs())+r.Policy().MaxOutputTokens != cap {
			t.Fatal("segment lost the full resolved input or reserved output")
		}
	}
}

func TestResolvedContextEvaluationRejectsRequestMismatch(t *testing.T) {
	session, units, terms := evaluationDialogue(t)
	resolved := resolveDialogue(t, session, contextresolve.Categories{Speaker: true, Scene: true})
	limits := config.DefaultLimits()
	for _, mismatch := range []string{"selection", "source", "glossary", "identity", "stop policy", "token count"} {
		t.Run(mismatch, func(t *testing.T) {
			h := contextEvaluationCore(t, false, limits, limits.ContextTokens, terms)
			h.rewrite = func(id unit.UnitID, p *inference.PreparedRequest) {
				if id != units[1].ID() {
					return
				}
				switch mismatch {
				case "selection":
					p.ResolvedContext = resolved[units[0].ID()]
				case "source":
					p.ProtectedSource = "truncated source"
				case "glossary":
					p.Terms = glossary.Glossary{}
				case "identity":
					p.Identity.Prompt = h.info.Prompt
				case "stop policy":
					p.StopIDs = []int{71}
				}
			}
			want := ErrInferenceProfile
			if mismatch == "token count" {
				want = segment.ErrInvalidRequest
				h.count = func(r inference.GenerationRequest) int {
					n := len(r.TokenIDs())
					if r.ResolvedContext().Equal(resolved[units[1].ID()]) {
						return n - 1
					}
					return n
				}
			}
			result, err := h.core.GenerateWithResolvedContext(t.Context(), session, h.profile, resolved)
			if !errors.Is(err, want) || result != nil || len(h.requests) != 0 {
				t.Fatalf("mismatch admitted: result=%v calls=%d error=%v", result, len(h.requests), err)
			}
		})
	}
}

func TestResolvedContextEvaluationHasNoPartialResult(t *testing.T) {
	for _, tc := range []struct {
		name string
		want error
	}{
		{"generation error", errors.New("decoder failed")},
		{"deadline", context.DeadlineExceeded},
		{"output limit", ErrIncompleteGeneration},
		{"missing protection", protect.ErrInvalidCandidate},
		{"context conflicts with mandatory glossary", validate.ErrInvalidCandidate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				session, units, terms := evaluationDialogue(t)
				resolved := resolveDialogue(t, session, contextresolve.Categories{Speaker: true, Scene: true})
				limits := config.DefaultLimits()
				h := contextEvaluationCore(t, false, limits, limits.ContextTokens, terms)
				observation := &AcceptanceObservation{}
				core := h.core.WithAcceptanceObservation(observation)
				h.finish = func(ctx context.Context, r inference.GenerationRequest, result inference.GenerationResult) (inference.GenerationResult, error) {
					if !r.ResolvedContext().Equal(resolved[units[1].ID()]) {
						return result, nil
					}
					switch tc.name {
					case "generation error":
						return result, tc.want
					case "deadline":
						<-ctx.Done()
					case "output limit":
						result.Finish = inference.MaxTokens
					case "missing protection":
						result.Text = "こんにちは 猫"
					case "context conflicts with mandatory glossary":
						scene := r.ResolvedContext().Items()[0]
						if scene.Category != contextresolve.Scene || scene.Text != evaluationGuardScene || !strings.Contains(r.RenderedPrompt(), scene.Text) {
							t.Fatal("missing conflicting scene reference in the generation request")
						}
						result.Text = strings.Replace(result.Text, "猫", "犬", 1)
					}
					return result, nil
				}
				got, err := core.GenerateWithResolvedContext(t.Context(), session, h.profile, resolved)
				if !errors.Is(err, tc.want) || got != nil || len(h.requests) != 2 || len(observation.Units) != 0 {
					t.Fatalf("result=%v calls=%d observation=%+v error=%v", got, len(h.requests), observation, err)
				}
			})
		})
	}
}

func TestResolvedContextEvaluationRejectsGlossaryConflict(t *testing.T) {
	session, _, _ := evaluationDialogue(t)
	resolved := resolveDialogue(t, session, contextresolve.Categories{Speaker: true, Scene: true})
	terms, err := glossary.New(glossary.Schema, []glossary.Entry{{Source: "{p}", Target: "別", Mode: glossary.Mandatory, Match: glossary.Literal}})
	if err != nil {
		t.Fatal(err)
	}
	limits := config.DefaultLimits()
	h := contextEvaluationCore(t, false, limits, limits.ContextTokens, terms)
	got, err := h.core.GenerateWithResolvedContext(t.Context(), session, h.profile, resolved)
	if !errors.Is(err, glossary.ErrConflict) || got != nil || len(h.requests) != 0 {
		t.Fatalf("glossary conflict admitted: result=%v calls=%d error=%v", got, len(h.requests), err)
	}
}

func TestResolvedContextStaysOutOfProductionPaths(t *testing.T) {
	session, units, _ := evaluationDialogue(t)
	resolved := resolveDialogue(t, session, contextresolve.Categories{Speaker: true})
	limits := config.DefaultLimits()
	h := contextEvaluationCore(t, false, limits, limits.ContextTokens, glossary.Glossary{})
	legacy, err := validate.NewProfile([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := h.core.GenerateWithResolvedContext(t.Context(), session, legacy, resolved); !errors.Is(err, ErrInferenceProfile) || result != nil {
		t.Fatalf("noncanonical evaluation admitted: result=%v error=%v", result, err)
	}
	for _, leak := range []string{"selection", "translation identity", "extraction identity"} {
		t.Run(leak, func(t *testing.T) {
			h.rewrite = func(_ unit.UnitID, p *inference.PreparedRequest) {
				switch leak {
				case "selection":
					p.ResolvedContext = resolved[units[0].ID()]
				case "translation identity":
					p.Identity.Prompt, _ = prompt.ResolvedContextIdentity(h.info.Prompt)
				case "extraction identity":
					p.Identity.Prompt = prompt.ContextIdentity()
				}
			}
			request, err := h.build(t.Context(), units[0].ID(), RequestInput{Text: "Welcome"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := EffectiveInferenceProfile(h.info, request, limits, false); !errors.Is(err, ErrInferenceProfile) {
				t.Fatalf("context request admitted to profile: %v", err)
			}
			if result, err := h.core.Generate(t.Context(), session, legacy); !errors.Is(err, ErrInferenceProfile) || result != nil {
				t.Fatalf("context request admitted to legacy generation: result=%v error=%v", result, err)
			}
			memory := &fakeTM{}
			var output bytes.Buffer
			if _, err := h.core.Text(t.Context(), []byte("Welcome"), textadapter.New("en", "ja"), h.profile, memory, &output); !errors.Is(err, ErrInferenceProfile) ||
				len(h.requests) != 0 || memory.calls != 0 || output.Len() != 0 {
				t.Fatalf("context request admitted to Text: calls=%d TM=%d output=%q error=%v", len(h.requests), memory.calls, output.String(), err)
			}
		})
	}
}
