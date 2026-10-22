package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

const (
	w3CLIGame     = "witcher3"
	w3CLIArtifact = "better-keybinds"
	w3CLIModel    = "selected-model"
)

// w3CLIEntry is authored from tools/w3spike/testdata/better-keybinds/en.expected.csv
// in string-table order. Key is the published uint32 hash in hex.
type w3CLIEntry struct {
	ID   string
	Text string
	Key  string
}

var w3CLIEntries = []w3CLIEntry{
	{"2114573011", "Discard Target", "0eda26a6"},
	{"2114573010", "Next Target (Cycle)", "865469eb"},
	{"2114573009", "Target Nearest", "180e4e66"},
	{"2114573008", "Cancel Aiming", "f270786c"},
	{"2114573015", "Switch to Bomb 2", "ade1593b"},
	{"2114573014", "Switch to Bomb 1", "ade1593a"},
	{"2114573013", "Throw Bomb 2", "07e52441"},
	{"2114573012", "Throw Bomb 1", "07e52440"},
	{"2114573018", "Switch to Pocket 2", "9eff2e07"},
	{"2114573017", "Switch to Pocket 1", "9eff2e06"},
	{"2114573016", "Switch to Crossbow", "ae1b023f"},
	{"2114573003", "Cast Quen", "16708e57"},
	{"2114573002", "Cast Igni", "166cb7e3"},
	{"2114573001", "Cast Yrden", "b810970a"},
	{"2114573000", "Cast Aard", "1668fedc"},
	{"2114573007", "Sheathe Auto", "83e9172a"},
	{"2114573006", "Draw Auto Sword", "dd901354"},
	{"2114573005", "Toggle Quickslot", "c86f3f76"},
	{"2114573004", "Cast Axii", "16695421"},
}

// w3CLIKeyOrder is the fixture's key-table order, authored independently of
// the production parser and distinct from string-table order.
var w3CLIKeyOrder = []string{
	"07e52440", "07e52441", "0eda26a6", "1668fedc", "16695421",
	"166cb7e3", "16708e57", "180e4e66", "83e9172a", "865469eb",
	"9eff2e06", "9eff2e07", "ade1593a", "ade1593b", "ae1b023f",
	"b810970a", "c86f3f76", "dd901354", "f270786c",
}

// w3JapaneseTexts is the test fixture mapping from the known English source to
// deterministic, distinct short Japanese expected text. It is never derived
// from generated or persisted output.
func w3JapaneseTexts() map[string]string {
	return map[string]string{
		"Discard Target":      "対象を破棄",
		"Next Target (Cycle)": "次の対象へ",
		"Target Nearest":      "最近接対象",
		"Cancel Aiming":       "照準を解除",
		"Switch to Bomb 2":    "爆弾2へ切替",
		"Switch to Bomb 1":    "爆弾1へ切替",
		"Throw Bomb 2":        "爆弾2を投擲",
		"Throw Bomb 1":        "爆弾1を投擲",
		"Switch to Pocket 2":  "ポケット2へ",
		"Switch to Pocket 1":  "ポケット1へ",
		"Switch to Crossbow":  "クロスボウへ",
		"Cast Quen":           "クエンを詠唱",
		"Cast Igni":           "イグニを詠唱",
		"Cast Yrden":          "イルデンを詠唱",
		"Cast Aard":           "アードを詠唱",
		"Sheathe Auto":        "自動で納刀",
		"Draw Auto Sword":     "自動で抜刀",
		"Toggle Quickslot":    "クイックスロット",
		"Cast Axii":           "アクシィを詠唱",
	}
}

type w3CLIEngine struct {
	*translationEngine
	modelID          string
	japanese         map[string]string
	requestedBackend string
	opened           bool
	generatedTexts   []string
}

func newW3CLIEngine() *w3CLIEngine {
	return &w3CLIEngine{
		translationEngine: &translationEngine{text: "訳文", finish: inference.Stop, info: inference.ModelInfo{
			ModelSHA256: [32]byte{1}, Backend: "cpu", BackendPin: "goinfer-v1", ComputeQuant: "int4", ContextTokens: 4096,
			Tokenizer:    inference.TokenizerIdentity{ModelSHA256: [32]byte{1}, BackendPin: "goinfer-v1"},
			Template:     inference.TemplateIdentity{Family: "chatml", Source: "embedded", RendererVersion: "v1"},
			PolicySchema: inference.PolicySchemaV1, Prompt: prompt.PlainIdentity(), Thinking: inference.ThinkingDisabled,
			Prefill: inference.PrefillPolicy{Path: "cpu", Fast: "off"},
		}},
		modelID:  w3CLIModel,
		japanese: w3JapaneseTexts(),
	}
}

func (e *w3CLIEngine) Generate(_ context.Context, request inference.GenerationRequest) (inference.GenerationResult, error) {
	e.generated++
	source := request.RenderedPrompt()
	text, exists := e.japanese[source]
	if !exists {
		return inference.GenerationResult{}, fmt.Errorf("unexpected fixture source")
	}
	e.generatedTexts = append(e.generatedTexts, source)
	return inference.GenerationResult{Text: text, Finish: inference.Stop, PromptTokens: len(request.TokenIDs())}, nil
}

func (e *w3CLIEngine) open(ctx context.Context, backend string) (string, inference.Engine, error) {
	e.opened, e.requestedBackend = true, backend
	if _, _, err := e.translationEngine.open(ctx, backend); err != nil {
		return "", nil, err
	}
	return e.modelID, e, nil
}

type w3CLIFixture struct {
	dir    string
	paths  config.Paths
	engine *w3CLIEngine
	raw    []byte
	source string
}

func w3FixtureRaw(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "tools", "w3spike", "testdata", "better-keybinds", "en.w3strings"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 952 {
		t.Fatalf("fixture bytes = %d, want the pinned 952-byte BetterKeybinds input", len(raw))
	}
	_, _, _, units, keys := w3FixtureLayout(t, raw)
	total, maximum := 0, 0
	for _, entry := range w3CLIEntries {
		total += len(entry.Text)
		maximum = max(maximum, len(entry.Text))
	}
	if units != 19 || keys != 19 || total != 259 || maximum != 19 {
		t.Fatalf("fixture units=%d keys=%d text_bytes=%d max_unit=%d", units, keys, total, maximum)
	}
	return raw
}

func newW3CLIFixture(t *testing.T, raw, seedDB []byte) *w3CLIFixture {
	t.Helper()
	dir := t.TempDir()
	paths := config.Paths{
		Data: filepath.Join(dir, "data"), Config: filepath.Join(dir, "config"),
		Cache: filepath.Join(dir, "cache"), State: filepath.Join(dir, "state"),
	}
	for _, path := range []string{paths.Data, paths.Config, paths.Cache, paths.State} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(dir, "source.w3strings")
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if seedDB != nil {
		if err := os.WriteFile(config.ResolveStoragePaths(paths).TMFile, seedDB, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return &w3CLIFixture{dir: dir, paths: paths, engine: newW3CLIEngine(), raw: raw, source: source}
}

func (f *w3CLIFixture) run(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	var out, diag bytes.Buffer
	code := runWithTranslationOpener(context.Background(), args, &out, &diag,
		func() (config.Paths, error) { return f.paths, nil }, nil, f.engine.open)
	return code, out.String(), diag.String()
}

func w3Args(backend, sourceLanguage, game, artifact, source string, mode publication.Mode, output string) []string {
	args := []string{
		"localize", "--format", "w3strings", "--source-language", sourceLanguage,
		"--target-language", "ja", "--game", game, "--artifact", artifact, "--backend", backend,
	}
	switch mode {
	case publication.Create:
		args = append(args, "--output", output)
	case publication.Replace:
		args = append(args, "--output", output, "--replace-output")
	case publication.InPlace:
		args = append(args, "--in-place")
	}
	return append(args, source)
}

type w3Expectation struct {
	Texts     map[string]string
	UnitOrder []string
	KeyOrder  []string
	KeyIDs    map[string]string
}

func (e w3Expectation) keys(t *testing.T) map[string][][]byte {
	t.Helper()
	result := make(map[string][][]byte, len(e.KeyIDs))
	for hash, stable := range e.KeyIDs {
		result[stable] = append(result[stable], w3KeyBytes(t, hash))
	}
	return result
}

func w3BaseExpectation() w3Expectation {
	japanese := w3JapaneseTexts()
	exp := w3Expectation{
		Texts: make(map[string]string, len(w3CLIEntries)), UnitOrder: make([]string, len(w3CLIEntries)),
		KeyOrder: slices.Clone(w3CLIKeyOrder), KeyIDs: make(map[string]string, len(w3CLIEntries)),
	}
	for i, entry := range w3CLIEntries {
		exp.UnitOrder[i], exp.Texts[entry.ID], exp.KeyIDs[entry.Key] = entry.ID, japanese[entry.Text], entry.ID
	}
	return exp
}

func w3ExpectedRows(t *testing.T, f *w3CLIFixture, raw []byte, game, artifact string, exp w3Expectation) []translationTMRow {
	t.Helper()
	return expectedW3TranslationRows(t, w3TMInput{
		Raw: raw, SourceLanguage: "en", ModelID: f.engine.modelID,
		Game: game, Artifact: artifact, Keys: exp.keys(t), Texts: exp.Texts,
	}, f.engine)
}

func w3KeyBytes(t *testing.T, text string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(text)
	if err != nil || len(raw) != 4 {
		t.Fatalf("fixture key %q is not four hex bytes: %v", text, err)
	}
	slices.Reverse(raw)
	return raw
}

func w3UnitID(t *testing.T, stable string) unit.UnitID {
	t.Helper()
	id, err := unit.NewUnitID("witcher3-w3strings", "w3strings-v162-en-jp-v1", stable)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func w3Relations(t *testing.T, exp w3Expectation) []artifact.KeyRelation {
	t.Helper()
	relations := make([]artifact.KeyRelation, 0, len(exp.KeyOrder))
	for _, hash := range exp.KeyOrder {
		stable, exists := exp.KeyIDs[hash]
		if !exists {
			t.Fatalf("key order hash %s has no unit", hash)
		}
		relations = append(relations, artifact.KeyRelation{UnitID: w3UnitID(t, stable), Key: string(w3KeyBytes(t, hash))})
	}
	return relations
}

func w3ObserveFile(t *testing.T, raw []byte, sourceLanguage, path string) artifact.FinalObservation {
	t.Helper()
	adapter, err := w3strings.NewForSource(sourceLanguage, config.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Import(raw, validate.Profile{}); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	observed, err := adapter.Observe(file)
	if err != nil {
		t.Fatal(err)
	}
	return observed
}

func w3AssertPublished(t *testing.T, raw []byte, sourceLanguage, path string, exp w3Expectation) artifact.FinalObservation {
	t.Helper()
	observed := w3ObserveFile(t, raw, sourceLanguage, path)
	if len(observed.Units) != len(exp.UnitOrder) {
		t.Fatalf("published units = %d, want %d", len(observed.Units), len(exp.UnitOrder))
	}
	texts, order := make(map[string]string, len(observed.Units)), make([]string, 0, len(observed.Units))
	for _, u := range observed.Units {
		if u.TargetLanguage != "ja" {
			t.Fatalf("published unit %s language = %s", u.ID.StableID(), u.TargetLanguage)
		}
		texts[u.ID.StableID()], order = u.Text, append(order, u.ID.StableID())
	}
	if !maps.Equal(texts, exp.Texts) {
		t.Fatalf("published texts differ\nactual=%v\nexpected=%v", texts, exp.Texts)
	}
	if !slices.Equal(order, exp.UnitOrder) {
		t.Fatalf("published table order = %v, want %v", order, exp.UnitOrder)
	}
	if want := w3Relations(t, exp); !reflect.DeepEqual(observed.Keys, want) {
		t.Fatalf("published key relations/order = %v, want %v", observed.Keys, want)
	}
	if !bytes.Equal(observed.Metadata["language"], []byte{0x83, 0x54, 0x93, 0x48}) {
		t.Fatalf("published language header = %x", observed.Metadata["language"])
	}
	return observed
}

func w3RowKey(row translationTMRow) string {
	return fmt.Sprintf("%d|%s|%s|%s", row.Schema, hex.EncodeToString(row.Source), hex.EncodeToString(row.Profile), row.Text)
}

func w3AssertRows(t *testing.T, paths config.Paths, expected []translationTMRow) []translationTMRow {
	t.Helper()
	actual := readTranslationTM(t, paths)
	if len(actual) != len(expected) {
		t.Fatalf("TM rows = %d, want %d\nactual=%v", len(actual), len(expected), actual)
	}
	counts := make(map[string]int, len(actual))
	for _, row := range actual {
		counts[w3RowKey(row)]++
	}
	for _, row := range expected {
		counts[w3RowKey(row)]--
	}
	for key, count := range counts {
		if count != 0 {
			t.Fatalf("TM row %s count difference %d\nactual=%v\nexpected=%v", key, count, actual, expected)
		}
	}
	return actual
}

func w3Records(t *testing.T, root string) []string {
	t.Helper()
	var records []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == "record.json" && strings.HasPrefix(filepath.Base(filepath.Dir(path)), ".yakuori-run-") {
			records = append(records, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(records)
	return records
}

type w3Record struct {
	Phase  string `json:"phase"`
	RunID  string `json:"run_id"`
	Mode   string `json:"mode"`
	Output struct {
		Path string `json:"path"`
	} `json:"output"`
	Backup *struct {
		Path string `json:"path"`
	} `json:"backup"`
}

func w3NewRecord(t *testing.T, root string, before []string) (string, w3Record) {
	t.Helper()
	var added []string
	for _, path := range w3Records(t, root) {
		if !slices.Contains(before, path) {
			added = append(added, path)
		}
	}
	if len(added) != 1 {
		t.Fatalf("new publication records = %d, want exactly one: %v", len(added), added)
	}
	raw, err := os.ReadFile(added[0])
	if err != nil {
		t.Fatal(err)
	}
	var record w3Record
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	return added[0], record
}

func w3OpenTM(t *testing.T, paths config.Paths) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+config.ResolveStoragePaths(paths).TMFile+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func w3UpdateText(t *testing.T, paths config.Paths, row translationTMRow, text string) {
	t.Helper()
	db := w3OpenTM(t, paths)
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	result, err := db.Exec("UPDATE tm SET translated_text=? WHERE key_schema=? AND source_identity=? AND translation_profile=?", text, row.Schema, row.Source, row.Profile)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("updated rows=%d err=%v", n, err)
	}
}

func w3MoveSource(t *testing.T, paths config.Paths, from, to translationTMRow) {
	t.Helper()
	db := w3OpenTM(t, paths)
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	result, err := db.Exec("UPDATE tm SET source_identity=?, translated_text=? WHERE key_schema=? AND source_identity=? AND translation_profile=?", to.Source, to.Text, from.Schema, from.Source, from.Profile)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("moved rows=%d err=%v", n, err)
	}
}

func w3SeedBaseline(t *testing.T, raw []byte) (*w3CLIFixture, w3Expectation, []translationTMRow) {
	t.Helper()
	f := newW3CLIFixture(t, raw, nil)
	exp := w3BaseExpectation()
	before := w3Records(t, f.dir)
	output := filepath.Join(f.dir, "baseline.w3strings")
	code, out, diag := f.run(t, w3Args("cpu", "en", w3CLIGame, w3CLIArtifact, f.source, publication.Create, output))
	if !translationPublicationSupported(t, f.dir) {
		if code != ExitFailure || out != "" || diag == "" || f.engine.generated != 0 || f.engine.closed != 1 {
			t.Fatalf("unsupported filesystem refusal: code=%d stdout=%q stderr=%q generated=%d closed=%d", code, out, diag, f.engine.generated, f.engine.closed)
		}
		if rows := readTranslationTM(t, f.paths); len(rows) != 0 {
			t.Fatalf("refused run committed rows: %v", rows)
		}
		if current, err := os.ReadFile(f.source); err != nil || !bytes.Equal(current, raw) {
			t.Fatalf("refused run changed source: err=%v", err)
		}
		if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refused run produced output: %v", err)
		}
		return f, exp, nil
	}
	if code != ExitOK || out != "" || diag != "" || f.engine.generated != 19 || f.engine.closed != 1 {
		t.Fatalf("baseline: code=%d stdout=%q stderr=%q generated=%d closed=%d", code, out, diag, f.engine.generated, f.engine.closed)
	}
	if _, record := w3NewRecord(t, f.dir, before); record.Phase != "published" {
		t.Fatalf("baseline record phase = %q", record.Phase)
	}
	return f, exp, w3AssertRows(t, f.paths, w3ExpectedRows(t, f, raw, w3CLIGame, w3CLIArtifact, exp))
}

func w3CLIEvidence(t *testing.T, name string, f *w3CLIFixture, code int, stdout, stderr, output, record string, extra map[string]any) {
	t.Helper()
	root := os.Getenv("YAKUORI_W3_CLI_EVIDENCE")
	if root == "" {
		return
	}
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var logical strings.Builder
	for _, row := range readTranslationTM(t, f.paths) {
		fmt.Fprintf(&logical, "key_schema=%d source_identity=%s translation_profile=%s translated_text=%q\n",
			row.Schema, hex.EncodeToString(row.Source), hex.EncodeToString(row.Profile), row.Text)
	}
	if err := os.WriteFile(filepath.Join(dir, "logical-rows.log"), []byte(logical.String()), 0600); err != nil {
		t.Fatal(err)
	}
	files := []struct{ from, to string }{
		{f.source, "source.w3strings"}, {output, "output.w3strings"},
		{config.ResolveStoragePaths(f.paths).TMFile, "tm.sqlite"}, {record, "record.json"},
	}
	if record != "" {
		if raw, err := os.ReadFile(record); err == nil {
			var published w3Record
			if json.Unmarshal(raw, &published) == nil && published.Backup != nil {
				files = append(files, struct{ from, to string }{published.Backup.Path, "backup.w3strings"})
			}
		}
	}
	for _, copy := range files {
		if copy.from == "" {
			continue
		}
		raw, err := os.ReadFile(copy.from)
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, copy.to), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	result := map[string]any{
		"exit_code": code, "stdout": stdout, "stderr": stderr,
		"generated": f.engine.generated, "closed": f.engine.closed,
		"engine": "controlled-common-Engine", "requested_backend": f.engine.requestedBackend,
		"effective_backend": f.engine.info.Backend,
	}
	maps.Copy(result, extra)
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "result.json"), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func w3FixtureLayout(t *testing.T, raw []byte) (stringsAt, keysAt, payloadAt, units, keys int) {
	t.Helper()
	units, pos := w3FixtureCount(t, raw, 10)
	stringsAt = pos
	pos += units * 12
	keys, pos = w3FixtureCount(t, raw, pos)
	keysAt = pos
	pos += keys * 8
	_, payloadAt = w3FixtureCount(t, raw, pos)
	return stringsAt, keysAt, payloadAt, units, keys
}

func w3FixtureCount(t *testing.T, raw []byte, pos int) (int, int) {
	t.Helper()
	if pos >= len(raw) {
		t.Fatal("fixture count is truncated")
	}
	first := raw[pos]
	pos++
	if first < 64 {
		return int(first), pos
	}
	if first >= 128 || pos >= len(raw) {
		t.Fatal("fixture count is not minimal Bit6")
	}
	second := raw[pos]
	pos++
	if second == 0 || second >= 64 {
		t.Fatal("fixture count is not minimal Bit6")
	}
	return int(first&63) | int(second)<<6, pos
}

func w3EntryRecord(t *testing.T, raw []byte, stable string) (index, offset, length int) {
	t.Helper()
	stringsAt, _, _, units, _ := w3FixtureLayout(t, raw)
	want, err := strconv.ParseUint(stable, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	for i := range units {
		pos := stringsAt + i*12
		if uint64(binary.LittleEndian.Uint32(raw[pos:])^0x79321793) == want {
			return i, int(binary.LittleEndian.Uint32(raw[pos+4:])), int(binary.LittleEndian.Uint32(raw[pos+8:]))
		}
	}
	t.Fatalf("fixture ID %s is missing", stable)
	return 0, 0, 0
}

func w3KeyRecord(t *testing.T, raw []byte, hash uint32) (index, pos int) {
	t.Helper()
	_, keysAt, _, _, keys := w3FixtureLayout(t, raw)
	for i := range keys {
		pos := keysAt + i*8
		if binary.LittleEndian.Uint32(raw[pos:]) == hash {
			return i, pos
		}
	}
	t.Fatalf("fixture key %08x is missing", hash)
	return 0, 0
}

// w3MutateSource changes one same-length source text, leaving every other
// byte and the table layout untouched.
func w3MutateSource(t *testing.T, raw []byte) []byte {
	t.Helper()
	out := bytes.Clone(raw)
	_, offset, length := w3EntryRecord(t, out, "2114573000")
	_, _, payloadAt, _, _ := w3FixtureLayout(t, out)
	old, replacement := "Cast Aard", "Cast Axii"
	if len(old) != len(replacement) || len(old) != length {
		t.Fatalf("source mutation lengths old=%d new=%d record=%d", len(old), len(replacement), length)
	}
	for i := range length {
		pos := payloadAt + 2*(offset+i)
		unit := binary.LittleEndian.Uint16(out[pos:]) ^ uint16(old[i]^replacement[i])
		binary.LittleEndian.PutUint16(out[pos:], unit)
	}
	return out
}

func w3MutateID(t *testing.T, raw []byte) []byte {
	t.Helper()
	out := bytes.Clone(raw)
	index, _, _ := w3EntryRecord(t, out, "2114573000")
	stringsAt, _, _, _, _ := w3FixtureLayout(t, out)
	const replacement = uint32(2114573020)
	binary.LittleEndian.PutUint32(out[stringsAt+index*12:], replacement^0x79321793)
	_, keyPos := w3KeyRecord(t, out, 0x1668fedc)
	binary.LittleEndian.PutUint32(out[keyPos+4:], replacement^0x79321793)
	return out
}

func w3MutateKey(t *testing.T, raw []byte) []byte {
	t.Helper()
	out := bytes.Clone(raw)
	_, keyPos := w3KeyRecord(t, out, 0x1668fedc)
	binary.LittleEndian.PutUint32(out[keyPos:], 0x1668fedd)
	return out
}

func TestW3StringsCLIPipeline(t *testing.T) {
	t.Run("create", testW3CLICreate)
	t.Run("replace", testW3CLIReplace)
	t.Run("in_place", testW3CLIInPlace)
	t.Run("repeat", testW3CLIRepeat)
	t.Run("miss", testW3CLIMiss)
	t.Run("invalid_candidate", testW3CLIInvalidCandidate)
	t.Run("language", testW3CLILanguage)
	t.Run("malformed", testW3CLIMalformed)
	t.Run("limits", testW3CLILimits)
	t.Run("backends", testW3CLIBackends)
}

func testW3CLICreate(t *testing.T) {
	raw := w3FixtureRaw(t)
	f := newW3CLIFixture(t, raw, nil)
	exp := w3BaseExpectation()
	output := filepath.Join(f.dir, "output.w3strings")
	before := w3Records(t, f.dir)
	code, out, diag := f.run(t, w3Args("cpu", "en", w3CLIGame, w3CLIArtifact, f.source, publication.Create, output))
	if !translationPublicationSupported(t, f.dir) {
		if code != ExitFailure || out != "" || diag == "" || f.engine.generated != 0 || f.engine.closed != 1 {
			t.Fatalf("unsupported refusal: code=%d stdout=%q stderr=%q generated=%d", code, out, diag, f.engine.generated)
		}
		return
	}
	if code != ExitOK || out != "" || diag != "" || f.engine.generated != 19 || f.engine.closed != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q generated=%d closed=%d", code, out, diag, f.engine.generated, f.engine.closed)
	}
	if current, err := os.ReadFile(f.source); err != nil || !bytes.Equal(current, raw) {
		t.Fatalf("create changed source: err=%v", err)
	}
	recordPath, record := w3NewRecord(t, f.dir, before)
	if record.Phase != "published" || record.Mode != "create" || record.Output.Path != output || record.Backup != nil {
		t.Fatalf("record=%+v", record)
	}
	w3AssertPublished(t, raw, "en", output, exp)
	w3AssertRows(t, f.paths, w3ExpectedRows(t, f, raw, w3CLIGame, w3CLIArtifact, exp))
	w3CLIEvidence(t, "create", f, code, out, diag, output, recordPath, map[string]any{"mode": "create"})
	t.Logf("mode=create generated=%d closed=%d tm_rows=19 published_record=%s", f.engine.generated, f.engine.closed, recordPath)
}

func testW3CLIReplace(t *testing.T) {
	raw := w3FixtureRaw(t)
	f := newW3CLIFixture(t, raw, nil)
	exp := w3BaseExpectation()
	output := filepath.Join(f.dir, "output.w3strings")
	old := w3Table("en", []string{"Old output."})
	if err := os.WriteFile(output, old, 0600); err != nil {
		t.Fatal(err)
	}
	before := w3Records(t, f.dir)
	code, out, diag := f.run(t, w3Args("cpu", "en", w3CLIGame, w3CLIArtifact, f.source, publication.Replace, output))
	if !translationPublicationSupported(t, f.dir) {
		if code != ExitFailure || out != "" || f.engine.generated != 0 || f.engine.closed != 1 {
			t.Fatalf("unsupported refusal: code=%d stdout=%q generated=%d", code, out, f.engine.generated)
		}
		if current, err := os.ReadFile(output); err != nil || !bytes.Equal(current, old) {
			t.Fatalf("refused replace changed seeded output: err=%v", err)
		}
		return
	}
	if code != ExitOK || out != "" || diag != "" || f.engine.generated != 19 || f.engine.closed != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q generated=%d closed=%d", code, out, diag, f.engine.generated, f.engine.closed)
	}
	if current, err := os.ReadFile(f.source); err != nil || !bytes.Equal(current, raw) {
		t.Fatalf("replace changed source: err=%v", err)
	}
	recordPath, record := w3NewRecord(t, f.dir, before)
	if record.Phase != "published" || record.Mode != "replace" || record.Output.Path != output || record.Backup != nil {
		t.Fatalf("record=%+v", record)
	}
	w3AssertPublished(t, raw, "en", output, exp)
	w3AssertRows(t, f.paths, w3ExpectedRows(t, f, raw, w3CLIGame, w3CLIArtifact, exp))
	w3CLIEvidence(t, "replace", f, code, out, diag, output, recordPath, map[string]any{"mode": "replace", "seeded_old_output": true})
	t.Logf("mode=replace generated=%d closed=%d source_preserved=true seeded_output_replaced=true", f.engine.generated, f.engine.closed)
}

func testW3CLIInPlace(t *testing.T) {
	raw := w3FixtureRaw(t)
	f := newW3CLIFixture(t, raw, nil)
	exp := w3BaseExpectation()
	before := w3Records(t, f.dir)
	code, out, diag := f.run(t, w3Args("cpu", "en", w3CLIGame, w3CLIArtifact, f.source, publication.InPlace, ""))
	if !translationPublicationSupported(t, f.dir) {
		if code != ExitFailure || out != "" || f.engine.generated != 0 || f.engine.closed != 1 {
			t.Fatalf("unsupported refusal: code=%d stdout=%q generated=%d", code, out, f.engine.generated)
		}
		if current, err := os.ReadFile(f.source); err != nil || !bytes.Equal(current, raw) {
			t.Fatalf("refused in-place changed source: err=%v", err)
		}
		return
	}
	if code != ExitOK || out != "" || diag != "" || f.engine.generated != 19 || f.engine.closed != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q generated=%d closed=%d", code, out, diag, f.engine.generated, f.engine.closed)
	}
	recordPath, record := w3NewRecord(t, f.dir, before)
	if record.Phase != "published" || record.Mode != "in-place" || record.Output.Path != f.source || record.Backup == nil {
		t.Fatalf("record=%+v", record)
	}
	backup, err := os.ReadFile(record.Backup.Path)
	if err != nil || !bytes.Equal(backup, raw) {
		t.Fatalf("backup bytes differ from original: err=%v bytes=%d", err, len(backup))
	}
	if current, err := os.ReadFile(f.source); err != nil || bytes.Equal(current, raw) {
		t.Fatalf("in-place source was not replaced: err=%v", err)
	}
	w3AssertPublished(t, raw, "en", f.source, exp)
	w3AssertRows(t, f.paths, w3ExpectedRows(t, f, raw, w3CLIGame, w3CLIArtifact, exp))
	w3CLIEvidence(t, "in-place", f, code, out, diag, f.source, recordPath, map[string]any{"mode": "in-place", "backup": record.Backup.Path})
	t.Logf("mode=in-place generated=%d closed=%d backup_exact_original=true source_replaced=true", f.engine.generated, f.engine.closed)
}

func testW3CLIRepeat(t *testing.T) {
	raw := w3FixtureRaw(t)
	f := newW3CLIFixture(t, raw, nil)
	exp := w3BaseExpectation()
	first := filepath.Join(f.dir, "first.w3strings")
	beforeFirst := w3Records(t, f.dir)
	code, out, diag := f.run(t, w3Args("cpu", "en", w3CLIGame, w3CLIArtifact, f.source, publication.Create, first))
	if !translationPublicationSupported(t, f.dir) {
		if code != ExitFailure || out != "" || f.engine.generated != 0 {
			t.Fatalf("unsupported refusal: code=%d stdout=%q generated=%d", code, out, f.engine.generated)
		}
		return
	}
	if code != ExitOK || out != "" || diag != "" || f.engine.generated != 19 {
		t.Fatalf("first: code=%d stdout=%q stderr=%q generated=%d", code, out, diag, f.engine.generated)
	}
	w3NewRecord(t, f.dir, beforeFirst)
	rows := w3AssertRows(t, f.paths, w3ExpectedRows(t, f, raw, w3CLIGame, w3CLIArtifact, exp))
	second := filepath.Join(f.dir, "repeat.w3strings")
	beforeSecond := w3Records(t, f.dir)
	code, out, diag = f.run(t, w3Args("cpu", "en", w3CLIGame, w3CLIArtifact, f.source, publication.Create, second))
	if code != ExitOK || out != "" || diag != "" || f.engine.generated != 19 || f.engine.closed != 2 {
		t.Fatalf("repeat: code=%d stdout=%q stderr=%q generated=%d closed=%d", code, out, diag, f.engine.generated, f.engine.closed)
	}
	recordPath, record := w3NewRecord(t, f.dir, beforeSecond)
	if record.Phase != "published" || record.Output.Path != second {
		t.Fatalf("repeat record=%+v", record)
	}
	if current, err := os.ReadFile(f.source); err != nil || !bytes.Equal(current, raw) {
		t.Fatalf("repeat changed original source: err=%v", err)
	}
	w3AssertPublished(t, raw, "en", second, exp)
	again := w3AssertRows(t, f.paths, rows)
	if !reflect.DeepEqual(rows, again) {
		t.Fatalf("repeat changed TM rows\nfirst=%v\nsecond=%v", rows, again)
	}
	w3CLIEvidence(t, "repeat", f, code, out, diag, second, recordPath, map[string]any{"mode": "create", "second_generate_delta": 0})
	t.Logf("repeat generated_delta=0 total_generated=%d closed=%d rows_identical=true", f.engine.generated, f.engine.closed)
}

func testW3CLIMiss(t *testing.T) {
	raw := w3FixtureRaw(t)
	base, baseExp, baselineRows := w3SeedBaseline(t, raw)
	if !translationPublicationSupported(t, base.dir) {
		return
	}
	seed, err := os.ReadFile(config.ResolveStoragePaths(base.paths).TMFile)
	if err != nil {
		t.Fatal(err)
	}
	type variant struct {
		name, game, artifact, backend, model string
		raw                                  []byte
		expectation                          w3Expectation
		wrongKey                             bool
	}
	variants := []variant{
		{name: "source", raw: w3MutateSource(t, raw), expectation: w3BaseExpectation()},
		{name: "decoded_id", raw: w3MutateID(t, raw), expectation: w3BaseExpectation()},
		{name: "key_table", raw: w3MutateKey(t, raw), expectation: w3BaseExpectation()},
		{name: "profile", raw: raw, model: "selected-model-variant", expectation: w3BaseExpectation()},
		{name: "game", raw: raw, game: "witcher3-variant", expectation: w3BaseExpectation()},
		{name: "artifact", raw: raw, artifact: "better-keybinds-variant", expectation: w3BaseExpectation()},
		{name: "wrong_key", raw: raw, expectation: w3BaseExpectation(), wrongKey: true},
	}
	variants[0].expectation.Texts["2114573000"] = w3JapaneseTexts()["Cast Axii"]
	variants[1].expectation.Texts = maps.Clone(variants[1].expectation.Texts)
	delete(variants[1].expectation.Texts, "2114573000")
	variants[1].expectation.Texts["2114573020"] = w3JapaneseTexts()["Cast Aard"]
	variants[1].expectation.UnitOrder = slices.Clone(variants[1].expectation.UnitOrder)
	for i, id := range variants[1].expectation.UnitOrder {
		if id == "2114573000" {
			variants[1].expectation.UnitOrder[i] = "2114573020"
		}
	}
	variants[1].expectation.KeyIDs = maps.Clone(variants[1].expectation.KeyIDs)
	variants[1].expectation.KeyIDs["1668fedc"] = "2114573020"
	variants[2].expectation.KeyOrder = slices.Clone(variants[2].expectation.KeyOrder)
	for i, hash := range variants[2].expectation.KeyOrder {
		if hash == "1668fedc" {
			variants[2].expectation.KeyOrder[i] = "1668fedd"
		}
	}
	variants[2].expectation.KeyIDs = maps.Clone(variants[2].expectation.KeyIDs)
	delete(variants[2].expectation.KeyIDs, "1668fedc")
	variants[2].expectation.KeyIDs["1668fedd"] = "2114573000"

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			game, artifact := v.game, v.artifact
			if game == "" {
				game = w3CLIGame
			}
			if artifact == "" {
				artifact = w3CLIArtifact
			}
			backend := v.backend
			if backend == "" {
				backend = "cpu"
			}
			f := newW3CLIFixture(t, v.raw, seed)
			if v.model != "" {
				f.engine.modelID = v.model
			}
			output := filepath.Join(f.dir, "output.w3strings")
			wantGenerate := 19
			var wrongRow translationTMRow
			if v.wrongKey {
				wantGenerate = 1
				profile := w3ExpectedProfile(t, f.engine, f.engine.modelID, "en")
				adapter, err := w3strings.NewForSource("en", config.DefaultLimits())
				if err != nil {
					t.Fatal(err)
				}
				session, err := adapter.Import(raw, profile)
				if err != nil {
					t.Fatal(err)
				}
				source, err := unit.NewSourceIdentity(session, w3UnitID(t, "2114573000"), w3CLIGame, w3CLIArtifact, [][]byte{{0xde, 0xad, 0xbe, 0xef}})
				if err != nil {
					t.Fatal(err)
				}
				key, err := profile.ExactKey(source)
				if err != nil {
					t.Fatal(err)
				}
				wrongRow = translationTMRow{Schema: key.Schema(), Source: key.SourceBytes(), Profile: key.ProfileBytes(), Text: "WRONG-KEY"}
				var correct translationTMRow
				for _, row := range w3ExpectedRows(t, f, raw, w3CLIGame, w3CLIArtifact, baseExp) {
					if row.Text == w3JapaneseTexts()["Cast Aard"] {
						correct = row
					}
				}
				if correct.Source == nil {
					t.Fatal("correct wrong-key baseline row was not located")
				}
				w3MoveSource(t, f.paths, correct, wrongRow)
			}
			before := w3Records(t, f.dir)
			code, out, diag := f.run(t, w3Args(backend, "en", game, artifact, f.source, publication.Create, output))
			if code != ExitOK || out != "" || diag != "" || f.engine.generated != wantGenerate || f.engine.closed != 1 {
				t.Fatalf("code=%d stdout=%q stderr=%q generated=%d closed=%d", code, out, diag, f.engine.generated, f.engine.closed)
			}
			recordPath, record := w3NewRecord(t, f.dir, before)
			if record.Phase != "published" {
				t.Fatalf("record=%+v", record)
			}
			w3AssertPublished(t, v.raw, "en", output, v.expectation)
			expected := w3ExpectedRows(t, f, v.raw, game, artifact, v.expectation)
			if v.wrongKey {
				expected = append(expected, wrongRow)
			} else {
				freshSet := make(map[string]bool, len(expected))
				for _, row := range expected {
					freshSet[w3RowKey(row)] = true
				}
				for _, row := range baselineRows {
					if freshSet[w3RowKey(row)] {
						t.Fatalf("baseline row was reused for %s: %v", v.name, row)
					}
				}
				expected = append(expected, baselineRows...)
			}
			w3AssertRows(t, f.paths, expected)
			w3CLIEvidence(t, "miss-"+v.name, f, code, out, diag, output, recordPath, map[string]any{"variant": v.name, "generated": f.engine.generated})
			t.Logf("variant=%s generated=%d rows=%d published=true", v.name, f.engine.generated, len(expected))
		})
	}
}

func testW3CLIInvalidCandidate(t *testing.T) {
	raw := w3FixtureRaw(t)
	f := newW3CLIFixture(t, raw, nil)
	exp := w3BaseExpectation()
	first := filepath.Join(f.dir, "baseline.w3strings")
	before := w3Records(t, f.dir)
	code, out, diag := f.run(t, w3Args("cpu", "en", w3CLIGame, w3CLIArtifact, f.source, publication.Create, first))
	if !translationPublicationSupported(t, f.dir) {
		if code != ExitFailure || out != "" || f.engine.generated != 0 {
			t.Fatalf("unsupported refusal: code=%d stdout=%q generated=%d", code, out, f.engine.generated)
		}
		return
	}
	if code != ExitOK || out != "" || diag != "" {
		t.Fatalf("baseline: code=%d stdout=%q stderr=%q", code, out, diag)
	}
	w3NewRecord(t, f.dir, before)
	expected := w3ExpectedRows(t, f, raw, w3CLIGame, w3CLIArtifact, exp)
	var target translationTMRow
	for _, row := range expected {
		if row.Text == w3JapaneseTexts()["Cast Aard"] {
			target = row
		}
	}
	if target.Source == nil {
		t.Fatal("target row was not located")
	}
	w3UpdateText(t, f.paths, target, "")
	output := filepath.Join(f.dir, "candidate.w3strings")
	before = w3Records(t, f.dir)
	generated := len(f.engine.generatedTexts)
	code, out, diag = f.run(t, w3Args("cpu", "en", w3CLIGame, w3CLIArtifact, f.source, publication.Create, output))
	if code != ExitOK || out != "" || diag != "" || f.engine.generated != 20 || f.engine.closed != 2 {
		t.Fatalf("candidate: code=%d stdout=%q stderr=%q generated=%d closed=%d", code, out, diag, f.engine.generated, f.engine.closed)
	}
	recordPath, record := w3NewRecord(t, f.dir, before)
	if record.Phase != "published" {
		t.Fatalf("record=%+v", record)
	}
	if calls := f.engine.generatedTexts[generated:]; !slices.Equal(calls, []string{"Cast Aard"}) {
		t.Fatalf("invalid candidate generation calls=%v", calls)
	}
	w3AssertPublished(t, raw, "en", output, exp)
	w3AssertRows(t, f.paths, expected)
	w3CLIEvidence(t, "invalid-candidate", f, code, out, diag, output, recordPath, map[string]any{"repaired": "2114573000"})
	t.Logf("invalid candidate repaired with exactly one generation; total_generated=%d rows=19", f.engine.generated)
}

func testW3CLILanguage(t *testing.T) {
	raw := w3FixtureRaw(t)
	f := newW3CLIFixture(t, raw, nil)
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"source_fr", []string{"localize", "--format", "w3strings", "--source-language", "fr", "--target-language", "ja", "--game", w3CLIGame, "--artifact", w3CLIArtifact, "--backend", "cpu", "--output", filepath.Join(f.dir, "out.w3strings"), f.source}},
		{"target_en", []string{"localize", "--format", "w3strings", "--source-language", "en", "--target-language", "en", "--game", w3CLIGame, "--artifact", w3CLIArtifact, "--backend", "cpu", "--output", filepath.Join(f.dir, "out.w3strings"), f.source}},
		{"format_binary", []string{"localize", "--format", "binary", "--source-language", "en", "--target-language", "ja", "--game", w3CLIGame, "--artifact", w3CLIArtifact, "--backend", "cpu", "--output", filepath.Join(f.dir, "out.w3strings"), f.source}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, diag bytes.Buffer
			resolves, opens := 0, 0
			code := runWithTranslationOpener(context.Background(), tt.args, &out, &diag,
				func() (config.Paths, error) { resolves++; return f.paths, nil }, nil,
				func(ctx context.Context, backend string) (string, inference.Engine, error) {
					opens++
					return f.engine.open(ctx, backend)
				})
			if code != ExitUsage || resolves != 0 || opens != 0 || out.Len() != 0 || f.engine.generated != 0 {
				t.Fatalf("code=%d resolves=%d opens=%d stdout=%q generated=%d", code, resolves, opens, out.String(), f.engine.generated)
			}
			if _, err := os.Lstat(filepath.Join(f.dir, "out.w3strings")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected arguments produced output: %v", err)
			}
		})
	}
}

func testW3CLIMalformed(t *testing.T) {
	for _, tt := range []struct {
		name           string
		raw            []byte
		sourceLanguage string
	}{
		{"header_mismatch", w3FixtureRaw(t), "ja"},
		{"empty_table_header_mismatch", w3Table("en", nil), "ja"},
		{"malformed", []byte("RTSW not a w3strings table"), "en"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newW3CLIFixture(t, tt.raw, nil)
			output := filepath.Join(f.dir, "output.w3strings")
			code, out, diag := f.run(t, w3Args("cpu", tt.sourceLanguage, w3CLIGame, w3CLIArtifact, f.source, publication.Create, output))
			if code != ExitFailure || out != "" || diag == "" || f.engine.generated != 0 || f.engine.closed != 1 {
				t.Fatalf("code=%d stdout=%q stderr=%q generated=%d closed=%d", code, out, diag, f.engine.generated, f.engine.closed)
			}
			if rows := readTranslationTM(t, f.paths); len(rows) != 0 {
				t.Fatalf("malformed input committed rows: %v", rows)
			}
			if current, err := os.ReadFile(f.source); err != nil || !bytes.Equal(current, tt.raw) {
				t.Fatalf("malformed input changed source: err=%v", err)
			}
			if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("malformed input produced output: %v", err)
			}
			t.Logf("rejected before generation: stderr=%q", diag)
		})
	}
}

func testW3CLILimits(t *testing.T) {
	texts := make([]string, 10)
	for i := range texts {
		texts[i] = strings.Repeat("a", 200)
	}
	raw := w3Table("en", texts)
	if len(raw) <= config.DefaultLimits().ArtifactBytes {
		t.Fatalf("fixture bytes = %d, want above the default artifact limit", len(raw))
	}
	f := newW3CLIFixture(t, raw, nil)
	output := filepath.Join(f.dir, "output.w3strings")
	code, out, diag := f.run(t, w3Args("cpu", "en", w3CLIGame, w3CLIArtifact, f.source, publication.Create, output))
	if code != ExitFailure || out != "" || diag == "" || f.engine.generated != 0 || f.engine.closed != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q generated=%d closed=%d", code, out, diag, f.engine.generated, f.engine.closed)
	}
	if rows := readTranslationTM(t, f.paths); len(rows) != 0 {
		t.Fatalf("input-bound failure committed rows: %v", rows)
	}
	if current, err := os.ReadFile(f.source); err != nil || !bytes.Equal(current, raw) {
		t.Fatalf("input-bound failure changed source: err=%v", err)
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("input-bound failure produced output: %v", err)
	}
	t.Logf("artifact_bytes=%d default_limit=%d rejected before generation: stderr=%q", len(raw), config.DefaultLimits().ArtifactBytes, diag)
}

func testW3CLIBackends(t *testing.T) {
	raw := w3FixtureRaw(t)
	profiles := map[string]string{}
	for _, backend := range []string{"cpu", "cuda"} {
		t.Run(backend, func(t *testing.T) {
			f := newW3CLIFixture(t, raw, nil)
			exp := w3BaseExpectation()
			output := filepath.Join(f.dir, "output.w3strings")
			before := w3Records(t, f.dir)
			code, out, diag := f.run(t, w3Args(backend, "en", w3CLIGame, w3CLIArtifact, f.source, publication.Create, output))
			if f.engine.requestedBackend != backend {
				t.Fatalf("requested backend = %q", f.engine.requestedBackend)
			}
			if !translationPublicationSupported(t, f.dir) {
				if code != ExitFailure || out != "" || f.engine.generated != 0 || f.engine.closed != 1 {
					t.Fatalf("unsupported refusal: code=%d stdout=%q generated=%d", code, out, f.engine.generated)
				}
				return
			}
			if code != ExitOK || out != "" || diag != "" || f.engine.generated != 19 || f.engine.closed != 1 {
				t.Fatalf("code=%d stdout=%q stderr=%q generated=%d closed=%d", code, out, diag, f.engine.generated, f.engine.closed)
			}
			if f.engine.info.Backend != backend {
				t.Fatalf("effective backend = %q, want %q", f.engine.info.Backend, backend)
			}
			if backend == "cuda" && (f.engine.info.KVPrecision != "fp16" || f.engine.info.CUDABackendPin != "cuda-test-v1") {
				t.Fatalf("cuda-labelled metadata = %+v", f.engine.info)
			}
			profile := w3ExpectedProfile(t, f.engine, f.engine.modelID, "en")
			profiles[backend] = hex.EncodeToString(profile.CanonicalBytes())
			recordPath, record := w3NewRecord(t, f.dir, before)
			if record.Phase != "published" {
				t.Fatalf("record=%+v", record)
			}
			w3AssertPublished(t, raw, "en", output, exp)
			w3AssertRows(t, f.paths, w3ExpectedRows(t, f, raw, w3CLIGame, w3CLIArtifact, exp))
			w3CLIEvidence(t, "backend-"+backend, f, code, out, diag, output, recordPath, map[string]any{
				"requested_backend": backend, "effective_profile": profiles[backend],
			})
			t.Logf("engine=controlled-common-Engine requested_backend=%s effective_backend=%s profile=%s", backend, f.engine.info.Backend, profiles[backend])
		})
	}
	if len(profiles) == 2 && profiles["cpu"] == profiles["cuda"] {
		t.Fatalf("cpu and cuda-labelled profiles are equal")
	}
}
