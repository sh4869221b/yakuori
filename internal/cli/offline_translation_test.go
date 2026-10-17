//go:build offlinesmoke

package cli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/models"
)

const offlineTranslationSource = "Hello.\n"
const offlineTranslationMemory = "記憶した訳文。\n"

func offlineTranslationText(text string) bool {
	return utf8.ValidString(text) && strings.TrimSpace(text) != "" && text != offlineTranslationSource &&
		strings.ContainsFunc(text, func(r rune) bool {
			return unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Han)
		})
}

// Read the actual stored key and bytes; a row count cannot establish identity.
func offlineTranslationKeys(t *testing.T, l *offlineSmokeLayout) map[string]identity.ExactKey {
	t.Helper()
	id, engine, err := models.New(l.paths).OpenSelected(context.Background(), offlineSmokeBackend(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := engine.Close(); err != nil {
			t.Error(err)
		}
	}()
	keys := make(map[string]identity.ExactKey)
	for _, artifact := range []string{"sample", "other-scope", "publication"} {
		command, _, err := parseTranslation("translate", translationArgs("translate",
			"--artifact", artifact, "--backend", offlineSmokeBackend(t), "source")[1:])
		if err != nil {
			t.Fatal(err)
		}
		keys[artifact] = expectedTranslationKey(t, translationTMInput{
			command: command, modelID: id, source: []byte(offlineTranslationSource),
		}, engine)
	}
	return keys
}

func offlineTranslationSeed(t *testing.T, l *offlineSmokeLayout, key identity.ExactKey) bool {
	t.Helper()
	if len(readTranslationTM(t, l.paths)) == 0 {
		return false // Preserve aggregate evidence for an empty-success mutant.
	}
	db, err := sql.Open("sqlite", "file:"+l.storage.TMFile+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	result, err := db.Exec("UPDATE tm SET translated_text=? WHERE key_schema=? AND source_identity=? AND translation_profile=?",
		offlineTranslationMemory, key.Schema(), key.SourceBytes(), key.ProfileBytes())
	if err != nil {
		t.Fatal(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// Called while the managed model is intact, before the original corruption cases.
func offlineTranslationMatrix(t *testing.T, l *offlineSmokeLayout) {
	t.Helper()
	source, output := filepath.Join(l.root, "source.txt"), filepath.Join(l.root, "translated.txt")
	writeFixture(t, source, offlineTranslationSource)
	keys := offlineTranslationKeys(t, l)
	var runs []offlineSmokeRun
	completed := false
	defer func() {
		if !completed {
			recordOfflineSmokeEvidence(t, "matrix-translation", map[string]any{
				"kind": "real_model_cli", "completed": false, "pass": false,
				"layout": l.describe(), "runs": runs, "tm": readTranslationTM(t, l.paths),
			})
		}
	}()
	run := func(name string, expect int, args ...string) offlineSmokeRun {
		got := l.run(t, name, expect, args...)
		runs = append(runs, got)
		return got
	}
	args := func(action, artifact string, tail ...string) []string {
		return translationArgs(action, append([]string{"--artifact", artifact, "--backend", offlineSmokeBackend(t)}, tail...)...)
	}
	cold := run("translation-cold", 0, args("translate", "sample", source)...)
	coldRows := readTranslationTM(t, l.paths)
	seeded := offlineTranslationSeed(t, l, keys["sample"])
	hit := run("translation-same-scope", 0, args("translate", "sample", source)...)
	hitRows := readTranslationTM(t, l.paths)
	miss := run("translation-other-scope", 0, args("translate", "other-scope", source)...)
	missRows := readTranslationTM(t, l.paths)
	created := run("localize-cold-create", 0, args("localize", "publication", "--output", output, source)...)
	createBytes, createErr := os.ReadFile(output)
	createRows := readTranslationTM(t, l.paths)
	rejected := run("localize-existing-create", 1, args("localize", "publication", "--output", output, source)...)
	rejectedBytes, rejectErr := os.ReadFile(output)
	rejectRows := readTranslationTM(t, l.paths)
	writeFixture(t, output, "old output bytes\n")
	replaced := run("localize-replace", 0, args("localize", "publication", "--output", output, "--replace-output", source)...)
	replaceBytes, replaceErr := os.ReadFile(output)
	replaceRows := readTranslationTM(t, l.paths)
	inPlace := run("localize-in-place", 0, args("localize", "publication", "--in-place", source)...)
	inPlaceBytes, inPlaceErr := os.ReadFile(source)
	backups, err := filepath.Glob(filepath.Join(l.root, ".yakuori-backup-*"))
	if err != nil {
		t.Fatal(err)
	}
	var backup []byte
	if len(backups) == 1 {
		backup, err = os.ReadFile(backups[0])
		if err != nil {
			t.Fatal(err)
		}
	}
	finalRows := readTranslationTM(t, l.paths)
	checks := []map[string]any{
		{"name": "cold_translate_generates_nonempty_japanese_not_echo", "pass": offlineTranslationText(cold.Stdout) && cold.Stderr == ""},
		{"name": "cold_translate_canonical_key_and_text", "pass": len(coldRows) == 1 && translationTMContains(coldRows, keys["sample"], cold.Stdout)},
		{"name": "same_scope_reuses_distinct_valid_persistent_text", "pass": seeded && hit.Stdout == offlineTranslationMemory && len(hitRows) == 1 && translationTMContains(hitRows, keys["sample"], offlineTranslationMemory)},
		{"name": "other_scope_misses_and_keeps_both_exact_keys", "pass": offlineTranslationText(miss.Stdout) && miss.Stdout != offlineTranslationMemory && len(missRows) == 2 &&
			translationTMContains(missRows, keys["other-scope"], miss.Stdout) && translationTMContains(missRows, keys["sample"], offlineTranslationMemory)},
		{"name": "cold_localize_create_publishes_canonical_translation", "pass": createErr == nil && offlineTranslationText(string(createBytes)) && created.Stdout == "" && created.Stderr == "" &&
			len(createRows) == 3 && translationTMContains(createRows, keys["publication"], string(createBytes))},
		{"name": "create_existing_rejected_without_output_or_tm_change", "pass": rejectErr == nil && bytes.Equal(createBytes, rejectedBytes) && reflect.DeepEqual(createRows, rejectRows) &&
			rejected.Stdout == "" && strings.Contains(rejected.Stderr, "prepare failed") && strings.Contains(rejected.Stderr, "TMCommitted=false")},
		{"name": "replace_publishes_exact_tm_bytes", "pass": replaceErr == nil && bytes.Equal(createBytes, replaceBytes) && replaced.Stdout == "" && replaced.Stderr == "" && reflect.DeepEqual(createRows, replaceRows)},
		{"name": "in_place_retains_original_backup_and_exact_tm_bytes", "pass": inPlaceErr == nil && bytes.Equal(createBytes, inPlaceBytes) && len(backups) == 1 && string(backup) == offlineTranslationSource &&
			inPlace.Stdout == "" && inPlace.Stderr == "" && reflect.DeepEqual(createRows, finalRows)},
	}
	expected := make(map[string]translationTMRow)
	for name, key := range keys {
		expected[name] = translationTMRow{Schema: key.Schema(), Source: key.SourceBytes(), Profile: key.ProfileBytes()}
	}
	// Preserve raw published bytes and the closed persistent database before
	// t.TempDir cleanup; observer already preserves each child's raw streams.
	for name, data := range map[string][]byte{
		"translation-create.output": createBytes, "translation-replace.output": replaceBytes,
		"translation-in-place.output": inPlaceBytes, "translation-original.backup": backup,
	} {
		if err := os.WriteFile(filepath.Join(offlineSmokeEvidence(t), name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if data, err := os.ReadFile(l.storage.TMFile); err == nil {
		if err := os.WriteFile(filepath.Join(offlineSmokeEvidence(t), "translation.tm.sqlite"), data, 0600); err != nil {
			t.Fatal(err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	recordOfflineSmokeEvidence(t, "matrix-translation", map[string]any{
		"task": "issue-19-cli-offline-completion/task-4", "kind": "real_model_cli", "completed": true,
		"binary": offlineSmokeBinary(t), "backend": offlineSmokeBackend(t), "layout": l.describe(),
		"source_before": offlineTranslationSource, "source_after": string(inPlaceBytes),
		"output_create": string(createBytes), "output_rejected": string(rejectedBytes),
		"output_replace": string(replaceBytes), "backup_paths": backups, "backup_bytes": string(backup),
		"tm_cold": coldRows, "tm_hit": hitRows, "tm_miss": missRows, "tm_create": createRows, "tm_final": finalRows,
		"expected_keys": expected, "runs": runs, "checks": checks,
	})
	completed = true
	offlineSmokeChecks(t, checks)
}

func offlineTranslationModelFailure(t *testing.T, l *offlineSmokeLayout, kind string) {
	t.Helper()
	source, output := filepath.Join(l.root, "failure-source.txt"), filepath.Join(l.root, "failure-output.txt")
	writeFixture(t, source, offlineTranslationSource)
	before := readTranslationTM(t, l.paths)
	var runs []offlineSmokeRun
	for _, action := range []string{"translate", "localize"} {
		args := translationArgs(action, "--backend", offlineSmokeBackend(t))
		if action == "localize" {
			args = append(args, "--output", output)
		}
		runs = append(runs, l.run(t, kind+"-"+action, 1, append(args, source)...))
	}
	after := readTranslationTM(t, l.paths)
	_, outputErr := os.Lstat(output)
	checks := []map[string]any{{"name": kind + "_model_keeps_tm_and_output", "pass": reflect.DeepEqual(before, after) && errors.Is(outputErr, os.ErrNotExist)}}
	for _, run := range runs {
		checks = append(checks, map[string]any{"name": run.Name + "_model_phase", "pass": run.Stdout == "" && strings.Contains(run.Stderr, "localize model failed") && strings.Contains(run.Stderr, "TMCommitted=false")})
	}
	recordOfflineSmokeEvidence(t, "matrix-translation-"+kind, map[string]any{"runs": runs, "tm_before": before, "tm_after": after, "checks": checks})
	offlineSmokeChecks(t, checks)
}
