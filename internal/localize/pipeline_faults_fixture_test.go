//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
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
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

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

// pipelineForeignExport exercises the real adapter's binding gate in Core.Stage.
type pipelineForeignExport struct {
	Adapter
	accepted []validate.AcceptedTranslation
}

func (a pipelineForeignExport) Export(w io.Writer, s unit.Session, p validate.Profile, _ []validate.AcceptedTranslation) (artifact.Manifest, error) {
	return a.Adapter.Export(w, s, p, a.accepted)
}

type pipelineFixture struct {
	core    Core
	profile validate.Profile
	limits  config.Limits
	adapter Adapter
	backend *pipelineBackend
	store   *tm.Store
	db      *sql.DB
	raw     []byte
	input   unit.Session // Independently authored CSV/text inputs, not Adapter output.
	texts   map[string]string
	scope   tm.Scope
	initial []pipelineTMRow
	paths   publication.PipelineFaultPaths
}

func newPipelineFixture(t *testing.T, kind string) *pipelineFixture {
	t.Helper()
	f := &pipelineFixture{limits: config.DefaultLimits(), raw: []byte("Hello.\n"),
		adapter: textadapter.New("en", "ja"), texts: map[string]string{"document": "訳文。\n"},
		scope: tm.Scope{GameNamespace: "issue21", ArtifactNamespace: kind}}
	rows := [][4]string{{"text", "text-v1", "document", "Hello.\n"}}
	if kind == "w3" {
		f.limits.ArtifactBytes, f.limits.Units = 65536, 4095
		f.limits.UnitTextBytes, f.limits.TotalTextBytes, f.limits.Segments = 16384, 32768, 4095
		var err error
		f.adapter, err = w3strings.NewWithLimits(f.limits)
		if err != nil {
			t.Fatal(err)
		}
		f.raw = fileBytes(t, "../../tools/w3spike/testdata/en.w3strings")
		f.texts = map[string]string{"1001": "訳一", "1002": "", "1003": "訳三", "4294967295": "  <b>{player}</b>|%s  "}
		rows = nil
		// Saved independent decoder expectations; this is not a codec copy.
		for line := range strings.SplitSeq(string(fileBytes(t, "../../tools/w3spike/testdata/en.csv")), "\n") {
			if line == "" || strings.HasPrefix(line, ";") {
				continue
			}
			fields := strings.SplitN(line, "|", 4)
			if len(fields) != 4 {
				t.Fatal("invalid authored CSV fixture")
			}
			rows = append(rows, [4]string{"witcher3-w3strings", "w3strings-v162-en-jp-v1", fields[0], fields[3]})
			if fields[0] != "1003" { // Known keyless ID; CSV zero is not a real hash.
				key, err := strconv.ParseUint(fields[1], 16, 32)
				if err != nil {
					t.Fatal(err)
				}
				id, err := unit.NewUnitID(rows[len(rows)-1][0], rows[len(rows)-1][1], fields[0])
				if err != nil {
					t.Fatal(err)
				}
				f.scope.Keys = append(f.scope.Keys, artifact.KeyRelation{UnitID: id, Key: string(binary.LittleEndian.AppendUint32(nil, uint32(key)))})
			}
		}
	}
	var units []unit.TranslationUnit
	f.backend = &pipelineBackend{texts: make(map[string]string), info: inference.ModelInfo{
		ModelSHA256: [32]byte{21}, Backend: "cpu", RequestedBackend: "cpu", BackendPin: "controlled-common-v1",
		ComputeQuant: "int4", ContextTokens: 4096, PolicySchema: inference.PolicySchemaV1,
		Tokenizer: inference.TokenizerIdentity{ModelSHA256: [32]byte{21}, BackendPin: "controlled-common-v1"},
		Template:  inference.TemplateIdentity{Family: "chatml", Source: "embedded", RendererVersion: "v1"},
		Prompt:    prompt.PlainIdentity(), Thinking: inference.ThinkingDisabled, Prefill: inference.PrefillPolicy{Path: "cpu", Fast: "off"},
	}}
	for _, row := range rows {
		id, err := unit.NewUnitID(row[0], row[1], row[2])
		if err != nil {
			t.Fatal(err)
		}
		u, err := unit.NewTranslationUnit(id, []byte(row[3]), "en", "ja", nil)
		if err != nil {
			t.Fatal(err)
		}
		units = append(units, u)
		f.backend.texts[row[3]] = f.texts[row[2]]
	}
	var err error
	f.input, err = unit.NewSession(f.raw, units)
	if err != nil {
		t.Fatal(err)
	}
	info := f.backend.info // Request identity remains immutable if backend Info changes.
	request, err := pipelineRequest(info, "")
	if err != nil {
		t.Fatal(err)
	}
	f.profile, err = NewCanonicalInferenceProfile(info, request, InferenceProfileInput{
		ModelID: "issue21-controlled", Glossary: identity.GlossaryInput{Mode: "none"}, Limits: f.limits,
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
	f.store, f.db = tmStoreFixture(t)
	unrelated, err := textadapter.New("en", "ja").Import([]byte("Unrelated.\n"), f.profile)
	if err != nil {
		t.Fatal(err)
	}
	source, err := unit.NewSourceIdentity(unrelated, unrelated.Units()[0].ID(), "issue21", "unrelated", nil)
	if err != nil {
		t.Fatal(err)
	}
	key, err := f.profile.ExactKey(source)
	if err != nil {
		t.Fatal(err)
	}
	row := pipelineTMRow{key.Schema(), key.SourceBytes(), key.ProfileBytes(), "無関係。\n"}
	if _, err := f.db.Exec("INSERT INTO tm VALUES(?,?,?,?)", row.Schema, row.Source, row.Profile, row.Text); err != nil {
		t.Fatal(err)
	}
	f.initial = []pipelineTMRow{row}
	return f
}
func (f *pipelineFixture) expectedRows(t *testing.T) []pipelineTMRow {
	t.Helper()
	rows := slices.Clone(f.initial)
	for _, u := range f.input.Units() {
		var keys [][]byte
		for _, relation := range f.scope.Keys {
			if relation.UnitID == u.ID() {
				keys = append(keys, []byte(relation.Key))
			}
		}
		source, err := unit.NewSourceIdentity(f.input, u.ID(), f.scope.GameNamespace, f.scope.ArtifactNamespace, keys)
		if err != nil {
			t.Fatal(err)
		}
		key, err := f.profile.ExactKey(source)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, pipelineTMRow{key.Schema(), key.SourceBytes(), key.ProfileBytes(), f.texts[u.ID().StableID()]})
	}
	slices.SortFunc(rows, func(a, b pipelineTMRow) int {
		if c := bytes.Compare(a.Source, b.Source); c != 0 {
			return c
		}
		return bytes.Compare(a.Profile, b.Profile)
	})
	return rows
}

func pipelineReadRows(t *testing.T, db *sql.DB) []pipelineTMRow {
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
	result := []pipelineTMRow{}
	for rows.Next() {
		var row pipelineTMRow
		if err := rows.Scan(&row.Schema, &row.Source, &row.Profile, &row.Text); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func pipelineSave(t *testing.T, name string, raw []byte) {
	t.Helper()
	if root := os.Getenv("YAKUORI_PIPELINE_EVIDENCE"); root != "" {
		path := filepath.Join(root, t.Name(), name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
