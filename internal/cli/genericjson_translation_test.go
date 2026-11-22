package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/adapter/genericjson"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

var genericJSONMetadata = genericjson.Metadata{
	SourceLanguage: "en", TargetLanguage: "ja", GameNamespace: "sample-game", ArtifactNamespace: "dialogue",
}

func genericJSONFixture(t *testing.T) ([]byte, *genericjson.Adapter) {
	t.Helper()
	raw, err := os.ReadFile("../adapter/genericjson/testdata/minimal-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := genericjson.New(genericJSONMetadata, config.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return raw, adapter
}

type genericJSONEngine struct {
	*translationEngine
	rejectSecond bool
}

func (e *genericJSONEngine) open(ctx context.Context, backend string) (string, inference.Engine, error) {
	model, _, err := e.translationEngine.open(ctx, backend)
	return model, e, err
}

func (e *genericJSONEngine) Generate(_ context.Context, request inference.GenerationRequest) (inference.GenerationResult, error) {
	label := []string{"喫茶店", "カフェ"}[e.generated]
	e.generated++
	// The model equivalent changes the payload while retaining the masked span.
	text := strings.Replace(request.RenderedPrompt(), "Cafe\u0301", label, 1)
	if e.rejectSecond && e.generated == 2 {
		text = "Translation:" + text
	}
	return inference.GenerationResult{Text: text, Finish: inference.Stop, PromptTokens: len(request.TokenIDs())}, nil
}

func TestGenericJSONCLIPipeline(t *testing.T) {
	raw, adapter := genericJSONFixture(t)
	dir := t.TempDir()
	if !translationPublicationSupported(t, dir) {
		t.Skip("requires qualified ext4/Btrfs TMPDIR")
	}
	command, paths, backend := translationFixtureAt(t, dir, string(raw))
	input := command.options.Source
	output := filepath.Join(dir, "translated.json")
	run := func(t *testing.T, engine *genericJSONEngine, input string, outputFlags ...string) (int, string, string) {
		t.Helper()
		args := []string{"localize", "--format", "generic-json", "--source-language", "en", "--target-language", "ja",
			"--game", "sample-game", "--artifact", "dialogue", "--output", output}
		args = append(args, outputFlags...)
		args = append(args, input)
		var stdout, stderr bytes.Buffer
		code := runWithTranslationOpener(t.Context(), args, &stdout, &stderr,
			func() (config.Paths, error) { return paths, nil }, nil, engine.open)
		return code, stdout.String(), stderr.String()
	}

	engine := &genericJSONEngine{translationEngine: backend}
	code, stdout, stderr := run(t, engine, input)
	if code != ExitOK || stdout != "" || stderr != "" || engine.generated != 2 || engine.closed != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q generations=%d closed=%d", code, stdout, stderr, engine.generated, engine.closed)
	}
	published, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	// Decode the real public file as an external consumer would. Compare
	// preserved data with the fixture and construct the translation map by ID.
	type jsonUnit struct {
		ID          string            `json:"id"`
		Translation *string           `json:"translation,omitempty"`
		Source      string            `json:"source"`
		Protection  []map[string]any  `json:"protection"`
		Context     map[string]string `json:"context"`
	}
	type jsonExchange struct {
		Schema         string            `json:"schema"`
		Version        int               `json:"version"`
		SourceLanguage string            `json:"source_language"`
		TargetLanguage string            `json:"target_language"`
		Scope          map[string]string `json:"scope"`
		Units          []jsonUnit        `json:"units"`
	}
	var original, translated jsonExchange
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(published, &translated); err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{"001": "  喫茶店 {name}\r\n", "002": "  カフェ {name}\r\n"}
	for i, u := range translated.Units {
		text, exists := expected[u.ID]
		if !exists || u.Translation == nil || *u.Translation != text {
			t.Fatalf("unexpected translation at record %d: %q", i, u.ID)
		}
		translated.Units[i].Translation = nil
	}
	if !reflect.DeepEqual(translated, original) {
		t.Fatalf("published JSON lost input data: got %+v, want %+v", translated, original)
	}
	rows := readTranslationTM(t, paths)
	if len(rows) != len(expected) {
		t.Fatalf("SQLite row count=%d, want %d", len(rows), len(expected))
	}
	profile := translationTestProfile(t, engine, "selected-model", "en", "ja")
	session, err := adapter.Import(raw, profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range session.Units() {
		identity, err := unit.NewSourceIdentity(session, u.ID(), "sample-game", "dialogue", nil)
		if err != nil {
			t.Fatal(err)
		}
		key, err := profile.ExactKey(identity)
		if err != nil {
			t.Fatal(err)
		}
		if !translationTMContains(rows, key, expected[u.ID().StableID()]) {
			t.Fatalf("SQLite is missing exact translation for ID %q", u.ID().StableID())
		}
	}
	t.Logf("published JSON bytes=%d, ordered IDs=001,002, SQLite exact rows=%d", len(published), len(rows))
	if current, err := os.ReadFile(input); err != nil || !bytes.Equal(current, raw) {
		t.Fatalf("successful publication changed source: %v", err)
	}

	// A distinct source artifact misses both existing exact TM entries.
	// The valid first generated unit must remain uncommitted on second-unit failure.
	failedInput := filepath.Join(dir, "rejected.json")
	otherRaw := append(slices.Clone(raw), '\n')
	if err := os.WriteFile(failedInput, otherRaw, 0600); err != nil {
		t.Fatal(err)
	}
	backend.generated, backend.closed = 0, 0
	engine.rejectSecond = true
	code, stdout, stderr = run(t, engine, failedInput, "--replace-output")
	if code != ExitFailure || stdout != "" || !strings.Contains(stderr, "localize validate failed for unit 002; TMCommitted=false; publication=not-published") ||
		engine.generated != 2 || engine.closed != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q generations=%d closed=%d", code, stdout, stderr, engine.generated, engine.closed)
	}
	if current, err := os.ReadFile(failedInput); err != nil || !bytes.Equal(current, otherRaw) {
		t.Fatalf("validation failure changed source: %v", err)
	}
	if current, err := os.ReadFile(output); err != nil || !bytes.Equal(current, published) {
		t.Fatalf("validation failure changed output: %v", err)
	}
	if after := readTranslationTM(t, paths); !reflect.DeepEqual(rows, after) {
		t.Fatalf("validation failure modified SQLite: rows before=%d after=%d", len(rows), len(after))
	}
	t.Logf("second-unit validation rejected after %d generations; output bytes and %d SQLite rows unchanged", engine.generated, len(rows))
}

type corruptGenericJSONStage struct{ *genericjson.Adapter }

func (a corruptGenericJSONStage) Export(w io.Writer, session unit.Session, profile validate.Profile, accepted []validate.AcceptedTranslation) (artifact.Manifest, error) {
	var raw bytes.Buffer
	manifest, err := a.Adapter.Export(&raw, session, profile, accepted)
	if err != nil {
		return artifact.Manifest{}, err
	}
	_, err = w.Write(bytes.Replace(raw.Bytes(), []byte(`"speaker":"keeper"`), []byte(`"speaker":"foreign"`), 1))
	return manifest, err
}

func TestGenericJSONCorruptedStageNeverCommits(t *testing.T) {
	raw, adapter := genericJSONFixture(t)
	dir := t.TempDir()
	if !translationPublicationSupported(t, dir) {
		t.Skip("requires qualified ext4/Btrfs TMPDIR")
	}
	command, paths, backend := translationFixtureAt(t, dir, string(raw))
	source := command.options.Source
	output := filepath.Join(dir, "translated.json")
	before := []byte("existing output")
	if err := os.WriteFile(output, before, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := tm.Create(t.Context(), config.ResolveStoragePaths(paths).TMFile, config.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	profile := translationTestProfile(t, backend, "selected-model", "en", "ja")
	generated := 0
	core := localize.NewCore(cliEngine(func(_ context.Context, _ unit.UnitID, text string) (localize.Generation, error) {
		generated++
		return localize.Generation{Text: strings.Replace(text, "Cafe\u0301", "翻訳", 1), Finish: localize.Stop}, nil
	}))
	result, err := core.FileWithTM(t.Context(), publication.Options{Source: source, Output: output, Mode: publication.Replace},
		corruptGenericJSONStage{adapter}, profile, store, tm.Scope{GameNamespace: "sample-game", ArtifactNamespace: "dialogue"})
	var stage, compare *localize.Error
	if !errors.Is(err, artifact.ErrFinalMismatch) || !errors.As(err, &stage) || stage.Phase != "stage" ||
		!errors.As(stage.Err, &compare) || compare.Phase != "compare_final" ||
		result.TMCommitted || result.Publication.State != publication.NotPublished || generated != 2 {
		t.Fatalf("result=%+v err=%v generated=%d", result, err, generated)
	}
	stages, err := filepath.Glob(filepath.Join(dir, ".yakuori-run-*", "stage"))
	if err != nil || len(stages) != 1 {
		t.Fatalf("stage paths: %v err=%v", stages, err)
	}
	actual, err := os.ReadFile(stages[0])
	if err != nil || !bytes.Contains(actual, []byte(`"speaker":"foreign"`)) {
		t.Fatalf("physical stage lacks mutated speaker: %v", err)
	}
	if current, err := os.ReadFile(output); err != nil || !bytes.Equal(current, before) {
		t.Fatalf("stage failure changed output: %v", err)
	}
	if current, err := os.ReadFile(source); err != nil || !bytes.Equal(current, raw) {
		t.Fatalf("stage failure changed input: %v", err)
	}
	if rows := readTranslationTM(t, paths); len(rows) != 0 {
		t.Fatalf("stage failure committed %d SQLite rows", len(rows))
	}
	t.Logf("reopened stage bytes=%d; valid JSON speaker mismatch rejected at CompareFinal; output unchanged; SQLite rows=0", len(actual))
}
