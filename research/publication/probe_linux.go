//go:build linux && (amd64 || arm64)

// Package publication is an isolated issue #8 experiment, not a product API.
package publication

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// rename uses an already-open directory and single-component names. No fallback.
func rename(dir *os.File, from, to string, flags uintptr) error {
	a, err := syscall.BytePtrFromString(from)
	if err != nil {
		return err
	}
	b, err := syscall.BytePtrFromString(to)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(renameat2Trap, dir.Fd(), uintptr(unsafe.Pointer(a)), dir.Fd(), uintptr(unsafe.Pointer(b)), flags, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func hash(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// observe refuses links/non-regular files; absent is distinct from empty content.
func observe(path string) (string, error) {
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file: %s", path)
	}
	if st.Sys().(*syscall.Stat_t).Nlink != 1 {
		return "", fmt.Errorf("hardlinked file: %s", path)
	}
	return hash(path)
}

// classify is a finite-state decision table for same-path recovery fixtures.
// Production #10 must also verify inode identities, record/path integrity and lock.
func classify(original, staged, final, backup, remainingStage string) string {
	switch {
	case backup == "" && final == original:
		return "unchanged"
	case backup == original && final == "":
		return "restore-no-replace"
	case backup == original && final == staged && remainingStage == "":
		return "published-keep-backup"
	default:
		return "manual-no-mutation"
	}
}
