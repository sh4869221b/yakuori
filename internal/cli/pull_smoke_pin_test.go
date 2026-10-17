//go:build modelsmoke

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"testing"
)

// pullSmokeDigest is the SHA-256 that ci/cpu-spike.Dockerfile:24 and
// internal/inference/goinfer/cpu_smoke_test.go:42 pin for the Qwen revision
// ebb2015119c907b064c512bf053e945850b5875f.
const pullSmokeDigest = "1d9614638d18024d0fbb36575a15f1302a3adf044df10345688ec4f6e1c4ff32"

const pullSmokeDigestSource = "repo-pinned Qwen revision ebb2015119c907b064c512bf053e945850b5875f"

const pullSmokeModelSize = 491400064

func pullSmokeBinary(t *testing.T) string {
	t.Helper()
	path := os.Getenv("YAKUORI_PULL_BINARY")
	if path == "" {
		t.Fatal("YAKUORI_PULL_BINARY must name the built product CLI")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("YAKUORI_PULL_BINARY %s is not a regular file: %v", path, err)
	}
	return path
}

// pullSmokeModel verifies the fixture size and digest against the repository pin.
func pullSmokeModel(t *testing.T) string {
	t.Helper()
	path := os.Getenv("YAKUORI_PULL_MODEL")
	if path == "" {
		t.Fatal("YAKUORI_PULL_MODEL must name the pinned local Qwen GGUF fixture")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("fixture %s is not a regular file", path)
	}
	if info.Size() != pullSmokeModelSize {
		t.Fatalf("fixture size %d does not match the pinned %d", info.Size(), pullSmokeModelSize)
	}
	if digest := pullSmokeFileDigest(t, path); digest != pullSmokeDigest {
		t.Fatalf("fixture digest %s does not match the pinned %s", digest, pullSmokeDigest)
	}
	return path
}

func pullSmokeFileDigest(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	if err := errors.Join(copyErr, file.Close()); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func pullSmokeDigestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
