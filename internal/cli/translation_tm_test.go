package cli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/unit"
)

type translationTMRow struct {
	Schema  int    `json:"schema"`
	Source  []byte `json:"source_identity"`
	Profile []byte `json:"translation_profile"`
	Text    string `json:"translated_text"`
}

func readTranslationTM(t *testing.T, paths config.Paths) []translationTMRow {
	t.Helper()
	path := config.ResolveStoragePaths(paths).TMFile
	result := []translationTMRow{}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return result
	} else if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	rows, err := db.Query("SELECT key_schema, source_identity, translation_profile, translated_text FROM tm ORDER BY source_identity, translation_profile")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	for rows.Next() {
		var row translationTMRow
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

type translationTMInput struct {
	command translationCommand
	modelID string
	source  []byte
}

// Build the expected key from the fixture input, not from persisted row fields.
func expectedTranslationKey(t *testing.T, input translationTMInput, engine inference.Engine) identity.ExactKey {
	t.Helper()
	ctx := context.Background()
	info, err := engine.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	limits := config.DefaultLimits()
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, limits.MaxOutputTokens, limits.RequestTimeout)
	if err != nil {
		t.Fatal(err)
	}
	request, err := engine.(requestFactory).NewRequest(ctx, prompt.Input{
		SourceLanguage: input.command.sourceLanguage, TargetLanguage: input.command.targetLanguage,
	}, policy)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := localize.NewCanonicalInferenceProfile(info, request, localize.InferenceProfileInput{
		ModelID: input.modelID, Glossary: identity.GlossaryInput{Mode: "none"}, Limits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := textadapter.New(input.command.sourceLanguage, input.command.targetLanguage).Import(input.source, profile)
	if err != nil {
		t.Fatal(err)
	}
	source, err := unit.NewSourceIdentity(session, session.Units()[0].ID(),
		input.command.scope.GameNamespace, input.command.scope.ArtifactNamespace, nil)
	if err != nil {
		t.Fatal(err)
	}
	key, err := profile.ExactKey(source)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func (row translationTMRow) matches(key identity.ExactKey, text string) bool {
	return row.Schema == key.Schema() && bytes.Equal(row.Source, key.SourceBytes()) &&
		bytes.Equal(row.Profile, key.ProfileBytes()) && row.Text == text
}

func translationTMContains(rows []translationTMRow, key identity.ExactKey, text string) bool {
	for _, row := range rows {
		if row.matches(key, text) {
			return true
		}
	}
	return false
}
