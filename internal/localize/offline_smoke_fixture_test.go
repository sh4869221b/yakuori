//go:build offlinesmoke

package localize

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/models"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

// The fixture constants are the repository pin that ci/cpu-spike.Dockerfile and
// internal/inference/goinfer/cpu_smoke_test.go also use; the owned copy is local.
const (
	offlineSmokeSHA256    = "1d9614638d18024d0fbb36575a15f1302a3adf044df10345688ec4f6e1c4ff32"
	offlineSmokeSizeBytes = 491400064
)

const offlineSmokeText = "Hello."

func offlineSmokeFixture(t *testing.T) string {
	t.Helper()
	path := os.Getenv("YAKUORI_OFFLINE_MODEL")
	if path == "" {
		t.Fatal("YAKUORI_OFFLINE_MODEL must name the pinned local Qwen GGUF fixture")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Size() != offlineSmokeSizeBytes {
		t.Fatalf("fixture %s must be a regular %d byte file, got mode=%v size=%d", path, offlineSmokeSizeBytes, info.Mode(), info.Size())
	}
	if digest := offlineSmokeFileDigest(t, path); digest != offlineSmokeSHA256 {
		t.Fatalf("fixture digest %s does not match the pinned %s", digest, offlineSmokeSHA256)
	}
	return path
}

func offlineSmokeFileDigest(t *testing.T, path string) string {
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

func offlineSmokeBackend(t *testing.T) string {
	t.Helper()
	backend := os.Getenv("YAKUORI_OFFLINE_BACKEND")
	if backend == "" {
		t.Fatal("YAKUORI_OFFLINE_BACKEND must name the requested backend")
	}
	return backend
}

// offlineSmokeRegistry builds private XDG storage for one real managed model.
func offlineSmokeRegistry(t *testing.T) (*models.Registry, config.StoragePaths) {
	t.Helper()
	root := t.TempDir()
	paths := config.Paths{Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"), State: filepath.Join(root, "state")}
	return models.New(paths), config.ResolveStoragePaths(paths)
}

// offlineSmokeEngine injects one failure into the real engine through the
// existing localize seam. Every case that uses it is labelled injected; the real
// engine is never replaced by a fake generation.
type offlineSmokeEngine struct {
	inference.Engine
	countErr, generateErr, closeErr error
}

func (e offlineSmokeEngine) CountTokens(ctx context.Context, request inference.GenerationRequest) (int, error) {
	if e.countErr != nil {
		return 0, e.countErr
	}
	return e.Engine.CountTokens(ctx, request)
}

func (e offlineSmokeEngine) Generate(ctx context.Context, request inference.GenerationRequest) (inference.GenerationResult, error) {
	if e.generateErr != nil {
		return inference.GenerationResult{}, e.generateErr
	}
	return e.Engine.Generate(ctx, request)
}

func (e offlineSmokeEngine) Close() error { return errors.Join(e.Engine.Close(), e.closeErr) }

// offlineSmokeChain renders the wrapped chain so the injected label is visible
// in the recorded evidence, not only in the assertion.
func offlineSmokeChain(err error) string {
	parts := []string{}
	for err != nil {
		parts = append(parts, err.Error())
		err = errors.Unwrap(err)
	}
	return strings.Join(parts, " <- ")
}

func offlineSmokePhase(t *testing.T, err error) string {
	t.Helper()
	var failure *Error
	if !errors.As(err, &failure) {
		t.Fatalf("error %v is not a localize phase error", err)
	}
	return failure.Phase
}

// offlineSmokeValidate runs the shared Validate that the pipeline itself uses on
// the produced artifact, so a non-empty output is also checked by the common
// acceptance contract.
func offlineSmokeValidate(t *testing.T, profile validate.Profile, text string) validate.AcceptedTranslation {
	t.Helper()
	session, err := textadapter.New("en", "ja").Import([]byte(offlineSmokeText), profile)
	if err != nil {
		t.Fatal(err)
	}
	id, err := unit.NewUnitID("text", "text-v1", "document")
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := validate.Validate(session, id, profile, text)
	if err != nil {
		t.Fatalf("common Validate rejected the artifact: %v", err)
	}
	if accepted.Text() != text {
		t.Fatalf("accepted text does not match the artifact")
	}
	return accepted
}
