//go:build linux

package tm

import (
	"context"
	"errors"
	"testing"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func runProfile(t *testing.T, model string) validate.Profile {
	t.Helper()
	profile, err := validate.NewCanonicalProfile(identity.ProfileInput{
		Version: 1, ModelID: model, ModelDigest: [32]byte{1}, Backend: "cpu", BackendPin: "v1", ComputeQuant: "q4", KVPrecision: "not-applicable",
		Tokenizer: identity.TokenizerInput{ModelDigest: [32]byte{1}, BackendPin: "v1"}, Template: identity.TemplateInput{Family: "chat", Source: "embedded", RendererVersion: "v1"},
		Prompt:       identity.PromptInput{Schema: 1, Variant: "plain", SystemTemplate: []byte("system"), UserTemplate: []byte("user"), LanguageAliases: "v1"},
		Generation:   identity.GenerationInput{Schema: 1, MaxOutputTokens: 32, RequestTimeoutNS: 1, SeedFixed: true, Thinking: "disabled", Prefill: identity.PrefillInput{Path: "cpu"}},
		Segmentation: identity.SegmentationInput{Mode: "enabled", Schema: "v1", BoundaryFixtures: "v1", UnitTextBytes: 980, Segments: 76},
		Glossary:     identity.GlossaryInput{Mode: "none"}, Protection: identity.SchemaInput{Schema: "v1", FixtureSet: "v1"}, Validation: identity.SchemaInput{Schema: "v1", FixtureSet: "v1"}, Context: identity.ContextInput{Tokens: 4096, Mode: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func runSession(t *testing.T, sources ...string) unit.Session {
	t.Helper()
	units := make([]unit.TranslationUnit, len(sources))
	for i, source := range sources {
		id, err := unit.NewUnitID("fixture", "v1", string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		units[i], err = unit.NewTranslationUnit(id, []byte(source), "en", "ja", nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	session, err := unit.NewSession([]byte("original artifact"), units)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func beginFixture(t *testing.T, s *Store, session unit.Session, profile validate.Profile, scope Scope) *Run {
	t.Helper()
	run, err := s.BeginRun(session, profile, scope)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func testScope() Scope { return Scope{GameNamespace: "game", ArtifactNamespace: "artifact"} }

func seedCandidate(t *testing.T, r *Run, id unit.UnitID, text string) {
	t.Helper()
	key := r.keys[id]
	storeExec(t, r.store.db, `INSERT INTO tm VALUES(?,?,?,?)`, key.Schema(), key.SourceBytes(), key.ProfileBytes(), text)
}

func rowCount(t *testing.T, s *Store) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM tm").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestRunExactLookup(t *testing.T) {
	_, s := storeFixture(t)
	session, profile := runSession(t, "source"), runProfile(t, "model")
	id := session.Units()[0].ID()
	scope := testScope()
	scope.Keys = []artifact.KeyRelation{{UnitID: id, Key: ""}, {UnitID: id, Key: string([]byte{0xff, 0})}, {UnitID: id, Key: ""}}
	run := beginFixture(t, s, session, profile, scope)
	seedCandidate(t, run, id, "訳文")
	scope.Keys[0].Key = "mutated"
	fresh := runSession(t, "source")
	same := beginFixture(t, s, fresh, profile, run.scope)
	if run.keys[id] != same.keys[id] {
		t.Fatal("import identity leaked into exact key")
	}
	text, found, err := same.Lookup(context.Background(), id)
	if err != nil || !found || text != "訳文" {
		t.Fatalf("lookup = %q %v %v", text, found, err)
	}
	accepted, err := validate.Validate(session, id, profile, text)
	if err != nil {
		t.Fatal(err)
	}
	if err := validate.CheckBinding(fresh, id, profile, accepted); !errors.Is(err, validate.ErrBindingMismatch) {
		t.Fatalf("binding = %v", err)
	}
	for _, name := range []string{"source", "profile", "game", "artifact", "key", "multiplicity"} {
		t.Run(name, func(t *testing.T) {
			input, p, sc := session, profile, run.scope
			sc.Keys = append([]artifact.KeyRelation(nil), sc.Keys...)
			switch name {
			case "source":
				input = runSession(t, "changed")
			case "profile":
				p = runProfile(t, "other")
			case "game":
				sc.GameNamespace = "other"
			case "artifact":
				sc.ArtifactNamespace = "other"
			case "key":
				sc.Keys[1].Key = string([]byte{0xfe, 0})
			case "multiplicity":
				sc.Keys = sc.Keys[:2]
			}
			r := beginFixture(t, s, input, p, sc)
			if _, found, err := r.Lookup(context.Background(), id); err != nil || found {
				t.Fatalf("exact miss = %v %v", found, err)
			}
		})
	}
	if count := rowCount(t, s); count != 1 {
		t.Fatalf("rows = %d", count)
	}
}

func stageFixture(t *testing.T, r *Run, texts ...string) []validate.AcceptedTranslation {
	t.Helper()
	accepted := make([]validate.AcceptedTranslation, len(texts))
	for i, u := range r.session.Units() {
		if _, _, err := r.Lookup(context.Background(), u.ID()); err != nil {
			t.Fatal(err)
		}
		a, err := validate.Validate(r.session, u.ID(), r.profile, texts[i])
		if err != nil {
			t.Fatal(err)
		}
		accepted[i] = a
	}
	return accepted
}

func TestRunStageAccepted(t *testing.T) {
	_, s := storeFixture(t)
	session, profile := runSession(t, "source", "other"), runProfile(t, "model")
	run := beginFixture(t, s, session, profile, testScope())
	accepted := stageFixture(t, run, "訳文", "別訳")
	if err := run.Stage(accepted); err != nil {
		t.Fatal(err)
	}
	accepted[0] = validate.AcceptedTranslation{}
	if run.staged[0].Text() != "訳文" {
		t.Fatal("staging did not snapshot accepted set")
	}
	other := beginFixture(t, s, session, profile, testScope())
	if other.staged != nil || len(other.observed) != 0 {
		t.Fatal("run state leaked")
	}
	if count := rowCount(t, s); count != 0 {
		t.Fatalf("staging wrote %d rows", count)
	}
	manifest, err := artifact.NewManifest(session, profile, "fixture", "v1", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.CheckManifest(manifest); err != nil {
		t.Fatal(err)
	}
	mismatch, err := artifact.NewManifest(session, profile, "fixture", "v1", []artifact.KeyRelation{{UnitID: session.Units()[0].ID(), Key: ""}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.CheckManifest(mismatch); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("manifest mismatch = %v", err)
	}
}

func TestRunRejectBindingAndScope(t *testing.T) {
	_, s := storeFixture(t)
	session, profile := runSession(t, "source", "other"), runProfile(t, "model")
	unknown, err := unit.NewUnitID("fixture", "v1", "segment")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := validate.NewProfile([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		scope   Scope
		profile validate.Profile
		want    error
	}{
		{Scope{ArtifactNamespace: "artifact"}, profile, ErrInvalidScope},
		{Scope{GameNamespace: "game"}, profile, ErrInvalidScope},
		{Scope{GameNamespace: "game", ArtifactNamespace: "artifact", Keys: []artifact.KeyRelation{{UnitID: unknown}}}, profile, ErrInvalidScope},
		{testScope(), legacy, identity.ErrInvalidKey},
	} {
		if _, err := s.BeginRun(session, tt.profile, tt.scope); !errors.Is(err, tt.want) {
			t.Fatalf("scope error = %v", err)
		}
	}
	for _, name := range []string{"missing", "zero", "different import", "segment"} {
		t.Run(name, func(t *testing.T) {
			r := beginFixture(t, s, session, profile, testScope())
			accepted := stageFixture(t, r, "訳文", "別訳")
			var want error
			switch name {
			case "missing":
				accepted = accepted[:1]
				want = artifact.ErrIncompleteExport
			case "zero":
				accepted[0] = validate.AcceptedTranslation{}
				want = unit.ErrUnknownUnit
			case "different import":
				fresh := runSession(t, "source", "other")
				accepted[0], err = validate.Validate(fresh, fresh.Units()[0].ID(), profile, "訳文")
				if err != nil {
					t.Fatal(err)
				}
				want = validate.ErrBindingMismatch
			case "segment":
				segment, err := unit.NewTranslationUnit(unknown, []byte("source"), "en", "ja", nil)
				if err != nil {
					t.Fatal(err)
				}
				child, err := unit.NewSession([]byte("original artifact"), []unit.TranslationUnit{segment})
				if err != nil {
					t.Fatal(err)
				}
				accepted[0], err = validate.Validate(child, unknown, profile, "訳文")
				if err != nil {
					t.Fatal(err)
				}
				want = unit.ErrUnknownUnit
			}
			if err := r.Stage(accepted); !errors.Is(err, want) {
				t.Fatalf("stage error = %v; want %v", err, want)
			}
		})
	}
	if count := rowCount(t, s); count != 0 {
		t.Fatalf("rejection wrote %d rows", count)
	}
}

func TestRunLookupFailure(t *testing.T) {
	_, s := storeFixture(t)
	session := runSession(t, "source")
	run := beginFixture(t, s, session, runProfile(t, "model"), testScope())
	id := session.Units()[0].ID()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := run.Lookup(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	if len(run.observed) != 0 {
		t.Fatal("read error recorded as miss")
	}
	if err := run.RecordValidation(id, false); !errors.Is(err, ErrRunState) {
		t.Fatalf("unobserved validation = %v", err)
	}
	accepted := stageFixture(t, run, "訳文")
	other := beginFixture(t, s, session, run.profile, testScope())
	if err := other.Stage(accepted); !errors.Is(err, ErrRunState) {
		t.Fatalf("unobserved stage = %v", err)
	}
	seedCandidate(t, other, id, "")
	if _, found, err := other.Lookup(context.Background(), id); err != nil || !found {
		t.Fatalf("empty candidate = %v %v", found, err)
	}
	if err := other.Stage(accepted); !errors.Is(err, ErrRunState) {
		t.Fatalf("unchecked candidate = %v", err)
	}
	if err := other.RecordValidation(id, false); err != nil {
		t.Fatal(err)
	}
	if err := other.Stage(accepted); err != nil {
		t.Fatal(err)
	}
	if count := rowCount(t, s); count != 1 {
		t.Fatalf("rows = %d", count)
	}
	storeExec(t, s.db, "DROP TABLE tm")
	broken := beginFixture(t, s, session, run.profile, testScope())
	if _, _, err := broken.Lookup(context.Background(), id); err == nil {
		t.Fatal("SQL error became miss")
	}
	if len(broken.observed) != 0 {
		t.Fatal("SQL failure recorded as miss")
	}
}
