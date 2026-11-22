// Command evidence-index checks the recorded evidence for W3 v1 acceptance.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/sh4869221b/yakuori/tools/acceptance"
)

type requirement struct{ id, kind string }

// A requirement lives here so deleting or deferring its index row cannot make
// the remaining evidence sufficient. Source kinds select the applicable pins.
var requirements = []requirement{
	{"R1.bindings", "component"},
	{"R2.identity-validation", "component"},
	{"R2.w3-sqlite", "component"},
	{"R3.publication", "component"},
	{"R4.failures", "component"},
	{"R4.cancellation-process-stop", "component"},
	{"R4.commit-publication-stdout", "component"},
	{"R5.structure", "component"},
	{"R5.independent-roundtrip", "component"},
	{"R5.gpu-published-reader", "gpu"},
	{"R6.segment-plan-join", "component"},
	{"R6.atomic-parent", "component"},
	{"R7.clean-gate", "component"},
	{"R7.cpu-smoke", "cpu"},
	{"R7.w3-cpu", "cpu"},
	{"R7.gpu-resident-pipeline", "gpu"},
	{"R8.confirmation", "fixtures"},
	{"R8.gpu-conditions", "gpu-approval"},
	{"R8.fixed-cases", "component"},
	{"R9.parser-limits-fuzz", "component"},
	{"R9.deadline-drain-no-retry", "component"},
	{"R9.gpu-performance", "gpu"},
	{"R10.cpu-offline-controls", "component"},
	{"R10.cpu-offline-real", "cpu"},
	{"R10.gpu-offline", "gpu-offline"},
	{"R10.pull-driver-logs", "component"},
}

// These rows combine distinct observations. Each check must be covered by
// a passing source cited by the row.
var requiredSourceChecks = map[string][]string{
	"R6.atomic-parent": {
		"internal/localize/TestSegmentedCoreFailure",
		"internal/localize/TestTMSegmentedParent",
		"TestPipelinePrecommitSegmented",
	},
	"R9.parser-limits-fuzz": {
		"internal/adapter/w3strings/TestParseRejectMalformed",
		"internal/adapter/w3strings/TestParseLimits",
		"internal/adapter/w3strings/TestCountBoundaries",
		"internal/localize/TestCoreInputBoundaries",
		"internal/adapter/w3strings/FuzzParse (active fuzzing)",
	},
	"R10.cpu-offline-controls": {
		"TestOfflineCLIMatrix/model-free",
		"TestOfflinePipelineMatrix",
		"TestOfflinePipelineCanaryDetection",
	},
	"R10.cpu-offline-real": {
		"internal/cli/TestOfflineCLIMatrix/real-model",
		"internal/localize/TestOfflineCoreMatrix",
	},
	"R10.pull-driver-logs": {
		"TestPullCLIFailure",
		"TestPull",
		"TestPullLeftovers",
		"TestOfflinePipelineMatrix/cuda-unsupported",
		"TestPipelineCLIStdoutExit",
		"TestPipelineDiagnostics",
	},
}

type index struct {
	TargetRevision string            `json:"target_revision"`
	GPUDevice      string            `json:"gpu_device"`
	GPUBackendPin  string            `json:"gpu_backend_pin,omitempty"`
	GPUModelSHA256 string            `json:"gpu_model_sha256,omitempty"`
	GPULimits      gpuLimits         `json:"gpu_limits"`
	Requirements   []observation     `json:"requirements"`
	Sources        map[string]source `json:"sources"`
	History        []observation     `json:"history"`
}

type gpuLimits struct {
	WallSeconds      int   `json:"wall_seconds"`
	WorkerRSSBytes   int64 `json:"worker_rss_bytes"`
	SampledVRAMBytes int64 `json:"sampled_vram_bytes"`
}

type observation struct {
	ID      string   `json:"id"`
	Status  string   `json:"status"`
	Checks  []string `json:"checks,omitempty"`
	Sources []string `json:"sources,omitempty"`
	Notes   string   `json:"notes"`
}

type source struct {
	Kind        string            `json:"kind"`
	Status      string            `json:"status"`
	Revision    string            `json:"revision,omitempty"`
	Location    string            `json:"location"`
	Command     string            `json:"command,omitempty"`
	Environment string            `json:"environment,omitempty"`
	Pins        map[string]string `json:"pins,omitempty"`
	Covers      []string          `json:"covers,omitempty"`
	Checks      []string          `json:"checks,omitempty"`
	GPULimits   gpuLimits         `json:"gpu_limits,omitempty"`
}

type decision struct {
	Revision string   `json:"target_revision"`
	Status   string   `json:"status"`
	Blockers []string `json:"blockers"`
}

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var replacement = regexp.MustCompile(`(?m)^replace[ \t]+github\.com/townsendmerino/goinfer[ \t]+=>[ \t]+(\S+)[ \t]+(\S+)[ \t]*$`)
var webGPUReplacement = regexp.MustCompile(`(?m)^replace[ \t]+github\.com/townsendmerino/goinfer/backend/webgpu[ \t]+=>[ \t]+(\S+)[ \t]+(\S+)[ \t]*$`)
var webGPUReportedPin = regexp.MustCompile(`(?m)^const webGPUBackendPin = "([^"@]+)@([0-9a-f]{40})"$`)
var modelChecksum = regexp.MustCompile(`(?m)'([0-9a-f]{64})  /model\.gguf'`)

func main() {
	path := flag.String("index", "docs/evidence/acceptance/evidence-index.json", "evidence index JSON")
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*path, *root, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(path, root string, output io.Writer) error {
	var record index
	if err := acceptance.Load(path, &record); err != nil {
		return err
	}
	if !revisionPattern.MatchString(record.TargetRevision) {
		return errors.New("target_revision must be a full Git commit ID")
	}
	if strings.TrimSpace(record.GPUDevice) == "" {
		return errors.New("gpu_device must identify the adopted GPU")
	}
	if record.GPUModelSHA256 != "" && !sha256Pattern.MatchString(record.GPUModelSHA256) {
		return errors.New("gpu_model_sha256 must be a SHA-256 pin")
	}
	if record.GPULimits.WallSeconds <= 0 || record.GPULimits.WorkerRSSBytes <= 0 || record.GPULimits.SampledVRAMBytes <= 0 {
		return errors.New("gpu_limits must contain three positive ceilings")
	}
	pins, err := targetPins(root, record)
	if err != nil {
		return err
	}
	if record.GPUBackendPin != "" && record.GPUBackendPin != pins["gpu_backend"] {
		return fmt.Errorf("gpu_backend_pin %q does not match target WebGPU pin %q", record.GPUBackendPin, pins["gpu_backend"])
	}
	pins["gpu_model_sha256"] = record.GPUModelSHA256
	pins["gpu_device"] = record.GPUDevice
	result, err := evaluate(root, record, pins)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(output).Encode(result); err != nil {
		return err
	}
	if result.Status != "pass" {
		return errors.New("v1 acceptance evidence is incomplete")
	}
	return nil
}

func targetPins(root string, record index) (map[string]string, error) {
	revision := record.TargetRevision
	read := func(path string) ([]byte, error) {
		data, err := exec.Command("git", "-C", root, "show", revision+":"+path).Output()
		if err != nil {
			return nil, fmt.Errorf("read %s at %s: %w", path, revision, err)
		}
		return data, nil
	}
	pins := make(map[string]string)
	module, err := read("go.mod")
	if err != nil {
		return nil, err
	}
	modulePin := replacement.FindSubmatch(module)
	if modulePin == nil {
		return nil, errors.New("target go.mod must pin the goinfer replacement")
	}
	pins["goinfer"] = string(modulePin[1]) + "@" + string(modulePin[2])
	docker, err := read("ci/cpu-spike.Dockerfile")
	if err != nil {
		return nil, err
	}
	match := modelChecksum.FindSubmatch(docker)
	if match == nil {
		return nil, errors.New("target CPU smoke Dockerfile has no model pin")
	}
	pins["cpu_model_sha256"] = string(match[1])
	cpuFixture, err := read("tools/acceptance/testdata/small.w3strings")
	if err != nil {
		return nil, err
	}
	pins["w3_cpu_fixture_sha256"] = fmt.Sprintf("%x", sha256.Sum256(cpuFixture))

	dir := "docs/evidence/acceptance/"
	criteriaFile := "criteria.json"
	pins["gpu_backend"] = ""
	if backend := webGPUReplacement.FindStringSubmatch(string(module)); backend != nil {
		source, err := read("internal/inference/goinfer/webgpu.go")
		if err != nil {
			return nil, err
		}
		pin, err := effectiveWebGPUPin(backend[1], backend[2], source)
		if err != nil {
			return nil, err
		}
		pins["gpu_backend"] = pin
		criteriaFile = "criteria-amd.json"
	}
	var manifest acceptance.Manifest
	var criteria struct {
		acceptance.Criteria
		Backend string `json:"gpu_backend"`
	}
	data, err := read(dir + "fixtures.json")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("target fixtures: %w", err)
	}
	data, err = read(dir + criteriaFile)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &criteria); err != nil {
		return nil, fmt.Errorf("target criteria: %w", err)
	}
	if manifest.Version == "" || criteria.Version == "" || criteria.ManifestVersion != manifest.Version ||
		criteria.Confirmation.Status != "confirmed" || criteria.Confirmation.Decision != "approved" ||
		criteria.Confirmation.ManifestVersion != manifest.Version ||
		criteria.Confirmation.CriteriaVersion != criteria.Version || len(criteria.FixtureIDs) == 0 {
		return nil, errors.New("target manifest and criteria lack matching approval")
	}
	if criteriaFile == "criteria-amd.json" {
		if criteria.Backend != "webgpu" {
			return nil, errors.New("target AMD criteria must select webgpu")
		}
		if err := checkAMDLimits(criteria.Values, record.GPULimits); err != nil {
			return nil, err
		}
	}
	pins["manifest"], pins["criteria"] = manifest.Version, criteria.Version
	fixturePins := make(map[string]string)
	for _, fixture := range manifest.Fixtures {
		fixturePins[fixture.ID] = fixture.SHA256
	}
	for _, id := range criteria.FixtureIDs {
		hash := fixturePins[id]
		if !sha256Pattern.MatchString(hash) {
			return nil, fmt.Errorf("target selected fixture %s has no SHA-256 pin", id)
		}
		pins["fixture/"+id] = hash
	}
	return pins, nil
}

func effectiveWebGPUPin(path, version string, source []byte) (string, error) {
	// The worker reports the full commit, while go.mod pins a pseudo-version.
	pin := webGPUReportedPin.FindStringSubmatch(string(source))
	revision := version[strings.LastIndexByte(version, '-')+1:]
	if pin == nil || pin[1] != path || len(revision) != 12 || !strings.HasPrefix(pin[2], revision) {
		return "", errors.New("target WebGPU backend pin does not match the go.mod replacement")
	}
	return pin[1] + "@" + pin[2], nil
}

func checkAMDLimits(values map[string]acceptance.Criterion, limits gpuLimits) error {
	for _, ceiling := range []struct {
		key   string
		value int64
	}{
		{"duration_seconds", int64(limits.WallSeconds)},
		{"ram_bytes", limits.WorkerRSSBytes},
		{"vram_bytes", limits.SampledVRAMBytes},
	} {
		if got := values[ceiling.key].Value; got != fmt.Sprint(ceiling.value) {
			return fmt.Errorf("target AMD criteria %s is %q, index ceiling is %d", ceiling.key, got, ceiling.value)
		}
	}
	return nil
}

func evaluate(root string, record index, pins map[string]string) (decision, error) {
	result := decision{Revision: record.TargetRevision, Status: "pass", Blockers: []string{}}
	rows := make(map[string]observation, len(record.Requirements))
	for _, row := range record.Requirements {
		if _, exists := rows[row.ID]; exists {
			return result, fmt.Errorf("duplicate requirement %s", row.ID)
		}
		rows[row.ID] = row
	}
	for _, row := range record.History {
		switch row.Status {
		case "pass", "fail", "not_run", "skip", "not_reached", "unavailable", "deferred", "human_incomplete":
		default:
			return result, fmt.Errorf("invalid historical status for %s: %q", row.ID, row.Status)
		}
		if row.ID == "" || row.Notes == "" {
			return result, errors.New("historical entries require an ID and notes")
		}
		for _, ref := range row.Sources {
			if _, exists := record.Sources[ref]; !exists {
				return result, fmt.Errorf("%s: unknown historical source %s", row.ID, ref)
			}
		}
	}
	for _, required := range requirements {
		row, exists := rows[required.id]
		if !exists {
			result.Blockers = append(result.Blockers, required.id+": missing")
			continue
		}
		delete(rows, required.id)
		if row.Notes == "" {
			return result, fmt.Errorf("%s: missing observation", row.ID)
		}
		switch row.Status {
		case "pass", "fail", "not_run", "skip", "not_reached", "unavailable":
		default:
			return result, fmt.Errorf("%s: invalid required status %q", row.ID, row.Status)
		}
		for _, ref := range row.Sources {
			if _, exists := record.Sources[ref]; !exists {
				return result, fmt.Errorf("%s: unknown source %s", row.ID, ref)
			}
		}
		if row.Status != "pass" {
			result.Blockers = append(result.Blockers, row.ID+": "+row.Status+" — "+row.Notes)
			continue
		}
		if len(row.Sources) == 0 || len(row.Checks) == 0 {
			return result, fmt.Errorf("%s: pass requires checks and sources", row.ID)
		}
		for _, ref := range row.Sources {
			if err := checkSource(root, record.Sources[ref], required, record, pins); err != nil {
				return result, fmt.Errorf("%s (%s): %w", row.ID, ref, err)
			}
		}
		for _, check := range requiredSourceChecks[required.id] {
			if !slices.ContainsFunc(row.Sources, func(ref string) bool {
				return slices.Contains(record.Sources[ref].Checks, check)
			}) {
				return result, fmt.Errorf("%s: missing observed check %s", row.ID, check)
			}
		}
	}
	if len(rows) != 0 {
		return result, fmt.Errorf("unknown requirement IDs: %v", slices.Sorted(maps.Keys(rows)))
	}
	if len(result.Blockers) != 0 {
		result.Status = "incomplete"
	}
	return result, nil
}

func checkSource(root string, s source, required requirement, record index, pins map[string]string) error {
	if s.Status != "pass" {
		return fmt.Errorf("source recorded %q, want pass", s.Status)
	}
	if !slices.Contains(s.Covers, required.id) {
		return errors.New("source does not cover this requirement")
	}
	if s.Kind != required.kind {
		return fmt.Errorf("expected %s evidence, got %s", required.kind, s.Kind)
	}
	if required.kind != "gpu-approval" {
		if s.Revision != record.TargetRevision {
			return fmt.Errorf("expected evidence at %s, got %s at %s", record.TargetRevision, s.Kind, s.Revision)
		}
		if strings.TrimSpace(s.Command) == "" || strings.TrimSpace(s.Environment) == "" {
			return errors.New("execution source requires a reproduction command and environment")
		}
	}
	if s.Location == "" {
		return errors.New("source requires a location")
	}
	if !strings.HasPrefix(s.Location, "https://") {
		path, _, _ := strings.Cut(s.Location, "#")
		if !filepath.IsLocal(path) {
			return fmt.Errorf("invalid repository source path %q", path)
		}
		if info, err := os.Stat(filepath.Join(root, path)); err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("source %q is unavailable", path)
		}
	}
	if (required.kind == "gpu-approval" || required.id == "R9.gpu-performance") && s.GPULimits != record.GPULimits {
		return errors.New("gpu_limits differ from the adopted ceilings")
	}
	for _, pin := range slices.Sorted(maps.Keys(pins)) {
		relevant := false
		switch required.kind {
		case "cpu":
			relevant = pin == "goinfer" || pin == "cpu_model_sha256" ||
				(required.id == "R7.w3-cpu" && pin == "w3_cpu_fixture_sha256")
		case "gpu-offline":
			relevant = pin == "goinfer" || pin == "gpu_device" || pin == "gpu_backend" || pin == "gpu_model_sha256"
		case "gpu-approval":
			relevant = pin == "gpu_device" || pin == "manifest" || (pin == "criteria" && pins["gpu_backend"] != "")
		case "fixtures":
			relevant = pin == "manifest" || pin == "criteria" || strings.HasPrefix(pin, "fixture/")
		case "gpu":
			relevant = pin == "goinfer" || pin == "manifest" || pin == "criteria" || strings.HasPrefix(pin, "fixture/") ||
				pin == "gpu_device" || pin == "gpu_backend" || pin == "gpu_model_sha256"
		}
		expected := pins[pin]
		if relevant && (expected == "" || s.Pins[pin] != expected) {
			return fmt.Errorf("pin mismatch for %s: expected %q, got %q", pin, expected, s.Pins[pin])
		}
	}
	return nil
}
