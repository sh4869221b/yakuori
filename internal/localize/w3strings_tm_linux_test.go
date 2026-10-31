//go:build linux

package localize

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/bits"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
	"golang.org/x/sys/unix"
)

const (
	w3SQLiteGame     = "witcher3"
	w3SQLiteArtifact = "w3-sqlite"
)

type w3SQLiteEntry struct {
	id   uint32
	text string
	keys []uint32
}

// Hand-authored table order: keyless ID0 and IDmax endpoints, a real zero
// hash, one two-key unit, and three keys whose raw four bytes are not UTF-8.
func w3SQLiteEntries() []w3SQLiteEntry {
	return []w3SQLiteEntry{
		{0, "Zero", nil},
		{1, "One", []uint32{0}},
		{2, "Two", []uint32{0x89abcdef, 0x01020304}},
		{3, "Three", []uint32{0xfffffffe}},
		{4294967295, "End", []uint32{0xefbeadde}},
	}
}

func w3SQLiteCount(n int) []byte {
	if n < 64 {
		return []byte{byte(n)}
	}
	return []byte{byte(n&63) | 64, byte(n >> 6)}
}

// Independently authored bytes following the existing codec test pattern.
func w3SQLiteArtifactBytes(entries []w3SQLiteEntry) []byte {
	mask, prefix, suffix := uint32(0x79321793), uint16(0x4397), uint16(0x5139)
	raw := []byte{'R', 'T', 'S', 'W', 162, 0, 0, 0}
	raw = binary.LittleEndian.AppendUint16(raw, prefix)
	raw = append(raw, w3SQLiteCount(len(entries))...)
	offsets, lengths := make([]uint32, len(entries)), make([]uint32, len(entries))
	total := uint32(0)
	for i, entry := range entries {
		units := utf16.Encode([]rune(entry.text))
		offsets[i], lengths[i] = total, uint32(len(units))
		total += uint32(len(units)) + 1
	}
	for i, entry := range entries {
		raw = binary.LittleEndian.AppendUint32(raw, entry.id^mask)
		raw = binary.LittleEndian.AppendUint32(raw, offsets[i])
		raw = binary.LittleEndian.AppendUint32(raw, lengths[i])
	}
	type keyRecord struct{ hash, id uint32 }
	var keys []keyRecord
	for _, entry := range entries {
		for _, hash := range entry.keys {
			keys = append(keys, keyRecord{hash, entry.id})
		}
	}
	raw = append(raw, w3SQLiteCount(len(keys))...)
	for _, key := range keys {
		raw = binary.LittleEndian.AppendUint32(raw, key.hash)
		raw = binary.LittleEndian.AppendUint32(raw, key.id^mask)
	}
	raw = append(raw, w3SQLiteCount(int(total))...)
	for _, entry := range entries {
		units := utf16.Encode([]rune(entry.text))
		state := uint16(mask >> 8)
		for _, u := range units {
			raw = binary.LittleEndian.AppendUint16(raw, u^uint16(uint32(len(units)+1)*uint32(state)))
			state = bits.RotateLeft16(state, 1)
		}
		raw = binary.LittleEndian.AppendUint16(raw, 0)
	}
	return binary.LittleEndian.AppendUint16(raw, suffix)
}

func w3SQLiteStable(id uint32) string { return strconv.FormatUint(uint64(id), 10) }

func w3SQLiteKeysBytes(entry w3SQLiteEntry) [][]byte {
	keys := make([][]byte, len(entry.keys))
	for i, hash := range entry.keys {
		keys[i] = binary.LittleEndian.AppendUint32(nil, hash)
	}
	return keys
}

func w3SQLiteAuthored() (map[string]string, map[string][][]byte) {
	texts, keys := map[string]string{}, map[string][][]byte{}
	for _, entry := range w3SQLiteEntries() {
		stable := w3SQLiteStable(entry.id)
		texts[stable], keys[stable] = entry.text, w3SQLiteKeysBytes(entry)
	}
	return texts, keys
}

func w3SQLiteUnit(t *testing.T, session unit.Session, stable string) unit.UnitID {
	t.Helper()
	for _, u := range session.Units() {
		if u.ID().StableID() == stable {
			return u.ID()
		}
	}
	t.Fatalf("fixture unit %s is missing", stable)
	return unit.UnitID{}
}

type w3SQLiteRow struct {
	Schema  int
	Source  string
	Profile string
	Text    string
}

func (r w3SQLiteRow) String() string {
	return fmt.Sprintf("{schema=%d source=%s profile=%s text=%q}", r.Schema, r.Source, r.Profile, r.Text)
}

func w3SQLiteRows(t *testing.T, db *sql.DB) []w3SQLiteRow {
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
	var result []w3SQLiteRow
	for rows.Next() {
		var schema int
		var source, profile []byte
		var text string
		if err := rows.Scan(&schema, &source, &profile, &text); err != nil {
			t.Fatal(err)
		}
		result = append(result, w3SQLiteRow{Schema: schema, Source: hex.EncodeToString(source), Profile: hex.EncodeToString(profile), Text: text})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func w3SQLiteRowSet(rows []w3SQLiteRow) map[w3SQLiteRow]int {
	set := make(map[w3SQLiteRow]int, len(rows))
	for _, row := range rows {
		set[row]++
	}
	return set
}

func w3SQLiteAssertRows(t *testing.T, db *sql.DB, expected []w3SQLiteRow) []w3SQLiteRow {
	t.Helper()
	actual := w3SQLiteRows(t, db)
	if !maps.Equal(w3SQLiteRowSet(actual), w3SQLiteRowSet(expected)) {
		t.Fatalf("actual rows differ from independently expected canonical rows\nactual=%v\nexpected=%v", actual, expected)
	}
	return actual
}

// Build expected keys from fixture keys, the current import and the canonical
// profile; never from persisted row fields.
func w3SQLiteExpectedRows(t *testing.T, session unit.Session, profile validate.Profile, texts map[string]string, keys map[string][][]byte) []w3SQLiteRow {
	t.Helper()
	rows := make([]w3SQLiteRow, 0, len(session.Units()))
	for _, u := range session.Units() {
		stable := u.ID().StableID()
		source, err := unit.NewSourceIdentity(session, u.ID(), w3SQLiteGame, w3SQLiteArtifact, keys[stable])
		if err != nil {
			t.Fatal(err)
		}
		key, err := profile.ExactKey(source)
		if err != nil {
			t.Fatal(err)
		}
		text, exists := texts[stable]
		if !exists {
			t.Fatalf("no expected text for unit %s", stable)
		}
		rows = append(rows, w3SQLiteRow{Schema: key.Schema(), Source: hex.EncodeToString(key.SourceBytes()), Profile: hex.EncodeToString(key.ProfileBytes()), Text: text})
	}
	return rows
}

func w3SQLiteSeed(t *testing.T, db *sql.DB, session unit.Session, profile validate.Profile, stable string, keys [][]byte, text string) w3SQLiteRow {
	t.Helper()
	source, err := unit.NewSourceIdentity(session, w3SQLiteUnit(t, session, stable), w3SQLiteGame, w3SQLiteArtifact, keys)
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
	return w3SQLiteRow{Schema: key.Schema(), Source: hex.EncodeToString(key.SourceBytes()), Profile: hex.EncodeToString(key.ProfileBytes()), Text: text}
}

func w3SQLiteAssertNoOutput(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed run produced output: stat error=%v", err)
	}
}

func w3SQLitePhase(t *testing.T, err error, outer, inner string) *Error {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) || failure.Phase != outer {
		t.Fatalf("outer phase = %v, want %s", failure, outer)
	}
	if inner == "" {
		return failure
	}
	var nested *Error
	if !errors.As(failure.Err, &nested) || nested.Phase != inner {
		t.Fatalf("nested phase = %v, want %s (%v)", nested, inner, err)
	}
	return nested
}

func w3SQLiteCopy(t *testing.T, from, to string) {
	t.Helper()
	raw, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// Test-only evidence copy; inactive unless the owned run exports the root.
func w3SQLiteEvidence(t *testing.T, name string, f *w3SQLiteHarness, rows []w3SQLiteRow, output string) {
	t.Helper()
	root := os.Getenv("YAKUORI_W3_SQLITE_EVIDENCE")
	if root == "" {
		return
	}
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var logical strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&logical, "key_schema=%d source_identity=%s translation_profile=%s translated_text=%q\n", row.Schema, row.Source, row.Profile, row.Text)
	}
	if err := os.WriteFile(filepath.Join(dir, "logical-rows.log"), []byte(logical.String()), 0600); err != nil {
		t.Fatal(err)
	}
	w3SQLiteCopy(t, f.dbPath, filepath.Join(dir, "tm.db"))
	if output != "" {
		w3SQLiteCopy(t, output, filepath.Join(dir, "artifact.w3strings"))
	}
}

type w3SQLiteGeneration struct {
	corpus map[string]string
	calls  []string
	during func(inference.GenerationRequest) error
}

func (g *w3SQLiteGeneration) generate(request inference.GenerationRequest) (inference.GenerationResult, error) {
	text := request.RenderedPrompt()
	g.calls = append(g.calls, text)
	if g.during != nil {
		if err := g.during(request); err != nil {
			return inference.GenerationResult{}, err
		}
	}
	translation, exists := g.corpus[text]
	if !exists {
		return inference.GenerationResult{}, errors.New("unexpected fixture source")
	}
	return inference.GenerationResult{Text: translation, Finish: inference.Stop, PromptTokens: len(request.TokenIDs())}, nil
}

type w3SQLiteBackend struct {
	commonEngine
	info inference.ModelInfo
}

func (b w3SQLiteBackend) Info(context.Context) (inference.ModelInfo, error) { return b.info, nil }

type w3SQLiteHarness struct {
	t         *testing.T
	dbPath    string
	session   unit.Session
	profile   validate.Profile
	gen       *w3SQLiteGeneration
	core      Core
	adapter   *w3strings.Adapter
	store     *tm.Store
	db        *sql.DB
	options   publication.Options
	supported bool
	magic     int64
}

func newW3SQLiteHarness(t *testing.T) *w3SQLiteHarness {
	t.Helper()
	raw := w3SQLiteArtifactBytes(w3SQLiteEntries())
	limits := config.DefaultLimits()
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
		return inference.NewGenerationRequest(inference.PreparedRequest{
			RenderedPrompt: text, TokenIDs: make([]int, len(text)), StopIDs: []int{42},
			Identity: inference.RequestIdentity{
				ModelSHA256: info.ModelSHA256, Tokenizer: info.Tokenizer, Template: info.Template,
				PromptSchema: info.Prompt.Schema, Prompt: info.Prompt, Thinking: info.Thinking,
			},
		}, policy)
	}
	base, err := request("one")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewCanonicalInferenceProfile(info, base, InferenceProfileInput{ModelID: "w3-sqlite-fixture", Glossary: identity.GlossaryInput{Mode: "none"}, Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	gen := &w3SQLiteGeneration{corpus: map[string]string{}}
	texts, _ := w3SQLiteAuthored()
	for _, text := range texts {
		gen.corpus[text] = "訳" + text
	}
	backend := w3SQLiteBackend{info: info, commonEngine: commonEngine{
		count: func(_ context.Context, r inference.GenerationRequest) (int, error) { return len(r.TokenIDs()), nil },
		generate: func(_ context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
			return gen.generate(r)
		},
	}}
	build := func(_ context.Context, _ unit.UnitID, text string) (inference.GenerationRequest, error) {
		return request(text)
	}
	core := NewCore(NewInferenceEngine(backend, build))
	adapter, err := w3strings.NewForSource("en", limits)
	if err != nil {
		t.Fatal(err)
	}
	session, err := adapter.Import(raw, profile)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	options := publication.Options{Source: filepath.Join(dir, "source.w3strings"), Output: filepath.Join(dir, "output.w3strings"), Mode: publication.Create}
	if err := os.WriteFile(options.Source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		t.Fatal(err)
	}
	supported := st.Type == unix.EXT4_SUPER_MAGIC || st.Type == unix.BTRFS_SUPER_MAGIC
	dbPath := filepath.Join(dir, "memory.db")
	store, err := tm.Create(context.Background(), dbPath, limits)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	f := &w3SQLiteHarness{t: t, dbPath: dbPath, session: session, profile: profile, gen: gen, core: core, adapter: adapter, store: store, db: db, options: options, supported: supported, magic: st.Type}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return f
}

type w3SQLiteRun struct {
	publicationRun
	closed    int
	published int
}

func (r *w3SQLiteRun) Close() error {
	r.closed++
	return r.publicationRun.Close()
}

func (r *w3SQLiteRun) Publish(ctx context.Context) (publication.Result, error) {
	r.published++
	return r.publicationRun.Publish(ctx)
}

func (f *w3SQLiteHarness) checkUnsupported(result Result, err error, before []w3SQLiteRow, source []byte) {
	f.t.Helper()
	var failure *Error
	if !errors.Is(err, publication.ErrUnsupportedFilesystem) || !errors.As(err, &failure) || failure.Phase != "prepare" ||
		result.TMCommitted || result.BytesWritten != 0 || result.Publication.State != publication.NotPublished || len(f.gen.calls) != 0 {
		f.t.Fatalf("unsupported filesystem: result=%+v error=%v Generate=%d", result, err, len(f.gen.calls))
	}
	if current := fileBytes(f.t, f.options.Source); !bytes.Equal(current, source) {
		f.t.Fatal("refusal changed source")
	}
	w3SQLiteAssertNoOutput(f.t, f.options.Output)
	if after := w3SQLiteRows(f.t, f.db); !maps.Equal(w3SQLiteRowSet(after), w3SQLiteRowSet(before)) {
		f.t.Fatalf("refusal changed rows: before=%v after=%v", before, after)
	}
	f.t.Logf("unsupported filesystem magic=0x%x: typed prepare refusal Generate=0 NotPublished; source unchanged, output absent, %d rows unchanged; prepare returned no run, closure not applicable", f.magic, len(before))
}

// Same composition as FileWithTM; only the prepare seam is observed so resource
// closure and publish counts are explicit without adding production hooks.
func (f *w3SQLiteHarness) run(adapter Adapter) (Result, error, *w3SQLiteRun) {
	f.t.Helper()
	before := w3SQLiteRows(f.t, f.db)
	source := fileBytes(f.t, f.options.Source)
	var observed *w3SQLiteRun
	result, err := f.core.fileWithTM(context.Background(), f.options, adapter, f.profile, nil,
		&tmExecution{store: f.store, scope: tm.Scope{GameNamespace: w3SQLiteGame, ArtifactNamespace: w3SQLiteArtifact}},
		func(ctx context.Context, options publication.Options) (publicationRun, error) {
			real, err := publication.PrepareWithLimits(ctx, options, config.DefaultLimits())
			if err != nil {
				return nil, err
			}
			observed = &w3SQLiteRun{publicationRun: real}
			return observed, nil
		})
	if !f.supported {
		f.checkUnsupported(result, err, before, source)
		return result, err, nil
	}
	if observed == nil {
		f.t.Fatalf("publication prepare seam not reached: %v", err)
	}
	return result, err, observed
}

type w3SQLiteAdapter struct {
	*w3strings.Adapter
	t          *testing.T
	tamper     bool
	observeErr error
	onObserve  func()
	observed   bool
}

func (a *w3SQLiteAdapter) Observe(reader io.Reader) (artifact.FinalObservation, error) {
	a.t.Helper()
	if a.onObserve != nil {
		a.onObserve()
	}
	if a.observeErr != nil {
		return artifact.FinalObservation{}, a.observeErr
	}
	if !a.tamper {
		observed, err := a.Adapter.Observe(reader)
		a.observed = err == nil
		return observed, err
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return artifact.FinalObservation{}, err
	}
	if count := w3SQLiteCount(len(w3SQLiteEntries())); len(raw) < 11+12*len(w3SQLiteEntries()) || !bytes.Equal(raw[10:10+len(count)], count) {
		a.t.Fatal("unexpected authored table layout")
	}
	before, err := a.Adapter.Observe(bytes.NewReader(raw))
	if err != nil {
		a.t.Fatal(err)
	}
	// Swap two complete records so only the ordered metadata can reject it.
	first := bytes.Clone(raw[11:23])
	copy(raw[11:23], raw[23:35])
	copy(raw[23:35], first)
	after, err := a.Adapter.Observe(bytes.NewReader(raw))
	if err != nil {
		a.t.Fatal(err)
	}
	texts := func(o artifact.FinalObservation) map[unit.UnitID]string {
		m := make(map[unit.UnitID]string)
		for _, u := range o.Units {
			m[u.ID] = u.Text
		}
		return m
	}
	if !reflect.DeepEqual(texts(before), texts(after)) || !reflect.DeepEqual(before.Keys, after.Keys) ||
		bytes.Equal(before.Metadata["decoded-id-order"], after.Metadata["decoded-id-order"]) {
		a.t.Fatal("mutation was not a valid table-order-only change")
	}
	a.observed = true
	return after, nil
}

func w3SQLiteObserve(t *testing.T, adapter *w3strings.Adapter, path string) map[string]string {
	t.Helper()
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
	texts := make(map[string]string, len(observed.Units))
	for _, u := range observed.Units {
		if u.TargetLanguage != "ja" {
			t.Fatalf("published language = %s", u.TargetLanguage)
		}
		texts[u.ID.StableID()] = u.Text
	}
	return texts
}

func w3SQLiteGenerated() map[string]string {
	texts, _ := w3SQLiteAuthored()
	result := make(map[string]string, len(texts))
	for stable, text := range texts {
		result[stable] = "訳" + text
	}
	return result
}

func w3SQLiteAllCalls() []string { return []string{"Zero", "One", "Two", "Three", "End"} }

func TestW3StringsSQLite(t *testing.T) {
	t.Run("keys", testW3SQLiteKeys)
	t.Run("candidate", testW3SQLiteCandidate)
	t.Run("candidate_then_failure", testW3SQLiteCandidateThenFailure)
	t.Run("candidate_output_limit", testW3SQLiteCandidateOutputLimit)
	t.Run("observed_before_commit", testW3SQLiteObservedBeforeCommit)
	t.Run("stage_tamper", testW3SQLiteStageTamper)
	t.Run("observe_error", testW3SQLiteObserveError)
	t.Run("commit_error", testW3SQLiteCommitError)
}

func testW3SQLiteKeys(t *testing.T) {
	f := newW3SQLiteHarness(t)
	_, authoredKeys := w3SQLiteAuthored()
	relations, err := f.adapter.KeyRelations(f.session)
	if err != nil {
		t.Fatal(err)
	}
	var want []artifact.KeyRelation
	for _, entry := range w3SQLiteEntries() {
		for _, key := range w3SQLiteKeysBytes(entry) {
			want = append(want, artifact.KeyRelation{UnitID: w3SQLiteUnit(t, f.session, w3SQLiteStable(entry.id)), Key: string(key)})
		}
	}
	if !reflect.DeepEqual(relations, want) {
		t.Fatalf("ordered authored key relations lost: got %v want %v", relations, want)
	}
	keyless, multiple, zeroHash, nonUTF8 := 0, 0, false, false
	for _, entry := range w3SQLiteEntries() {
		id := w3SQLiteUnit(t, f.session, w3SQLiteStable(entry.id))
		count := 0
		for _, relation := range relations {
			if relation.UnitID != id {
				continue
			}
			count++
			if !utf8.ValidString(relation.Key) {
				nonUTF8 = true
			}
			if relation.Key == "\x00\x00\x00\x00" {
				zeroHash = true
			}
		}
		if count == 0 {
			keyless++
		}
		if count > 1 {
			multiple++
		}
	}
	if keyless != 1 || multiple != 1 || !zeroHash || !nonUTF8 || w3SQLiteStable(0) != "0" || w3SQLiteStable(4294967295) != "4294967295" {
		t.Fatalf("fixture classes keyless=%d multiple=%d zeroHash=%v nonUTF8=%v", keyless, multiple, zeroHash, nonUTF8)
	}
	wrong := map[string][][]byte{
		"0":          {{0xde, 0xad, 0xbe, 0xef}},
		"1":          nil,
		"2":          {binary.LittleEndian.AppendUint32(nil, 0x89abcdef)},
		"3":          {{0xfe, 0xff, 0xff, 0xfd}},
		"4294967295": nil,
	}
	var baseline []w3SQLiteRow
	for _, entry := range w3SQLiteEntries() {
		stable := w3SQLiteStable(entry.id)
		baseline = append(baseline, w3SQLiteSeed(t, f.db, f.session, f.profile, stable, wrong[stable], "WRONG-"+stable))
	}
	result, err, run := f.run(f.adapter)
	if !f.supported {
		return
	}
	if err != nil || !result.TMCommitted || result.Publication.State != publication.Published || run.closed != 1 || run.published != 1 {
		t.Fatalf("result=%+v error=%v run=%+v", result, err, run)
	}
	if !slices.Equal(f.gen.calls, w3SQLiteAllCalls()) {
		t.Fatalf("wrong-key rows were reused: generation calls=%v", f.gen.calls)
	}
	expected := append(w3SQLiteExpectedRows(t, f.session, f.profile, w3SQLiteGenerated(), authoredKeys), baseline...)
	actual := w3SQLiteAssertRows(t, f.db, expected)
	wrongRows := 0
	for _, row := range actual {
		if strings.HasPrefix(row.Text, "WRONG-") {
			wrongRows++
		}
	}
	if wrongRows != len(baseline) {
		t.Fatalf("seeded wrong rows changed: %d of %d", wrongRows, len(baseline))
	}
	if observed := w3SQLiteObserve(t, f.adapter, f.options.Output); !maps.Equal(observed, w3SQLiteGenerated()) {
		t.Fatalf("published artifact texts=%v", observed)
	}
	w3SQLiteEvidence(t, "keys", f, actual, f.options.Output)
	t.Logf("opaque key relations=%d keyless=%d multiple=%d zeroHash=%v nonUTF8=%v Generate=%d rows=%d Published", len(relations), keyless, multiple, zeroHash, nonUTF8, len(f.gen.calls), len(actual))
}

func testW3SQLiteCandidate(t *testing.T) {
	f := newW3SQLiteHarness(t)
	_, authoredKeys := w3SQLiteAuthored()
	for stable, text := range map[string]string{"0": "", "1": "TM-One", "3": "TM-Three"} {
		w3SQLiteSeed(t, f.db, f.session, f.profile, stable, authoredKeys[stable], text)
	}
	result, err, run := f.run(f.adapter)
	if !f.supported {
		return
	}
	if err != nil || !result.TMCommitted || result.Publication.State != publication.Published || run.closed != 1 || run.published != 1 {
		t.Fatalf("result=%+v error=%v run=%+v", result, err, run)
	}
	if !slices.Equal(f.gen.calls, []string{"Zero", "Two", "End"}) {
		t.Fatalf("generation calls=%v, want only the invalid/missing units", f.gen.calls)
	}
	texts := w3SQLiteGenerated()
	texts["1"], texts["3"] = "TM-One", "TM-Three"
	actual := w3SQLiteAssertRows(t, f.db, w3SQLiteExpectedRows(t, f.session, f.profile, texts, authoredKeys))
	for _, row := range actual {
		if row.Text == "" {
			t.Fatalf("invalid empty candidate survived: %v", row)
		}
	}
	if observed := w3SQLiteObserve(t, f.adapter, f.options.Output); !maps.Equal(observed, texts) {
		t.Fatalf("published artifact texts=%v want %v", observed, texts)
	}
	w3SQLiteEvidence(t, "candidate", f, actual, f.options.Output)
	t.Logf("exact hits reused=2 invalid repair=1 missing generated=2 Generate=%d rows=%d Published", len(f.gen.calls), len(actual))
}

func testW3SQLiteCandidateThenFailure(t *testing.T) {
	f := newW3SQLiteHarness(t)
	sentinel := errors.New("later unit generation failure")
	f.gen.during = func(request inference.GenerationRequest) error {
		if request.RenderedPrompt() == "End" {
			return sentinel
		}
		return nil
	}
	_, authoredKeys := w3SQLiteAuthored()
	baseline := []w3SQLiteRow{w3SQLiteSeed(t, f.db, f.session, f.profile, "0", authoredKeys["0"], "")}
	result, err, run := f.run(f.adapter)
	if !f.supported {
		return
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("failure cause lost: %v", err)
	}
	if failure := w3SQLitePhase(t, err, "generate", ""); failure.UnitID.StableID() != "4294967295" {
		t.Fatalf("failed unit=%s", failure.UnitID.StableID())
	}
	if result.TMCommitted || result.Publication.State != publication.NotPublished || run.closed != 1 || run.published != 0 {
		t.Fatalf("result=%+v run=%+v", result, run)
	}
	if !slices.Equal(f.gen.calls, w3SQLiteAllCalls()) {
		t.Fatalf("generation calls=%v", f.gen.calls)
	}
	actual := w3SQLiteAssertRows(t, f.db, baseline)
	w3SQLiteAssertNoOutput(t, f.options.Output)
	w3SQLiteEvidence(t, "candidate_then_failure", f, actual, "")
	t.Logf("repair and inserts uncommitted: Generate=%d rows=%d NotPublished cause preserved", len(f.gen.calls), len(actual))
}

func testW3SQLiteCandidateOutputLimit(t *testing.T) {
	f := newW3SQLiteHarness(t)
	_, authoredKeys := w3SQLiteAuthored()
	oversized := strings.Repeat("x", 4096)
	seeds := map[string]string{"0": oversized, "1": "TM-One", "2": "TM-Two", "3": "TM-Three", "4294967295": "TM-End"}
	var baseline []w3SQLiteRow
	for _, entry := range w3SQLiteEntries() {
		stable := w3SQLiteStable(entry.id)
		baseline = append(baseline, w3SQLiteSeed(t, f.db, f.session, f.profile, stable, authoredKeys[stable], seeds[stable]))
	}
	result, err, run := f.run(f.adapter)
	if !f.supported {
		return
	}
	if err == nil {
		t.Fatalf("common-valid oversized candidate exported: %+v", result)
	}
	if failure := w3SQLitePhase(t, err, "stage", "export"); !strings.Contains(failure.Err.Error(), "unsupported w3strings output payload count") {
		t.Fatalf("export cause=%v", failure.Err)
	}
	if len(f.gen.calls) != 0 {
		t.Fatalf("output limit fell back to regeneration: %v", f.gen.calls)
	}
	if result.TMCommitted || result.Publication.State != publication.NotPublished || run.closed != 1 || run.published != 0 {
		t.Fatalf("result=%+v run=%+v", result, run)
	}
	actual := w3SQLiteAssertRows(t, f.db, baseline)
	w3SQLiteAssertNoOutput(t, f.options.Output)
	w3SQLiteEvidence(t, "candidate_output_limit", f, actual, "")
	t.Logf("oversized cached candidate rejected at export: Generate=0 rows=%d NotPublished", len(actual))
}

func testW3SQLiteObservedBeforeCommit(t *testing.T) {
	f := newW3SQLiteHarness(t)
	wrapper := &w3SQLiteAdapter{Adapter: f.adapter, t: t, onObserve: func() {
		if rows := w3SQLiteRows(t, f.db); len(rows) != 0 {
			t.Fatalf("current-run rows visible before final observation: %v", rows)
		}
	}}
	result, err, run := f.run(wrapper)
	if !f.supported {
		return
	}
	if err != nil || !result.TMCommitted || result.Publication.State != publication.Published || !wrapper.observed || run.closed != 1 || run.published != 1 {
		t.Fatalf("result=%+v error=%v run=%+v observed=%v", result, err, run, wrapper.observed)
	}
	_, keys := w3SQLiteAuthored()
	actual := w3SQLiteAssertRows(t, f.db, w3SQLiteExpectedRows(t, f.session, f.profile, w3SQLiteGenerated(), keys))
	if observed := w3SQLiteObserve(t, f.adapter, f.options.Output); !maps.Equal(observed, w3SQLiteGenerated()) {
		t.Fatalf("published artifact texts=%v", observed)
	}
	w3SQLiteEvidence(t, "observed_before_commit", f, actual, f.options.Output)
	t.Logf("observer saw 0 committed rows; after success rows=%d and Published", len(actual))
}

func testW3SQLiteStageTamper(t *testing.T) {
	f := newW3SQLiteHarness(t)
	baseline := []w3SQLiteRow{w3SQLiteSeed(t, f.db, f.session, f.profile, "2", [][]byte{{0xde, 0xad, 0xbe, 0xef}}, "WRONG-STAGE")}
	wrapper := &w3SQLiteAdapter{Adapter: f.adapter, t: t, tamper: true}
	result, err, run := f.run(wrapper)
	if !f.supported {
		return
	}
	if !errors.Is(err, artifact.ErrFinalMismatch) {
		t.Fatalf("table-order tamper not rejected: %v", err)
	}
	w3SQLitePhase(t, err, "stage", "compare_final")
	if !wrapper.observed || result.TMCommitted || result.Publication.State != publication.NotPublished || run.closed != 1 || run.published != 0 {
		t.Fatalf("result=%+v run=%+v observed=%v", result, run, wrapper.observed)
	}
	if !slices.Equal(f.gen.calls, w3SQLiteAllCalls()) {
		t.Fatalf("generation calls=%v", f.gen.calls)
	}
	actual := w3SQLiteAssertRows(t, f.db, baseline)
	w3SQLiteAssertNoOutput(t, f.options.Output)
	w3SQLiteEvidence(t, "stage_tamper", f, actual, "")
	t.Logf("Observe succeeded, CompareFinal rejected, Commit=0 rows=%d NotPublished", len(actual))
}

func testW3SQLiteObserveError(t *testing.T) {
	f := newW3SQLiteHarness(t)
	sentinel := errors.New("injected observe failure")
	baseline := []w3SQLiteRow{w3SQLiteSeed(t, f.db, f.session, f.profile, "1", nil, "WRONG-OBSERVE")}
	wrapper := &w3SQLiteAdapter{Adapter: f.adapter, t: t, observeErr: sentinel}
	result, err, run := f.run(wrapper)
	if !f.supported {
		return
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("observe cause lost: %v", err)
	}
	w3SQLitePhase(t, err, "stage", "observe")
	if wrapper.observed || result.TMCommitted || result.Publication.State != publication.NotPublished || run.closed != 1 || run.published != 0 {
		t.Fatalf("result=%+v run=%+v observed=%v", result, run, wrapper.observed)
	}
	if !slices.Equal(f.gen.calls, w3SQLiteAllCalls()) {
		t.Fatalf("generation calls=%v", f.gen.calls)
	}
	actual := w3SQLiteAssertRows(t, f.db, baseline)
	w3SQLiteAssertNoOutput(t, f.options.Output)
	w3SQLiteEvidence(t, "observe_error", f, actual, "")
	t.Logf("Observe failure phase/cause preserved: Commit=0 rows=%d NotPublished", len(actual))
}

func testW3SQLiteCommitError(t *testing.T) {
	f := newW3SQLiteHarness(t)
	installed := false
	f.gen.during = func(inference.GenerationRequest) error {
		if installed {
			return nil
		}
		installed = true
		_, err := f.db.Exec("CREATE TRIGGER reject_w3_commit BEFORE INSERT ON tm BEGIN SELECT RAISE(ABORT, 'reject w3 sqlite insert'); END")
		return err
	}
	wrapper := &w3SQLiteAdapter{Adapter: f.adapter, t: t}
	result, err, run := f.run(wrapper)
	if !f.supported {
		return
	}
	if err == nil || !strings.Contains(w3SQLitePhase(t, err, "commit", "").Err.Error(), "reject w3 sqlite insert") {
		t.Fatalf("commit abort cause lost: %v", err)
	}
	if !wrapper.observed || result.TMCommitted || result.Publication.State != publication.NotPublished || run.closed != 1 || run.published != 0 {
		t.Fatalf("result=%+v run=%+v observed=%v", result, run, wrapper.observed)
	}
	if !slices.Equal(f.gen.calls, w3SQLiteAllCalls()) {
		t.Fatalf("generation calls=%v", f.gen.calls)
	}
	actual := w3SQLiteAssertRows(t, f.db, nil)
	w3SQLiteAssertNoOutput(t, f.options.Output)
	w3SQLiteEvidence(t, "commit_error", f, actual, "")
	t.Logf("final observation succeeded, commit aborted, rows=%d NotPublished", len(actual))
}
