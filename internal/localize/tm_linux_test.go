//go:build linux

package localize

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func TestTMFailureGates(t *testing.T) {
	testTMFailureGates(t, []string{"database read", "bad source", "profile mismatch", "nil store"})
	testTMPreflightGates(t)
}

func TestTMAtomicFailureAndRepair(t *testing.T) {
	testTMFailureGates(t, []string{"later unit", "final artifact"})
}

func testTMFailureGates(t *testing.T, conditions []string) {
	for _, condition := range conditions {
		t.Run(condition, func(t *testing.T) {
			store, db := tmStoreFixture(t)
			calls := 0
			_, profile := canonicalCoreFixture(t, false, "", &calls)
			core := NewCore(fakeEngine(func(_ context.Context, id unit.UnitID, text string) (Generation, error) {
				calls++
				if condition == "later unit" && id.StableID() == "b" {
					return Generation{}, ErrIncompleteGeneration
				}
				return Generation{Text: text, Finish: Stop}, nil
			}))
			adapter := &fakeAdapter{t: t, tm: &fakeTM{}}
			input, err := json.Marshal(fakeImage{Adapter: "fixture", Schema: "v1", Language: "en", Records: []fakeRecord{{"a", "one"}, {"b", "two"}}})
			if err != nil {
				t.Fatal(err)
			}
			session, err := adapter.Import(input, profile)
			if err != nil {
				t.Fatal(err)
			}
			seedTMRow(t, db, session, profile, 0, "")
			wantCalls := 0
			var want error
			var target Adapter = adapter
			switch condition {
			case "database read":
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			case "later unit":
				want, wantCalls = ErrIncompleteGeneration, 2
			case "bad source":
				input, target, want = []byte("\xff"), textadapter.New("en", "ja"), protect.ErrInvalidSource
			case "profile mismatch":
				core, profile = canonicalCoreFixture(t, false, "model", &calls)
				want = ErrInferenceProfile
			case "final artifact":
				adapter.corrupt = func(image *fakeImage) { image.Records[0].Text = "changed" }
				want, wantCalls = artifact.ErrFinalMismatch, 2
			case "nil store":
				store, want = nil, ErrNilTMStore
			}
			var output bytes.Buffer
			result, err := core.TextWithTM(context.Background(), input, target, profile, store, tmScope(), &output)
			if err == nil || (want != nil && !errors.Is(err, want)) || result.TMCommitted || output.Len() != 0 || calls != wantCalls {
				t.Fatalf("error=%v result=%+v output=%q calls=%d", err, result, output.String(), calls)
			}
			var text string
			if err := db.QueryRow("SELECT translated_text FROM tm").Scan(&text); err != nil || text != "" || tmRowCount(t, db) != 1 {
				t.Fatalf("error=%v stored text=%q", err, text)
			}
			if condition == "later unit" || condition == "final artifact" {
				adapter.corrupt = nil
				core = NewCore(fakeEngine(func(_ context.Context, _ unit.UnitID, text string) (Generation, error) {
					return Generation{Text: text, Finish: Stop}, nil
				}))
				result, err = core.TextWithTM(context.Background(), input, adapter, profile, store, tmScope(), &output)
				if err != nil || !result.TMCommitted || output.Len() == 0 || tmRowCount(t, db) != 2 {
					t.Fatalf("explicit repair error=%v result=%+v output=%q", err, result, output.String())
				}
				var empty int
				if err := db.QueryRow("SELECT count(*) FROM tm WHERE translated_text=''").Scan(&empty); err != nil || empty != 0 {
					t.Fatalf("repair error=%v empty=%d", err, empty)
				}
			}
		})
	}
}

func testTMPreflightGates(t *testing.T) {
	t.Run("all units preflight before hit", func(t *testing.T) {
		store, db := tmStoreFixture(t)
		calls := 0
		_, profile := canonicalCoreFixture(t, false, "", &calls)
		session, _ := generationFixture(t, []string{"one", "\xff"}, nil)
		seedTMRow(t, db, session, profile, 0, "cached")
		memory := &tmExecution{store: store, scope: tmScope()}
		accepted, err := NewCore(fakeEngine(func(context.Context, unit.UnitID, string) (Generation, error) {
			calls++
			return Generation{}, nil
		})).generate(context.Background(), session, profile, memory)
		if !errors.Is(err, protect.ErrInvalidSource) || accepted != nil || memory.run != nil || calls != 0 || tmRowCount(t, db) != 1 {
			t.Fatalf("error=%v accepted=%v run=%v calls=%d", err, accepted, memory.run, calls)
		}
	})
	t.Run("all requests preflight before hit", func(t *testing.T) {
		store, db := tmStoreFixture(t)
		calls := 0
		core, profile := canonicalCoreFixture(t, false, "later policy", &calls)
		session, _ := generationFixture(t, []string{"one", "two"}, nil)
		seedTMRow(t, db, session, profile, 0, "cached")
		seedTMRow(t, db, session, profile, 1, "cached too")
		memory := &tmExecution{store: store, scope: tmScope()}
		accepted, err := core.generate(context.Background(), session, profile, memory)
		if !errors.Is(err, ErrInferenceProfile) || accepted != nil || memory.run != nil || calls != 0 || tmRowCount(t, db) != 2 {
			t.Fatalf("error=%v accepted=%v run=%v calls=%d", err, accepted, memory.run, calls)
		}
	})
}

func TestTMCommitPublishFailureRerun(t *testing.T) {
	store, db := tmStoreFixture(t)
	f := newFileFixture(t, publication.Create)
	calls := 0
	_, f.profile = canonicalCoreFixture(t, false, "", &calls)
	f.core = NewCore(fakeEngine(func(_ context.Context, _ unit.UnitID, text string) (Generation, error) {
		calls++
		return Generation{Text: text, Finish: Stop}, nil
	}))
	scope := tmFileScope(t, f)
	var run *testRun
	result, err := f.core.fileWithTM(context.Background(), f.options, f.adapter, f.profile, nil, &tmExecution{store: store, scope: scope}, func(ctx context.Context, options publication.Options) (publicationRun, error) {
		real, err := publication.Prepare(ctx, options)
		if err != nil {
			return nil, err
		}
		run = &testRun{publicationRun: real, afterStage: func() {
			if tmRowCount(t, db) != 0 {
				t.Fatal("rows persisted before final artifact check")
			}
			writeFile(t, f.options.Output, []byte("external"))
		}}
		return run, nil
	})
	if errors.Is(err, publication.ErrUnsupportedFilesystem) {
		t.Skip("requires qualified ext4/Btrfs TMPDIR")
	}
	if !errors.Is(err, publication.ErrChanged) || !result.TMCommitted || result.Publication.State != publication.NotPublished || run.published != 1 || run.closed != 1 || tmRowCount(t, db) != 2 || calls != 2 {
		t.Fatalf("error=%v result=%+v run=%+v calls=%d", err, result, run, calls)
	}
	if !bytes.Equal(fileBytes(t, f.options.Source), f.input) || string(fileBytes(t, f.options.Output)) != "external" {
		t.Fatal("failed publication changed source or output")
	}
	failedOutput := f.options.Output
	failedStage := filepath.Join(filepath.Dir(result.Publication.RecordPath), "stage")
	stageBytes := fileBytes(t, failedStage)
	// Preserve the failed run's recovery assets; reuse the same TM in a fresh output parent.
	f.options.Output = filepath.Join(t.TempDir(), "rerun-output")
	result, err = f.core.FileWithTM(context.Background(), f.options, f.adapter, f.profile, store, scope)
	if err != nil || !result.TMCommitted || result.Publication.State != publication.Published || calls != 2 || tmRowCount(t, db) != 2 {
		t.Fatalf("rerun error=%v result=%+v calls=%d", err, result, calls)
	}
	if !bytes.Equal(fileBytes(t, f.options.Output), f.adapter.observed) {
		t.Fatal("rerun did not publish validated bytes")
	}
	if string(fileBytes(t, failedOutput)) != "external" || !bytes.Equal(fileBytes(t, failedStage), stageBytes) {
		t.Fatal("rerun changed the failed target or retained stage")
	}
}

func TestTMCommitDeadlineNoPublication(t *testing.T) {
	for _, mode := range []string{"writer", "reader"} {
		t.Run(mode, func(t *testing.T) {
			store, db := tmStoreFixture(t)
			f := newFileFixture(t, publication.Replace)
			calls := 0
			_, f.profile = canonicalCoreFixture(t, false, "", &calls)
			f.core.limits.DBOperationTimeout = 20 * time.Millisecond
			scope := tmFileScope(t, f)
			var holder *sql.Conn
			var run *testRun
			result, err := f.core.fileWithTM(context.Background(), f.options, f.adapter, f.profile, nil, &tmExecution{store: store, scope: scope}, func(ctx context.Context, options publication.Options) (publicationRun, error) {
				real, err := publication.Prepare(ctx, options)
				if err != nil {
					return nil, err
				}
				run = &testRun{publicationRun: real, afterStage: func() {
					var err error
					holder, err = db.Conn(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					begin := "BEGIN IMMEDIATE"
					if mode == "reader" {
						begin = "BEGIN"
					}
					if _, err := holder.ExecContext(context.Background(), begin); err != nil {
						t.Fatal(err)
					}
					if mode == "reader" {
						var count int
						if err := holder.QueryRowContext(context.Background(), "SELECT count(*) FROM tm").Scan(&count); err != nil {
							t.Fatal(err)
						}
					}
				}}
				return run, nil
			})
			if errors.Is(err, publication.ErrUnsupportedFilesystem) {
				t.Skip("requires qualified ext4/Btrfs TMPDIR")
			}
			if holder == nil {
				t.Fatalf("lock fixture did not reach commit: %v", err)
			}
			if _, rollbackErr := holder.ExecContext(context.Background(), "ROLLBACK"); rollbackErr != nil {
				t.Fatal(rollbackErr)
			}
			if closeErr := holder.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if !errors.Is(err, context.DeadlineExceeded) || result.TMCommitted || result.Publication.State != publication.NotPublished || run.published != 0 || run.closed != 1 || tmRowCount(t, db) != 0 {
				t.Fatalf("error=%v result=%+v run=%+v", err, result, run)
			}
			if !bytes.Equal(fileBytes(t, f.options.Source), f.input) || string(fileBytes(t, f.options.Output)) != "existing" {
				t.Fatal("commit deadline changed source or output")
			}
			var output bytes.Buffer
			result, err = f.core.TextWithTM(context.Background(), f.input, f.adapter, f.profile, store, scope, &output)
			if err != nil || !result.TMCommitted || output.Len() == 0 || tmRowCount(t, db) != 2 {
				t.Fatalf("post-rollback explicit run error=%v result=%+v", err, result)
			}
		})
	}
}

func tmFileScope(t *testing.T, f fileFixture) tm.Scope {
	t.Helper()
	session, err := f.adapter.Import(f.input, f.profile)
	if err != nil {
		t.Fatal(err)
	}
	scope := tmScope()
	for _, key := range f.adapter.input.Keys {
		scope.Keys = append(scope.Keys, artifact.KeyRelation{UnitID: session.Units()[0].ID(), Key: key})
	}
	return scope
}

func TestTMScopeManifestMismatch(t *testing.T) {
	store, db := tmStoreFixture(t)
	fixture := newFileFixture(t, publication.Create)
	calls := 0
	_, fixture.profile = canonicalCoreFixture(t, false, "", &calls)
	var output bytes.Buffer
	result, err := fixture.core.TextWithTM(context.Background(), fixture.input, fixture.adapter, fixture.profile, store, tmScope(), &output)
	if !errors.Is(err, tm.ErrInvalidScope) || result.TMCommitted || output.Len() != 0 || tmRowCount(t, db) != 0 {
		t.Fatalf("error=%v result=%+v output=%q", err, result, output.String())
	}
}

func TestTMFileReuse(t *testing.T) {
	store, db := tmStoreFixture(t)
	fixture := newFileFixture(t, publication.Create)
	calls := 0
	_, fixture.profile = canonicalCoreFixture(t, false, "", &calls)
	fixture.core = NewCore(fakeEngine(func(_ context.Context, _ unit.UnitID, text string) (Generation, error) {
		calls++
		return Generation{Text: text, Finish: Stop}, nil
	}))
	session, err := fixture.adapter.Import(fixture.input, fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	scope := tmScope()
	for _, key := range fixture.adapter.input.Keys {
		scope.Keys = append(scope.Keys, artifact.KeyRelation{UnitID: session.Units()[0].ID(), Key: key})
	}
	for run := range 2 {
		result, err := fixture.core.FileWithTM(context.Background(), fixture.options, fixture.adapter, fixture.profile, store, scope)
		if errors.Is(err, publication.ErrUnsupportedFilesystem) {
			t.Skip("requires qualified ext4/Btrfs TMPDIR")
		}
		if err != nil || !result.TMCommitted || result.Publication.State != publication.Published || calls != 2 || tmRowCount(t, db) != 2 {
			t.Fatalf("run=%d error=%v result=%+v calls=%d", run, err, result, calls)
		}
		fixture.options.Mode = publication.Replace
	}
	if _, err := fixture.core.FileWithTM(context.Background(), fixture.options, fixture.adapter, fixture.profile, nil, scope); !errors.Is(err, ErrNilTMStore) {
		t.Fatalf("nil Store error=%v", err)
	}
}
