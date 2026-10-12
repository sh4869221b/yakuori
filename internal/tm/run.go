//go:build linux

package tm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

var (
	ErrInvalidScope = errors.New("invalid translation run scope")
	ErrRunState     = errors.New("translation run is not ready")
)

type Scope struct {
	GameNamespace     string
	ArtifactNamespace string
	Keys              []artifact.KeyRelation
}

type observation struct {
	text    string
	found   bool
	checked bool
	valid   bool
}

// Run belongs to one import and is used sequentially by its caller.
// Independent runs retain their own candidates and accepted translations.
type Run struct {
	store           *Store
	session         unit.Session
	profile         validate.Profile
	scope           Scope
	keys            map[unit.UnitID]identity.ExactKey
	observed        map[unit.UnitID]observation
	staged          []validate.AcceptedTranslation
	manifestChecked bool
	finished        bool
}

func (s *Store) BeginRun(session unit.Session, profile validate.Profile, scope Scope) (*Run, error) {
	if err := session.Check(); err != nil {
		return nil, err
	}
	if len(profile.CanonicalBytes()) == 0 {
		return nil, identity.ErrInvalidKey
	}
	if scope.GameNamespace == "" || scope.ArtifactNamespace == "" {
		return nil, ErrInvalidScope
	}
	scope.Keys = slices.Clone(scope.Keys)
	relations := make(map[unit.UnitID][][]byte)
	for _, relation := range scope.Keys {
		if _, err := session.Unit(relation.UnitID); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidScope, err)
		}
		relations[relation.UnitID] = append(relations[relation.UnitID], []byte(relation.Key))
	}
	run := &Run{store: s, session: session, profile: profile, scope: scope, keys: make(map[unit.UnitID]identity.ExactKey), observed: make(map[unit.UnitID]observation)}
	for _, u := range session.Units() {
		source, err := unit.NewSourceIdentity(session, u.ID(), scope.GameNamespace, scope.ArtifactNamespace, relations[u.ID()])
		if err != nil {
			return nil, err
		}
		key, err := profile.ExactKey(source)
		if err != nil {
			return nil, err
		}
		run.keys[u.ID()] = key
	}
	return run, nil
}

func (r *Run) Lookup(ctx context.Context, id unit.UnitID) (string, bool, error) {
	if r.finished || r.staged != nil {
		return "", false, ErrRunState
	}
	key, exists := r.keys[id]
	if !exists {
		return "", false, unit.ErrUnknownUnit
	}
	ctx, cancel := context.WithTimeout(ctx, r.store.limits.DBOperationTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if previous, exists := r.observed[id]; exists {
		return previous.text, previous.found, nil
	}
	var text string
	err := r.store.db.QueryRowContext(ctx, `SELECT translated_text FROM tm WHERE key_schema=? AND source_identity=? AND translation_profile=?`, key.Schema(), key.SourceBytes(), key.ProfileBytes()).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		r.observed[id] = observation{}
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	r.observed[id] = observation{text: text, found: true}
	return text, true, nil
}

// RecordValidation records the caller's common Validation result for the exact
// candidate returned by Lookup. Storage never promotes a row to Accepted.
func (r *Run) RecordValidation(id unit.UnitID, valid bool) error {
	if r.finished || r.staged != nil {
		return ErrRunState
	}
	observed, exists := r.observed[id]
	if !exists || !observed.found || observed.checked {
		return ErrRunState
	}
	observed.checked, observed.valid = true, valid
	r.observed[id] = observed
	return nil
}

func (r *Run) Stage(accepted []validate.AcceptedTranslation) error {
	if r.finished || r.staged != nil {
		return ErrRunState
	}
	if _, err := artifact.PrepareExport(r.session, r.profile, accepted); err != nil {
		return err
	}
	for id := range r.keys {
		observed, exists := r.observed[id]
		if !exists || (observed.found && !observed.checked) {
			return ErrRunState
		}
	}
	r.staged = slices.Clone(accepted)
	// Preserve a staged empty session distinctly from a run that was never staged.
	if r.staged == nil {
		r.staged = []validate.AcceptedTranslation{}
	}
	return nil
}

func (r *Run) CheckManifest(manifest artifact.Manifest) error {
	r.manifestChecked = false
	if r.finished {
		return ErrRunState
	}
	if err := manifest.CheckInput(r.session); err != nil {
		return err
	}
	expected := make(map[artifact.KeyRelation]int)
	actual := make(map[artifact.KeyRelation]int)
	for _, relation := range r.scope.Keys {
		expected[relation]++
	}
	for _, relation := range manifest.KeyRelations() {
		actual[relation]++
	}
	if !maps.Equal(expected, actual) {
		return fmt.Errorf("%w: manifest key relations", ErrInvalidScope)
	}
	r.manifestChecked = true
	return nil
}
