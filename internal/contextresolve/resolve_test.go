package contextresolve

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/contextpack"
	"github.com/sh4869221b/yakuori/internal/unit"
)

const (
	game = "example-game"
	inn  = "mod-inn"
	gate = "mod-gate"
)

func fixture(t *testing.T) (Request, contextpack.Pack) {
	t.Helper()
	confirmed := contextpack.Provenance{
		Method: "manual", Version: "v1", Status: "confirmed", UserEdited: new(false),
		Sources: []contextpack.Source{{Kind: "manual", Reference: new("fixture notes")}},
	}
	candidate := contextpack.Provenance{Method: "llm", Version: "v1", Status: "candidate", UserEdited: new(true)}
	sameSource := "Welcome."
	current := fixtureUnit(t, "welcome", sameSource, "ja")
	other := fixtureUnit(t, "arrival", sameSource, "ja")
	unknown := fixtureUnit(t, "missing", sameSource, "ja")
	intro := fixtureUnit(t, "introduction", "Leave your boots by the door.", "ja")
	alternate := fixtureUnit(t, "alternate", "Draw the curtains.", "ja")
	later := fixtureUnit(t, "later", "Have a seat.", "ja")
	draft := fixtureUnit(t, "draft", "Draft source.", "ja")
	session, err := unit.NewSession([]byte("original inn artifact"), []unit.TranslationUnit{alternate, current, intro, unknown, other, later, draft})
	if err != nil {
		t.Fatal(err)
	}
	pack := contextpack.Pack{
		Schema: contextpack.Schema,
		Scope: contextpack.Scope{GameNamespace: game, Artifacts: []contextpack.Artifact{
			{ArtifactNamespace: inn, OriginalArtifactDigest: hexDigest(session.ArtifactDigest())},
			{ArtifactNamespace: gate, OriginalArtifactDigest: strings.Repeat("2", 64)},
		}},
		SourceLanguage: "en",
		TargetLanguage: "ja",
		World: []contextpack.World{
			{ID: "z-game", Text: "The coast is cold.", Provenance: confirmed},
			{ID: "a-inn", Text: "The inn opens at dusk.", Artifacts: []string{inn}, Provenance: confirmed},
			{ID: "z-inn-duplicate", Text: "The coast is cold.", Artifacts: []string{inn}, Provenance: confirmed},
			{ID: "a-quest", Text: "The lantern belongs to the quest.", Tags: []string{"quest:lantern", "night"}, Provenance: confirmed},
			{ID: "a-unrelated", Text: "At the outer gates.", Artifacts: []string{gate}, Provenance: confirmed},
			{ID: "b-candidate", Text: "Candidate world.", Provenance: candidate},
		},
		Characters: []contextpack.Character{
			{ID: "character/inn/keeper", Name: new("Keeper"), Description: new("Welcomes weary travelers."), Artifacts: []string{inn}, Provenance: confirmed},
			{ID: "character/inn/guard", Name: new("Guard"), Artifacts: []string{inn}, Provenance: confirmed},
			{ID: "character/other/guard", Name: new("Other guard"), Artifacts: []string{gate}, Provenance: confirmed},
			{ID: "character/inn/draft", Name: new("Candidate speaker"), Artifacts: []string{inn}, Provenance: candidate},
		},
		Scenes: []contextpack.Scene{
			{ID: "scene/inn", Summary: new("At the inn."), Artifacts: []string{inn}, SpeakerIDs: []string{"character/inn/keeper"}, Provenance: confirmed},
			{ID: "scene/court", Summary: new("At the courtyard."), Artifacts: []string{inn}, SpeakerIDs: []string{"character/inn/guard"}, Provenance: confirmed},
			{ID: "scene/draft", Summary: new("Candidate scene."), Artifacts: []string{inn}, SpeakerIDs: []string{"character/inn/draft"}, Provenance: candidate},
		},
		Units: []contextpack.Unit{
			packUnit(current, "unit/welcome", new("scene/inn"), new("character/inn/keeper"), new("unit/introduction"), []string{"night", "quest:lantern"}, confirmed),
			packUnit(other, "unit/arrival", new("scene/court"), new("character/inn/guard"), nil, nil, confirmed),
			packUnit(intro, "unit/introduction", new("scene/inn"), nil, nil, nil, confirmed),
			packUnit(alternate, "unit/alternate", nil, nil, nil, nil, confirmed),
			packUnit(later, "unit/later", new("scene/inn"), nil, new("unit/welcome"), nil, confirmed),
			packUnit(draft, "unit/draft", new("scene/inn"), new("character/inn/keeper"), new("unit/introduction"), []string{"night", "quest:lantern"}, candidate),
		},
		GlossaryRefs: []contextpack.GlossaryRef{
			{ID: "glossary/terms", Schema: "glossary-v1", Reference: "optional glossary reference", Provenance: confirmed},
		},
	}
	pack.Units[5].RawSourceDigest = strings.Repeat("3", 64)
	return Request{
		Session: session, UnitID: current.ID(), GameNamespace: game, ArtifactNamespace: inn,
		Categories: Categories{Speaker: true, Scene: true, Neighbor: true}, Budget: Budget{MaxItems: 8, MaxTokens: 200},
	}, pack
}

func fixtureUnit(t *testing.T, stableID, text, target string) unit.TranslationUnit {
	t.Helper()
	id, err := unit.NewUnitID("fixture", "v1", stableID)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := unit.NewTranslationUnit(id, []byte(text), "en", target, map[string]string{"speaker": "character/inn/keeper"})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func packUnit(entry unit.TranslationUnit, id string, scene, speaker, previous *string, tags []string, provenance contextpack.Provenance) contextpack.Unit {
	return contextpack.Unit{
		ID: id, ArtifactNamespace: inn, Adapter: entry.ID().Adapter(), FormatSchema: entry.ID().FormatSchema(), StableID: entry.ID().StableID(),
		RawSourceDigest: hexDigest(entry.SourceDigest()), SceneID: scene, SpeakerID: speaker, PreviousUnitID: previous, Tags: tags, Provenance: provenance,
	}
}

func hexDigest(digest [32]byte) string { return hex.EncodeToString(digest[:]) }

func readPack(pack contextpack.Pack) (*contextpack.Document, error) {
	raw, err := json.Marshal(pack)
	if err != nil {
		return nil, err
	}
	return contextpack.Read(bytes.NewReader(raw))
}

func mustResolve(t *testing.T, req Request, pack contextpack.Pack) (ResolvedContext, Diagnostics) {
	t.Helper()
	doc, err := readPack(pack)
	if err != nil {
		t.Fatal(err)
	}
	req.Pack = doc
	resolved, diag, err := Resolve(t.Context(), req, func(_ context.Context, items []Item) (int, error) {
		return len(items), nil
	})
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	return resolved, diag
}

func assertItems(t *testing.T, resolved ResolvedContext, want ...Item) {
	t.Helper()
	if items := resolved.Items(); !slices.Equal(items, want) {
		t.Fatalf("items = %v, want %v", items, want)
	}
}

func TestSelectionGolden(t *testing.T) {
	req, pack := fixture(t)
	resolved, diag := mustResolve(t, req, pack)
	golden, err := os.ReadFile("testdata/resolved-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved.Canonical(); !bytes.Equal(got, golden) {
		t.Fatalf("canonical = %s, want %s", got, golden)
	}
	const fingerprint = "3c4fbc7ae13d9be2a900e52f521d2a8f6bdc0568d8f60b69ee4651bcfe8f091d"
	if hexDigest(resolved.Fingerprint()) != fingerprint || diag.Fingerprint != resolved.Fingerprint() {
		t.Fatalf("fingerprint = %x, want %s", resolved.Fingerprint(), fingerprint)
	}
	if !slices.Equal(diag.Matched, []Decision{
		{"z-game", "game_world"}, {"a-inn", "artifact_world"}, {"a-quest", "tagged_world"},
		{"scene/inn", "confirmed_unit"}, {"character/inn/keeper", "confirmed_unit"}, {"unit/introduction", "confirmed_unit"},
	}) || !slices.Contains(diag.Excluded, Decision{"z-inn-duplicate", "duplicate_payload"}) {
		t.Fatalf("selection diagnostics: %+v", diag)
	}
	resolved.Items()[0].Text = "changed"
	resolved.Canonical()[0] = '!'
	if !bytes.Equal(resolved.Canonical(), golden) || resolved.Items()[0].Text != "The coast is cold." {
		t.Fatal("mutating returned slices changed the selection")
	}
}

func TestUnitAndCategorySelection(t *testing.T) {
	req, pack := fixture(t)
	resolved, _ := mustResolve(t, req, pack)
	welcome := resolved.Items()
	req.UnitID = fixtureUnit(t, "arrival", "Welcome.", "ja").ID()
	resolved, _ = mustResolve(t, req, pack)
	assertItems(t, resolved, welcome[0], welcome[1],
		Item{Category: Scene, Text: "At the courtyard."}, Item{Category: Speaker, Name: "Guard"})

	req.UnitID = fixtureUnit(t, "missing", "Welcome.", "ja").ID()
	resolved, _ = mustResolve(t, req, pack)
	assertItems(t, resolved, welcome[:2]...)

	req.UnitID = fixtureUnit(t, "welcome", "Welcome.", "ja").ID()
	for _, tc := range []struct {
		name    string
		options Categories
		want    []Item
	}{
		{"default", Categories{}, welcome[:3]},
		{"scene", Categories{Scene: true}, welcome[:4]},
		{"speaker", Categories{Speaker: true}, append(slices.Clone(welcome[:3]), welcome[4])},
		{"neighbor", Categories{Neighbor: true}, append(slices.Clone(welcome[:3]), welcome[5])},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req.Categories = tc.options
			got, _ := mustResolve(t, req, pack)
			assertItems(t, got, tc.want...)
		})
	}
}

func TestGameOnlyAndTagReplacement(t *testing.T) {
	req, pack := fixture(t)
	baseline, _ := mustResolve(t, req, pack)
	world := baseline.Items()[:3]
	req.Selectors.Tags = []string{"quest:lantern"}
	got, _ := mustResolve(t, req, pack)
	assertItems(t, got, append(slices.Clone(world[:2]), baseline.Items()[3:]...)...)

	req.ArtifactNamespace = ""
	req.Selectors.Tags = []string{"quest:lantern", "night"}
	got, _ = mustResolve(t, req, pack)
	assertItems(t, got, world[0], world[2])

	req.Selectors.Tags = []string{}
	got, _ = mustResolve(t, req, pack)
	assertItems(t, got, world[0])

	pack.Scope.Artifacts = nil
	pack.World = []contextpack.World{pack.World[0], pack.World[3]}
	pack.Units = nil
	pack.Characters = nil
	pack.Scenes = nil
	noArtifact, _ := mustResolve(t, req, pack)
	if got.Fingerprint() != noArtifact.Fingerprint() {
		t.Fatal("game-only scope depended on registered artifacts")
	}
}

func TestExplicitSelectionAndCandidates(t *testing.T) {
	t.Run("overrides", func(t *testing.T) {
		req, pack := fixture(t)
		req.Selectors = Selectors{SceneID: "scene/court", SpeakerID: "character/inn/guard", PreviousUnitID: "unit/alternate", Tags: []string{}}
		got, diag := mustResolve(t, req, pack)
		assertItems(t, got,
			Item{Category: World, Text: "The coast is cold."}, Item{Category: World, Text: "The inn opens at dusk."},
			Item{Category: Scene, Text: "At the courtyard."}, Item{Category: Speaker, Name: "Guard"},
			Item{Category: Neighbor, Text: "Draw the curtains."})
		if !slices.Equal(diag.Matched[2:], []Decision{
			{"scene/court", "explicit_selector"}, {"character/inn/guard", "explicit_selector"}, {"unit/alternate", "explicit_selector"},
		}) {
			t.Fatalf("match reasons: %v", diag.Matched)
		}
	})

	t.Run("candidate unit", func(t *testing.T) {
		req, pack := fixture(t)
		req.UnitID = fixtureUnit(t, "draft", "Draft source.", "ja").ID()
		got, diag := mustResolve(t, req, pack)
		assertItems(t, got, Item{Category: World, Text: "The coast is cold."}, Item{Category: World, Text: "The inn opens at dusk."})
		if !slices.Contains(diag.Excluded, Decision{"unit/draft", "not_confirmed"}) {
			t.Fatalf("missing candidate exclusion: %v", diag.Excluded)
		}
	})

	t.Run("candidate selectors", func(t *testing.T) {
		req, pack := fixture(t)
		req.Selectors = Selectors{SceneID: "scene/draft", PreviousUnitID: "unit/draft"}
		got, _ := mustResolve(t, req, pack)
		assertItems(t, got,
			Item{Category: World, Text: "The coast is cold."}, Item{Category: World, Text: "The inn opens at dusk."},
			Item{Category: World, Text: "The lantern belongs to the quest."},
			Item{Category: Speaker, Name: "Keeper", Text: "Welcomes weary travelers."})
		req.Selectors = Selectors{SpeakerID: "character/inn/draft"}
		got, _ = mustResolve(t, req, pack)
		assertItems(t, got,
			Item{Category: World, Text: "The coast is cold."}, Item{Category: World, Text: "The inn opens at dusk."},
			Item{Category: World, Text: "The lantern belongs to the quest."},
			Item{Category: Scene, Text: "At the inn."}, Item{Category: Neighbor, Text: "Leave your boots by the door."})
	})

	t.Run("metadata only", func(t *testing.T) {
		req, pack := fixture(t)
		pack.Scenes[0].Summary = nil
		pack.Characters[0].Name = nil
		pack.Characters[0].Description = nil
		got, diag := mustResolve(t, req, pack)
		if len(got.Items()) != 4 || !slices.Contains(diag.Excluded, Decision{"scene/inn", "no_payload"}) ||
			!slices.Contains(diag.Excluded, Decision{"character/inn/keeper", "no_payload"}) {
			t.Fatalf("metadata-only selections: %v, %v", got.Items(), diag.Excluded)
		}
	})
}

func TestRejectInconsistentSelection(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Request, *contextpack.Pack)
		want error
	}{
		{"wrong game", func(r *Request, _ *contextpack.Pack) { r.GameNamespace = "unknown" }, ErrGameScope},
		{"unknown artifact", func(r *Request, _ *contextpack.Pack) { r.ArtifactNamespace = "unknown" }, ErrArtifactScope},
		{"other artifact snapshot", func(r *Request, _ *contextpack.Pack) { r.ArtifactNamespace = gate }, ErrArtifactDigest},
		{"wrong target language", func(_ *Request, p *contextpack.Pack) { p.TargetLanguage = "de" }, ErrLanguages},
		{"stale current source", func(_ *Request, p *contextpack.Pack) { p.Units[0].RawSourceDigest = strings.Repeat("1", 64) }, ErrSourceDigest},
		{"stale previous source", func(_ *Request, p *contextpack.Pack) { p.Units[2].RawSourceDigest = strings.Repeat("1", 64) }, ErrSourceDigest},
		{"ambiguous short ID", func(r *Request, _ *contextpack.Pack) { r.Selectors.SpeakerID = "guard" }, ErrSelector},
		{"wrong ID type", func(r *Request, _ *contextpack.Pack) { r.Selectors.SceneID = "unit/introduction" }, ErrSelector},
		{"speaker out of scope", func(r *Request, _ *contextpack.Pack) { r.Selectors.SpeakerID = "character/other/guard" }, ErrSelectorScope},
		{"speaker conflict", func(r *Request, _ *contextpack.Pack) { r.Selectors.SpeakerID = "character/inn/guard" }, ErrRelationship},
		{"previous scene conflict", func(r *Request, _ *contextpack.Pack) { r.Selectors.PreviousUnitID = "unit/arrival" }, ErrRelationship},
		{"speaker conflicts with list of candidates", func(r *Request, p *contextpack.Pack) {
			r.Selectors.SpeakerID = "character/inn/guard"
			p.Characters[0].Provenance.Status = "candidate"
		}, ErrRelationship},
		{"previous unit points to other candidate scene", func(r *Request, p *contextpack.Pack) {
			r.Selectors.PreviousUnitID = "unit/arrival"
			p.Scenes[1].Provenance.Status = "candidate"
		}, ErrRelationship},
		{"previous self reference", func(r *Request, _ *contextpack.Pack) { r.Selectors.PreviousUnitID = "unit/welcome" }, ErrRelationship},
		{"previous indirect cycle", func(r *Request, _ *contextpack.Pack) { r.Selectors.PreviousUnitID = "unit/later" }, ErrRelationship},
		{"explicit conflicting quest", func(r *Request, _ *contextpack.Pack) {
			r.Selectors.Tags = []string{"quest:lantern", "quest:ship"}
		}, ErrQuest},
		{"inherited conflicting quest", func(_ *Request, p *contextpack.Pack) {
			p.Units[0].Tags = append(p.Units[0].Tags, "quest:ship")
		}, ErrQuest},
		{"empty quest tag", func(r *Request, _ *contextpack.Pack) { r.Selectors.Tags = []string{"quest:"} }, ErrTags},
		{"missing full previous ID", func(_ *Request, p *contextpack.Pack) { p.Units[2].FormatSchema = "v2" }, ErrPreviousUnit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, pack := fixture(t)
			tc.edit(&req, &pack)
			doc, err := readPack(pack)
			if err != nil {
				t.Fatal(err)
			}
			req.Pack = doc
			got, diag, err := Resolve(t.Context(), req, nil)
			if !errors.Is(err, tc.want) || len(got.Items()) != 0 || diag.Fingerprint != [32]byte{} {
				t.Fatalf("selection = %v, diag = %+v, error = %v, want %v", got.Items(), diag, err, tc.want)
			}
		})
	}

	t.Run("ambiguous identity in pack", func(t *testing.T) {
		_, pack := fixture(t)
		duplicate := pack.Units[0]
		duplicate.ID = "unit/duplicate"
		pack.Units = append(pack.Units, duplicate)
		if _, err := readPack(pack); err == nil || !strings.Contains(err.Error(), "duplicate stable unit in artifact") {
			t.Fatalf("reader error = %v", err)
		}
	})
}

func TestPreviousSourceIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		text   string
		target string
		want   error
	}{
		{"invalid UTF-8", "\xff", "ja", ErrPreviousSource},
		{"wrong target language", "Keep out.", "de", ErrLanguages},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, pack := fixture(t)
			units := req.Session.Units()
			previous := fixtureUnit(t, "introduction", tc.text, tc.target)
			units[slices.IndexFunc(units, func(u unit.TranslationUnit) bool { return u.ID() == previous.ID() })] = previous
			session, err := unit.NewSession(req.Session.Artifact(), units)
			if err != nil {
				t.Fatal(err)
			}
			pack.Units[2].RawSourceDigest = hexDigest(previous.SourceDigest())
			req.Session = session
			req.Pack, err = readPack(pack)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := Resolve(t.Context(), req, nil); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestEffectiveFingerprint(t *testing.T) {
	req, pack := fixture(t)
	first, firstDiag := mustResolve(t, req, pack)

	pack.Scope.Artifacts[1].OriginalArtifactDigest = strings.Repeat("7", 64)
	pack.World[4].Text = "The outer gates were replaced."
	pack.World[5].Text = "Another candidate world."
	pack.Characters[0].Provenance.Sources = []contextpack.Source{{Kind: "manual", Reference: new("different notes")}}
	pack.Scenes[1].Summary = new("At another courtyard.")
	pack.Units[0].Tags = append(pack.Units[0].Tags, "unused-tag")
	pack.GlossaryRefs[0].Reference = "another external glossary"
	pack.World = append(pack.World, contextpack.World{
		ID: "z-other-quest", Text: "Another quest reference.", Tags: []string{"quest:ship"}, Provenance: pack.World[0].Provenance,
	})
	slices.Reverse(pack.World)
	slices.Reverse(pack.Units)
	req.Budget.MaxTokens++
	unchanged, changedDiag := mustResolve(t, req, pack)
	if unchanged.Fingerprint() != first.Fingerprint() || !bytes.Equal(unchanged.Canonical(), first.Canonical()) ||
		!slices.Equal(unchanged.Items(), first.Items()) || slices.Equal(changedDiag.Excluded, firstDiag.Excluded) {
		t.Fatal("unused changes affected effective context or went unreported")
	}

	pack.Characters[0].Description = new("Greets travelers at dusk.")
	changed, _ := mustResolve(t, req, pack)
	if changed.Fingerprint() == first.Fingerprint() || bytes.Equal(changed.Canonical(), first.Canonical()) {
		t.Fatal("changed speaker payload kept the same effective identity")
	}
}

func TestContextBudget(t *testing.T) {
	const (
		world          = "Long world description. "
		source         = "Welcome."
		reservedTokens = 12
		contextTokens  = 4
	)
	for _, tc := range []struct {
		name                         string
		modelTokens, maxItems, count int
		countErr                     error
		want                         error
		wantCalls                    int
	}{
		{"exact fit after reservations", reservedTokens + contextTokens, 3, contextTokens, nil, nil, 1},
		{"token overflow", reservedTokens + contextTokens - 1, 3, contextTokens, nil, ErrTokenLimit, 1},
		{"item overflow", reservedTokens + contextTokens, 2, contextTokens, nil, ErrItemLimit, 0},
		{"all tokens reserved", reservedTokens, 3, contextTokens, nil, ErrTokenLimit, 1},
		{"counter unavailable", reservedTokens + contextTokens, 3, 0, fmt.Errorf("%s %s", world, source), ErrTokenCount, 1},
		{"counter negative", reservedTokens + contextTokens, 3, -1, nil, ErrTokenCount, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, pack := fixture(t)
			longWorld := strings.Repeat(world, 400)
			pack.World[0].Text = longWorld
			pack.World[2].Text = longWorld
			req.Categories = Categories{}
			req.Budget = Budget{MaxItems: tc.maxItems, MaxTokens: tc.modelTokens - reservedTokens}
			doc, err := readPack(pack)
			if err != nil {
				t.Fatal(err)
			}
			req.Pack = doc
			calls := 0
			got, diag, err := Resolve(t.Context(), req, func(_ context.Context, items []Item) (int, error) {
				calls++
				if len(items) != 3 || items[0].Text != longWorld {
					t.Fatalf("counter received altered selection: %d items", len(items))
				}
				items[0].Text = "mutated token counter item"
				return tc.count, tc.countErr
			})
			if !errors.Is(err, tc.want) || calls != tc.wantCalls || diag.Budget != req.Budget || len(diag.Matched) != 3 {
				t.Fatalf("error = %v, want %v, calls = %d, diag = %+v", err, tc.want, calls, diag)
			}
			if tc.want == nil {
				if got.Items()[0].Text != longWorld || diag.Tokens != contextTokens || diag.Fingerprint != got.Fingerprint() {
					t.Fatal("accepted budget changed selection or lost diagnostics")
				}
			} else if len(got.Items()) != 0 || len(got.Canonical()) != 0 || diag.Fingerprint != [32]byte{} {
				t.Fatal("failed budget returned partial context")
			}
			emitted := fmt.Sprint(diag, err)
			for _, text := range []string{world, source, "The inn opens at dusk.", pack.GlossaryRefs[0].Reference} {
				if strings.Contains(emitted, text) {
					t.Fatal("diagnostics exposed source, context or glossary data")
				}
			}
		})
	}
}

func TestEmptyContextNeedsNoTokens(t *testing.T) {
	req, pack := fixture(t)
	req.Budget = Budget{}
	pack.World, pack.Units = nil, nil
	doc, err := readPack(pack)
	if err != nil {
		t.Fatal(err)
	}
	for _, pack := range []*contextpack.Document{nil, doc} {
		req.Pack = pack
		got, diag, err := Resolve(t.Context(), req, nil)
		const emptyFingerprint = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
		if err != nil || len(got.Items()) != 0 || len(got.Canonical()) != 0 ||
			hexDigest(got.Fingerprint()) != emptyFingerprint || diag.Fingerprint != got.Fingerprint() || len(diag.Matched) != 0 || diag.Tokens != 0 {
			t.Fatalf("empty context = %x, diag = %+v, error = %v", got.Canonical(), diag, err)
		}
	}
}
