//go:build linux

// Package tm stores exact translation candidates; stored text is not accepted by storage alone.
package tm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/sh4869221b/yakuori/internal/config"
	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

var (
	ErrOwnership        = errors.New("translation store is already owned")
	ErrRecoveryRequired = errors.New("translation store requires explicit recovery")
	ErrUnsupportedStore = errors.New("unsupported translation store")
)

// Store owns cooperative access until Close. Independent runs may share this store.
type Store struct {
	db        *sql.DB
	limits    config.Limits
	lock      *os.File
	closeOnce sync.Once
	closeErr  error
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		if s.db != nil {
			s.closeErr = s.db.Close()
		}
		s.closeErr = errors.Join(s.closeErr, s.lock.Close())
	})
	return s.closeErr
}

func own(ctx context.Context, path string, limits config.Limits) (*Store, string, error) {
	if err := limits.Check(); err != nil {
		return nil, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, "", err
	}
	path = filepath.Join(parent, filepath.Base(path))
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Nlink != 1 {
			return nil, "", fmt.Errorf("%w: database must be a regular file with one link", ErrUnsupportedStore)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, "", fmt.Errorf("store ownership: %w", err)
	}
	info, err = lock.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, "", errors.Join(ErrUnsupportedStore, err, lock.Close())
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		closeErr := lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			err = errors.Join(ErrOwnership, err)
		}
		return nil, "", errors.Join(err, closeErr)
	}
	return &Store{limits: limits, lock: lock}, path, nil
}

func connect(path, mode string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path, RawQuery: url.Values{"mode": {mode}, "_pragma": {"busy_timeout(100)"}}.Encode()}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func sidecars(path string) (journal bool, err error) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		_, statErr := os.Lstat(path + suffix)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return false, statErr
		}
		if suffix != "-journal" {
			return false, fmt.Errorf("%w: %s sidecar", ErrUnsupportedStore, suffix)
		}
		journal = true
	}
	return journal, nil
}

// Create initializes a new file exclusively. A failed creation is never an Open fallback.
func Create(ctx context.Context, path string, limits config.Limits) (store *Store, err error) {
	ctx, cancel := context.WithTimeout(ctx, limits.DBOperationTimeout)
	defer cancel()
	s, path, err := own(ctx, path, limits)
	if err != nil {
		return nil, err
	}
	created := false
	defer func() {
		if err != nil {
			if created {
				err = errors.Join(err, s.removeCreated(path))
			}
			err = errors.Join(err, s.Close())
		}
	}()
	journal, err := sidecars(path)
	if err != nil {
		return nil, err
	}
	if journal {
		return nil, ErrRecoveryRequired
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	created = true
	if err := f.Close(); err != nil {
		return nil, err
	}
	s.db, err = connect(path, "rw")
	if err != nil {
		return nil, err
	}
	if err := initialize(ctx, s); err != nil {
		return nil, err
	}
	if err := inspect(ctx, s.db); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

// Only Create calls this after exclusive creation and absent-sidecar checks.
// Keep ownership until the new database and its DELETE journal are removed.
func (s *Store) removeCreated(path string) error {
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			return err
		}
		s.db = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.limits.DBCleanupTimeout)
	defer cancel()
	var result error
	for _, suffix := range []string{"-journal", ""} {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}
	return result
}

// Open admits only an existing, clean v1 database. It never performs recovery.
func Open(ctx context.Context, path string, limits config.Limits) (store *Store, err error) {
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
	if journal {
		return nil, ErrRecoveryRequired
	}
	if err := inspectReadOnly(ctx, path); err != nil {
		return nil, err
	}
	s.db, err = connect(path, "rw")
	if err != nil {
		return nil, err
	}
	if err := inspect(ctx, s.db); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s, nil
}
