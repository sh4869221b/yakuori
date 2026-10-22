//go:build linux

package localize

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"testing"
	"time"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

type keyRelationAdapter struct {
	*fakeAdapter
	rawKeys    []string
	unitIndex  int
	expected   *unit.Session
	err        error
	imports    int
	keyCalls   int
	sameImport bool
	returned   []artifact.KeyRelation
}

func (a *keyRelationAdapter) Import(raw []byte, profile validate.Profile) (unit.Session, error) {
	a.imports++
	return a.fakeAdapter.Import(raw, profile)
}

func (a *keyRelationAdapter) KeyRelations(session unit.Session) ([]artifact.KeyRelation, error) {
	a.keyCalls++
	expected := a.session
	if a.expected != nil {
		expected = *a.expected
	}
	a.sameImport = expected.SameImport(session)
	if !a.sameImport {
		return nil, unit.ErrInvalidSession
	}
	if a.err != nil {
		return nil, a.err
	}
	id := session.Units()[a.unitIndex].ID()
	relations := make([]artifact.KeyRelation, 0, len(a.rawKeys))
	for _, key := range a.rawKeys {
		relations = append(relations, artifact.KeyRelation{UnitID: id, Key: key})
	}
	a.returned = relations
	return relations, nil
}

type keyGenerationState struct {
	calls   int
	planned bool
	onPlan  func()
}

type keyFixture struct {
	core    Core
	adapter *keyRelationAdapter
	profile validate.Profile
	input   []byte
	state   *keyGenerationState
}

func keyInput(t *testing.T, rawKeys []string) []byte {
	t.Helper()
	input, err := json.Marshal(fakeImage{
		Adapter: "fixture", Schema: "v1", Language: "en",
		Records: []fakeRecord{{"a", "Hello"}, {"b", "World"}},
		Keys:    rawKeys, Header: []byte{7}, Length: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func newKeyFixture(t *testing.T, rawKeys []string) keyFixture {
	t.Helper()
	var unused int
	_, profile := canonicalCoreFixture(t, false, "", &unused)
	state := &keyGenerationState{}
	fixture := keyFixture{
		adapter: &keyRelationAdapter{fakeAdapter: &fakeAdapter{t: t, tm: &fakeTM{}}, rawKeys: rawKeys},
		profile: profile,
		input:   keyInput(t, rawKeys),
		state:   state,
	}
	fixture.core = NewCore(fakeEngine(func(_ context.Context, _ unit.UnitID, text string) (Generation, error) {
		state.calls++
		return Generation{Text: "訳" + text, Finish: Stop}, nil
	}))
	return fixture
}

func newKeyPipelineFixture(t *testing.T, rawKeys []string) (keyFixture, *keyGenerationState) {
	t.Helper()
	info := inference.ModelInfo{
		ModelSHA256: [32]byte{1}, Backend: "cpu", BackendPin: "goinfer-v1", ComputeQuant: "int4", ContextTokens: 7,
		Tokenizer:    inference.TokenizerIdentity{ModelSHA256: [32]byte{1}, BackendPin: "goinfer-v1"},
		Template:     inference.TemplateIdentity{Family: "chatml", Source: "embedded", RendererVersion: "v1"},
		PolicySchema: inference.PolicySchemaV1, Prompt: prompt.PlainIdentity(), Thinking: inference.ThinkingDisabled,
		Prefill: inference.PrefillPolicy{Path: "cpu", Fast: "off"},
	}
	request := func(text string) (inference.GenerationRequest, error) {
		policy, err := inference.NewGenerationPolicy(1, 2, time.Second)
		if err != nil {
			return inference.GenerationRequest{}, err
		}
		return inference.NewGenerationRequest(inference.PreparedRequest{RenderedPrompt: text, TokenIDs: make([]int, len(text)), StopIDs: []int{42}, Identity: inference.RequestIdentity{
			ModelSHA256: info.ModelSHA256, Tokenizer: info.Tokenizer, Template: info.Template,
			PromptSchema: info.Prompt.Schema, Prompt: info.Prompt, Thinking: info.Thinking,
		}}, policy)
	}
	base, err := request("one")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewCanonicalInferenceProfile(info, base, InferenceProfileInput{ModelID: "model", Glossary: identity.GlossaryInput{Mode: "none"}, Limits: config.DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	state := &keyGenerationState{}
	backend := segmentedBackend{info: info}
	backend.commonEngine = commonEngine{
		count: func(_ context.Context, r inference.GenerationRequest) (int, error) { return len(r.TokenIDs()), nil },
		generate: func(_ context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
			state.calls++
			return inference.GenerationResult{Text: "訳" + r.RenderedPrompt(), Finish: inference.Stop, PromptTokens: len(r.TokenIDs())}, nil
		},
	}
	build := func(_ context.Context, _ unit.UnitID, text string) (inference.GenerationRequest, error) {
		if !state.planned {
			state.planned = true
			if state.onPlan != nil {
				state.onPlan()
			}
		}
		return request(text)
	}
	fixture := keyFixture{
		adapter: &keyRelationAdapter{fakeAdapter: &fakeAdapter{t: t, tm: &fakeTM{}}, rawKeys: rawKeys},
		profile: profile,
		input:   keyInput(t, rawKeys),
		state:   state,
	}
	fixture.core = NewCore(NewInferenceEngine(backend, build))
	return fixture, state
}

func keyUnitID(t *testing.T, stableID string) unit.UnitID {
	t.Helper()
	id, err := unit.NewUnitID("fixture", "v1", stableID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func rowIdentity(schema int, source, profile []byte) string {
	return fmt.Sprintf("%d|%x|%x", schema, source, profile)
}

func readTMColumns(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT key_schema, source_identity, translation_profile, translated_text FROM tm`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	actual := make(map[string]string)
	for rows.Next() {
		var schema int
		var source, profile []byte
		var text string
		if err := rows.Scan(&schema, &source, &profile, &text); err != nil {
			t.Fatal(err)
		}
		actual[rowIdentity(schema, source, profile)] = text
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return actual
}

func expectedTMRows(t *testing.T, session unit.Session, profile validate.Profile, scope tm.Scope, texts map[string]string) map[string]string {
	t.Helper()
	byID := make(map[unit.UnitID][][]byte)
	for _, relation := range scope.Keys {
		byID[relation.UnitID] = append(byID[relation.UnitID], []byte(relation.Key))
	}
	expected := make(map[string]string, len(texts))
	for _, u := range session.Units() {
		source, err := unit.NewSourceIdentity(session, u.ID(), scope.GameNamespace, scope.ArtifactNamespace, byID[u.ID()])
		if err != nil {
			t.Fatal(err)
		}
		key, err := profile.ExactKey(source)
		if err != nil {
			t.Fatal(err)
		}
		expected[rowIdentity(key.Schema(), key.SourceBytes(), key.ProfileBytes())] = texts[u.ID().StableID()]
	}
	return expected
}

func seedImportRow(t *testing.T, db *sql.DB, session unit.Session, profile validate.Profile, scope tm.Scope, index int, keys []string, text string) {
	t.Helper()
	rawKeys := make([][]byte, len(keys))
	for i, key := range keys {
		rawKeys[i] = []byte(key)
	}
	source, err := unit.NewSourceIdentity(session, session.Units()[index].ID(), scope.GameNamespace, scope.ArtifactNamespace, rawKeys)
	if err != nil {
		t.Fatal(err)
	}
	key, err := profile.ExactKey(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tm VALUES(?,?,?,?)`, key.Schema(), key.SourceBytes(), key.ProfileBytes(), text); err != nil {
		t.Fatal(err)
	}
}

func keyInputSession(t *testing.T, raw []byte) unit.Session {
	t.Helper()
	var image fakeImage
	if err := json.Unmarshal(raw, &image); err != nil {
		t.Fatal(err)
	}
	units := make([]unit.TranslationUnit, len(image.Records))
	for i, record := range image.Records {
		id, err := unit.NewUnitID(image.Adapter, image.Schema, record.ID)
		if err != nil {
			t.Fatal(err)
		}
		units[i], err = unit.NewTranslationUnit(id, []byte(record.Text), image.Language, "ja", nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	session, err := unit.NewSession(raw, units)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func assertImageTexts(t *testing.T, raw []byte) {
	t.Helper()
	var image fakeImage
	if err := json.Unmarshal(raw, &image); err != nil {
		t.Fatal(err)
	}
	if len(image.Records) != 2 || image.Records[0].Text != "訳Hello" || image.Records[1].Text != "訳World" {
		t.Fatalf("image=%+v", image)
	}
}

func assertRows(t *testing.T, db *sql.DB, want map[string]string) map[string]string {
	t.Helper()
	actual := readTMColumns(t, db)
	if !maps.Equal(actual, want) {
		t.Fatalf("database rows=%v want=%v", actual, want)
	}
	t.Logf("key_schema|source_identity|translation_profile|translated_text rows=%v", actual)
	return actual
}

func keyFailureFixture(t *testing.T, rawKeys []string) (*tm.Store, *sql.DB, keyFixture, map[string]string) {
	t.Helper()
	store, db := tmStoreFixture(t)
	f := newKeyFixture(t, rawKeys)
	seedImportRow(t, db, keyInputSession(t, f.input), f.profile, tmScope(), 0, nil, "unchanged")
	return store, db, f, readTMColumns(t, db)
}

func assertKeyFailure(t *testing.T, f keyFixture, store *tm.Store, db *sql.DB, scope tm.Scope, before map[string]string, cause error) {
	t.Helper()
	var output bytes.Buffer
	result, err := f.core.TextWithTM(context.Background(), f.input, f.adapter, f.profile, store, scope, &output)
	var localizeErr *Error
	if !errors.As(err, &localizeErr) || localizeErr.Phase != "tm_begin" || !errors.Is(err, cause) {
		t.Fatalf("error=%v", err)
	}
	if result.TMCommitted || output.Len() != 0 || f.state.calls != 0 || f.adapter.keyCalls != 1 || f.adapter.imports != 1 {
		t.Fatalf("result=%+v output=%q generate=%d key calls=%d imports=%d", result, output.String(), f.state.calls, f.adapter.keyCalls, f.adapter.imports)
	}
	if after := readTMColumns(t, db); !maps.Equal(after, before) {
		t.Fatalf("failure wrote rows: before=%v after=%v", before, after)
	}
	t.Logf("phase=%s cause=%v key calls=%d imports=%d; %d rows unchanged: %v", localizeErr.Phase, cause, f.adapter.keyCalls, f.adapter.imports, len(before), before)
}

func TestTMImportKeys(t *testing.T) {
	t.Run("automatic", func(t *testing.T) {
		store, db := tmStoreFixture(t)
		f := newKeyFixture(t, []string{"K0", "K1", "K0"})
		scope := tmScope()
		var output bytes.Buffer
		result, err := f.core.TextWithTM(context.Background(), f.input, f.adapter, f.profile, store, scope, &output)
		if err != nil || !result.TMCommitted {
			t.Fatalf("error=%v result=%+v output=%q", err, result, output.String())
		}
		if f.state.calls != 2 {
			t.Fatalf("generation calls=%d, want 2", f.state.calls)
		}
		t.Logf("generation calls=%d observed through shared fixture state", f.state.calls)
		assertImageTexts(t, output.Bytes())
		if f.adapter.imports != 1 || f.adapter.keyCalls != 1 || !f.adapter.sameImport {
			t.Fatalf("imports=%d key calls=%d same import=%v", f.adapter.imports, f.adapter.keyCalls, f.adapter.sameImport)
		}
		session := f.adapter.session
		clone, err := unit.NewSession(session.Artifact(), session.Units())
		if err != nil || session.SameImport(clone) {
			t.Fatalf("identical bytes collapsed SameImport: %v", err)
		}
		bound := scope
		bound.Keys = f.adapter.returned
		actual := assertRows(t, db, expectedTMRows(t, session, f.profile, bound, map[string]string{"a": "訳Hello", "b": "訳World"}))
		noKeys, err := unit.NewSourceIdentity(session, keyUnitID(t, "a"), scope.GameNamespace, scope.ArtifactNamespace, nil)
		if err != nil {
			t.Fatal(err)
		}
		key, err := f.profile.ExactKey(noKeys)
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := actual[rowIdentity(key.Schema(), key.SourceBytes(), key.ProfileBytes())]; exists {
			t.Fatal("keyed import was stored under the keyless identity")
		}
	})

	t.Run("explicit_reordered", func(t *testing.T) {
		store, db := tmStoreFixture(t)
		f := newKeyFixture(t, []string{"K0", "K1", "K0"})
		id := keyUnitID(t, "a")
		scope := tmScope()
		scope.Keys = []artifact.KeyRelation{{UnitID: id, Key: "K0"}, {UnitID: id, Key: "K0"}, {UnitID: id, Key: "K1"}}
		dir := t.TempDir()
		options := publication.Options{Source: filepath.Join(dir, "source"), Output: filepath.Join(dir, "output"), Mode: publication.Create}
		writeFile(t, options.Source, f.input)
		result, err := f.core.FileWithTM(context.Background(), options, f.adapter, f.profile, store, scope)
		if err != nil || !result.TMCommitted || result.Publication.State != publication.Published {
			t.Fatalf("error=%v result=%+v", err, result)
		}
		if f.adapter.imports != 1 || f.adapter.keyCalls != 1 || !f.adapter.sameImport {
			t.Fatalf("imports=%d key calls=%d same import=%v", f.adapter.imports, f.adapter.keyCalls, f.adapter.sameImport)
		}
		assertImageTexts(t, fileBytes(t, options.Output))
		assertRows(t, db, expectedTMRows(t, f.adapter.session, f.profile, scope, map[string]string{"a": "訳Hello", "b": "訳World"}))
	})

	t.Run("explicit_empty_keyless", func(t *testing.T) {
		store, db := tmStoreFixture(t)
		f := newKeyFixture(t, nil)
		scope := tmScope()
		scope.Keys = []artifact.KeyRelation{}
		var output bytes.Buffer
		result, err := f.core.TextWithTM(context.Background(), f.input, f.adapter, f.profile, store, scope, &output)
		if err != nil || !result.TMCommitted {
			t.Fatalf("error=%v result=%+v output=%q", err, result, output.String())
		}
		assertImageTexts(t, output.Bytes())
		if f.adapter.imports != 1 || f.adapter.keyCalls != 1 || !f.adapter.sameImport {
			t.Fatalf("imports=%d key calls=%d same import=%v", f.adapter.imports, f.adapter.keyCalls, f.adapter.sameImport)
		}
		assertRows(t, db, expectedTMRows(t, f.adapter.session, f.profile, scope, map[string]string{"a": "訳Hello", "b": "訳World"}))
	})

	t.Run("providerless", func(t *testing.T) {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("explicit_%v", explicit), func(t *testing.T) {
				store, db := tmStoreFixture(t)
				var unused, calls int
				_, profile := canonicalCoreFixture(t, false, "", &unused)
				core := NewCore(fakeEngine(func(_ context.Context, _ unit.UnitID, text string) (Generation, error) {
					calls++
					return Generation{Text: "訳" + text, Finish: Stop}, nil
				}))
				scope := tmScope()
				if explicit {
					scope.Keys = []artifact.KeyRelation{}
				}
				var output bytes.Buffer
				result, err := core.TextWithTM(context.Background(), []byte("Hello"), textadapter.New("en", "ja"), profile, store, scope, &output)
				if err != nil || !result.TMCommitted || output.String() != "訳Hello" || calls != 1 {
					t.Fatalf("error=%v result=%+v output=%q calls=%d", err, result, output.String(), calls)
				}
				id, err := unit.NewUnitID("text", "text-v1", "document")
				if err != nil {
					t.Fatal(err)
				}
				u, err := unit.NewTranslationUnit(id, []byte("Hello"), "en", "ja", nil)
				if err != nil {
					t.Fatal(err)
				}
				session, err := unit.NewSession([]byte("Hello"), []unit.TranslationUnit{u})
				if err != nil {
					t.Fatal(err)
				}
				assertRows(t, db, expectedTMRows(t, session, profile, scope, map[string]string{"document": "訳Hello"}))
			})
		}
	})

	t.Run("snapshot", func(t *testing.T) {
		opaque := []string{string([]byte{0, 0, 0, 0}), string([]byte{1, 2, 3, 4}), string([]byte{1, 2, 3, 4})}
		idA, idB := keyUnitID(t, "a"), keyUnitID(t, "b")
		original := func() []artifact.KeyRelation {
			return []artifact.KeyRelation{{UnitID: idA, Key: opaque[0]}, {UnitID: idA, Key: opaque[1]}, {UnitID: idA, Key: opaque[2]}}
		}
		t.Run("returned", func(t *testing.T) {
			store, db := tmStoreFixture(t)
			f, state := newKeyPipelineFixture(t, opaque)
			mutated := false
			f.state.onPlan = func() {
				mutated = true
				f.adapter.returned[0] = artifact.KeyRelation{UnitID: idB, Key: "mutated"}
			}
			scope := tmScope()
			var output bytes.Buffer
			result, err := f.core.TextWithTM(context.Background(), f.input, f.adapter, f.profile, store, scope, &output)
			if err != nil || !result.TMCommitted {
				t.Fatalf("error=%v result=%+v output=%q", err, result, output.String())
			}
			if !mutated || !state.planned || state.calls != 2 || f.adapter.returned[0].Key != "mutated" {
				t.Fatalf("plan mutation=%v planned=%v calls=%d returned=%v", mutated, state.planned, state.calls, f.adapter.returned)
			}
			if f.adapter.imports != 1 || f.adapter.keyCalls != 1 || !f.adapter.sameImport {
				t.Fatalf("imports=%d key calls=%d same import=%v", f.adapter.imports, f.adapter.keyCalls, f.adapter.sameImport)
			}
			assertImageTexts(t, output.Bytes())
			bound := scope
			bound.Keys = original()
			assertRows(t, db, expectedTMRows(t, f.adapter.session, f.profile, bound, map[string]string{"a": "訳Hello", "b": "訳World"}))
			t.Logf("planning-callback provider mutation left original opaque/hash0/duplicate relations; calls=%d", state.calls)
		})
		t.Run("caller", func(t *testing.T) {
			store, db := tmStoreFixture(t)
			f, state := newKeyPipelineFixture(t, opaque)
			caller := original()
			mutated := false
			f.state.onPlan = func() {
				mutated = true
				caller[0].Key = "mutated"
				f.adapter.returned[0].Key = "mutated"
			}
			scope := tmScope()
			scope.Keys = caller
			var output bytes.Buffer
			result, err := f.core.TextWithTM(context.Background(), f.input, f.adapter, f.profile, store, scope, &output)
			if err != nil || !result.TMCommitted {
				t.Fatalf("error=%v result=%+v output=%q", err, result, output.String())
			}
			if !mutated || !state.planned || state.calls != 2 || caller[0].Key != "mutated" || f.adapter.returned[0].Key != "mutated" {
				t.Fatalf("plan mutation=%v planned=%v calls=%d caller=%v returned=%v", mutated, state.planned, state.calls, caller, f.adapter.returned)
			}
			if f.adapter.imports != 1 || f.adapter.keyCalls != 1 || !f.adapter.sameImport {
				t.Fatalf("imports=%d key calls=%d same import=%v", f.adapter.imports, f.adapter.keyCalls, f.adapter.sameImport)
			}
			assertImageTexts(t, output.Bytes())
			bound := tmScope()
			bound.Keys = original()
			assertRows(t, db, expectedTMRows(t, f.adapter.session, f.profile, bound, map[string]string{"a": "訳Hello", "b": "訳World"}))
			t.Logf("planning-callback caller/provider mutation left original relations; calls=%d", state.calls)
		})
	})

	t.Run("explicit_conflict", func(t *testing.T) {
		idA, idB := keyUnitID(t, "a"), keyUnitID(t, "b")
		for _, variant := range []string{"empty", "missing", "extra", "changed", "unit_id", "multiplicity"} {
			t.Run(variant, func(t *testing.T) {
				store, db, f, before := keyFailureFixture(t, []string{"K0", "K0", "K1"})
				scope := tmScope()
				switch variant {
				case "empty":
					scope.Keys = []artifact.KeyRelation{}
				case "missing":
					scope.Keys = []artifact.KeyRelation{{UnitID: idA, Key: "K0"}, {UnitID: idA, Key: "K1"}}
				case "extra":
					scope.Keys = []artifact.KeyRelation{{UnitID: idA, Key: "K0"}, {UnitID: idA, Key: "K0"}, {UnitID: idA, Key: "K1"}, {UnitID: idA, Key: "K2"}}
				case "changed":
					scope.Keys = []artifact.KeyRelation{{UnitID: idA, Key: "K0"}, {UnitID: idA, Key: "K0"}, {UnitID: idA, Key: "KX"}}
				case "unit_id":
					scope.Keys = []artifact.KeyRelation{{UnitID: idA, Key: "K0"}, {UnitID: idB, Key: "K0"}, {UnitID: idA, Key: "K1"}}
				case "multiplicity":
					scope.Keys = []artifact.KeyRelation{{UnitID: idA, Key: "K0"}, {UnitID: idA, Key: "K1"}, {UnitID: idA, Key: "K1"}}
				}
				assertKeyFailure(t, f, store, db, scope, before, tm.ErrInvalidScope)
			})
		}
	})

	t.Run("provider_error", func(t *testing.T) {
		store, db, f, before := keyFailureFixture(t, []string{"K0"})
		injected := errors.New("key provider failed")
		f.adapter.err = injected
		assertKeyFailure(t, f, store, db, tmScope(), before, injected)
	})

	t.Run("foreign_session", func(t *testing.T) {
		store, db, f, before := keyFailureFixture(t, []string{"K0"})
		other := &fakeAdapter{t: t, tm: &fakeTM{}}
		foreign, err := other.Import(f.input, f.profile)
		if err != nil {
			t.Fatal(err)
		}
		if foreign.SameImport(keyInputSession(t, f.input)) {
			t.Fatal("identical bytes collapsed SameImport")
		}
		f.adapter.expected = &foreign
		assertKeyFailure(t, f, store, db, tmScope(), before, unit.ErrInvalidSession)
		if f.adapter.sameImport {
			t.Fatal("provider accepted the current import as its foreign session")
		}
	})
}
