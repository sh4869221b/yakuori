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
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/adapter/genericjson"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/contextpack"
	"github.com/sh4869221b/yakuori/internal/contextresolve"
	"github.com/sh4869221b/yakuori/internal/contextselection"
	"github.com/sh4869221b/yakuori/internal/glossary"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
	"golang.org/x/sys/unix"
)

const genericJSONSourceTerm = "Cafe\u0301"
const genericJSONTargetTerm = "カフェ"

type genericJSONContextLine struct {
	speaker, scene, text string
}

var genericJSONLines = [2]genericJSONContextLine{
	{"Innkeeper", "Guests enter the inn.", "  店主・宿のカフェ {name}\r\n"},
	{"Guard", "Soldiers return to the gate.", "  衛兵・門のカフェ {name}\r\n"},
}

var genericJSONScope = tm.Scope{GameNamespace: "sample-game", ArtifactNamespace: "dialogue"}

type genericJSONContextFixture struct {
	source, packPath string
	raw              []byte
	session          unit.Session
	store            *tm.Store
	db               *sql.DB
	h                *evaluationHarness
}

func newGenericJSONContextFixture(t *testing.T) genericJSONContextFixture {
	t.Helper()
	dir := t.TempDir()
	var fs unix.Statfs_t
	if err := unix.Statfs(dir, &fs); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 || fs.Type != unix.EXT4_SUPER_MAGIC && fs.Type != unix.BTRFS_SUPER_MAGIC {
		t.Skip("requires non-root ext4 or Btrfs")
	}
	f := genericJSONContextFixture{source: filepath.Join(dir, "source.json"), packPath: filepath.Join(dir, "context.json")}
	f.raw = fileBytes(t, "../adapter/genericjson/testdata/minimal-v1.json")
	writeFile(t, f.source, f.raw)

	terms, err := glossary.New(glossary.Schema, []glossary.Entry{{
		Source: genericJSONSourceTerm, Target: genericJSONTargetTerm, Mode: glossary.Mandatory, Match: glossary.Literal,
	}})
	if err != nil {
		t.Fatal(err)
	}
	limits := config.DefaultLimits()
	f.h = contextEvaluationCore(t, false, limits, limits.ContextTokens, terms)
	f.h.finish = func(_ context.Context, req inference.GenerationRequest, result inference.GenerationResult) (inference.GenerationResult, error) {
		items := req.ResolvedContext().Items()
		if len(items) != 2 || items[0].Category != contextresolve.Scene || items[1].Category != contextresolve.Speaker {
			return result, fmt.Errorf("expected selected scene and speaker, got %v", items)
		}
		speaker := map[string]string{"Innkeeper": "店主", "Guard": "衛兵", "Host": "亭主"}[items[1].Name]
		scene := map[string]string{"Guests enter the inn.": "宿", "Soldiers return to the gate.": "門", "Guests cross the courtyard.": "中庭"}[items[0].Text]
		if speaker == "" || scene == "" || strings.Contains(req.ProtectedSource(), "{name}") {
			return result, fmt.Errorf("wrong selected context or unprotected source: %v", items)
		}
		result.Text = strings.Replace(result.Text, genericJSONSourceTerm, speaker+"・"+scene+"の"+genericJSONTargetTerm, 1)
		return result, nil
	}
	f.session, err = newGenericJSONAdapter(t).Import(f.raw, f.h.profile)
	if err != nil {
		t.Fatal(err)
	}
	entries := f.session.Units()
	if len(entries) != 2 || entries[0].ID().StableID() != "001" || entries[1].ID().StableID() != "002" ||
		!bytes.Equal(entries[0].Source(), entries[1].Source()) {
		t.Fatal("fixture must contain two IDs with the same source")
	}
	provenance := contextpack.Provenance{
		Method: "manual", Version: "v1", Status: "confirmed", UserEdited: new(true),
		Sources: []contextpack.Source{{Kind: "manual", Reference: new("Generic JSON fixture")}},
	}
	digest := f.session.ArtifactDigest()
	pack := contextpack.Pack{
		Schema: contextpack.Schema,
		Scope: contextpack.Scope{GameNamespace: genericJSONScope.GameNamespace, Artifacts: []contextpack.Artifact{{
			ArtifactNamespace: genericJSONScope.ArtifactNamespace, OriginalArtifactDigest: hex.EncodeToString(digest[:]),
		}}},
		SourceLanguage: entries[0].SourceLanguage(), TargetLanguage: entries[0].TargetLanguage(),
	}
	for i, entry := range entries {
		id, origin, sourceDigest := entry.ID(), entry.Origin(), entry.SourceDigest()
		pack.Characters = append(pack.Characters, contextpack.Character{
			ID: origin["speaker"], Name: new(genericJSONLines[i].speaker),
			Artifacts: []string{genericJSONScope.ArtifactNamespace}, Provenance: provenance,
		})
		pack.Scenes = append(pack.Scenes, contextpack.Scene{
			ID: origin["scene"], Summary: new(genericJSONLines[i].scene),
			Artifacts: []string{genericJSONScope.ArtifactNamespace}, Provenance: provenance,
		})
		pack.Units = append(pack.Units, contextpack.Unit{
			ID: "line-" + id.StableID(), ArtifactNamespace: genericJSONScope.ArtifactNamespace,
			Adapter: id.Adapter(), FormatSchema: id.FormatSchema(), StableID: id.StableID(), RawSourceDigest: hex.EncodeToString(sourceDigest[:]),
			SpeakerID: new(origin["speaker"]), SceneID: new(origin["scene"]), Provenance: provenance,
		})
	}
	writeGenericJSONPack(t, f.packPath, pack)
	f.store, f.db = tmStoreFixture(t)
	return f
}

func newGenericJSONAdapter(t *testing.T) *genericjson.Adapter {
	t.Helper()
	adapter, err := genericjson.New(genericjson.Metadata{
		SourceLanguage: "en", TargetLanguage: "ja",
		GameNamespace: genericJSONScope.GameNamespace, ArtifactNamespace: genericJSONScope.ArtifactNamespace,
	}, config.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func writeGenericJSONPack(t *testing.T, path string, pack contextpack.Pack) {
	t.Helper()
	raw, err := json.Marshal(pack)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, raw)
}

func readGenericJSONPack(t *testing.T, path string) ContextPackOptions {
	t.Helper()
	doc, err := contextpack.Read(bytes.NewReader(fileBytes(t, path)))
	if err != nil {
		t.Fatal(err)
	}
	return ContextPackOptions{
		Pack: doc, Categories: contextresolve.Categories{Scene: true, Speaker: true},
		Budget:      contextresolve.Budget{MaxItems: 2, MaxTokens: 128},
		CountTokens: func(_ context.Context, items []contextresolve.Item) (int, error) { return len(items), nil },
	}
}

func (f genericJSONContextFixture) editedPack(t *testing.T, edit func(*contextpack.Pack)) ContextPackOptions {
	t.Helper()
	pack := readGenericJSONPack(t, f.packPath).Pack.Pack
	edit(&pack)
	path := filepath.Join(t.TempDir(), "context.json")
	writeGenericJSONPack(t, path, pack)
	return readGenericJSONPack(t, path)
}

func (f genericJSONContextFixture) options(t *testing.T) publication.Options {
	t.Helper()
	return publication.Options{Source: f.source, Output: filepath.Join(t.TempDir(), "translated.json"), Mode: publication.Create}
}

func (f genericJSONContextFixture) run(t *testing.T, selection ContextPackOptions, want [2]genericJSONContextLine, kinds [2]string) ([]byte, *AcceptanceObservation) {
	t.Helper()
	options := f.options(t)
	f.h.requests = nil
	obs := &AcceptanceObservation{SourceSHA256: f.session.ArtifactDigest()}
	result, err := f.h.core.WithAcceptanceObservation(obs).FileWithTMContext(t.Context(), options, newGenericJSONAdapter(t),
		f.h.profile, f.store, genericJSONScope, selection)
	f.assertSuccess(t, result, err, obs, want, kinds)
	if !bytes.Equal(fileBytes(t, f.source), f.raw) {
		t.Fatal("create publication changed input JSON")
	}
	return f.assertOutput(t, options.Output, want), obs
}

func (f genericJSONContextFixture) assertSuccess(t *testing.T, result Result, err error, obs *AcceptanceObservation, want [2]genericJSONContextLine, kinds [2]string) {
	t.Helper()
	calls := 0
	var units []AcceptanceUnit
	for i, entry := range f.session.Units() {
		if kinds[i] == "generated" {
			calls++
		}
		units = append(units, AcceptanceUnit{ID: entry.ID().StableID(), Kind: kinds[i], Text: want[i].text})
	}
	if err != nil || !result.TMCommitted || result.Publication.State != publication.Published ||
		len(f.h.requests) != calls || obs.Validation != (ValidationCounts{Attempted: calls, Passed: calls}) ||
		obs.TMValidation.Passed != len(want)-calls || !obs.FinalValidated || obs.TMVerificationStatus != "pass" || obs.TMVerifiedUnits != 2 {
		t.Fatalf("result=%+v error=%v Generate=%d want=%d observation=%+v", result, err, len(f.h.requests), calls, obs)
	}
	if !slices.EqualFunc(obs.Units, units, func(got, want AcceptanceUnit) bool {
		return got.ID == want.ID && got.Text == want.Text && got.Kind == want.Kind
	}) {
		t.Fatalf("accepted units=%+v, want %+v", obs.Units, units)
	}
}

func (f genericJSONContextFixture) assertOutput(t *testing.T, path string, want [2]genericJSONContextLine) []byte {
	t.Helper()
	raw := fileBytes(t, path)
	adapter := newGenericJSONAdapter(t)
	reloaded, err := adapter.Import(raw, f.h.profile)
	if err != nil {
		t.Fatal(err)
	}
	final, err := adapter.Observe(bytes.NewReader(raw))
	if err != nil || reloaded.UnitCount() != 2 || len(final.Units) != 2 {
		t.Fatalf("reloaded=%d observed=%d error=%v", reloaded.UnitCount(), len(final.Units), err)
	}
	entries := reloaded.Units()
	for i, original := range f.session.Units() {
		entry := entries[i]
		if entry.ID() != original.ID() || !bytes.Equal(entry.Source(), original.Source()) ||
			!slices.Equal(entry.ProtectionSpans(), original.ProtectionSpans()) || !maps.Equal(entry.Origin(), original.Origin()) ||
			final.Units[i].ID != original.ID() || final.Units[i].Text != want[i].text {
			t.Fatalf("published unit %s changed preserved fields or has wrong translation: %+v", original.ID().StableID(), final.Units[i])
		}
	}
	return raw
}

type genericJSONTMRow struct {
	schema                int
	source, profile, text string
}

func (f genericJSONContextFixture) row(t *testing.T, index int, line genericJSONContextLine) genericJSONTMRow {
	t.Helper()
	entry := f.session.Units()[index]
	source, err := unit.NewSourceIdentity(f.session, entry.ID(), genericJSONScope.GameNamespace, genericJSONScope.ArtifactNamespace, nil)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := contextselection.New([]contextselection.Item{
		{Category: contextselection.Scene, Text: line.scene}, {Category: contextselection.Speaker, Name: line.speaker},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := prompt.ResolvedContextIdentity(f.h.info.Prompt)
	if !ok {
		t.Fatal("fixture requires a context-aware prompt")
	}
	key, err := identity.NewContextExactKey(source, f.h.profile.CanonicalIdentity(), identity.UnitContext{
		Prompt: identityPrompt(p, f.h.info.DisabledThinkingPrefix), Selection: selected.Canonical(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return genericJSONTMRow{key.Schema(), string(key.SourceBytes()), string(key.ProfileBytes()), line.text}
}

func (f genericJSONContextFixture) assertRows(t *testing.T, want ...genericJSONTMRow) {
	t.Helper()
	rows, err := f.db.Query("SELECT key_schema, source_identity, translation_profile, translated_text FROM tm")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var got genericJSONTMRow
		if err := rows.Scan(&got.schema, &got.source, &got.profile, &got.text); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(want, got) {
			t.Fatalf("unexpected SQLite row: %+v", got)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != len(want) {
		t.Fatalf("SQLite row count=%d, want %d", count, len(want))
	}
}

func (f genericJSONContextFixture) replaceRow(t *testing.T, original, replacement genericJSONTMRow) {
	t.Helper()
	result, err := f.db.Exec(`UPDATE tm SET source_identity=?, translation_profile=?, translated_text=?
		WHERE key_schema=? AND source_identity=? AND translation_profile=?`,
		[]byte(replacement.source), []byte(replacement.profile), replacement.text,
		original.schema, []byte(original.source), []byte(original.profile))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("updated %d exact rows: %v", n, err)
	}
}

func replaceGenericJSONKeyPart(t *testing.T, key, old, replacement string) string {
	t.Helper()
	if strings.Count(key, old) != 1 || old == replacement {
		t.Fatalf("expected one changed key component %q", old)
	}
	return strings.Replace(key, old, replacement, 1)
}

func TestGenericJSONContextTM(t *testing.T) {
	f := newGenericJSONContextFixture(t)
	generated, hits, firstMiss := [2]string{"generated", "generated"}, [2]string{"tm", "tm"}, [2]string{"generated", "tm"}
	first, second := f.row(t, 0, genericJSONLines[0]), f.row(t, 1, genericJSONLines[1])
	rows := []genericJSONTMRow{first, second}
	out, observed := f.run(t, readGenericJSONPack(t, f.packPath), genericJSONLines, generated)
	if observed.TMValidation != (ValidationCounts{}) {
		t.Fatalf("cold run unexpectedly found a candidate: %+v", observed.TMValidation)
	}
	f.assertRows(t, rows...)

	t.Run("exact-reuse", func(t *testing.T) {
		rerun, observed := f.run(t, readGenericJSONPack(t, f.packPath), genericJSONLines, hits)
		if observed.TMValidation != (ValidationCounts{Attempted: 2, Passed: 2}) || !bytes.Equal(rerun, out) {
			t.Fatalf("exact reuse changed output or missed revalidation: %+v", observed.TMValidation)
		}
		f.assertRows(t, rows...)
	})

	t.Run("unused-context", func(t *testing.T) {
		edited := f.editedPack(t, func(p *contextpack.Pack) {
			p.Characters[slices.IndexFunc(p.Characters, func(c contextpack.Character) bool { return c.ID == "keeper" })].Provenance.Version = "edited-metadata"
		})
		f.run(t, edited, genericJSONLines, hits)
		f.assertRows(t, rows...)
	})

	for _, change := range []struct {
		name string
		line genericJSONContextLine
		edit func(*contextpack.Pack)
	}{
		{"speaker", genericJSONContextLine{"Host", genericJSONLines[0].scene, "  亭主・宿のカフェ {name}\r\n"}, func(p *contextpack.Pack) {
			p.Characters[slices.IndexFunc(p.Characters, func(c contextpack.Character) bool { return c.ID == "keeper" })].Name = new("Host")
		}},
		{"scene", genericJSONContextLine{genericJSONLines[0].speaker, "Guests cross the courtyard.", "  店主・中庭のカフェ {name}\r\n"}, func(p *contextpack.Pack) {
			p.Scenes[slices.IndexFunc(p.Scenes, func(s contextpack.Scene) bool { return s.ID == "inn" })].Summary = new("Guests cross the courtyard.")
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			want := [2]genericJSONContextLine{change.line, genericJSONLines[1]}
			f.run(t, f.editedPack(t, change.edit), want, firstMiss)
			rows = append(rows, f.row(t, 0, change.line))
			f.assertRows(t, rows...)
		})
	}

	t.Run("source-and-selection-identity", func(t *testing.T) {
		var profile struct{ Context identity.ContextInput }
		if err := json.Unmarshal([]byte(first.profile), &profile); err != nil {
			t.Fatal(err)
		}
		selection := string(profile.Context.Additional)
		encode := base64.StdEncoding.EncodeToString
		for _, tc := range []struct {
			name, field, original, alternate string
		}{
			{"game-namespace", "game_namespace", genericJSONScope.GameNamespace, "other-game"},
			{"artifact-namespace", "artifact_namespace", genericJSONScope.ArtifactNamespace, "other-artifact"},
			{"format-schema", "format_schema", "generic-json-v1", "generic-json-v0"},
			{"selection-version", "", "yakuori.context-selection/v1", "yakuori.context-selection/v0"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				alternate := first
				if tc.field == "" {
					changed := replaceGenericJSONKeyPart(t, selection, tc.original, tc.alternate)
					alternate.profile = replaceGenericJSONKeyPart(t, first.profile, encode([]byte(selection)), encode([]byte(changed)))
				} else {
					prefix := `"` + tc.field + `":"`
					alternate.source = replaceGenericJSONKeyPart(t, first.source, prefix+tc.original+`"`, prefix+tc.alternate+`"`)
				}
				f.replaceRow(t, first, alternate)
				f.assertRows(t, append(slices.Clone(rows[1:]), alternate)...)
				f.run(t, readGenericJSONPack(t, f.packPath), genericJSONLines, firstMiss)
				rows = append(rows, alternate)
				f.assertRows(t, rows...)
			})
		}
	})

	t.Run("revalidate-current-unit", func(t *testing.T) {
		bad := first
		bad.text = strings.Replace(first.text, "{name}", "", 1)
		f.replaceRow(t, first, bad)
		f.assertRows(t, append(slices.Clone(rows[1:]), bad)...)
		_, observed := f.run(t, readGenericJSONPack(t, f.packPath), genericJSONLines, firstMiss)
		if observed.TMValidation != (ValidationCounts{Attempted: 2, Passed: 1, Failed: 1}) {
			t.Fatalf("invalid exact candidate was not rejected by validation: %+v", observed.TMValidation)
		}
		f.assertRows(t, rows...)
	})
}

type tamperedGenericJSONStage struct {
	*genericjson.Adapter
	observed artifact.FinalObservation
}

func (a *tamperedGenericJSONStage) Export(writer io.Writer, session unit.Session, profile validate.Profile, accepted []validate.AcceptedTranslation) (artifact.Manifest, error) {
	var stage bytes.Buffer
	manifest, err := a.Adapter.Export(&stage, session, profile, accepted)
	if err != nil {
		return manifest, err
	}
	_, err = writer.Write(bytes.Replace(stage.Bytes(), []byte("亭主"), []byte("誤訳"), 1))
	return manifest, err
}

func (a *tamperedGenericJSONStage) Observe(reader io.Reader) (artifact.FinalObservation, error) {
	var err error
	a.observed, err = a.Adapter.Observe(reader)
	return a.observed, err
}

func TestGenericJSONContextTMPrecommitFailure(t *testing.T) {
	for _, phase := range []string{"generate", "validate", "stage"} {
		t.Run(phase, func(t *testing.T) {
			f := newGenericJSONContextFixture(t)
			f.run(t, readGenericJSONPack(t, f.packPath), genericJSONLines, [2]string{"generated", "generated"})
			baseline := []genericJSONTMRow{f.row(t, 0, genericJSONLines[0]), f.row(t, 1, genericJSONLines[1])}
			f.assertRows(t, baseline...)
			edited := f.editedPack(t, func(p *contextpack.Pack) {
				for i, c := range p.Characters {
					switch c.ID {
					case "keeper":
						p.Characters[i].Name = new("Host")
					case "guard":
						p.Characters[i].Name = new("Innkeeper")
					}
				}
			})
			var adapter Adapter = newGenericJSONAdapter(t)
			var stage *tamperedGenericJSONStage
			injected := errors.New("fixture inference failure")
			want := error(injected)
			wantValidation := ValidationCounts{Attempted: 1, Passed: 1}
			switch phase {
			case "validate":
				want, wantValidation = validate.ErrInvalidCandidate, ValidationCounts{Attempted: 2, Passed: 1, Failed: 1}
			case "stage":
				stage = &tamperedGenericJSONStage{Adapter: newGenericJSONAdapter(t)}
				adapter, want = stage, artifact.ErrFinalMismatch
				wantValidation = ValidationCounts{Attempted: 2, Passed: 2}
			}
			finish := f.h.finish
			f.h.requests = nil
			f.h.finish = func(ctx context.Context, req inference.GenerationRequest, result inference.GenerationResult) (inference.GenerationResult, error) {
				if len(f.h.requests) == 2 && phase == "generate" {
					return inference.GenerationResult{}, injected
				}
				result, err := finish(ctx, req, result)
				if len(f.h.requests) == 2 && phase == "validate" {
					result.Text = strings.Replace(result.Text, genericJSONTargetTerm, "珈琲店", 1)
				}
				return result, err
			}
			options := f.options(t)
			obs := &AcceptanceObservation{SourceSHA256: f.session.ArtifactDigest()}
			result, err := f.h.core.WithAcceptanceObservation(obs).FileWithTMContext(t.Context(), options, adapter,
				f.h.profile, f.store, genericJSONScope, edited)
			var failure *Error
			if !errors.Is(err, want) || !errors.As(err, &failure) || failure.Phase != phase || result.TMCommitted ||
				result.Publication.State != publication.NotPublished || len(f.h.requests) != 2 || obs.FinalValidated ||
				obs.Validation != wantValidation || obs.TMValidation != (ValidationCounts{}) ||
				len(obs.Units) == 0 || obs.Units[0].ID != "001" || obs.Units[0].Kind != "generated" {
				t.Fatalf("result=%+v error=%v Generate=%d observation=%+v", result, err, len(f.h.requests), obs)
			}
			if phase != "stage" && failure.UnitID.StableID() != "002" {
				t.Fatalf("failed unit=%s, want 002", failure.UnitID.StableID())
			}
			if stage != nil {
				var compare *Error
				if !errors.As(failure.Err, &compare) || compare.Phase != "compare_final" ||
					len(stage.observed.Units) != 2 || stage.observed.Units[0].Text != "  誤訳・宿のカフェ {name}\r\n" ||
					stage.observed.Units[1].Text != "  店主・門のカフェ {name}\r\n" {
					t.Fatalf("expected real Observe of one altered stage translation before CompareFinal: %+v, error=%v", stage.observed.Units, err)
				}
			}
			if _, err := os.Lstat(options.Output); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed run produced output: %v", err)
			}
			if !bytes.Equal(fileBytes(t, f.source), f.raw) {
				t.Fatal("failed run changed input JSON")
			}
			f.assertRows(t, baseline...)
		})
	}
}
