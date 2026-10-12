package localize

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/config"
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
