//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

type pipelineFixtureUnit struct {
	stableID    string
	source      string
	translation string
	keys        [][]byte
}

type pipelineFixtureInput struct {
	kind              string
	adapterName       string
	formatSchema      string
	gameNamespace     string
	artifactNamespace string
	language          string
	header            []byte
	suffix            []byte
	targetHeader      []byte
	source            []byte
	target            []byte
	units             []pipelineFixtureUnit
}

// Independently authored W3 vectors as fixed bytes: the en source fed to the
// real Import and the expected ja target artifact. These are separate from the
// fixture units table and are not derived from production Export/Core output.
const (
	pipelineW3SourceHex = "52545357a20000009743047a14327900000000060000007914327907000000000000007814327908000000060000006ce8cd860f0000000500000002040302017a143279efcdab896ce8cd8615e95e27bde87a63f54aea64d400000000f65e2dbdf67a63f541ea64d40000c62c75595bb222649cc800003951"
	pipelineW3TargetHex = "52545357a2000000835404af5582590000000002000000ac5582590300000000000000ad5582590400000002000000b9a97da607000000020000000204030201af558259efcdab89b9a97da60a310d074000000000310d0e400000310d457300009348"
)

func pipelineFixedVector(t *testing.T, encoded string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatalf("fixed vector is not valid hex: %v", err)
	}
	return raw
}

func pipelineFixtureTable(t *testing.T) []pipelineFixtureInput {
	t.Helper()
	w3 := pipelineFixtureInput{
		kind: "w3", adapterName: "witcher3-w3strings", formatSchema: "w3strings-v162-en-jp-v1",
		gameNamespace: "issue68", artifactNamespace: "w3", language: "en",
		header: []byte{'R', 'T', 'S', 'W', 162, 0, 0, 0, 0x97, 0x43}, suffix: []byte{0x39, 0x51},
		targetHeader: []byte{0x83, 0x54, 0x93, 0x48},
		source:       pipelineFixedVector(t, pipelineW3SourceHex),
		target:       pipelineFixedVector(t, pipelineW3TargetHex),
		units: []pipelineFixtureUnit{
			{stableID: "1001", source: "Hello.", translation: "訳一", keys: [][]byte{{0x04, 0x03, 0x02, 0x01}}},
			{stableID: "1002", source: "", translation: ""},
			{stableID: "1003", source: "World.", translation: "訳三"},
			{stableID: "4294967295", source: "Last.", translation: "訳終", keys: [][]byte{{0xef, 0xcd, 0xab, 0x89}}},
		},
	}
	return []pipelineFixtureInput{
		{
			kind: "text", adapterName: "text", formatSchema: "text-v1",
			gameNamespace: "issue68", artifactNamespace: "text", language: "en",
			source: []byte("Hello.\n"),
			target: []byte("訳文。\n"),
			units:  []pipelineFixtureUnit{{stableID: "document", source: "Hello.\n", translation: "訳文。\n"}},
		},
		w3,
	}
}

type pipelineBackend struct {
	info  inference.ModelInfo
	texts map[string]string
	calls []string
}

func (e *pipelineBackend) Info(ctx context.Context) (inference.ModelInfo, error) {
	return e.info, ctx.Err()
}

func (e *pipelineBackend) CountTokens(ctx context.Context, r inference.GenerationRequest) (int, error) {
	return len(r.TokenIDs()), ctx.Err()
}

func (e *pipelineBackend) Generate(ctx context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
	e.calls = append(e.calls, r.RenderedPrompt())
	text, found := e.texts[r.RenderedPrompt()]
	if !found || r.RenderedPrompt() == "" {
		return inference.GenerationResult{}, fmt.Errorf("%w: unexpected controlled request", inference.ErrInvalidPolicy)
	}
	return inference.GenerationResult{Text: text, Finish: inference.Stop, PromptTokens: len(r.TokenIDs()), OutputTokens: 2}, ctx.Err()
}

func (*pipelineBackend) Close() error { return nil }

func pipelineRequest(info inference.ModelInfo, text string) (inference.GenerationRequest, error) {
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 2, time.Second)
	if err != nil {
		return inference.GenerationRequest{}, err
	}
	return inference.NewGenerationRequest(inference.PreparedRequest{
		RenderedPrompt: text, Spans: []inference.PromptSpan{{Text: text}},
		TokenIDs: []int{1, 2, 3}, StopIDs: []int{42},
		Identity: inference.RequestIdentity{
			ModelSHA256: info.ModelSHA256, Tokenizer: info.Tokenizer, Template: info.Template,
			PromptSchema: info.Prompt.Schema, Prompt: info.Prompt, Thinking: info.Thinking,
		},
	}, policy)
}

type pipelineFixtureRow struct {
	Schema  int    `json:"key_schema"`
	Source  string `json:"source_identity_hex"`
	Profile string `json:"translation_profile_hex"`
	Text    string `json:"translated_text"`
}

type pipelineFixtureLimits struct {
	ArtifactBytes      int `json:"artifact_bytes"`
	ArtifactMax        int `json:"artifact_max"`
	Units              int `json:"units"`
	UnitsMax           int `json:"units_max"`
	UnitTextBytesMax   int `json:"unit_text_bytes_max"`
	UnitTextBytesLimit int `json:"unit_text_bytes_limit"`
	TotalSourceBytes   int `json:"total_source_bytes"`
	TotalTargetBytes   int `json:"total_target_bytes"`
	TotalTextBytesMax  int `json:"total_text_bytes_max"`
	Segments           int `json:"segments"`
	SegmentsMax        int `json:"segments_max"`
}

type pipelineFixture struct {
	input     pipelineFixtureInput
	limits    config.Limits
	adapter   Adapter
	backend   *pipelineBackend
	profile   validate.Profile
	core      Core
	store     *tm.Store
	db        *sql.DB
	scope     tm.Scope
	authored  unit.Session
	session   unit.Session
	relations []artifact.KeyRelation
	targets   map[string]string
	keys      map[string][][]byte
	d0        []pipelineFixtureRow
	stage     string
	record    string
	backup    string
}

func newPipelineFixture(t *testing.T, input pipelineFixtureInput) *pipelineFixture {
	t.Helper()
	f := &pipelineFixture{input: input, limits: config.DefaultLimits(), targets: map[string]string{}, keys: map[string][][]byte{}}
	f.scope = tm.Scope{GameNamespace: input.gameNamespace, ArtifactNamespace: input.artifactNamespace}
	f.backend = &pipelineBackend{texts: map[string]string{}, info: inference.ModelInfo{
		ModelSHA256: [32]byte{68}, Backend: "cpu", RequestedBackend: "cpu", BackendPin: "controlled-common-v1",
		ComputeQuant: "int4", ContextTokens: 4096, PolicySchema: inference.PolicySchemaV1,
		Tokenizer: inference.TokenizerIdentity{ModelSHA256: [32]byte{68}, BackendPin: "controlled-common-v1"},
		Template:  inference.TemplateIdentity{Family: "chatml", Source: "embedded", RendererVersion: "v1"},
		Prompt:    prompt.PlainIdentity(), Thinking: inference.ThinkingDisabled,
		Prefill: inference.PrefillPolicy{Path: "cpu", Fast: "off"},
	}}
	for _, u := range input.units {
		f.targets[u.stableID], f.keys[u.stableID] = u.translation, u.keys
		f.backend.texts[u.source] = u.translation
	}
	info := f.backend.info
	request, err := pipelineRequest(info, "")
	if err != nil {
		t.Fatal(err)
	}
	f.profile, err = NewCanonicalInferenceProfile(info, request, InferenceProfileInput{
		ModelID: "issue68-controlled", Glossary: identity.GlossaryInput{Mode: "none"}, Limits: f.limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.core, err = NewCoreWithLimits(NewInferenceEngine(f.backend, func(_ context.Context, _ unit.UnitID, text string) (inference.GenerationRequest, error) {
		return pipelineRequest(info, text)
	}), f.limits)
	if err != nil {
		t.Fatal(err)
	}
	switch input.kind {
	case "text":
		f.adapter = textadapter.New(input.language, "ja")
	case "w3":
		f.adapter, err = w3strings.NewForSource(input.language, f.limits)
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown fixture kind %q", input.kind)
	}
	f.session, err = f.adapter.Import(input.source, f.profile)
	if err != nil {
		t.Fatal(err)
	}
	f.authored = pipelineAuthoredSession(t, input)
	if provider, ok := f.adapter.(keyRelationProvider); ok {
		f.relations, err = provider.KeyRelations(f.session)
		if err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	f.stage, f.record, f.backup = filepath.Join(dir, "stage"), filepath.Join(dir, "record"), filepath.Join(dir, "backup")
	f.store, f.db = tmStoreFixture(t)
	f.d0 = f.seedUnrelated(t)
	return f
}

// pipelineAuthoredSession keeps expectation identity separate from the real adapter import.
func pipelineAuthoredSession(t *testing.T, input pipelineFixtureInput) unit.Session {
	t.Helper()
	units := make([]unit.TranslationUnit, len(input.units))
	for i, entry := range input.units {
		id, err := unit.NewUnitID(input.adapterName, input.formatSchema, entry.stableID)
		if err != nil {
			t.Fatal(err)
		}
		units[i], err = unit.NewTranslationUnit(id, []byte(entry.source), input.language, "ja", nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	session, err := unit.NewSession(input.source, units)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func (f *pipelineFixture) seedUnrelated(t *testing.T) []pipelineFixtureRow {
	t.Helper()
	adapter := textadapter.New("en", "ja")
	session, err := adapter.Import([]byte("Unrelated.\n"), f.profile)
	if err != nil {
		t.Fatal(err)
	}
	source, err := unit.NewSourceIdentity(session, session.Units()[0].ID(), f.scope.GameNamespace, "unrelated", nil)
	if err != nil {
		t.Fatal(err)
	}
	key, err := f.profile.ExactKey(source)
	if err != nil {
		t.Fatal(err)
	}
	row := pipelineFixtureRow{key.Schema(), hex.EncodeToString(key.SourceBytes()), hex.EncodeToString(key.ProfileBytes()), "無関係。\n"}
	if _, err := f.db.Exec("INSERT INTO tm VALUES(?,?,?,?)", key.Schema(), key.SourceBytes(), key.ProfileBytes(), row.Text); err != nil {
		t.Fatal(err)
	}
	d0 := pipelineFixtureReadRows(t, f.db)
	if !slices.Equal(d0, []pipelineFixtureRow{row}) {
		t.Fatalf("D0 readback rows=%d, want the single authored sentinel", len(d0))
	}
	return d0
}

func (f *pipelineFixture) verifyAuthoredInputs(t *testing.T, expected []pipelineFixtureUnit) error {
	t.Helper()
	if err := f.session.Check(); err != nil {
		return err
	}
	units := f.session.Units()
	if len(units) != len(expected) {
		return fmt.Errorf("unit count = %d, want %d", len(units), len(expected))
	}
	want := make([]artifact.KeyRelation, 0, len(expected))
	for i, entry := range expected {
		id, err := unit.NewUnitID(f.input.adapterName, f.input.formatSchema, entry.stableID)
		if err != nil {
			return err
		}
		u := units[i]
		if u.ID() != id {
			return fmt.Errorf("unit %d ID = %q, want authored %q", i, u.ID().StableID(), entry.stableID)
		}
		if string(u.Source()) != entry.source {
			return fmt.Errorf("unit %d source bytes differ from the authored table", i)
		}
		if u.SourceLanguage() != f.input.language || u.TargetLanguage() != "ja" {
			return fmt.Errorf("unit %d languages = %q/%q", i, u.SourceLanguage(), u.TargetLanguage())
		}
		for _, key := range entry.keys {
			want = append(want, artifact.KeyRelation{UnitID: id, Key: string(key)})
		}
	}
	if !bytes.Equal(f.session.Artifact(), f.input.source) {
		return fmt.Errorf("adapter-retained artifact differs from authored bytes")
	}
	if len(f.input.header) > 0 {
		if !bytes.HasPrefix(f.session.Artifact(), f.input.header) || !bytes.HasSuffix(f.session.Artifact(), f.input.suffix) {
			return fmt.Errorf("authored source header/suffix bytes differ")
		}
	}
	if !slices.Equal(f.relations, want) {
		return fmt.Errorf("real key relations = %d, want authored order of %d", len(f.relations), len(want))
	}
	return nil
}

func (f *pipelineFixture) assertWithinDefaultLimits(t *testing.T) pipelineFixtureLimits {
	t.Helper()
	units := f.session.Units()
	measured := pipelineFixtureLimits{
		ArtifactBytes: len(f.input.source), ArtifactMax: f.limits.ArtifactBytes,
		Units: len(units), UnitsMax: f.limits.Units,
		TotalTextBytesMax: f.limits.TotalTextBytes, Segments: len(units), SegmentsMax: f.limits.Segments,
		UnitTextBytesLimit: f.limits.UnitTextBytes,
	}
	for _, u := range units {
		n := u.SourceBytes()
		if err := f.limits.CheckUnitTextBytes(n); err != nil {
			t.Fatal(err)
		}
		measured.TotalSourceBytes += n
		measured.UnitTextBytesMax = max(measured.UnitTextBytesMax, n)
	}
	for _, u := range f.input.units {
		measured.TotalTargetBytes += len(u.translation)
	}
	for _, err := range []error{
		f.limits.CheckArtifactBytes(measured.ArtifactBytes),
		f.limits.CheckUnits(measured.Units),
		f.limits.CheckTotalTextBytes(measured.TotalSourceBytes),
		f.limits.CheckTotalTextBytes(measured.TotalTargetBytes),
		f.limits.CheckSegments(measured.Segments),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return measured
}

func (f *pipelineFixture) expectedRows(t *testing.T, units []pipelineFixtureUnit) []pipelineFixtureRow {
	t.Helper()
	rows := slices.Clone(f.d0)
	for _, entry := range units {
		id, err := unit.NewUnitID(f.input.adapterName, f.input.formatSchema, entry.stableID)
		if err != nil {
			t.Fatal(err)
		}
		source, err := unit.NewSourceIdentity(f.authored, id, f.scope.GameNamespace, f.scope.ArtifactNamespace, f.keys[entry.stableID])
		if err != nil {
			t.Fatal(err)
		}
		key, err := f.profile.ExactKey(source)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, pipelineFixtureRow{key.Schema(), hex.EncodeToString(key.SourceBytes()), hex.EncodeToString(key.ProfileBytes()), entry.translation})
	}
	slices.SortFunc(rows, func(a, b pipelineFixtureRow) int {
		if c := strings.Compare(a.Source, b.Source); c != 0 {
			return c
		}
		if c := strings.Compare(a.Profile, b.Profile); c != 0 {
			return c
		}
		return a.Schema - b.Schema
	})
	return rows
}

func pipelineFixtureReadRows(t *testing.T, db *sql.DB) []pipelineFixtureRow {
	t.Helper()
	rows, err := db.Query("SELECT key_schema, source_identity, translation_profile, translated_text FROM tm ORDER BY source_identity, translation_profile")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	var result []pipelineFixtureRow
	for rows.Next() {
		var schema int
		var source, profile []byte
		var text string
		if err := rows.Scan(&schema, &source, &profile, &text); err != nil {
			t.Fatal(err)
		}
		result = append(result, pipelineFixtureRow{schema, hex.EncodeToString(source), hex.EncodeToString(profile), text})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

// pipelineFixtureDiff reports no source or translated text, keeping diagnostics free of input.
func pipelineFixtureDiff(expected, actual []pipelineFixtureRow) []string {
	rowKey := func(r pipelineFixtureRow) string { return strconv.Itoa(r.Schema) + "/" + r.Source + "/" + r.Profile }
	want := make(map[string]string, len(expected))
	for _, row := range expected {
		want[rowKey(row)] = row.Text
	}
	var diff []string
	for _, row := range actual {
		key := rowKey(row)
		text, exists := want[key]
		switch {
		case !exists:
			diff = append(diff, "unexpected row "+key)
		case text != row.Text:
			diff = append(diff, "translated text differs for "+key)
		}
		delete(want, key)
	}
	for key := range want {
		diff = append(diff, "missing row "+key)
	}
	slices.Sort(diff)
	return diff
}

// exercise runs only the unpublished pipeline; this fixture never prepares publication.
func (f *pipelineFixture) exercise(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	result, err := f.core.TextWithTM(context.Background(), f.input.source, f.adapter, f.profile, f.store, f.scope, &out)
	if err != nil {
		t.Fatalf("fixture pipeline: %v", err)
	}
	if !result.TMCommitted || result.BytesWritten != out.Len() {
		t.Fatalf("fixture pipeline result: TMCommitted=%v BytesWritten=%d output=%d", result.TMCommitted, result.BytesWritten, out.Len())
	}
	return bytes.Clone(out.Bytes())
}

func (f *pipelineFixture) assertFixtureOutput(t *testing.T, input pipelineFixtureInput, out []byte) {
	t.Helper()
	if err := f.checkFixtureOutput(input, out); err != nil {
		t.Fatal(err)
	}
}

// checkFixtureOutput compares the produced artifact to the fixed authored
// target vector, then verifies the ordered authored identity through the real
// Observe contract.
func (f *pipelineFixture) checkFixtureOutput(input pipelineFixtureInput, out []byte) error {
	if !bytes.Equal(out, input.target) {
		return fmt.Errorf("target artifact differs from the fixed authored vector")
	}
	if input.kind == "text" {
		return nil
	}
	return f.checkObservedOutput(input, out)
}

// checkObservedOutput is order-sensitive: every authored ID, including the
// empty unit, must appear at its authored record position with the authored
// text, keys and header. A missing unit never reads as an empty translation.
func (f *pipelineFixture) checkObservedOutput(input pipelineFixtureInput, out []byte) error {
	observed, err := f.adapter.Observe(bytes.NewReader(out))
	if err != nil {
		return fmt.Errorf("observe target: %w", err)
	}
	if len(observed.Units) != len(input.units) {
		return fmt.Errorf("target units = %d, want %d", len(observed.Units), len(input.units))
	}
	wantKeys := make([]artifact.KeyRelation, 0, len(observed.Keys))
	for i, entry := range input.units {
		u := observed.Units[i]
		if u.ID.StableID() != entry.stableID {
			return fmt.Errorf("target unit %d ID = %q, want authored %q", i, u.ID.StableID(), entry.stableID)
		}
		if u.TargetLanguage != "ja" {
			return fmt.Errorf("target language = %q, want ja", u.TargetLanguage)
		}
		if u.Text != entry.translation {
			return fmt.Errorf("target unit %s differs from the authored translation", entry.stableID)
		}
		id, err := unit.NewUnitID(input.adapterName, input.formatSchema, entry.stableID)
		if err != nil {
			return err
		}
		for _, key := range entry.keys {
			wantKeys = append(wantKeys, artifact.KeyRelation{UnitID: id, Key: string(key)})
		}
	}
	if !slices.Equal(observed.Keys, wantKeys) {
		return fmt.Errorf("target key records differ from the authored order or bytes")
	}
	if len(out) < len(input.targetHeader) || !bytes.Equal(out[:8], input.source[:8]) ||
		!bytes.Equal(out[8:10], input.targetHeader[:2]) || !bytes.HasSuffix(out, input.targetHeader[2:]) {
		return fmt.Errorf("target W3 header differs from the authored ja header")
	}
	return nil
}

func pipelineFixtureSave(t *testing.T, name string, raw []byte) {
	t.Helper()
	root := os.Getenv("YAKUORI_PIPELINE_EVIDENCE")
	if root == "" {
		return
	}
	path := filepath.Join(root, t.Name(), name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func pipelineFixtureSaveJSON(t *testing.T, name string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	pipelineFixtureSave(t, name, append(raw, '\n'))
}

func pipelineFixtureEvidence(output []byte, f *pipelineFixture, measured pipelineFixtureLimits) map[string]any {
	units := make([]map[string]any, len(f.input.units))
	for i, u := range f.input.units {
		keys := make([]string, len(u.keys))
		for j, key := range u.keys {
			keys[j] = hex.EncodeToString(key)
		}
		units[i] = map[string]any{"stable_id": u.stableID, "source": u.source, "translated_text": u.translation, "keys_hex": keys}
	}
	relations := make([]map[string]any, len(f.relations))
	for i, relation := range f.relations {
		relations[i] = map[string]any{"stable_id": relation.UnitID.StableID(), "key_hex": hex.EncodeToString([]byte(relation.Key))}
	}
	return map[string]any{
		"kind": f.input.kind, "adapter": f.input.adapterName, "format_schema": f.input.formatSchema,
		"scope":      map[string]string{"game": f.scope.GameNamespace, "artifact": f.scope.ArtifactNamespace},
		"language":   map[string]string{"source": f.input.language, "target": "ja"},
		"header_hex": hex.EncodeToString(f.input.header), "suffix_hex": hex.EncodeToString(f.input.suffix),
		"target_header_hex": hex.EncodeToString(f.input.targetHeader),
		"units":             units, "real_key_relations": relations,
		"paths":        map[string]string{"stage": f.stage, "record": f.record, "backup": f.backup},
		"target_bytes": len(output), "generate_calls": len(f.backend.calls), "limits": measured,
		"authored_source_hex": hex.EncodeToString(f.input.source), "authored_target_hex": hex.EncodeToString(f.input.target),
		"authored_targets": f.targets,
	}
}

func TestPipelineFixtureInputs(t *testing.T) {
	t.Run("valid", testPipelineFixtureInputsValid)
	t.Run("reject-authored-mismatch", testPipelineFixtureInputsReject)
}

func testPipelineFixtureInputsValid(t *testing.T) {
	for _, input := range pipelineFixtureTable(t) {
		t.Run(input.kind, func(t *testing.T) {
			f := newPipelineFixture(t, input)
			if err := f.verifyAuthoredInputs(t, input.units); err != nil {
				t.Fatalf("authored adapter inputs: %v", err)
			}
			measured := f.assertWithinDefaultLimits(t)
			if before := pipelineFixtureReadRows(t, f.db); !slices.Equal(before, f.d0) {
				t.Fatalf("D0 rows changed before the run: %d rows", len(before))
			}
			output := f.exercise(t)
			if err := f.limits.CheckArtifactBytes(len(output)); err != nil {
				t.Fatalf("target artifact: %v", err)
			}
			wantCalls := make([]string, 0, len(input.units))
			for _, u := range input.units {
				if u.source != "" {
					wantCalls = append(wantCalls, u.source)
				}
			}
			if len(f.backend.calls) != len(wantCalls) {
				t.Fatalf("Generate calls = %d, want %d non-empty authored units", len(f.backend.calls), len(wantCalls))
			}
			for i := range wantCalls {
				if f.backend.calls[i] != wantCalls[i] {
					t.Fatalf("Generate call %d does not match non-empty authored unit %d", i, i)
				}
			}
			f.assertFixtureOutput(t, input, output)
			expected := f.expectedRows(t, input.units)
			actual := pipelineFixtureReadRows(t, f.db)
			if diff := pipelineFixtureDiff(expected, actual); len(diff) != 0 {
				t.Fatalf("canonical D1 differs in %d row(s): %v", len(diff), diff)
			}
			for _, row := range f.d0 {
				if !slices.Contains(actual, row) {
					t.Fatal("unrelated sentinel D0 row was changed")
				}
			}
			pipelineFixtureSave(t, "fixture-source.bin", input.source)
			pipelineFixtureSave(t, "fixture-target.bin", output)
			pipelineFixtureSaveJSON(t, "fixture-inputs.json", pipelineFixtureEvidence(output, f, measured))
			pipelineFixtureSaveJSON(t, "expected-rows.json", map[string]any{"d0": f.d0, "d1": expected})
			pipelineFixtureSaveJSON(t, "actual-rows-d1.json", actual)
			var logical strings.Builder
			for _, row := range actual {
				fmt.Fprintf(&logical, "key_schema=%d source_identity=%s translation_profile=%s translated_text=%q\n", row.Schema, row.Source, row.Profile, row.Text)
			}
			pipelineFixtureSave(t, "logical-rows.log", []byte(logical.String()))
			t.Logf("kind=%s units=%d keys=%d Generate=%d D0=%d D1=%d source_bytes=%d target_bytes=%d TMCommitted",
				input.kind, len(input.units), len(f.relations), len(f.backend.calls), len(f.d0), len(actual), len(input.source), len(output))
		})
	}
}

func testPipelineFixtureInputsReject(t *testing.T) {
	for _, input := range pipelineFixtureTable(t) {
		t.Run(input.kind, func(t *testing.T) {
			valid := newPipelineFixture(t, input)
			if err := valid.verifyAuthoredInputs(t, input.units); err != nil {
				t.Fatalf("valid counterpart rejected: %v", err)
			}
			validOutput := valid.exercise(t)
			if err := valid.checkFixtureOutput(input, validOutput); err != nil {
				t.Fatalf("valid counterpart rejected by the output checker: %v", err)
			}
			if diff := pipelineFixtureDiff(valid.expectedRows(t, input.units), pipelineFixtureReadRows(t, valid.db)); len(diff) != 0 {
				t.Fatalf("valid counterpart D1 differs: %v", diff)
			}
			validD1 := len(pipelineFixtureReadRows(t, valid.db))

			idMutation := slices.Clone(input.units)
			idMutation[len(idMutation)-1].stableID = "99999"
			idFixture := newPipelineFixture(t, input)
			before := pipelineFixtureReadRows(t, idFixture.db)
			if err := idFixture.verifyAuthoredInputs(t, idMutation); err == nil {
				t.Fatal("mutated expected source ID was accepted")
			}
			if after := pipelineFixtureReadRows(t, idFixture.db); !slices.Equal(before, after) || !slices.Equal(before, idFixture.d0) {
				t.Fatal("source ID rejection changed D0")
			}
			if len(idFixture.backend.calls) != 0 {
				t.Fatal("rejected source ID reached Generate")
			}

			textMutation := slices.Clone(input.units)
			textMutation[0].translation += "改"
			textFixture := newPipelineFixture(t, input)
			textFixture.exercise(t)
			actual := pipelineFixtureReadRows(t, textFixture.db)
			diff := pipelineFixtureDiff(textFixture.expectedRows(t, textMutation), actual)
			if len(diff) != 1 {
				t.Fatalf("one translated-text mutation produced %d differences, want 1", len(diff))
			}
			if !slices.Contains(actual, textFixture.d0[0]) {
				t.Fatal("D0 sentinel row changed")
			}
			if validDiff := pipelineFixtureDiff(textFixture.expectedRows(t, input.units), actual); len(validDiff) != 0 {
				t.Fatalf("unmutated expectations do not match the same rows: %v", validDiff)
			}
			rejectedOutputs := pipelineRejectOutputMutations(t, valid, input, validOutput)
			pipelineFixtureSaveJSON(t, "mismatch.json", map[string]any{
				"valid_counterpart_d1_rows": validD1, "source_id_rejected": true,
				"d0_rows_after_rejection": len(before), "generate_after_id_rejection": len(idFixture.backend.calls),
				"translated_text_differences": len(diff), "d0_sentinel_unchanged": true,
				"rejected_output_mutations": rejectedOutputs,
			})
			t.Logf("kind=%s valid counterpart D1=%d; source-ID mutation rejected with D0 unchanged and Generate=0; translated-text mutation produced exactly 1 row difference with D0 sentinel unchanged; output mutations rejected=%v",
				input.kind, validD1, rejectedOutputs)
		})
	}
}

// pipelineRejectOutputMutations keeps each former false positive structurally
// valid for the real Observe contract, then requires the shipped ordered
// checker to reject it. A nil error is an actual checker failure.
func pipelineRejectOutputMutations(t *testing.T, f *pipelineFixture, input pipelineFixtureInput, out []byte) []string {
	t.Helper()
	if input.kind != "w3" {
		return nil
	}
	if len(input.units) < 4 {
		t.Fatalf("output mutation fixture has %d units, want at least 4", len(input.units))
	}
	recordsAt, keyAt := 11, 11+12*len(input.units)+1
	mutations := []struct {
		name   string
		mutate func([]byte)
	}{
		{"changed-key", func(raw []byte) { raw[keyAt] ^= 0x40 }},
		{"reordered-id-records", func(raw []byte) {
			first := bytes.Clone(raw[recordsAt : recordsAt+12])
			copy(raw[recordsAt:recordsAt+12], raw[recordsAt+12:recordsAt+24])
			copy(raw[recordsAt+12:recordsAt+24], first)
		}},
		{"missing-empty-id", func(raw []byte) {
			id := recordsAt + 12
			binary.LittleEndian.PutUint32(raw[id:], binary.LittleEndian.Uint32(raw[id:])^0x1000)
		}},
	}
	var rejected []string
	for _, mutation := range mutations {
		raw := bytes.Clone(out)
		mutation.mutate(raw)
		if _, err := f.adapter.Observe(bytes.NewReader(raw)); err != nil {
			t.Fatalf("mutation %s is structurally invalid, so rejection would not prove the checker: %v", mutation.name, err)
		}
		if err := f.checkObservedOutput(input, raw); err == nil {
			t.Fatalf("output checker accepted %s", mutation.name)
		}
		t.Logf("output mutation %s rejected by the ordered checker", mutation.name)
		rejected = append(rejected, mutation.name)
	}
	return rejected
}
