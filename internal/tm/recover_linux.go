//go:build linux

package tm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sh4869221b/yakuori/internal/config"
)

// Recover admits one interrupted DELETE journal, first checking SQLite's recovery on a disposable copy.
// External writers, ATTACH/super-journals and concurrent path replacement are outside this contract.
func Recover(ctx context.Context, path string, limits config.Limits) (store *Store, err error) {
	ctx, cancel := context.WithTimeout(ctx, limits.DBOperationTimeout)
	defer cancel()
	s, path, err := own(ctx, path, limits)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.Close())
		}
	}()
	journal, err := sidecars(path)
	if err != nil {
		return nil, err
	}
	if !journal {
		return nil, errors.New("recovery requires a journal")
	}
	dir, err := os.MkdirTemp("", "yakuori-tm-recover-")
	if err != nil {
		return nil, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), limits.DBCleanupTimeout)
		defer cancel()
		err = errors.Join(err, removeRecoveryCopy(cleanup, dir))
		if err != nil {
			store = nil
		}
	}()
	copyPath := filepath.Join(dir, "tm.sqlite")
	for _, suffix := range []string{"", "-journal"} {
		if err := copyRecoveryFile(ctx, path+suffix, copyPath+suffix); err != nil {
			return nil, fmt.Errorf("recovery copy: %w", err)
		}
	}
	if err := rollbackHeader(copyPath); err != nil {
		return nil, err
	}
	copyDB, err := connect(copyPath, "rw")
	if err != nil {
		return nil, err
	}
	copyErr := inspect(ctx, copyDB)
	copyErr = errors.Join(copyErr, copyDB.Close())
	if copyErr != nil {
		return nil, fmt.Errorf("recovery copy inspection: %w", copyErr)
	}
	if err := resolvedRecovery(copyPath); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.db, err = connect(path, "rw")
	if err != nil {
		return nil, err
	}
	if err := inspect(ctx, s.db); err != nil {
		return nil, fmt.Errorf("original recovery failed; retain remaining assets: %w", err)
	}
	if err := resolvedRecovery(path); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

func resolvedRecovery(path string) error {
	journal, err := sidecars(path)
	if err != nil {
		return err
	}
	if journal {
		return errors.New("SQLite did not resolve recovery journal")
	}
	return nil
}

func copyRecoveryFile(ctx context.Context, source, destination string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	from, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, from.Close()) }()
	info, err := from.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("recovery assets must be regular files")
	}
	to, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, to.Close()) }()
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := from.Read(buffer)
		if n > 0 {
			if _, err := to.Write(buffer[:n]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return ctx.Err()
		}
		if readErr != nil {
			return readErr
		}
	}
}

func removeRecoveryCopy(ctx context.Context, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Remove(dir)
}
