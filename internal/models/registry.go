// Package models manages private, immutable-identity copies of local models.
package models

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/inference/goinfer"
)

var ErrBusy = errors.New("model registry is busy")
var ErrCollision = errors.New("model ID or destination already exists")

// Entry records the identity of the managed bytes, independent of the source.
type Entry struct {
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

type document struct {
	Schema int              `json:"schema"`
	Models map[string]Entry `json:"models"`
}

type Registry struct {
	paths   config.Paths
	storage config.StoragePaths
	open    func(context.Context, string, goinfer.Options) (inference.Engine, error)
}

func New(paths config.Paths) *Registry {
	return &Registry{paths: paths, storage: config.ResolveStoragePaths(paths), open: func(ctx context.Context, path string, options goinfer.Options) (inference.Engine, error) {
		return goinfer.OpenWithOptions(ctx, path, options)
	}}
}

// Load checks the registry schema without hashing or loading any model.
// An absent registry returns os.ErrNotExist and creates nothing.
func (r *Registry) Load() (map[string]Entry, error) {
	if err := privateDir(r.paths.Data, false); err != nil {
		return nil, err
	}
	data, err := readPrivate(r.storage.RegistryFile)
	if err != nil {
		return nil, err
	}
	var doc document
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode registry: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("registry has trailing data: %v", err)
	}
	if doc.Schema != 1 || doc.Models == nil {
		return nil, errors.New("unsupported or missing registry schema/models")
	}
	for id, entry := range doc.Models {
		if err := config.ValidateModelID(id); err != nil {
			return nil, err
		}
		digest, err := hex.DecodeString(entry.SHA256)
		if err != nil || len(digest) != 32 || strings.ToLower(entry.SHA256) != entry.SHA256 || entry.SizeBytes <= 0 {
			return nil, fmt.Errorf("invalid identity for model %q", id)
		}
	}
	return doc.Models, nil
}

func (r *Registry) modelPath(id string) string {
	return filepath.Join(r.storage.ModelsDir, id, "model.gguf")
}

func (r *Registry) Selected() (string, error) {
	if err := privateDir(r.paths.Config, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	data, err := readPrivate(r.storage.ConfigFile)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	settings, err := config.DecodeSettings(data)
	return settings.SelectedModel, err
}

func (r *Registry) Verify(ctx context.Context, id string) (Entry, error) {
	if err := config.ValidateModelID(id); err != nil {
		return Entry{}, err
	}
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	entries, err := r.Load()
	if err != nil {
		return Entry{}, err
	}
	entry, ok := entries[id]
	if !ok {
		return Entry{}, fmt.Errorf("model %q is not registered", id)
	}
	for _, path := range []string{r.storage.ModelsDir, filepath.Dir(r.modelPath(id))} {
		if err := privateDir(path, false); err != nil {
			return Entry{}, err
		}
	}
	f, err := openPrivate(r.modelPath(id))
	if err != nil {
		return Entry{}, err
	}
	actual, hashErr := copyIdentity(ctx, io.Discard, f)
	err = errors.Join(hashErr, f.Close())
	if err != nil {
		return Entry{}, err
	}
	if actual != entry {
		return Entry{}, fmt.Errorf("model %q identity mismatch", id)
	}
	return entry, nil
}
