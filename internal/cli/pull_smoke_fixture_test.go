//go:build modelsmoke

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
)

func pullSmokePlantedBody() []byte {
	body := make([]byte, 4096)
	for i := range body {
		body[i] = byte('a' + i%26)
	}
	return body
}

func pullSmokeInvalidBody() []byte {
	return []byte(strings.Repeat("invalid-gguf-payload ", 200))
}

// pullSmokeNotice is the pre-acquisition metadata the product prints to stderr
// before it opens a connection.
func pullSmokeNotice(id, source, digest string, size int64, digestSource string) string {
	return fmt.Sprintf("pull id=%q source_url=%q expected_sha256=%s expected_size_bytes=%d digest_source=%q\n", id, source, digest, size, digestSource)
}

type pullSmokeProvenance struct {
	Schema            int    `json:"schema"`
	SourceURL         string `json:"source_url"`
	FinalURL          string `json:"final_url"`
	ExpectedSHA256    string `json:"expected_sha256"`
	ExpectedSizeBytes int64  `json:"expected_size_bytes"`
	DigestSource      string `json:"digest_source"`
}

func pullSmokeProvenanceMatches(text, source, digest string, size int64, digestSource string) bool {
	var document pullSmokeProvenance
	if err := json.Unmarshal([]byte(text), &document); err != nil {
		return false
	}
	return document.Schema == 1 && document.SourceURL == source && document.FinalURL == source &&
		document.ExpectedSHA256 == digest && document.ExpectedSizeBytes == size && document.DigestSource == digestSource
}

// pullSmokeLayout derives the child environment from config.Resolve, so a test
// layout never restates the product's storage contract.
type pullSmokeLayout struct {
	root    string
	env     map[string]string
	paths   config.Paths
	storage config.StoragePaths
}

func newPullSmokeLayout(t *testing.T) *pullSmokeLayout {
	t.Helper()
	root := t.TempDir()
	env := map[string]string{
		"HOME":            root,
		"XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"XDG_DATA_HOME":   filepath.Join(root, "data"),
		"XDG_CACHE_HOME":  filepath.Join(root, "cache"),
		"XDG_STATE_HOME":  filepath.Join(root, "state"),
	}
	paths, err := config.Resolve(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	return &pullSmokeLayout{root: root, env: env, paths: paths, storage: config.ResolveStoragePaths(paths)}
}

func (l *pullSmokeLayout) describe() map[string]string {
	return map[string]string{
		"root": l.root, "config": l.paths.Config, "data": l.paths.Data, "cache": l.paths.Cache, "state": l.paths.State,
		"registry": l.storage.RegistryFile, "selection": l.storage.ConfigFile, "models": l.storage.ModelsDir, "snapshots": l.storage.SnapshotDir,
	}
}

func (l *pullSmokeLayout) childEnv(caFile string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "SSL_CERT_FILE=" + caFile}
	for key, value := range l.env {
		env = append(env, key+"="+value)
	}
	sort.Strings(env)
	return env
}

func (l *pullSmokeLayout) writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (l *pullSmokeLayout) readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (l *pullSmokeLayout) managedDigest(t *testing.T, id string) string {
	t.Helper()
	return pullSmokeFileDigest(t, filepath.Join(l.storage.ModelsDir, id, "model.gguf"))
}

type pullSmokePlanted struct {
	ID             string `json:"id"`
	Registry       string `json:"registry"`
	Selection      string `json:"selection"`
	ModelSHA256    string `json:"model_sha256"`
	ModelSizeBytes int64  `json:"model_size_bytes"`
}

func (l *pullSmokeLayout) plant(t *testing.T, id string, body []byte) pullSmokePlanted {
	t.Helper()
	l.writeFile(t, filepath.Join(l.storage.ModelsDir, id, "model.gguf"), body)
	planted := pullSmokePlanted{
		ID:             id,
		Registry:       fmt.Sprintf(`{"schema":1,"models":{%q:{"sha256":%q,"size_bytes":%d}}}`, id, pullSmokeDigestOf(body), len(body)),
		Selection:      fmt.Sprintf(`{"schema":1,"selected_model":%q}`, id),
		ModelSHA256:    pullSmokeDigestOf(body),
		ModelSizeBytes: int64(len(body)),
	}
	l.writeFile(t, l.storage.RegistryFile, []byte(planted.Registry))
	l.writeFile(t, l.storage.ConfigFile, []byte(planted.Selection))
	return planted
}

type pullSmokeEntry struct {
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

type pullSmokeRegistryDocument struct {
	Schema int                       `json:"schema"`
	Models map[string]pullSmokeEntry `json:"models"`
}

type pullSmokeSelectionDocument struct {
	Schema        int    `json:"schema"`
	SelectedModel string `json:"selected_model"`
}

type pullSmokeManaged struct {
	Files          []string `json:"files"`
	ModelSizeBytes int64    `json:"model_size_bytes"`
}

type pullSmokeState struct {
	Registry  *string                     `json:"registry"`
	Selection *string                     `json:"selection"`
	Stages    []string                    `json:"stages"`
	Models    map[string]pullSmokeManaged `json:"managed_models"`
}

func pullSmokeReadOrNil(t *testing.T, path string) *string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	return &text
}

func (l *pullSmokeLayout) snapshot(t *testing.T) pullSmokeState {
	t.Helper()
	state := pullSmokeState{
		Registry:  pullSmokeReadOrNil(t, l.storage.RegistryFile),
		Selection: pullSmokeReadOrNil(t, l.storage.ConfigFile),
		Stages:    []string{},
		Models:    map[string]pullSmokeManaged{},
	}
	entries, err := os.ReadDir(l.storage.ModelsDir)
	if os.IsNotExist(err) {
		return state
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			state.Stages = append(state.Stages, entry.Name())
			continue
		}
		files, err := os.ReadDir(filepath.Join(l.storage.ModelsDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		managed := pullSmokeManaged{Files: []string{}}
		for _, file := range files {
			managed.Files = append(managed.Files, file.Name())
			if file.Name() == "model.gguf" {
				info, err := file.Info()
				if err != nil {
					t.Fatal(err)
				}
				managed.ModelSizeBytes = info.Size()
			}
		}
		sort.Strings(managed.Files)
		state.Models[entry.Name()] = managed
	}
	sort.Strings(state.Stages)
	return state
}

func pullSmokeEntryOf(t *testing.T, state pullSmokeState, id string) (pullSmokeEntry, bool) {
	t.Helper()
	if state.Registry == nil {
		return pullSmokeEntry{}, false
	}
	var document pullSmokeRegistryDocument
	if err := json.Unmarshal([]byte(*state.Registry), &document); err != nil {
		t.Fatalf("registry is not schema 1 JSON: %v", err)
	}
	entry, ok := document.Models[id]
	return entry, ok
}

func pullSmokeSelectionOf(t *testing.T, state pullSmokeState) string {
	t.Helper()
	if state.Selection == nil {
		return ""
	}
	var document pullSmokeSelectionDocument
	if err := json.Unmarshal([]byte(*state.Selection), &document); err != nil {
		t.Fatalf("selection is not schema 1 JSON: %v", err)
	}
	return document.SelectedModel
}

// unchangedChecks is shared by the failing, cancelled and killed cases, which
// must all leave the planted bytes untouched and register nothing.
func (l *pullSmokeLayout) unchangedChecks(t *testing.T, prefix string, state pullSmokeState, planted pullSmokePlanted, requestedID string) []pullSmokeCheck {
	t.Helper()
	registrySame := state.Registry != nil && *state.Registry == planted.Registry
	selectionSame := state.Selection != nil && *state.Selection == planted.Selection
	registrySeen, selectionSeen := "absent", "absent"
	if state.Registry != nil {
		registrySeen = *state.Registry
	}
	if state.Selection != nil {
		selectionSeen = *state.Selection
	}
	_, requestedRegistered := pullSmokeEntryOf(t, state, requestedID)
	plantedEntry, plantedRegistered := pullSmokeEntryOf(t, state, planted.ID)
	managed, managedPresent := state.Models[planted.ID]
	plantedIntact := plantedRegistered && plantedEntry.SHA256 == planted.ModelSHA256 &&
		plantedEntry.SizeBytes == planted.ModelSizeBytes && managedPresent &&
		managed.ModelSizeBytes == planted.ModelSizeBytes &&
		l.managedDigest(t, planted.ID) == planted.ModelSHA256
	return []pullSmokeCheck{
		{Name: prefix + "_registry_unchanged", Pass: registrySame, Detail: fmt.Sprintf("got %q want %q", registrySeen, planted.Registry)},
		{Name: prefix + "_selection_unchanged", Pass: selectionSame, Detail: fmt.Sprintf("got %q want %q", selectionSeen, planted.Selection)},
		{Name: prefix + "_requested_id_unregistered", Pass: !requestedRegistered, Detail: requestedID},
		{Name: prefix + "_planted_model_intact", Pass: plantedIntact, Detail: fmt.Sprintf("entry=%+v present=%t", plantedEntry, managedPresent)},
	}
}
