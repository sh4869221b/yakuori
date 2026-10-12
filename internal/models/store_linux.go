//go:build linux

package models

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func privateDir(path string, create bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("managed directory must be private and not a symlink: %s", path)
	}
	return nil
}

func openPrivate(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0) {
		err = fmt.Errorf("managed file must be private and regular: %s", path)
	}
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func readPrivate(path string) ([]byte, error) {
	f, err := openPrivate(path)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	return data, errors.Join(err, f.Close())
}

func (r *Registry) lock() (*os.File, error) {
	f, err := os.OpenFile(r.storage.RegistryLock, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0) {
		err = errors.New("registry lock must be private and regular")
	}
	if err == nil {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	}
	if err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			err = errors.Join(ErrBusy, err)
		}
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func atomicSave(path string, data []byte) (err error) {
	if f, openErr := openPrivate(path); openErr == nil {
		if err := f.Close(); err != nil {
			return err
		}
	} else if !errors.Is(openErr, os.ErrNotExist) {
		return openErr
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".json-*")
	if err != nil {
		return err
	}
	defer func() {
		removeErr := os.Remove(temp.Name())
		if !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	_, writeErr := temp.Write(data)
	if err := errors.Join(writeErr, temp.Close()); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
