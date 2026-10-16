//go:build offlinesmoke

package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestOfflineCLIMatrix runs the real product CLI once per command with private
// XDG state owned by this fixture. Only the product children are wrapped by
// ci/offline.py; the setup, the state read-back and this parent test are not
// observed.
//
// The model-free selection runs with no model at all and is separately
// selectable:
//
//	YAKUORI_PULL_BINARY=... YAKUORI_PULL_EVIDENCE=... go test -tags offlinesmoke ./internal/cli -run '^TestOfflineCLIMatrix$/^model-free$'
//
// translate and localize are dispatch-only: neither implements translation, so
// their cases assert the current dispatch exit codes and are never reported as a
// real translation path.
func TestOfflineCLIMatrix(t *testing.T) {
	t.Run("model-free", func(t *testing.T) {
		layout := newOfflineSmokeLayout(t)
		invalid := layout.invalidModel(t)
		source := filepath.Join(layout.root, "unused-source.txt")
		output := filepath.Join(layout.root, "unused-output.txt")

		doctor := layout.run(t, "doctor-empty", 0, "doctor")
		checkMissing := layout.run(t, "doctor-check-model-missing", 1, "doctor", "--check-model", "--backend", "cpu")
		useMissing := layout.run(t, "models-use-missing", 1, "models", "use", "absent")
		importInvalid := layout.run(t, "models-import-invalid", 1, "models", "import", "--id", "bad", invalid)
		translate := layout.run(t, "translate-dispatch", 2, "translate")
		localize := layout.run(t, "localize-dispatch", 1, "localize", "--output", output, source)
		state := layout.snapshot(t)

		checks := []map[string]any{
			{"name": "doctor_reports_without_a_model", "pass": doctor.TargetExit == 0 && doctor.Stdout != ""},
			{"name": "missing_model_is_a_distinct_command_failure", "pass": checkMissing.TargetExit == 1 && strings.Contains(checkMissing.Stderr, "no model selected")},
			{"name": "unregistered_use_is_a_command_failure", "pass": useMissing.TargetExit == 1 && useMissing.Stderr != ""},
			{"name": "invalid_gguf_import_is_rejected_without_leaking_the_path", "pass": importInvalid.TargetExit == 1 && importInvalid.Stdout == "" && importInvalid.Stderr != "" && !strings.Contains(importInvalid.Stderr, invalid)},
			{"name": "instruction_shaped_model_bytes_are_not_echoed", "pass": !strings.Contains(importInvalid.Stderr, offlineSmokeInjectedInstruction)},
			{"name": "translate_dispatch_is_usage_only", "pass": translate.TargetExit == 2},
			{"name": "localize_dispatch_reports_unimplemented", "pass": localize.TargetExit == 1 && localize.Stdout == "" && localize.Stderr != ""},
			{"name": "no_registry_selection_or_stage_was_written", "pass": !state.RegistryEntry && len(state.Models) == 0 && len(state.Stages) == 0 && state.Selection == ""},
		}
		offlineSmokeChecks(t, checks)
		recordOfflineSmokeEvidence(t, "matrix-model-free", map[string]any{
			"case": "model-free", "task": "issue-19-model-pull-offline/task-6",
			"binary": offlineSmokeBinary(t), "layout": layout.describe(), "invalid_model": invalid,
			"state_after": state, "checks": checks,
			"runs":          []offlineSmokeRun{doctor, checkMissing, useMissing, importInvalid, translate, localize},
			"dispatch_only": []string{"translate: unknown command, exit 2", "localize: pipeline is not implemented, exit 1"},
		})
	})

	t.Run("real-model", func(t *testing.T) {
		layout := newOfflineSmokeLayout(t)
		model := offlineSmokeModel(t)
		backend := offlineSmokeBackend(t)

		importCopy := layout.run(t, "real-models-import-copy", 0, "models", "import", "--id", "copy", model)
		afterImport := layout.snapshot(t)
		verifyCopy := layout.run(t, "real-models-verify-copy", 0, "models", "verify", "copy")
		checkUnselected := layout.run(t, "real-doctor-check-model-unselected", 1, "doctor", "--check-model", "--backend", backend)
		useCopy := layout.run(t, "real-models-use-copy", 0, "models", "use", "copy")
		checkSelected := layout.run(t, "real-doctor-check-model-copy", 0, "doctor", "--check-model", "--backend", backend)
		afterUse := layout.snapshot(t)
		layout.corruptManaged(t, "copy")
		verifyCorrupt := layout.run(t, "real-models-verify-corrupt", 1, "models", "verify", "copy")
		checkCorrupt := layout.run(t, "real-doctor-check-model-corrupt", 1, "doctor", "--check-model", "--backend", backend)
		afterCorrupt := layout.snapshot(t)

		selected := func(state offlineSmokeState) bool {
			return state.RegistryEntry && state.RegistrySHA == offlineSmokeSHA256 && state.RegistrySize == offlineSmokeSizeBytes &&
				state.Selection == "copy" && len(state.Models) == 1 && state.Models[0] == "copy" && len(state.Stages) == 0
		}
		checks := []map[string]any{
			{"name": "import_registers_the_pinned_identity", "pass": importCopy.TargetExit == 0 && strings.Contains(importCopy.Stdout, "import: copy") && strings.Contains(importCopy.Stdout, offlineSmokeSHA256) && strings.Contains(importCopy.Stdout, "491400064")},
			{"name": "import_does_not_select", "pass": afterImport.Selection == "" && afterImport.ManagedSHA == offlineSmokeSHA256},
			{"name": "verify_accepts_the_managed_copy", "pass": verifyCopy.TargetExit == 0 && strings.Contains(verifyCopy.Stdout, offlineSmokeSHA256)},
			{"name": "check_model_without_a_selection_is_a_command_failure", "pass": checkUnselected.TargetExit == 1 && strings.Contains(checkUnselected.Stderr, "no model selected")},
			{"name": "use_selects_the_pinned_copy", "pass": useCopy.TargetExit == 0 && useCopy.Stdout == "use: copy\n"},
			{"name": "check_model_loads_the_real_engine", "pass": checkSelected.TargetExit == 0 && strings.Contains(checkSelected.Stdout, "model_id: copy") && strings.Contains(checkSelected.Stdout, "effective_backend: cpu") && strings.Contains(checkSelected.Stdout, offlineSmokeSHA256)},
			{"name": "the_managed_copy_matches_the_pin", "pass": afterUse.ManagedSHA == offlineSmokeSHA256 && selected(afterUse)},
			{"name": "corrupted_bytes_fail_verify_and_check_model", "pass": verifyCorrupt.TargetExit == 1 && verifyCorrupt.Stdout == "" && strings.Contains(verifyCorrupt.Stderr, "model identity mismatch") && checkCorrupt.TargetExit == 1 && strings.Contains(checkCorrupt.Stderr, "model identity mismatch")},
			{"name": "old_state_survives_the_corrupted_copy", "pass": afterCorrupt.ManagedSHA != offlineSmokeSHA256 && selected(afterCorrupt)},
		}
		offlineSmokeChecks(t, checks)
		recordOfflineSmokeEvidence(t, "matrix-real-model", map[string]any{
			"case": "real-model", "task": "issue-19-model-pull-offline/task-6",
			"binary": offlineSmokeBinary(t), "model": model, "layout": layout.describe(),
			"fixture":            map[string]any{"sha256": offlineSmokeSHA256, "size_bytes": offlineSmokeSizeBytes, "origin": "owned copy of the preexisting local image fixture, no acquisition"},
			"state_after_import": afterImport, "state_after_use": afterUse, "state_after_corruption": afterCorrupt, "checks": checks,
			"runs": []offlineSmokeRun{importCopy, verifyCopy, checkUnselected, useCopy, checkSelected, verifyCorrupt, checkCorrupt},
		})
	})
}

type offlineSmokeState struct {
	RegistryEntry bool     `json:"registry_entry"`
	RegistrySHA   string   `json:"registry_sha256"`
	RegistrySize  int64    `json:"registry_size_bytes"`
	Selection     string   `json:"selection"`
	Models        []string `json:"models"`
	Stages        []string `json:"stages"`
	ManagedSHA    string   `json:"managed_sha256"`
}

func (l *offlineSmokeLayout) snapshot(t *testing.T) offlineSmokeState {
	t.Helper()
	state := offlineSmokeState{Models: []string{}, Stages: []string{}}
	if data, err := os.ReadFile(l.storage.RegistryFile); err == nil {
		var document struct {
			Models map[string]struct {
				SHA256    string `json:"sha256"`
				SizeBytes int64  `json:"size_bytes"`
			} `json:"models"`
		}
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatalf("registry %s: %v", l.storage.RegistryFile, err)
		}
		if entry, ok := document.Models["copy"]; ok {
			state.RegistryEntry, state.RegistrySHA, state.RegistrySize = true, entry.SHA256, entry.SizeBytes
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(l.storage.ConfigFile); err == nil {
		var settings struct {
			SelectedModel string `json:"selected_model"`
		}
		if err := json.Unmarshal(data, &settings); err != nil {
			t.Fatalf("selection %s: %v", l.storage.ConfigFile, err)
		}
		state.Selection = settings.SelectedModel
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(l.storage.ModelsDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			state.Stages = append(state.Stages, entry.Name())
			continue
		}
		state.Models = append(state.Models, entry.Name())
	}
	sort.Strings(state.Models)
	sort.Strings(state.Stages)
	managed := filepath.Join(l.storage.ModelsDir, "copy", "model.gguf")
	if info, err := os.Stat(managed); err == nil && info.Mode().IsRegular() {
		state.ManagedSHA = offlineSmokeFileDigest(t, managed)
	}
	return state
}

func recordOfflineSmokeEvidence(t *testing.T, name string, evidence map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(offlineSmokeEvidence(t), name+".json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func offlineSmokeChecks(t *testing.T, checks []map[string]any) {
	t.Helper()
	for _, check := range checks {
		pass, _ := check["pass"].(bool)
		if !pass {
			t.Fatalf("check %v failed", check)
		}
	}
}
