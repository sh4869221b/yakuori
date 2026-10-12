package localize

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func tmStoreFixture(t *testing.T) (*tm.Store, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memory.db")
	store, err := tm.Create(context.Background(), path, config.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return store, db
}

func tmScope() tm.Scope {
	return tm.Scope{GameNamespace: "fixture-game", ArtifactNamespace: "fixture-artifact"}
}

func seedTMRow(t *testing.T, db *sql.DB, session unit.Session, profile validate.Profile, index int, text string) {
	t.Helper()
	scope := tmScope()
	source, err := unit.NewSourceIdentity(session, session.Units()[index].ID(), scope.GameNamespace, scope.ArtifactNamespace, nil)
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

func tmRowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM tm").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestTMHitRevalidation(t *testing.T) {
	store, db := tmStoreFixture(t)
	calls := 0
	_, profile := canonicalCoreFixture(t, false, "", &calls)
	spans := map[int][]unit.ProtectionSpan{0: {{Start: 6, End: 9, Kind: unit.Placeholder}}, 2: {{Start: 0, End: 3, Kind: unit.Placeholder}}}
	old, _ := generationFixture(t, []string{"Hello {p}", "", "{p}"}, spans)
	for i, text := range []string{"translated {p}", "", "{p}"} {
		seedTMRow(t, db, old, profile, i, text)
	}
	fresh, _ := generationFixture(t, []string{"Hello {p}", "", "{p}"}, spans)
	core := NewCore(fakeEngine(func(context.Context, unit.UnitID, string) (Generation, error) {
		calls++
		return Generation{}, errors.New("hit generated")
	}))
	memory := &tmExecution{store: store, scope: tmScope()}
	accepted, err := core.generate(context.Background(), fresh, profile, memory)
	if err != nil || calls != 0 || len(accepted) != 3 || accepted[0].Text() != "translated {p}" {
		t.Fatalf("error=%v calls=%d accepted=%v", err, calls, accepted)
	}
	for _, row := range accepted {
		if err := validate.CheckBinding(fresh, row.UnitID(), profile, row); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(validate.CheckBinding(old, row.UnitID(), profile, row), validate.ErrBindingMismatch) {
			t.Fatal("candidate retained old import binding")
		}
	}
	if tmRowCount(t, db) != 3 {
		t.Fatal("lookup changed rows")
	}
}

func TestTMInvalidRowGeneration(t *testing.T) {
	store, db := tmStoreFixture(t)
	calls := 0
	_, profile := canonicalCoreFixture(t, false, "", &calls)
	session, _ := generationFixture(t, []string{"one", "two", "three"}, nil)
	seedTMRow(t, db, session, profile, 0, "cached")
	seedTMRow(t, db, session, profile, 1, "")
	core := NewCore(fakeEngine(func(_ context.Context, _ unit.UnitID, text string) (Generation, error) {
		calls++
		return Generation{Text: text, Finish: Stop}, nil
	}))
	memory := &tmExecution{store: store, scope: tmScope()}
	accepted, err := core.generate(context.Background(), session, profile, memory)
	if err != nil || calls != 2 || len(accepted) != 3 || accepted[0].Text() != "cached" {
		t.Fatalf("error=%v calls=%d accepted=%v", err, calls, accepted)
	}
	if err := memory.run.Stage(accepted); err != nil {
		t.Fatal(err)
	}
	manifest, err := artifact.NewManifest(session, profile, "fixture", "v1", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.run.CheckManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if err := memory.run.Commit(context.Background(), session, profile, accepted); err != nil {
		t.Fatal(err)
	}
	var empty int
	if err := db.QueryRow("SELECT count(*) FROM tm WHERE translated_text=''").Scan(&empty); err != nil || empty != 0 || tmRowCount(t, db) != 3 {
		t.Fatalf("error=%v empty rows=%d", err, empty)
	}
}

func TestTMSegmentedParent(t *testing.T) {
	store, db := tmStoreFixture(t)
	calls := 0
	core, profile := canonicalCoreFixture(t, true, "", &calls)
	for run := range 2 {
		var output bytes.Buffer
		result, err := core.TextWithTM(context.Background(), []byte("one two three"), textadapter.New("en", "ja"), profile, store, tmScope(), &output)
		if err != nil || !result.TMCommitted || output.String() != "one two three" || calls != 3 || tmRowCount(t, db) != 1 {
			t.Fatalf("run=%d error=%v result=%+v calls=%d output=%q", run, err, result, calls, output.String())
		}
	}
	var text string
	if err := db.QueryRow("SELECT translated_text FROM tm").Scan(&text); err != nil || text != "one two three" {
		t.Fatalf("error=%v parent=%q", err, text)
	}
}

func stageTMRun(t *testing.T, memory *tmExecution, session unit.Session, profile validate.Profile, accepted []validate.AcceptedTranslation) {
	t.Helper()
	if err := memory.run.Stage(accepted); err != nil {
		t.Fatal(err)
	}
	manifest, err := artifact.NewManifest(session, profile, "fixture", "v1", memory.scope.Keys, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.run.CheckManifest(manifest); err != nil {
		t.Fatal(err)
	}
}

func TestTMConcurrentRuns(t *testing.T) {
	store, db := tmStoreFixture(t)
	calls := 0
	_, profile := canonicalCoreFixture(t, false, "", &calls)
	core := NewCore(fakeEngine(func(_ context.Context, _ unit.UnitID, text string) (Generation, error) {
		calls++
		return Generation{Text: text, Finish: Stop}, nil
	}))
	left, _ := generationFixture(t, []string{"left only", "shared"}, nil)
	right, _ := generationFixture(t, []string{"right only", "shared"}, nil)
	leftRun, rightRun := &tmExecution{store: store, scope: tmScope()}, &tmExecution{store: store, scope: tmScope()}
	leftAccepted, err := core.generate(context.Background(), left, profile, leftRun)
	if err != nil {
		t.Fatal(err)
	}
	rightAccepted, err := core.generate(context.Background(), right, profile, rightRun)
	if err != nil {
		t.Fatal(err)
	}
	rightAccepted[1], err = validate.Validate(right, right.Units()[1].ID(), profile, "first valid winner")
	if err != nil {
		t.Fatal(err)
	}
	stageTMRun(t, leftRun, left, profile, leftAccepted)
	stageTMRun(t, rightRun, right, profile, rightAccepted)
	if tmRowCount(t, db) != 0 {
		t.Fatal("interleaved runs wrote before commit")
	}
	if err := rightRun.run.Commit(context.Background(), right, profile, rightAccepted); err != nil {
		t.Fatal(err)
	}
	if err := leftRun.run.Commit(context.Background(), left, profile, leftAccepted); !errors.Is(err, tm.ErrConflict) {
		t.Fatalf("unobserved key conflict=%v", err)
	}
	var partial int
	if err := db.QueryRow("SELECT count(*) FROM tm WHERE translated_text='left only'").Scan(&partial); err != nil || partial != 0 || tmRowCount(t, db) != 2 {
		t.Fatalf("atomic rollback error=%v partial=%d", err, partial)
	}
	fresh, _ := generationFixture(t, []string{"left only", "shared"}, nil)
	rerun := &tmExecution{store: store, scope: tmScope()}
	calls = 0
	accepted, err := core.generate(context.Background(), fresh, profile, rerun)
	if err != nil || calls != 1 || len(accepted) != 2 || accepted[1].Text() != "first valid winner" {
		t.Fatalf("rerun error=%v calls=%d accepted=%v", err, calls, accepted)
	}
	accepted[1], err = validate.Validate(fresh, fresh.Units()[1].ID(), profile, "different valid value")
	if err != nil {
		t.Fatal(err)
	}
	stageTMRun(t, rerun, fresh, profile, accepted)
	if err := rerun.run.Commit(context.Background(), fresh, profile, accepted); err != nil {
		t.Fatal(err)
	}
	var winners int
	if err := db.QueryRow("SELECT count(*) FROM tm WHERE translated_text='first valid winner'").Scan(&winners); err != nil || winners != 1 || tmRowCount(t, db) != 3 {
		t.Fatalf("first valid writer replaced: error=%v winners=%d", err, winners)
	}
}

func tmCapacityProfile(t *testing.T) validate.Profile {
	t.Helper()
	calls := 0
	core, _ := canonicalCoreFixture(t, false, "", &calls)
	info, err := core.inference.engine.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	info.ContextTokens = config.DefaultLimits().ContextTokens
	base, err := core.inference.buildRequest(context.Background(), unit.UnitID{}, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	limits := config.DefaultLimits()
	policy, err := inference.NewGenerationPolicy(1, limits.MaxOutputTokens, limits.RequestTimeout)
	if err != nil {
		t.Fatal(err)
	}
	request, err := inference.NewGenerationRequest(inference.PreparedRequest{TokenIDs: make([]int, limits.UnitTextBytes), Identity: base.Identity(), StopIDs: []int{42}}, policy)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewCanonicalInferenceProfile(info, request, InferenceProfileInput{ModelID: "synthetic-capacity-fixture", Glossary: identity.GlossaryInput{Mode: "none"}, Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestTMAdoptedCapacity(t *testing.T) {
	limits := config.DefaultLimits()
	for _, shape := range []string{"count-bound", "byte-bound"} {
		t.Run(shape, func(t *testing.T) {
			store, db := tmStoreFixture(t)
			var path string
			var sequence int
			var database string
			if err := db.QueryRow("PRAGMA database_list").Scan(&sequence, &database, &path); err != nil {
				t.Fatal(err)
			}
			var fs unix.Statfs_t
			if err := unix.Statfs(path, &fs); err != nil {
				t.Fatal(err)
			}
			if fs.Type != unix.BTRFS_SUPER_MAGIC && fs.Type != unix.EXT4_SUPER_MAGIC {
				t.Skipf("capacity requires persistent ext4/Btrfs TMPDIR; filesystem=0x%x", fs.Type)
			}
			sources := []string{strings.Repeat("s", limits.UnitTextBytes), strings.Repeat("t", limits.TotalTextBytes-limits.UnitTextBytes)}
			if shape == "count-bound" {
				sources = make([]string, limits.Units)
				for i := range sources {
					sources[i] = strings.Repeat("s", limits.TotalTextBytes/limits.Units)
				}
			}
			units := make([]unit.TranslationUnit, len(sources))
			total := 0
			for i, source := range sources {
				id, err := unit.NewUnitID("fixture", "v1", fmt.Sprintf("record-%02d", i))
				if err != nil {
					t.Fatal(err)
				}
				units[i], err = unit.NewTranslationUnit(id, []byte(source), "en", "ja", nil)
				if err != nil {
					t.Fatal(err)
				}
				total += len(source)
			}
			input := []byte(strings.Join(sources, ""))
			input = append(input, bytes.Repeat([]byte{' '}, limits.ArtifactBytes-len(input))...)
			session, err := unit.NewSession(input, units)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewCore(nil).inputUnits(session); err != nil {
				t.Fatal(err)
			}
			profile := tmCapacityProfile(t)
			memory := &tmExecution{store: store, scope: tmScope()}
			if err := memory.begin(session, profile); err != nil {
				t.Fatal(err)
			}
			accepted := make([]validate.AcceptedTranslation, len(units))
			output := strings.Repeat("x", limits.MaxOutputTokens)
			for i, u := range units {
				if _, found, err := memory.run.Lookup(context.Background(), u.ID()); err != nil || found {
					t.Fatalf("fresh lookup found=%v error=%v", found, err)
				}
				accepted[i], err = validate.Validate(session, u.ID(), profile, output)
				if err != nil {
					t.Fatal(err)
				}
			}
			stageTMRun(t, memory, session, profile, accepted)
			start := time.Now()
			if err := memory.run.Commit(context.Background(), session, profile, accepted); err != nil {
				t.Fatal(err)
			}
			elapsed := time.Since(start)
			if elapsed >= limits.DBOperationTimeout || tmRowCount(t, db) != len(units) {
				t.Fatalf("capacity failed: elapsed=%s rows=%d", elapsed, tmRowCount(t, db))
			}
			var sourceBytes, profileBytes, textBytes int
			if err := db.QueryRow("SELECT sum(length(source_identity)),sum(length(translation_profile)),sum(length(CAST(translated_text AS BLOB))) FROM tm").Scan(&sourceBytes, &profileBytes, &textBytes); err != nil {
				t.Fatal(err)
			}
			if textBytes != len(output)*len(units) {
				t.Fatalf("stored restored bytes=%d", textBytes)
			}
			stat, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("host=%s/%s runtime=%s filesystem=0x%x cache=new DB,warm OS caches; no eviction; no inference; rows=%d artifact=%d source_text=%d max_unit=%d canonical_source_BLOB=%d canonical_profile_BLOB=%d restored_bytes=%d db_bytes=%d conn_acquire_plus_commit=%s budget=%s", runtime.GOOS, runtime.GOARCH, runtime.Version(), fs.Type, len(units), len(input), total, len(sources[0]), sourceBytes, profileBytes, textBytes, stat.Size(), elapsed, limits.DBOperationTimeout)
			t.Logf("synthetic ASCII fixture: declared output ceiling=%d tokens/request, fixture=%d bytes/request; actual model/tokenizer maximum bytes and slow supported filesystem latency remain UNVERIFIED", limits.MaxOutputTokens, len(output))
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := tm.Open(context.Background(), path, limits)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := reopened.Close(); err != nil {
					t.Error(err)
				}
			})
			fresh, err := unit.NewSession(input, units)
			if err != nil {
				t.Fatal(err)
			}
			rerun, err := reopened.BeginRun(fresh, profile, tmScope())
			if err != nil {
				t.Fatal(err)
			}
			for _, u := range units {
				text, found, err := rerun.Lookup(context.Background(), u.ID())
				if err != nil || !found || text != output {
					t.Fatalf("reopened lookup found=%v error=%v bytes=%d", found, err, len(text))
				}
			}
		})
	}
}
