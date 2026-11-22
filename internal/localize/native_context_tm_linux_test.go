//go:build linux

package localize

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/contextpack"
	"github.com/sh4869221b/yakuori/internal/contextresolve"
	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
	"golang.org/x/sys/unix"
)

const (
	nativeContextKeeper = "Innkeeper"
	nativeContextGuard  = "Guard"
)

var nativeContextTexts = map[string]string{"1": "宿へようこそ 猫 TOKEN", "2": "門へようこそ 猫 TOKEN", "3": "TOKEN"}

type nativeContextFixture struct {
	adapter *w3strings.Adapter
	session unit.Session
	source  string
	raw     []byte
	scope   tm.Scope
	store   *tm.Store
	db      *sql.DB
}

func newNativeContextFixture(t *testing.T) nativeContextFixture {
	t.Helper()
	dir := t.TempDir()
	var fs unix.Statfs_t
	if err := unix.Statfs(dir, &fs); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 || fs.Type != unix.EXT4_SUPER_MAGIC && fs.Type != unix.BTRFS_SUPER_MAGIC {
		t.Skip("requires non-root ext4 or Btrfs")
	}
	raw := w3SQLiteArtifactBytes([]w3SQLiteEntry{
		{1, "Welcome cat TOKEN", []uint32{0x01020304}},
		{2, "Welcome cat TOKEN", []uint32{0x89abcdef}},
		{3, "TOKEN", nil},
	})
	adapter := w3strings.New()
	session, err := adapter.Import(raw, validate.Profile{})
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.w3strings")
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	store, db := tmStoreFixture(t)
	return nativeContextFixture{adapter: adapter, session: session, source: source, raw: raw, scope: tmScope(), store: store, db: db}
}

func nativeContextEngine(t *testing.T, segmented bool, target string, tune func(*inference.ModelInfo, *int)) *evaluationHarness {
	t.Helper()
	terms, err := glossary.New(glossary.Schema, []glossary.Entry{
		{Source: "cat", Target: target, Mode: glossary.Mandatory, Match: glossary.Word},
		{Source: "TOKEN", Mode: glossary.Protect, Match: glossary.Word},
	})
	if err != nil {
		t.Fatal(err)
	}
	limits := config.DefaultLimits()
	h := contextEvaluationCoreWithConditions(t, segmented, limits, limits.ContextTokens, terms, tune)
	h.finish = func(_ context.Context, req inference.GenerationRequest, result inference.GenerationResult) (inference.GenerationResult, error) {
		prefix := "ようこそ"
		for _, item := range req.ResolvedContext().Items() {
			if item.Category == contextresolve.Speaker && item.Name == nativeContextKeeper {
				prefix = "宿へようこそ"
			}
			if item.Category == contextresolve.Speaker && item.Name == nativeContextGuard {
				prefix = "門へようこそ"
			}
		}
		result.Text = strings.ReplaceAll(strings.ReplaceAll(result.Text, "Welcome", prefix), "猫", target)
		return result, nil
	}
	return h
}

func (f nativeContextFixture) pack(t *testing.T, scope tm.Scope, edit func(*contextpack.Pack)) ContextPackOptions {
	t.Helper()
	artifactDigest := f.session.ArtifactDigest()
	provenance := contextpack.Provenance{
		Method: "manual", Version: "v1", Status: "confirmed", UserEdited: new(true),
		Sources: []contextpack.Source{{Kind: "manual", Reference: new("hand-authored native fixture")}},
	}
	pack := contextpack.Pack{
		Schema: contextpack.Schema,
		Scope: contextpack.Scope{GameNamespace: scope.GameNamespace, Artifacts: []contextpack.Artifact{
			{ArtifactNamespace: scope.ArtifactNamespace, OriginalArtifactDigest: hex.EncodeToString(artifactDigest[:])},
		}},
		SourceLanguage: "en", TargetLanguage: "ja",
		Characters: []contextpack.Character{
			{ID: "keeper", Name: new(nativeContextKeeper), Artifacts: []string{scope.ArtifactNamespace}, Provenance: provenance},
			{ID: "guard", Name: new(nativeContextGuard), Artifacts: []string{scope.ArtifactNamespace}, Provenance: provenance},
		},
		Scenes: []contextpack.Scene{
			{ID: "inn", Summary: new("Guests enter the inn."), Artifacts: []string{scope.ArtifactNamespace}, Provenance: provenance},
			{ID: "gate", Summary: new("Soldiers return to the gate."), Artifacts: []string{scope.ArtifactNamespace}, Provenance: provenance},
		},
	}
	for _, u := range f.session.Units() {
		id := u.ID()
		scene, speaker := "inn", "keeper"
		if id.StableID() == "2" {
			scene, speaker = "gate", "guard"
		}
		digest := u.SourceDigest()
		pack.Units = append(pack.Units, contextpack.Unit{
			ID: "line-" + id.StableID(), ArtifactNamespace: scope.ArtifactNamespace, Adapter: id.Adapter(),
			FormatSchema: id.FormatSchema(), StableID: id.StableID(), RawSourceDigest: hex.EncodeToString(digest[:]),
			SceneID: &scene, SpeakerID: &speaker, Provenance: provenance,
		})
	}
	if edit != nil {
		edit(&pack)
	}
	raw, err := json.Marshal(pack)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := contextpack.Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return ContextPackOptions{
		Pack: doc, Categories: contextresolve.Categories{Scene: true, Speaker: true},
		Budget: contextresolve.Budget{MaxItems: 2, MaxTokens: 128},
		CountTokens: func(_ context.Context, items []contextresolve.Item) (int, error) {
			n := 0
			for _, item := range items {
				n += len(item.Text) + len(item.Name)
			}
			return n, nil
		},
	}
}

func (f nativeContextFixture) options(t *testing.T) publication.Options {
	t.Helper()
	return publication.Options{Source: f.source, Output: filepath.Join(t.TempDir(), "translated.w3strings"), Mode: publication.Create}
}

func (f nativeContextFixture) run(t *testing.T, h *evaluationHarness, selection ContextPackOptions, scope tm.Scope, existingAPI bool) (publication.Options, *AcceptanceObservation, Result, error) {
	t.Helper()
	h.requests = nil
	options := f.options(t)
	observation := &AcceptanceObservation{SourceSHA256: f.session.ArtifactDigest()}
	core := h.core.WithAcceptanceObservation(observation)
	if existingAPI {
		result, err := core.FileWithTM(t.Context(), options, f.adapter, h.profile, f.store, scope)
		return options, observation, result, err
	}
	result, err := core.FileWithTMContext(t.Context(), options, f.adapter, h.profile, f.store, scope, selection)
	return options, observation, result, err
}

func (f nativeContextFixture) success(t *testing.T, h *evaluationHarness, selection ContextPackOptions, scope tm.Scope, existingAPI bool, calls int, want map[string]string) *AcceptanceObservation {
	t.Helper()
	options, observed, result, err := f.run(t, h, selection, scope, existingAPI)
	if err != nil || !result.TMCommitted || result.Publication.State != publication.Published || len(h.requests) != calls ||
		!observed.FinalValidated || observed.TMVerificationStatus != "pass" || observed.TMVerifiedUnits != 3 {
		t.Fatalf("result=%+v error=%v Generate=%d want=%d observed=%+v", result, err, len(h.requests), calls, observed)
	}
	if got := w3SQLiteObserve(t, f.adapter, options.Output); !maps.Equal(got, want) {
		t.Fatalf("W3 output=%v, want %v", got, want)
	}
	return observed
}

func (f nativeContextFixture) row(t *testing.T, text string) (source, profile []byte) {
	t.Helper()
	if err := f.db.QueryRow("SELECT source_identity, translation_profile FROM tm WHERE translated_text = ?", text).Scan(&source, &profile); err != nil {
		t.Fatal(err)
	}
	return source, profile
}

func TestNativeContextTM(t *testing.T) {
	t.Run("exact-reuse", func(t *testing.T) {
		f := newNativeContextFixture(t)
		h := nativeContextEngine(t, false, "猫", nil)
		baseline := f.pack(t, f.scope, nil)
		legacyTexts := map[string]string{"1": "ようこそ 猫 TOKEN", "2": "ようこそ 猫 TOKEN", "3": "TOKEN"}
		f.success(t, h, ContextPackOptions{}, f.scope, true, 2, legacyTexts)
		legacyRows := w3SQLiteRows(t, f.db)
		f.success(t, h, ContextPackOptions{}, f.scope, false, 0, legacyTexts)
		disabled := baseline
		disabled.Categories = contextresolve.Categories{}
		f.success(t, h, disabled, f.scope, false, 0, legacyTexts)
		w3SQLiteAssertRows(t, f.db, legacyRows)

		f.success(t, h, baseline, f.scope, false, 2, nativeContextTexts)
		// Canonical selection and rendering versions are part of the stored
		// profile. A third unit matched the pack but needed no generation.
		source, contextProfile := f.row(t, nativeContextTexts["1"])
		var persisted struct {
			Context  identity.ContextInput
			Prompt   identity.PromptInput
			Template identity.TemplateInput
		}
		if err := json.Unmarshal(contextProfile, &persisted); err != nil {
			t.Fatal(err)
		}
		const firstSelection = "{\"version\":\"yakuori.context-selection/v1\",\"items\":[{\"category\":\"scene\",\"text\":\"Guests enter the inn.\"},{\"category\":\"speaker\",\"name\":\"Innkeeper\"}]}"
		if persisted.Context.Mode != "provided" || string(persisted.Context.Additional) != firstSelection ||
			persisted.Prompt.Schema != 1 || persisted.Prompt.Variant != "plain-resolved-context-v1" || persisted.Template.RendererVersion != "v1" {
			t.Fatalf("unexpected context profile: %s", contextProfile)
		}
		rows := w3SQLiteRows(t, f.db)
		if len(rows) != len(legacyRows)+2 {
			t.Fatalf("rows=%d, want %d", len(rows), len(legacyRows)+2)
		}
		f.success(t, h, baseline, f.scope, false, 0, nativeContextTexts)
		unusedChange := f.pack(t, f.scope, func(p *contextpack.Pack) {
			p.Characters[0].Provenance.Version = "edited-metadata"
		})
		f.success(t, h, unusedChange, f.scope, false, 0, nativeContextTexts)
		w3SQLiteAssertRows(t, f.db, rows)

		sceneChange := f.pack(t, f.scope, func(p *contextpack.Pack) {
			p.Scenes[0].Summary = new("The inn is empty.")
		})
		got := f.success(t, h, sceneChange, f.scope, false, 1, nativeContextTexts)
		assertNativeContextKinds(t, got, []string{"generated", "tm", "tm"})
		speakerChange := f.pack(t, f.scope, func(p *contextpack.Pack) {
			p.Units[0].SpeakerID = new("guard")
		})
		want := maps.Clone(nativeContextTexts)
		want["1"] = nativeContextTexts["2"]
		got = f.success(t, h, speakerChange, f.scope, false, 1, want)
		assertNativeContextKinds(t, got, []string{"generated", "tm", "tm"})

		// Simulate a persisted row from a different selection schema, using
		// the original full key so only this unit becomes a miss.
		const oldSelectionVersion = "yakuori.context-selection/v0"
		oldSelection := bytes.ReplaceAll(persisted.Context.Additional, []byte("yakuori.context-selection/v1"), []byte(oldSelectionVersion))
		oldProfile := bytes.Replace(contextProfile, []byte(base64.StdEncoding.EncodeToString(persisted.Context.Additional)), []byte(base64.StdEncoding.EncodeToString(oldSelection)), 1)
		if bytes.Equal(oldProfile, contextProfile) {
			t.Fatal("selection version was not located in the persisted key")
		}
		writeNativeContextRow(t, f.db, "UPDATE tm SET translation_profile=? WHERE key_schema=? AND source_identity=? AND translation_profile=?",
			oldProfile, identity.KeySchema, source, contextProfile)
		got = f.success(t, h, baseline, f.scope, false, 1, nativeContextTexts)
		assertNativeContextKinds(t, got, []string{"generated", "tm", "tm"})
		f.success(t, h, ContextPackOptions{}, f.scope, false, 0, legacyTexts)
	})

	t.Run("current-conditions", func(t *testing.T) {
		f := newNativeContextFixture(t)
		baseline := f.pack(t, f.scope, nil)
		h := nativeContextEngine(t, false, "猫", nil)
		f.success(t, h, baseline, f.scope, false, 2, nativeContextTexts)
		for _, change := range []struct {
			name   string
			target string
			tune   func(*inference.ModelInfo, *int)
		}{
			{"model-bytes", "猫", func(info *inference.ModelInfo, _ *int) {
				info.ModelSHA256[0]++
				info.Tokenizer.ModelSHA256 = info.ModelSHA256
			}},
			{"generation-policy", "猫", func(_ *inference.ModelInfo, n *int) { *n = *n + 1 }},
			{"template-renderer", "猫", func(info *inference.ModelInfo, _ *int) { info.Template.RendererVersion = "v2" }},
			{"glossary", "ねこ", nil},
		} {
			t.Run(change.name, func(t *testing.T) {
				variant := nativeContextEngine(t, false, change.target, change.tune)
				want := maps.Clone(nativeContextTexts)
				for id, value := range want {
					want[id] = strings.ReplaceAll(value, "猫", change.target)
				}
				f.success(t, variant, baseline, f.scope, false, 2, want)
				f.success(t, variant, baseline, f.scope, false, 0, want)
			})
		}
		f.success(t, h, baseline, f.scope, false, 0, nativeContextTexts)
	})

	t.Run("scope", func(t *testing.T) {
		f := newNativeContextFixture(t)
		h := nativeContextEngine(t, false, "猫", nil)
		f.success(t, h, f.pack(t, f.scope, nil), f.scope, false, 2, nativeContextTexts)
		rows := w3SQLiteRows(t, f.db)
		for _, tc := range []struct {
			name  string
			scope tm.Scope
			edit  func(*contextpack.Pack)
			want  error
		}{
			{"game", tm.Scope{GameNamespace: "other-game", ArtifactNamespace: f.scope.ArtifactNamespace}, nil, contextresolve.ErrGameScope},
			{"artifact", tm.Scope{GameNamespace: f.scope.GameNamespace, ArtifactNamespace: "other-artifact"}, nil, contextresolve.ErrArtifactScope},
			{"snapshot", f.scope, func(p *contextpack.Pack) {
				p.Scope.Artifacts[0].OriginalArtifactDigest = strings.Repeat("1", 64)
			}, contextresolve.ErrArtifactDigest},
		} {
			t.Run(tc.name, func(t *testing.T) {
				options, _, result, err := f.run(t, h, f.pack(t, f.scope, tc.edit), tc.scope, false)
				if !errors.Is(err, tc.want) || result.TMCommitted || result.Publication.State != publication.NotPublished || len(h.requests) != 0 {
					t.Fatalf("result=%+v Generate=%d error=%v", result, len(h.requests), err)
				}
				w3SQLiteAssertNoOutput(t, options.Output)
				w3SQLiteAssertRows(t, f.db, rows)
			})
		}
		for _, scope := range []tm.Scope{
			{GameNamespace: "other-game", ArtifactNamespace: f.scope.ArtifactNamespace},
			{GameNamespace: f.scope.GameNamespace, ArtifactNamespace: "other-artifact"},
		} {
			f.success(t, h, f.pack(t, scope, nil), scope, false, 2, nativeContextTexts)
		}
	})

	t.Run("revalidation", func(t *testing.T) {
		f := newNativeContextFixture(t)
		h := nativeContextEngine(t, false, "猫", nil)
		pack := f.pack(t, f.scope, nil)
		f.success(t, h, pack, f.scope, false, 2, nativeContextTexts)
		firstSrc, firstProfile := f.row(t, nativeContextTexts["1"])
		secondSrc, secondProfile := f.row(t, nativeContextTexts["2"])
		rows := w3SQLiteRows(t, f.db)
		const update = "UPDATE tm SET translated_text=? WHERE key_schema=? AND source_identity=? AND translation_profile=?"
		writeNativeContextRow(t, f.db, update, "宿へようこそ 犬 TOKEN", identity.KeySchema, firstSrc, firstProfile)
		writeNativeContextRow(t, f.db, update, "門へようこそ 猫", identity.KeySchema, secondSrc, secondProfile)
		observed := f.success(t, h, pack, f.scope, false, 2, nativeContextTexts)
		if observed.TMValidation != (ValidationCounts{Attempted: 3, Passed: 1, Failed: 2}) {
			t.Fatalf("invalid mandatory and protection hits were not rejected: %+v", observed.TMValidation)
		}
		w3SQLiteAssertRows(t, f.db, rows)
		f.success(t, h, pack, f.scope, false, 0, nativeContextTexts)
	})

	t.Run("segmented", func(t *testing.T) {
		f := newNativeContextFixture(t)
		h := nativeContextEngine(t, true, "猫", nil)
		pack := f.pack(t, f.scope, nil)
		f.success(t, h, pack, f.scope, false, 2, nativeContextTexts)
		f.success(t, h, pack, f.scope, false, 0, nativeContextTexts)
	})
}

func assertNativeContextKinds(t *testing.T, observed *AcceptanceObservation, want []string) {
	t.Helper()
	var kinds []string
	for _, u := range observed.Units {
		kinds = append(kinds, u.Kind)
	}
	if !slices.Equal(kinds, want) {
		t.Fatalf("accepted kinds=%v, want %v", kinds, want)
	}
}

func writeNativeContextRow(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	result, err := db.Exec(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		t.Fatalf("updated %d exact rows: %v", count, err)
	}
}

type invalidNativeContextFinal struct {
	*w3strings.Adapter
	observed bool
}

func (a *invalidNativeContextFinal) Observe(reader io.Reader) (artifact.FinalObservation, error) {
	final, err := a.Adapter.Observe(reader)
	if err == nil {
		a.observed = true
		final.Units[0].Text = "unexpected stage text"
	}
	return final, err
}

func TestNativeContextTMPrecommitFailure(t *testing.T) {
	for _, phase := range []string{"generate", "validate", "stage"} {
		t.Run(phase, func(t *testing.T) {
			f := newNativeContextFixture(t)
			h := nativeContextEngine(t, false, "猫", nil)
			pack := f.pack(t, f.scope, nil)
			legacy := map[string]string{"1": "ようこそ 猫 TOKEN", "2": "ようこそ 猫 TOKEN", "3": "TOKEN"}
			f.success(t, h, ContextPackOptions{}, f.scope, true, 2, legacy)
			before := w3SQLiteRows(t, f.db)
			h.requests = nil
			var adapter Adapter = f.adapter
			var final *invalidNativeContextFinal
			injected := errors.New("fixture inference failure")
			want := error(injected)
			finish := h.finish
			switch phase {
			case "stage":
				final = &invalidNativeContextFinal{Adapter: f.adapter}
				adapter, want = final, artifact.ErrFinalMismatch
			case "validate":
				want = validate.ErrInvalidCandidate
			}
			h.finish = func(ctx context.Context, r inference.GenerationRequest, result inference.GenerationResult) (inference.GenerationResult, error) {
				if len(h.requests) == 2 && phase == "generate" {
					return inference.GenerationResult{}, injected
				}
				result, err := finish(ctx, r, result)
				if len(h.requests) == 2 && phase == "validate" {
					result.Text = strings.ReplaceAll(result.Text, "猫", "犬")
				}
				return result, err
			}
			options := f.options(t)
			result, err := h.core.FileWithTMContext(t.Context(), options, adapter, h.profile, f.store, f.scope, pack)
			var failure *Error
			if !errors.Is(err, want) || !errors.As(err, &failure) || failure.Phase != phase || result.TMCommitted ||
				result.Publication.State != publication.NotPublished || len(h.requests) != 2 {
				t.Fatalf("result=%+v error=%v Generate=%d", result, err, len(h.requests))
			}
			if final != nil && !final.observed {
				t.Fatal("the closed W3 stage did not reach real Observe")
			}
			if !bytes.Equal(fileBytes(t, f.source), f.raw) {
				t.Fatal("failed run changed its source")
			}
			w3SQLiteAssertNoOutput(t, options.Output)
			w3SQLiteAssertRows(t, f.db, before)
		})
	}
}
