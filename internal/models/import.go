package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sh4869221b/yakuori/internal/config"
	"golang.org/x/sys/unix"
)

func copyIdentity(ctx context.Context, dst io.Writer, src io.Reader) (Entry, error) {
	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return Entry{}, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			written, err := io.MultiWriter(dst, hash).Write(buffer[:n])
			size += int64(written)
			if err != nil {
				return Entry{}, err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return Entry{}, readErr
		}
	}
	if size == 0 {
		return Entry{}, errors.New("model file is empty")
	}
	return Entry{SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: size}, nil
}

func (r *Registry) Import(ctx context.Context, id, source string) (entry Entry, err error) {
	if err := config.ValidateModelID(id); err != nil {
		return Entry{}, err
	}
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	for _, path := range []string{r.paths.Data, r.storage.ModelsDir, r.paths.Cache, r.storage.SnapshotDir} {
		if err := privateDir(path, true); err != nil {
			return Entry{}, err
		}
	}
	lock, err := r.lock()
	if err != nil {
		return Entry{}, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	entries, err := r.Load()
	if errors.Is(err, os.ErrNotExist) {
		entries = make(map[string]Entry)
	} else if err != nil {
		return Entry{}, err
	}
	if _, ok := entries[id]; ok {
		return Entry{}, ErrCollision
	}
	destination := filepath.Dir(r.modelPath(id))
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return Entry{}, err
		}
		return Entry{}, ErrCollision
	}
	src, err := os.OpenFile(source, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return Entry{}, err
	}
	defer func() { err = errors.Join(err, src.Close()) }()
	info, err := src.Stat()
	if err != nil {
		return Entry{}, err
	}
	if !info.Mode().IsRegular() {
		return Entry{}, errors.New("source model must be a regular file")
	}
	stage, err := os.MkdirTemp(r.storage.ModelsDir, ".import-*")
	if err != nil {
		return Entry{}, err
	}
	owned := stage
	defer func() {
		if owned != "" {
			err = errors.Join(err, os.RemoveAll(owned))
		}
	}()
	path := filepath.Join(stage, "model.gguf")
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Entry{}, err
	}
	entry, copyErr := copyIdentity(ctx, dst, src)
	if err := errors.Join(copyErr, dst.Close()); err != nil {
		return Entry{}, err
	}
	engine, err := r.open(ctx, path, r.options(entry, "cpu"))
	if err != nil {
		return Entry{}, fmt.Errorf("validate imported model: %w", err)
	}
	if err := engine.Close(); err != nil {
		return Entry{}, err
	}
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return Entry{}, errors.Join(ErrCollision, err)
		}
		return Entry{}, err
	}
	owned = destination
	entries[id] = entry
	data, err := json.Marshal(document{Schema: 1, Models: entries})
	if err != nil {
		return Entry{}, err
	}
	if err := atomicSave(r.storage.RegistryFile, data); err != nil {
		return Entry{}, err
	}
	owned = ""
	return entry, nil
}

func (r *Registry) Use(ctx context.Context, id string) (err error) {
	if err := config.ValidateModelID(id); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := privateDir(r.paths.Data, false); err != nil {
		return err
	}
	lock, err := r.lock()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	if _, err := r.Verify(ctx, id); err != nil {
		return err
	}
	if err := privateDir(r.paths.Config, true); err != nil {
		return err
	}
	// A corrupt existing selection is an error, not an implicit repair.
	if _, err := r.Selected(); err != nil {
		return err
	}
	data, err := config.EncodeSettings(config.Settings{SelectedModel: id})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return atomicSave(r.storage.ConfigFile, data)
}
