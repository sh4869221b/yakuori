package models

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/inference/goinfer"
)

func (r *Registry) options(entry Entry, backend string) goinfer.Options {
	digestBytes, _ := hex.DecodeString(entry.SHA256) // Entry was validated at the registry boundary or computed by Import.
	var digest [32]byte
	copy(digest[:], digestBytes)
	return goinfer.Options{Backend: backend, ComputeQuant: "int4", ContextTokens: config.DefaultLimits().ContextTokens, ExpectedModelSHA256: &digest, SnapshotDir: r.storage.SnapshotDir}
}

// OpenSelected binds the selected ID to the actual Engine snapshot identity.
func (r *Registry) OpenSelected(ctx context.Context, backend string) (string, inference.Engine, error) {
	id, err := r.Selected()
	if err != nil {
		return "", nil, err
	}
	if id == "" {
		return "", nil, errors.New("no model selected")
	}
	entry, err := r.Verify(ctx, id)
	if err != nil {
		return "", nil, err
	}
	if err := privateDir(r.paths.Cache, false); err != nil {
		return "", nil, err
	}
	if err := privateDir(r.storage.SnapshotDir, false); err != nil {
		return "", nil, err
	}
	engine, err := r.open(ctx, r.modelPath(id), r.options(entry, backend))
	if err != nil {
		return "", nil, err
	}
	return id, engine, nil
}
