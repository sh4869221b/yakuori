//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/validate"
	"golang.org/x/sys/unix"
)

func pipelineEqual[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("actual=%v expected=%v", got, want)
	}
}

func (f *pipelineFixture) options(t *testing.T, mode publication.Mode) publication.Options {
	t.Helper()
	f.paths = publication.PipelineFaultPaths{}
	parent := t.TempDir()
	options := publication.Options{Source: filepath.Join(parent, "source"), Output: filepath.Join(parent, "output"), Mode: mode}
	if mode == publication.InPlace {
		options.Output = options.Source
	}
	writeFile(t, options.Source, f.raw)
	return options
}

func (f *pipelineFixture) execute(ctx context.Context, options publication.Options, hooks publication.PipelineFaultHooks) (Result, error) {
	return f.core.fileWithTM(ctx, options, f.adapter, f.profile, nil, &tmExecution{store: f.store, scope: f.scope},
		func(ctx context.Context, options publication.Options) (publicationRun, error) {
			run, err := publication.PrepareWithLimits(ctx, options, f.limits)
			if err != nil {
				return nil, err
			}
			f.paths = run.SetPipelineFaultHooks(hooks)
			run.SetPipelineFaultHooks(publication.PipelineFaultHooks{})
			return run, nil
		})
}

type pipelineState struct {
	Options publication.Options
	Result  Result
	Files   map[string][]byte
	Stats   map[string]unix.Statx_t
	Rows    []pipelineTMRow
}

type pipelineTMRow struct {
	Schema  int
	Source  []byte
	Profile []byte
	Text    string
}

func (f *pipelineFixture) capture(t *testing.T, label string, state pipelineState) {
	t.Helper()
	state.Files, state.Stats = make(map[string][]byte), make(map[string]unix.Statx_t)
	paths := map[string]string{"source": state.Options.Source, "output": state.Options.Output,
		"stage": f.paths.Stage, "record": f.paths.Record, "backup": f.paths.Backup}
	if state.Result.Publication.RecordPath != "" {
		paths["record"], paths["backup"] = state.Result.Publication.RecordPath, state.Result.Publication.BackupPath
		paths["stage"] = filepath.Join(filepath.Dir(paths["record"]), "stage")
	}
	for name, path := range paths {
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			state.Files[name] = nil
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var stat unix.Statx_t
		if err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &stat); err != nil {
			t.Fatal(err)
		}
		state.Files[name], state.Stats[name] = raw, stat
		pipelineSave(t, label+"-"+name+".bin", raw)
	}
	state.Rows = pipelineReadRows(t, f.db)
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	pipelineSave(t, label+".json", raw)
}

func TestPipelineFixture(t *testing.T) {
	for _, kind := range []string{"text", "w3"} {
		for _, mode := range []struct {
			name string
			mode publication.Mode
		}{{"create", publication.Create}, {"replace-absent", publication.Replace}, {"replace-existing", publication.Replace}, {"in-place", publication.InPlace}} {
			for _, entry := range []string{"public", "bridge"} {
				t.Run(kind+"/"+mode.name+"/"+entry, func(t *testing.T) {
					// Given: real adapters, unrelated D0 row and independent expected identities.
					f := newPipelineFixture(t, kind)
					want := f.expectedRows(t)
					expected, err := json.MarshalIndent(want, "", "  ")
					if err != nil {
						t.Fatal(err)
					}
					pipelineSave(t, "expected-rows.json", expected)
					wantCalls := 1
					if kind == "w3" {
						wantCalls = 3
					}
					for iteration := range 2 {
						options := f.options(t, mode.mode) // Fresh parent and import on the TM-hit run.
						if mode.name == "replace-existing" {
							writeFile(t, options.Output, []byte("old separate output\n"))
						}
						session, err := f.adapter.Import(f.raw, f.profile)
						if err != nil {
							t.Fatal(err)
						}
						for _, u := range session.Units() {
							expected, err := f.input.Unit(u.ID())
							if err != nil {
								t.Fatal(err)
							}
							pipelineEqual(t, u, expected)
						}
						if a, ok := f.adapter.(*w3strings.Adapter); ok {
							keys, err := a.KeyRelations(session)
							if err != nil {
								t.Fatal(err)
							}
							f.scope.Keys = keys // Exact imported multiset, not guessed from CSV zero.
						}
						f.capture(t, "before-"+string(rune('0'+iteration)), pipelineState{Options: options})
						// When: execute ordinary public FileWithTM or its prepared-Run bridge.
						var result Result
						if entry == "public" {
							result, err = f.core.FileWithTM(t.Context(), options, f.adapter, f.profile, f.store, f.scope)
						} else {
							reached := make(map[string]int)
							var descriptors []*os.File
							closeHook := func(file *os.File) error {
								reached["close"]++
								descriptors = append(descriptors, file)
								return nil // Bridge owns the mandatory real Close.
							}
							result, err = f.execute(t.Context(), options, publication.PipelineFaultHooks{
								SyncStage:  func(file *os.File) error { reached["sync"]++; return file.Sync() },
								CloseStage: closeHook, RecordClose: closeHook,
								RecordWrite: func(file *os.File, b []byte) (int, error) { reached["write"]++; return file.Write(b) },
								RecordSync:  func(file *os.File) error { reached["record-sync"]++; return file.Sync() },
								Rename: func(a int, b string, c int, d string, flags uint) error {
									reached["rename"]++
									return unix.Renameat2(a, b, c, d, flags)
								},
							})
							for _, operation := range []string{"sync", "close", "write", "record-sync", "rename"} {
								if reached[operation] == 0 {
									t.Fatalf("bridge operation not reached: %s", operation)
								}
							}
							for _, file := range descriptors {
								if _, closeErr := file.Stat(); !errors.Is(closeErr, os.ErrClosed) {
									t.Fatalf("bridge did not close descriptor: %v", closeErr)
								}
							}
						}
						// Then: full D1, decoded output, original/backup and persistent-hit equality.
						if err != nil || !result.TMCommitted || result.Publication.State != publication.Published {
							t.Fatalf("result=%+v error=%v", result, err)
						}
						observed, err := f.adapter.Observe(bytes.NewReader(fileBytes(t, options.Output)))
						if err != nil {
							t.Fatal(err)
						}
						got := make(map[string]string)
						for _, u := range observed.Units {
							got[u.ID.StableID()] = u.Text
						}
						if !maps.Equal(got, f.texts) {
							t.Fatalf("decoded output=%v", got)
						}
						pipelineEqual(t, observed.Keys, f.scope.Keys)
						original := options.Source
						if mode.mode == publication.InPlace {
							original = result.Publication.BackupPath
						} else if result.Publication.BackupPath != "" {
							t.Fatal("separate output unexpectedly acquired a backup")
						}
						pipelineEqual(t, fileBytes(t, original), f.raw)
						pipelineEqual(t, pipelineReadRows(t, f.db), want)
						pipelineEqual(t, len(f.backend.calls), wantCalls)
						if _, err := os.Lstat(filepath.Join(filepath.Dir(result.Publication.RecordPath), "stage")); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("published stage remains: %v", err)
						}
						f.capture(t, "after-"+string(rune('0'+iteration)), pipelineState{Options: options, Result: result})
						t.Logf("controlled common Engine; iteration=%d Generate=%d full SQLite rows=%d Published; real Prepare/capability passed", iteration, len(f.backend.calls), len(want))
					}
				})
			}
		}
	}
}

func TestPipelineFixtureRejectsBindings(t *testing.T) {
	for _, kind := range []string{"text", "w3"} {
		for _, failure := range []string{"invalid-scope", "changed-profile", "foreign-accepted", "wrong-key-scope"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				// Given: unchanged source/output and a complete unrelated D0 row.
				f := newPipelineFixture(t, kind)
				options := f.options(t, publication.Replace)
				oldOutput := []byte("old separate output\n")
				writeFile(t, options.Output, oldOutput)
				want := error(tm.ErrInvalidScope)
				switch failure {
				case "invalid-scope":
					f.scope.GameNamespace = ""
				case "changed-profile":
					f.backend.info.ComputeQuant = "changed"
					want = ErrInferenceProfile
				case "wrong-key-scope":
					f.scope.Keys = append(f.scope.Keys, artifact.KeyRelation{UnitID: f.input.Units()[0].ID(), Key: "foreign"})
				case "foreign-accepted":
					old, err := f.adapter.Import(f.raw, f.profile)
					if err != nil {
						t.Fatal(err)
					}
					accepted, err := f.core.Generate(t.Context(), old, f.profile)
					if err != nil {
						t.Fatal(err)
					}
					f.adapter = pipelineForeignExport{f.adapter, accepted}
					want = validate.ErrBindingMismatch
				}
				f.capture(t, "before", pipelineState{Options: options})
				// When: a real FileWithTM pipeline meets invalid scope/profile/import binding.
				result, err := f.execute(t.Context(), options, publication.PipelineFaultHooks{})
				// Then: typed rejection, exact D0, no publication or output replacement.
				var phase *Error
				if !errors.Is(err, want) || !errors.As(err, &phase) || result.TMCommitted || result.Publication.State != publication.NotPublished {
					t.Fatalf("result=%+v error=%v expected=%v", result, err, want)
				}
				pipelineEqual(t, fileBytes(t, options.Source), f.raw)
				pipelineEqual(t, fileBytes(t, options.Output), oldOutput)
				pipelineEqual(t, pipelineReadRows(t, f.db), f.initial)
				f.capture(t, "after", pipelineState{Options: options, Result: result})
				t.Logf("typed rejection=%T phase=%s D0=%d NotPublished", want, phase.Phase, len(f.initial))
			})
		}
	}
}
