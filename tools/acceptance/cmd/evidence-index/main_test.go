package main

import (
	"slices"
	"strings"
	"testing"
)

func TestEvaluateEvidence(t *testing.T) {
	const revision = "current"
	pins := map[string]string{
		"goinfer": "current-module", "cpu_model_sha256": "cpu-model",
		"gpu_model_sha256": "gpu-model", "gpu_device": "AMD Radeon RX 9070 XT", "gpu_backend": "test-backend",
		"manifest": "fixtures-v2", "criteria": "criteria-v4", "fixture/example": "example-pin",
		"w3_cpu_fixture_sha256": "w3-cpu-fixture",
	}
	limits := gpuLimits{WallSeconds: 60, WorkerRSSBytes: 24 << 30, SampledVRAMBytes: 12 << 30}
	newIndex := func() index {
		record := index{TargetRevision: revision, GPULimits: limits, Sources: make(map[string]source)}
		for _, required := range requirements {
			record.Requirements = append(record.Requirements, observation{
				ID: required.id, Status: "pass", Checks: []string{"executed test"},
				Sources: []string{required.id}, Notes: "Observed pass.",
			})
			sourcePins := make(map[string]string)
			for k, v := range pins {
				sourcePins[k] = v
			}
			evidence := source{
				Kind: required.kind, Status: "pass", Revision: revision, Pins: sourcePins, Covers: []string{required.id},
				Location: "https://example.invalid/run", Command: "run test", Environment: "test host",
			}
			if required.kind == "gpu-approval" {
				evidence.Revision, evidence.Command, evidence.Environment = "", "", ""
				evidence.GPULimits = limits
				delete(sourcePins, "criteria")
			}
			if required.id == "R9.gpu-performance" {
				evidence.GPULimits = limits
			}
			if required.id == "R10.cpu-offline-real" {
				evidence.Checks = slices.Clone(cpuOfflineChecks)
			}
			record.Sources[required.id] = evidence
		}
		record.History = []observation{
			{ID: "issue-87-scope", Status: "deferred", Notes: "After v1."},
			{ID: "issue-87-verdict", Status: "fail", Notes: "Historical human failure."},
			{ID: "issue-87-review", Status: "human_incomplete", Notes: "Historical missing rows."},
		}
		return record
	}
	const gpu = "R7.gpu-resident-pipeline"
	const cpu = "R7.w3-cpu"
	for _, tc := range []struct {
		name    string
		edit    func(*index)
		blocker string
		wantErr string
	}{
		{name: "complete with deferred human review"},
		{name: "missing GPU despite CPU pass", edit: func(i *index) {
			i.Requirements = slices.DeleteFunc(i.Requirements, func(row observation) bool { return row.ID == gpu })
		}, blocker: gpu + ": missing"},
		{name: "required status cannot be deferred", edit: func(i *index) {
			i.Requirements[0].Status = "deferred"
		}, wantErr: "invalid required status"},
		{name: "skip cannot be relabeled as pass", edit: func(i *index) {
			s := i.Sources[gpu]
			s.Status = "skip"
			i.Sources[gpu] = s
		}, wantErr: `source recorded "skip"`},
		{name: "CUDA device mismatch", edit: func(i *index) {
			i.Sources[gpu].Pins["gpu_device"] = "NVIDIA RTX 4070"
		}, wantErr: "pin mismatch for gpu_device"},
		{name: "CUDA offline cannot serve AMD", edit: func(i *index) {
			i.Sources["R10.gpu-offline"].Pins["gpu_device"] = "NVIDIA RTX 4070"
		}, wantErr: "pin mismatch for gpu_device"},
		{name: "fixture selection criteria mismatch", edit: func(i *index) {
			i.Sources["R8.confirmation"].Pins["criteria"] = "criteria-v3"
		}, wantErr: "pin mismatch for criteria"},
		{name: "old GPU performance ceiling", edit: func(i *index) {
			s := i.Sources["R9.gpu-performance"]
			s.GPULimits.SampledVRAMBytes = 10 << 30
			i.Sources["R9.gpu-performance"] = s
		}, wantErr: "gpu_limits differ"},
		{name: "GPU fixture mismatch", edit: func(i *index) {
			delete(i.Sources[gpu].Pins, "fixture/example")
		}, wantErr: "pin mismatch for fixture/example"},
		{name: "GPU replacement mismatch", edit: func(i *index) {
			i.Sources[gpu].Pins["goinfer"] = "old-module"
		}, wantErr: "pin mismatch for goinfer"},
		{name: "wrong model pin", edit: func(i *index) {
			i.Sources[cpu].Pins["cpu_model_sha256"] = "wrong-model"
		}, wantErr: "pin mismatch for cpu_model_sha256"},
		{name: "missing W3 CPU fixture pin", edit: func(i *index) {
			delete(i.Sources[cpu].Pins, "w3_cpu_fixture_sha256")
		}, wantErr: "pin mismatch for w3_cpu_fixture_sha256"},
		{name: "W3 CPU regression cannot prove offline tracing", edit: func(i *index) {
			i.Sources["R10.cpu-offline-real"] = i.Sources[cpu]
		}, wantErr: "source does not cover this requirement"},
		{name: "CPU offline requires both CLI and Core observations", edit: func(i *index) {
			s := i.Sources["R10.cpu-offline-real"]
			s.Checks = []string{"internal/cli/TestOfflineCLIMatrix/real-model"}
			i.Sources["R10.cpu-offline-real"] = s
		}, wantErr: "missing observed check internal/localize/TestOfflineCoreMatrix"},
		{name: "different executed revision", edit: func(i *index) {
			s := i.Sources[gpu]
			s.Revision = "historical"
			i.Sources[gpu] = s
		}, wantErr: "got gpu at historical"},
		{name: "CPU evidence cannot serve GPU", edit: func(i *index) {
			s := i.Sources[cpu]
			s.Covers = []string{gpu}
			i.Sources[gpu] = s
		}, wantErr: "got cpu"},
		{name: "missing source", edit: func(i *index) {
			delete(i.Sources, gpu)
		}, wantErr: "unknown source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := newIndex()
			if tc.edit != nil {
				tc.edit(&record)
			}
			result, err := evaluate(".", record, pins)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.blocker != "" {
				if result.Status != "incomplete" || !slices.Contains(result.Blockers, tc.blocker) {
					t.Fatalf("decision = %+v, want blocker %q", result, tc.blocker)
				}
			} else if result.Status != "pass" || len(result.Blockers) != 0 {
				t.Fatalf("decision = %+v, want pass", result)
			}
		})
	}
}

func TestEvaluateRequiredNonPassStatuses(t *testing.T) {
	for _, status := range []string{"fail", "not_run", "skip", "not_reached", "unavailable"} {
		t.Run(status, func(t *testing.T) {
			record := index{Requirements: []observation{{ID: "R1.bindings", Status: status, Notes: "Observed."}}}
			result, err := evaluate(".", record, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := "R1.bindings: " + status + " — Observed."
			if result.Status != "incomplete" || !slices.Contains(result.Blockers, want) {
				t.Fatalf("decision = %+v, want blocker %q", result, want)
			}
		})
	}
}
