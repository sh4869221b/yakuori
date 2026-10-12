//go:build linux

package localize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func TestTMFailureGates(t *testing.T) {
	for _, condition := range []string{"database read", "later unit", "bad source", "profile mismatch", "final artifact", "nil store"} {
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
		})
	}
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
		if err != nil || !result.TMCommitted || result.Publication.State != publication.Published || calls != 2 || tmRowCount(t, db) != 2 {
			t.Fatalf("run=%d error=%v result=%+v calls=%d", run, err, result, calls)
		}
		fixture.options.Mode = publication.Replace
	}
	if _, err := fixture.core.FileWithTM(context.Background(), fixture.options, fixture.adapter, fixture.profile, nil, scope); !errors.Is(err, ErrNilTMStore) {
		t.Fatalf("nil Store error=%v", err)
	}
}
