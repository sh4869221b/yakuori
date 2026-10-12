package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/models"
)

type storageItem struct {
	label, path string
	directory   bool
}

func inspectStorage(paths config.Paths, registry *models.Registry) (string, error) {
	var output strings.Builder
	fmt.Fprintf(&output, "config: %s\ndata: %s\ncache: %s\nstate: %s\n", paths.Config, paths.Data, paths.Cache, paths.State)
	storage := config.ResolveStoragePaths(paths)
	items := []storageItem{
		{"config directory", paths.Config, true}, {"data directory", paths.Data, true},
		{"cache directory", paths.Cache, true}, {"state directory", paths.State, true},
		{"config file", storage.ConfigFile, false}, {"registry file", storage.RegistryFile, false},
		{"models directory", storage.ModelsDir, true}, {"TM file", storage.TMFile, false},
		{"snapshot directory", storage.SnapshotDir, true}, {"registry lock", storage.RegistryLock, false},
	}
	var diagnostic error
	for _, item := range items {
		diagnostic = errors.Join(diagnostic, inspectStorageItem(&output, item))
	}
	if diagnostic != nil {
		return output.String(), diagnostic
	}
	selected, err := registry.Selected()
	if err != nil {
		diagnostic = errors.Join(diagnostic, errors.New("invalid or unreadable model selection configuration"))
	}
	entries, err := registry.Load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		diagnostic = errors.Join(diagnostic, errors.New("invalid or unreadable model registry"))
	}
	if diagnostic != nil {
		return output.String(), diagnostic
	}
	if selected == "" {
		fmt.Fprintln(&output, "selected model: none")
	} else {
		fmt.Fprintf(&output, "selected model: %s\n", selected)
	}
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		dir := filepath.Join(storage.ModelsDir, id)
		diagnostic = errors.Join(diagnostic, inspectStorageItem(&output, storageItem{"model directory " + id, dir, true}))
		diagnostic = errors.Join(diagnostic, inspectStorageItem(&output, storageItem{"model file " + id, filepath.Join(dir, "model.gguf"), false}))
	}
	return output.String(), diagnostic
}

func inspectStorageItem(output *strings.Builder, item storageItem) error {
	info, err := os.Lstat(item.path)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(output, "%s: %s [missing]\n", item.label, item.path)
		return nil
	}
	if err != nil {
		fmt.Fprintf(output, "%s: %s [unreadable]\n", item.label, item.path)
		return fmt.Errorf("%s is unreadable", item.label)
	}
	private := info.Mode().Perm()&0o077 == 0
	validType := info.Mode().IsRegular()
	if item.directory {
		validType = info.IsDir()
	}
	state := "private"
	if !private || !validType {
		state = "insecure mode or type"
	}
	fmt.Fprintf(output, "%s: %s [%s; mode=%04o]\n", item.label, item.path, state, info.Mode().Perm())
	if !private || !validType {
		return fmt.Errorf("%s requires a private regular file or directory", item.label)
	}
	readMode := os.FileMode(0o400)
	if item.directory {
		readMode = 0o500
	}
	if info.Mode().Perm()&readMode != readMode {
		return fmt.Errorf("%s requires owner read access (and directory search access)", item.label)
	}
	return nil
}

type selectedOpener func(context.Context, string) (string, inference.Engine, error)

func checkModel(ctx context.Context, backend string, open selectedOpener) (string, error) {
	id, engine, err := open(ctx, backend)
	if err != nil {
		return "", fmt.Errorf("check model requested_backend=%s: %s", backend, modelDiagnostic(err))
	}
	info, infoErr := engine.Info(ctx)
	closeErr := engine.Close()
	if infoErr != nil {
		return "", fmt.Errorf("check model requested_backend=%s: read Engine.Info failed", backend)
	}
	if closeErr != nil {
		return "", fmt.Errorf("check model requested_backend=%s: close engine failed", backend)
	}
	return fmt.Sprintf("model_id: %s\nmodel_sha256: %x\ntokenizer_sha256: %x\nrequested_backend: %s\neffective_backend: %s\nmodel_context_tokens: %d\nconfigured_context_tokens: %d\neffective_context_tokens: %d\ncompute_quant: %s\nbackend_pin: %s\ncuda_backend_pin: %s\ntokenizer_backend_pin: %s\ncuda_resident_active: %t\ncuda_resident_context_cap: %d\nkv_precision: %s\n",
		id, info.ModelSHA256, info.Tokenizer.ModelSHA256, info.RequestedBackend, info.Backend,
		info.ModelContextTokens, info.ConfiguredContextTokens, info.ContextTokens, info.ComputeQuant,
		info.BackendPin, info.CUDABackendPin, info.Tokenizer.BackendPin, info.ResidentActive, info.ResidentContextCap, info.KVPrecision), nil
}
