//go:build cuda && offlinesmoke

package goinfer

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestCUDAUnavailableDriverDiagnostics(t *testing.T) {
	// Given: the existing real-load fixture requires absent driver libraries and
	// proves CUDA refusal plus an explicitly selected CPU control.
	if os.Getenv("YAKUORI_OFFLINE_CUDA_DRIVER") != "absent" {
		t.Skip("requires the driver-free offline fixture")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), binary,
		"-test.run", "^TestOfflineCUDAMatrix$/^real_load$", "-test.v", "-test.timeout", "15m")
	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr

	// When: the public wrapper loads the pinned model without a CUDA driver.
	err = child.Run()

	// Then: refusal is returned to the caller, not printed by the dependency.
	if err != nil {
		t.Fatalf("real-load control: %v\nstdout=%s\nstderr=%s", err, &stdout, &stderr)
	}
	if stderr.Len() != 0 {
		t.Fatalf("CUDA refusal wrote %d unsolicited stderr bytes: %s", stderr.Len(), &stderr)
	}
}

func TestCUDAUnavailableDriverErrorIdentity(t *testing.T) {
	// Given: the pinned model on the driver-free fixture.
	if os.Getenv("YAKUORI_OFFLINE_CUDA_DRIVER") != "absent" {
		t.Skip("requires the driver-free offline fixture")
	}
	model := offlineCUDAFixture(t)

	// When: the public CUDA loader refuses the unavailable driver.
	engine, err := OpenWithOptions(t.Context(), model, Options{
		Backend: "cuda", ComputeQuant: "int4", ContextTokens: 4096,
	})
	if engine != nil {
		t.Cleanup(func() {
			if err := engine.Close(); err != nil {
				t.Error(err)
			}
		})
	}

	// Then: callers retain the existing CUDA refusal classification.
	if engine != nil || !errors.Is(err, ErrCUDAResidency) {
		t.Fatalf("engine=%v error=%v; want CUDA residency refusal", engine, err)
	}
}
