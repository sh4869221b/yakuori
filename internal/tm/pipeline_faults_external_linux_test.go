//go:build linux && pipelinefaults

package tm_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
	"golang.org/x/sys/unix"
)

type commitHookEngine struct{ calls int }

func (e *commitHookEngine) Generate(context.Context, unit.UnitID, string) (localize.Generation, error) {
	e.calls++
	return localize.Generation{Text: "translated.\n", Finish: localize.Stop}, nil
}

type commitHookRow struct {
	Schema          int
	Source, Profile []byte
	Text            string
}

func commitHookOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func commitHookRows(t *testing.T, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) []commitHookRow {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT key_schema,source_identity,translation_profile,translated_text FROM tm ORDER BY source_identity,translation_profile")
	commitHookOK(t, err)
	defer func() { commitHookOK(t, rows.Close()) }()
	result := []commitHookRow{}
	for rows.Next() {
		var row commitHookRow
		commitHookOK(t, rows.Scan(&row.Schema, &row.Source, &row.Profile, &row.Text))
		result = append(result, row)
	}
	commitHookOK(t, rows.Err())
	return result
}

func commitHookIdentity(t *testing.T, path string) [6]uint64 {
	t.Helper()
	var st unix.Stat_t
	commitHookOK(t, unix.Lstat(path, &st))
	var sx unix.Statx_t
	commitHookOK(t, unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &sx))
	if sx.Mask&unix.STATX_MNT_ID == 0 {
		t.Fatal("missing mount identity")
	}
	return [6]uint64{st.Dev, st.Ino, sx.Mnt_id, uint64(st.Size), uint64(st.Mode), uint64(st.Nlink)}
}

func TestPipelineTMCommitHooksCore(t *testing.T) {
	for _, mode := range []string{"before-cancel", "after-cancel"} {
		t.Run(mode, func(t *testing.T) {
			// Given: a real text import, SQLite Store, and qualified publication.
			work := t.TempDir()
			var fs unix.Statfs_t
			commitHookOK(t, unix.Statfs(work, &fs))
			if fs.Type != unix.EXT4_SUPER_MAGIC && fs.Type != unix.BTRFS_SUPER_MAGIC {
				t.Fatalf("qualified Core proof requires ext4/Btrfs: type=%x", fs.Type)
			}
			save := func(name string, data []byte) {
				if root := os.Getenv("YAKUORI_PIPELINE_EVIDENCE"); root != "" {
					dir := filepath.Join(root, t.Name())
					commitHookOK(t, os.MkdirAll(dir, 0700))
					commitHookOK(t, os.WriteFile(filepath.Join(dir, name), data, 0600))
				}
			}
			profile, err := validate.NewCanonicalProfile(identity.ProfileInput{
				Version: 1, ModelID: "hook-core", ModelDigest: [32]byte{1}, Backend: "cpu", BackendPin: "v1", ComputeQuant: "q4", KVPrecision: "not-applicable",
				Tokenizer: identity.TokenizerInput{ModelDigest: [32]byte{1}, BackendPin: "v1"}, Template: identity.TemplateInput{Family: "chat", Source: "embedded", RendererVersion: "v1"},
				Prompt:       identity.PromptInput{Schema: 1, Variant: "plain", SystemTemplate: []byte("system"), UserTemplate: []byte("user"), LanguageAliases: "v1"},
				Generation:   identity.GenerationInput{Schema: 1, MaxOutputTokens: 32, RequestTimeoutNS: 1, SeedFixed: true, Thinking: "disabled", Prefill: identity.PrefillInput{Path: "cpu"}},
				Segmentation: identity.SegmentationInput{Mode: "none"}, Glossary: identity.GlossaryInput{Mode: "none"},
				Protection: identity.SchemaInput{Schema: protect.Schema, FixtureSet: localize.ProtectionFixtures},
				Validation: identity.SchemaInput{Schema: validate.Schema, FixtureSet: localize.ValidationFixtures}, Context: identity.ContextInput{Tokens: 4096, Mode: "none"},
			})
			commitHookOK(t, err)
			source, output, dbPath := filepath.Join(work, "source"), filepath.Join(work, "output"), filepath.Join(work, "memory.sqlite")
			original := []byte("source.\n")
			commitHookOK(t, os.WriteFile(source, original, 0600))
			sourceID := commitHookIdentity(t, source)
			store, err := tm.Create(t.Context(), dbPath, config.DefaultLimits())
			commitHookOK(t, err)
			defer func() { commitHookOK(t, store.Close()) }()
			db, err := sql.Open("sqlite", dbPath)
			commitHookOK(t, err)
			defer func() { commitHookOK(t, db.Close()) }()
			adapter := textadapter.New("en", "ja")
			input, err := adapter.Import(original, profile)
			commitHookOK(t, err)
			sourceKey, err := unit.NewSourceIdentity(input, input.Units()[0].ID(), "game", "artifact", nil)
			commitHookOK(t, err)
			key, err := profile.ExactKey(sourceKey)
			commitHookOK(t, err)
			d0 := []commitHookRow{{1, []byte("sentinel-source"), []byte("sentinel-profile"), "retained.\n"}, {key.Schema(), key.SourceBytes(), key.ProfileBytes(), ""}}
			slices.SortFunc(d0, func(a, b commitHookRow) int { return bytes.Compare(a.Source, b.Source) })
			for _, row := range d0 {
				_, err := db.ExecContext(t.Context(), "INSERT INTO tm VALUES(?,?,?,?)", row.Schema, row.Source, row.Profile, row.Text)
				commitHookOK(t, err)
			}
			d1 := slices.Clone(d0)
			for i := range d1 {
				if bytes.Equal(d1[i].Source, key.SourceBytes()) {
					d1[i].Text = "translated.\n"
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var order []string
			var stage string
			var stageID [6]uint64
			store.SetPipelineCommitHooks(tm.PipelineCommitHooks{
				Before: func(_ context.Context, c *sql.Conn) error {
					order = append(order, "before")
					if !reflect.DeepEqual(commitHookRows(t, c), d1) || !reflect.DeepEqual(commitHookRows(t, db), d0) {
						t.Fatal("Core before checkpoint is not between DML and SQL COMMIT")
					}
					stages, err := filepath.Glob(filepath.Join(work, ".yakuori-run-*", "stage"))
					commitHookOK(t, err)
					if len(stages) != 1 {
						t.Fatalf("expected one real validated stage: %v", stages)
					}
					stage, stageID = stages[0], commitHookIdentity(t, stages[0])
					if mode == "before-cancel" {
						cancel()
					}
					return nil
				},
				After: func() {
					order = append(order, "after")
					if !reflect.DeepEqual(commitHookRows(t, db), d1) {
						t.Fatal("Core after checkpoint preceded SQL COMMIT")
					}
					cancel()
				},
			})
			engine := &commitHookEngine{}
			core := localize.NewCore(engine)
			// When: drive the actual Core, Store and Linux publication stage.
			result, err := core.FileWithTM(ctx, publication.Options{Source: source, Output: output, Mode: publication.Create},
				adapter, profile, store, tm.Scope{GameNamespace: "game", ArtifactNamespace: "artifact"})
			// Then: preserve the complete physical state before any assertion.
			actual := commitHookRows(t, db)
			raw, readErr := os.ReadFile(dbPath)
			commitHookOK(t, readErr)
			save("returned.sqlite", raw)
			sourceAfter, stageAfter := commitHookIdentity(t, source), commitHookIdentity(t, stage)
			_, recordErr := os.Lstat(filepath.Join(filepath.Dir(stage), "record.json"))
			_, outputErr := os.Lstat(output)
			state, marshalErr := json.MarshalIndent(struct {
				Result                        localize.Result
				Rows, D0, D1                  []commitHookRow
				Order                         []string
				SourceIdentity, StageIdentity [6]uint64
				SourceAfter, StageAfter       [6]uint64
				RecordInstalled, OutputExists bool
			}{result, actual, d0, d1, order, sourceID, stageID, sourceAfter, stageAfter, recordErr == nil, outputErr == nil}, "", "  ")
			commitHookOK(t, marshalErr)
			save("state.json", state)
			var phase *localize.Error
			wantRows, wantOrder := d0, []string{"before"}
			if mode == "after-cancel" {
				wantRows, wantOrder = d1, []string{"before", "after"}
			}
			if !errors.Is(err, context.Canceled) || !errors.As(err, &phase) || phase.Phase != "commit" ||
				result.TMCommitted != (mode == "after-cancel") || result.Publication.State != publication.NotPublished ||
				engine.calls != 1 || !slices.Equal(order, wantOrder) || !reflect.DeepEqual(actual, wantRows) {
				t.Fatalf("Core boundary: result=%+v error=%v order=%v generation=%d", result, err, order, engine.calls)
			}
			for _, absent := range []string{output, filepath.Join(filepath.Dir(stage), "record.json")} {
				if _, err := os.Lstat(absent); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("unexpected published asset %s: %v", absent, err)
				}
			}
			for _, item := range []struct {
				path string
				want []byte
				id   [6]uint64
			}{{source, original, sourceID}, {stage, []byte("translated.\n"), stageID}} {
				raw, err := os.ReadFile(item.path)
				commitHookOK(t, err)
				save(filepath.Base(item.path)+".bin", raw)
				if !bytes.Equal(raw, item.want) || commitHookIdentity(t, item.path) != item.id {
					t.Fatalf("bytes or identity changed: %s", item.path)
				}
			}
			t.Logf("phase=commit canceled; TMCommitted=%v; recordInstalled=false; source/stage full bytes and six identity fields unchanged; output absent", result.TMCommitted)
		})
	}
}
