//go:build modelsmoke

package cli

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestPullCLIRealModel acquires the pinned Qwen fixture over TLS, then verifies and selects it.
func TestPullCLIRealModel(t *testing.T) {
	bin := pullSmokeBinary(t)
	model := pullSmokeModel(t)
	layout := newPullSmokeLayout(t)
	planted := layout.plant(t, "existing", pullSmokePlantedBody())
	server := newPullSmokeServer(t, pullSmokeFile{path: model})
	source := server.url + "/model.gguf"
	args := []string{"models", "pull", "--id", "fetched", "--sha256", pullSmokeDigest,
		"--size", strconv.Itoa(pullSmokeModelSize), "--digest-source", pullSmokeDigestSource, source}

	pull := layout.run(t, bin, server.caFile, args...)
	pullState := layout.snapshot(t)
	managedDigest := layout.managedDigest(t, "fetched")
	provenance := layout.readFile(t, filepath.Join(layout.storage.ModelsDir, "fetched", "provenance.json"))
	verify := layout.run(t, bin, server.caFile, "models", "verify", "fetched")
	use := layout.run(t, bin, server.caFile, "models", "use", "fetched")
	useState := layout.snapshot(t)
	doctor := layout.run(t, bin, server.caFile, "doctor", "--check-model", "--backend", "cpu")

	entry, registered := pullSmokeEntryOf(t, pullState, "fetched")
	managed, managedPresent := pullState.Models["fetched"]
	previous, previousRegistered := pullSmokeEntryOf(t, useState, planted.ID)
	checks := []pullSmokeCheck{
		{Name: "pull_exit_ok", Pass: pull.ExitCode == ExitOK && pull.Signal == "", Detail: fmt.Sprintf("exit=%d signal=%s", pull.ExitCode, pull.Signal)},
		{Name: "pull_stdout_result", Pass: pull.Stdout == fmt.Sprintf("pull: fetched\nmodel_sha256: %s\nsize_bytes: %d\n", pullSmokeDigest, pullSmokeModelSize), Detail: pull.Stdout},
		{Name: "pull_notice_precedes_acquisition", Pass: pull.Stderr == pullSmokeNotice("fetched", source, pullSmokeDigest, pullSmokeModelSize, pullSmokeDigestSource), Detail: pull.Stderr},
		{Name: "registry_entry_matches_pin", Pass: registered && entry.SHA256 == pullSmokeDigest && entry.SizeBytes == pullSmokeModelSize, Detail: fmt.Sprintf("%+v registered=%t", entry, registered)},
		{Name: "managed_bytes_match_pin", Pass: managedPresent && managed.ModelSizeBytes == pullSmokeModelSize && managedDigest == pullSmokeDigest, Detail: fmt.Sprintf("%+v digest=%s", managed, managedDigest)},
		{Name: "provenance_records_the_expected_identity", Pass: pullSmokeProvenanceMatches(provenance, source, pullSmokeDigest, pullSmokeModelSize, pullSmokeDigestSource), Detail: provenance},
		{Name: "pull_keeps_the_selection", Pass: pullSmokeSelectionOf(t, pullState) == planted.ID && pullState.Selection != nil && *pullState.Selection == planted.Selection, Detail: pullSmokeSelectionOf(t, pullState)},
		{Name: "pull_leaves_no_stage", Pass: len(pullState.Stages) == 0, Detail: strings.Join(pullState.Stages, ",")},
		{Name: "verify_exit_ok", Pass: verify.ExitCode == ExitOK && verify.Stdout == fmt.Sprintf("verify: fetched\nmodel_sha256: %s\nsize_bytes: %d\n", pullSmokeDigest, pullSmokeModelSize), Detail: fmt.Sprintf("exit=%d stdout=%q stderr=%q", verify.ExitCode, verify.Stdout, verify.Stderr)},
		{Name: "use_exit_ok", Pass: use.ExitCode == ExitOK && use.Stdout == "use: fetched\n", Detail: fmt.Sprintf("exit=%d stdout=%q stderr=%q", use.ExitCode, use.Stdout, use.Stderr)},
		{Name: "use_changes_the_selection", Pass: pullSmokeSelectionOf(t, useState) == "fetched", Detail: pullSmokeSelectionOf(t, useState)},
		{Name: "real_loader_accepts_the_selection", Pass: doctor.ExitCode == ExitOK && strings.Contains(doctor.Stdout, "model_sha256: "+pullSmokeDigest) && strings.Contains(doctor.Stdout, "effective_backend: cpu"), Detail: fmt.Sprintf("exit=%d stdout=%q stderr=%q", doctor.ExitCode, doctor.Stdout, doctor.Stderr)},
		{Name: "previous_registration_intact", Pass: previousRegistered && previous.SHA256 == planted.ModelSHA256 && previous.SizeBytes == planted.ModelSizeBytes && layout.managedDigest(t, planted.ID) == planted.ModelSHA256, Detail: fmt.Sprintf("%+v", previous)},
	}
	recordPullSmokeEvidence(t, "real-cli", &pullSmokeEvidence{
		Case:     "real-cli",
		Task:     "issue-19-model-pull-offline/task-4",
		Binary:   bin,
		Model:    model,
		Fixture:  map[string]any{"sha256": pullSmokeDigest, "size_bytes": pullSmokeModelSize, "origin": "existing local image fixture, no acquisition"},
		Env:      layout.childEnv(server.caFile),
		Commands: []pullSmokeRun{pull, verify, use, doctor},
		States:   map[string]pullSmokeState{"after_pull": pullState, "after_use": useState},
		Extra:    map[string]any{"tls_fixture_url": source, "managed_digest": managedDigest, "provenance": provenance, "planted": planted},
		Checks:   checks,
		Layout:   layout.describe(),
	})
}

// TestPullCLIFailure covers the model-free acquisition failures and invalid arguments.
func TestPullCLIFailure(t *testing.T) {
	bin := pullSmokeBinary(t)
	layout := newPullSmokeLayout(t)
	planted := layout.plant(t, "existing", pullSmokePlantedBody())

	mismatch := bytes.Repeat([]byte("mismatch"), 256)
	other := bytes.Repeat([]byte("other"), 256)
	short := bytes.Repeat([]byte("short"), 256)
	long := bytes.Repeat([]byte("long"), 2048)
	invalid := pullSmokeInvalidBody()

	mux := http.NewServeMux()
	mux.Handle("/mismatch.gguf", &pullSmokePayload{body: mismatch})
	mux.Handle("/short.gguf", &pullSmokePayload{body: short})
	mux.Handle("/long.gguf", &pullSmokePayload{body: long})
	mux.Handle("/status.gguf", &pullSmokePayload{status: http.StatusNotFound})
	mux.Handle("/invalid.gguf", &pullSmokePayload{body: invalid})
	server := newPullSmokeServer(t, mux)

	commands := []pullSmokeRun{}
	states := map[string]pullSmokeState{}
	checks := []pullSmokeCheck{}
	for _, tt := range []struct {
		name       string
		path       string
		digest     string
		size       int64
		wantReason string
	}{
		{"digest_mismatch", "/mismatch.gguf", pullSmokeDigestOf(other), int64(len(mismatch)), "received bytes do not match the expected SHA-256"},
		{"short_body", "/short.gguf", pullSmokeDigestOf(short), int64(len(short) + 1), "received size does not match the expected size"},
		{"long_body", "/long.gguf", pullSmokeDigestOf(long), int64(len(long) - 1), "source sent more bytes than the expected size"},
		{"http_status", "/status.gguf", pullSmokeDigest, 1024, "source did not answer with HTTP 200"},
		{"invalid_gguf", "/invalid.gguf", pullSmokeDigestOf(invalid), int64(len(invalid)), ""},
	} {
		source := server.url + tt.path
		args := []string{"models", "pull", "--id", "fetched", "--sha256", tt.digest,
			"--size", strconv.FormatInt(tt.size, 10), "--digest-source", pullSmokeDigestSource, source}
		run := layout.run(t, bin, server.caFile, args...)
		state := layout.snapshot(t)
		commands = append(commands, run)
		states["after_"+tt.name] = state

		// The loader-rejection reason is not part of the pinned classification;
		// every other failure must be reported as its known reason.
		diagnostic := strings.TrimPrefix(run.Stderr, pullSmokeNotice("fetched", source, tt.digest, tt.size, pullSmokeDigestSource))
		classified := strings.HasPrefix(diagnostic, "yakuori: pull model: ") && len(diagnostic) > len("yakuori: pull model: ")
		if tt.wantReason != "" {
			classified = diagnostic == "yakuori: pull model: "+tt.wantReason+"\n"
		}
		checks = append(checks,
			pullSmokeCheck{Name: tt.name + "_exit_failure", Pass: run.ExitCode == ExitFailure && run.Signal == "", Detail: fmt.Sprintf("exit=%d signal=%s", run.ExitCode, run.Signal)},
			pullSmokeCheck{Name: tt.name + "_stdout_empty", Pass: run.Stdout == "", Detail: run.Stdout},
			pullSmokeCheck{Name: tt.name + "_classified_reason", Pass: classified, Detail: diagnostic},
			pullSmokeCheck{Name: tt.name + "_no_owned_stage", Pass: len(state.Stages) == 0, Detail: strings.Join(state.Stages, ",")},
		)
		checks = append(checks, layout.unchangedChecks(t, tt.name, state, planted, "fetched")...)
	}

	usageLayout := newPullSmokeLayout(t)
	usage := usageLayout.run(t, bin, server.caFile, "models", "pull", "--id", "fetched",
		"--sha256", pullSmokeDigest, "--size", "0", "--digest-source", pullSmokeDigestSource, server.url+"/short.gguf")
	usageState := usageLayout.snapshot(t)
	_, dataErr := os.Stat(usageLayout.paths.Data)
	_, configErr := os.Stat(usageLayout.paths.Config)
	commands = append(commands, usage)
	states["invalid_arguments"] = usageState
	checks = append(checks,
		pullSmokeCheck{Name: "invalid_arguments_exit_usage", Pass: usage.ExitCode == ExitUsage && usage.Signal == "", Detail: fmt.Sprintf("exit=%d signal=%s", usage.ExitCode, usage.Signal)},
		pullSmokeCheck{Name: "invalid_arguments_stdout_empty", Pass: usage.Stdout == "" && usage.Stderr != "", Detail: usage.Stderr},
		pullSmokeCheck{Name: "invalid_arguments_resolves_no_storage", Pass: os.IsNotExist(dataErr) && os.IsNotExist(configErr), Detail: fmt.Sprintf("data=%v config=%v", dataErr, configErr)},
	)
	recordPullSmokeEvidence(t, "failure-cli", &pullSmokeEvidence{
		Case:     "failure-cli",
		Task:     "issue-19-model-pull-offline/task-4",
		Binary:   bin,
		Env:      layout.childEnv(server.caFile),
		Commands: commands,
		States:   states,
		Extra:    map[string]any{"planted": planted, "invalid_arguments_layout": usageLayout.describe()},
		Checks:   checks,
		Layout:   layout.describe(),
	})
}

// TestPullCLIInterrupt cancels a live transfer after the fixture flushed its first chunk.
func TestPullCLIInterrupt(t *testing.T) {
	bin := pullSmokeBinary(t)
	body := bytes.Repeat([]byte("yakuori-interrupt-payload"), 1<<15)
	commands := []pullSmokeRun{}
	states := map[string]pullSmokeState{}
	checks := []pullSmokeCheck{}
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		name := signal.String()
		gate := newPullSmokeGate(t)
		server := newPullSmokeServer(t, &pullSmokePayload{body: body, first: 64 << 10, reached: gate.reached, release: gate.release})
		layout := newPullSmokeLayout(t)
		planted := layout.plant(t, "existing", pullSmokePlantedBody())
		source := server.url + "/model.gguf"
		args := []string{"models", "pull", "--id", "interrupted", "--sha256", pullSmokeDigestOf(body),
			"--size", strconv.Itoa(len(body)), "--digest-source", pullSmokeDigestSource, source}

		process := layout.start(t, bin, server.caFile, args...)
		// A cancelled child must exit at once; the shorter bound fails loudly if it does not.
		process.timeout = 30 * time.Second
		pullSmokeAwait(t, gate.reached, "the first flushed response chunk")
		if err := process.cmd.Process.Signal(signal); err != nil {
			t.Fatal(err)
		}
		run := process.finish(t)
		state := layout.snapshot(t)
		verify := layout.run(t, bin, server.caFile, "models", "verify", "existing")
		commands = append(commands, run, verify)
		states["after_"+name] = state

		checks = append(checks,
			pullSmokeCheck{Name: name + "_exit_failure_not_signal", Pass: run.ExitCode == ExitFailure && run.Signal == "", Detail: fmt.Sprintf("exit=%d signal=%s", run.ExitCode, run.Signal)},
			pullSmokeCheck{Name: name + "_stdout_empty", Pass: run.Stdout == "", Detail: run.Stdout},
			pullSmokeCheck{Name: name + "_cancel_classified", Pass: run.Stderr == pullSmokeNotice("interrupted", source, pullSmokeDigestOf(body), int64(len(body)), pullSmokeDigestSource)+"yakuori: pull model: pull was cancelled\n", Detail: run.Stderr},
			pullSmokeCheck{Name: name + "_no_owned_stage", Pass: len(state.Stages) == 0, Detail: strings.Join(state.Stages, ",")},
			pullSmokeCheck{Name: name + "_previous_registration_still_usable", Pass: verify.ExitCode == ExitOK && strings.Contains(verify.Stdout, "verify: existing"), Detail: fmt.Sprintf("exit=%d stdout=%q stderr=%q", verify.ExitCode, verify.Stdout, verify.Stderr)},
		)
		checks = append(checks, layout.unchangedChecks(t, name, state, planted, "interrupted")...)
	}
	recordPullSmokeEvidence(t, "interrupt", &pullSmokeEvidence{
		Case:     "interrupt",
		Task:     "issue-19-model-pull-offline/task-4",
		Binary:   bin,
		Env:      nil,
		Commands: commands,
		States:   states,
		Extra:    map[string]any{"fixture": "64 KiB flushed then stalled", "signals": []string{syscall.SIGINT.String(), syscall.SIGTERM.String()}},
		Checks:   checks,
		Layout:   map[string]string{"note": "one fresh private XDG root per signal"},
	})
}

// TestPullCLIKilledLeftovers keeps a SIGKILLed transfer's leftover unregistered and untouched.
func TestPullCLIKilledLeftovers(t *testing.T) {
	bin := pullSmokeBinary(t)
	body := bytes.Repeat([]byte("yakuori-killed-payload"), 1<<15)
	gate := newPullSmokeGate(t)
	mux := http.NewServeMux()
	mux.Handle("/model.gguf", &pullSmokePayload{body: body, first: 64 << 10, reached: gate.reached, release: gate.release})
	mux.Handle("/fail.gguf", &pullSmokePayload{status: http.StatusNotFound})
	server := newPullSmokeServer(t, mux)
	layout := newPullSmokeLayout(t)
	planted := layout.plant(t, "existing", pullSmokePlantedBody())
	source := server.url + "/model.gguf"

	process := layout.start(t, bin, server.caFile, "models", "pull", "--id", "interrupted",
		"--sha256", pullSmokeDigestOf(body), "--size", strconv.Itoa(len(body)), "--digest-source", pullSmokeDigestSource, source)
	process.timeout = 30 * time.Second
	pullSmokeAwait(t, gate.reached, "the first flushed response chunk")
	if err := process.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	killed := process.finish(t)
	killedState := layout.snapshot(t)

	follow := layout.run(t, bin, server.caFile, "models", "pull", "--id", "other", "--sha256", pullSmokeDigest,
		"--size", "1024", "--digest-source", pullSmokeDigestSource, server.url+"/fail.gguf")
	afterFollow := layout.snapshot(t)
	_, interruptedRegistered := pullSmokeEntryOf(t, afterFollow, "interrupted")
	_, otherRegistered := pullSmokeEntryOf(t, afterFollow, "other")
	ownedStage := len(killedState.Stages) == 1 && strings.HasPrefix(killedState.Stages[0], ".pull-")

	cleanup := map[string]string{}
	for _, stage := range afterFollow.Stages {
		path := filepath.Join(layout.storage.ModelsDir, stage)
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
		_, readBack := os.Stat(path)
		cleanup[path] = fmt.Sprintf("read_back_absent=%t err=%v", os.IsNotExist(readBack), readBack)
	}
	afterCleanup := layout.snapshot(t)
	cleanup["stages_after_cleanup"] = strings.Join(afterCleanup.Stages, ",")

	checks := []pullSmokeCheck{
		{Name: "killed_by_signal", Pass: killed.Signal == "killed" && killed.ExitCode == -1, Detail: fmt.Sprintf("exit=%d signal=%s", killed.ExitCode, killed.Signal)},
		{Name: "killed_stdout_empty", Pass: killed.Stdout == "", Detail: killed.Stdout},
		{Name: "leftover_stage_retained", Pass: ownedStage, Detail: strings.Join(killedState.Stages, ",")},
		{Name: "unregistered_leftover", Pass: !interruptedRegistered, Detail: strings.Join(afterFollow.Stages, ",")},
		{Name: "later_run_exit_failure", Pass: follow.ExitCode == ExitFailure && follow.Stdout == "", Detail: fmt.Sprintf("exit=%d stdout=%q", follow.ExitCode, follow.Stdout)},
		{Name: "later_run_keeps_foreign_leftover", Pass: reflect.DeepEqual(killedState.Stages, afterFollow.Stages), Detail: fmt.Sprintf("killed=%v after=%v", killedState.Stages, afterFollow.Stages)},
		{Name: "later_run_publishes_nothing", Pass: !otherRegistered && !interruptedRegistered && afterFollow.Registry != nil && *afterFollow.Registry == planted.Registry, Detail: fmt.Sprintf("registry=%v", afterFollow.Registry)},
		{Name: "owned_leftovers_removed_on_request", Pass: len(afterCleanup.Stages) == 0, Detail: strings.Join(afterCleanup.Stages, ",")},
	}
	checks = append(checks, layout.unchangedChecks(t, "killed", killedState, planted, "interrupted")...)
	recordPullSmokeEvidence(t, "killed-leftovers", &pullSmokeEvidence{
		Case:     "killed-leftovers",
		Task:     "issue-19-model-pull-offline/task-4",
		Binary:   bin,
		Env:      layout.childEnv(server.caFile),
		Commands: []pullSmokeRun{killed, follow},
		States:   map[string]pullSmokeState{"after_kill": killedState, "after_later_run": afterFollow, "after_cleanup": afterCleanup},
		Extra:    map[string]any{"leftover_was_preserved": reflect.DeepEqual(killedState.Stages, afterFollow.Stages), "planted": planted},
		Checks:   checks,
		Cleanup:  cleanup,
		Layout:   layout.describe(),
	})
}
