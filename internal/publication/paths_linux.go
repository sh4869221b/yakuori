//go:build linux

package publication

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type identity struct {
	Dev     uint64 `json:"dev"`
	Ino     uint64 `json:"ino"`
	MountID uint64 `json:"mount_id"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
	Nlink   uint64 `json:"nlink"`
}

type directory struct {
	file *os.File
	id   identity
	path string
}

type leaf struct {
	parent     *directory
	name       string
	path       string
	lookupPath string
	id         identity
	digest     string
	present    bool
}

func identify(f *os.File) (identity, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return identity{}, fmt.Errorf("stat descriptor: %w", err)
	}
	var sx unix.Statx_t
	if err := unix.Statx(int(f.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &sx); err != nil {
		return identity{}, fmt.Errorf("identify mount: %w", err)
	}
	if sx.Mask&unix.STATX_MNT_ID == 0 {
		return identity{}, fmt.Errorf("missing mount ID: %w", ErrUnsupportedFilesystem)
	}
	return identity{st.Dev, st.Ino, sx.Mnt_id, st.Size, st.Mode, uint64(st.Nlink)}, nil
}

func openParent(path string) (_ leaf, err error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return leaf{}, ErrInvalidPath
	}
	components := strings.Split(path, "/")
	name := components[len(components)-1]
	if name == "" || name == "." || name == ".." {
		return leaf{}, ErrInvalidPath
	}
	start := "."
	if filepath.IsAbs(path) {
		start = "/"
	}
	fd, err := unix.Open(start, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return leaf{}, fmt.Errorf("open path root: %w", err)
	}
	f := os.NewFile(uintptr(fd), start)
	defer func() {
		if err != nil {
			err = errors.Join(err, f.Close())
		}
	}()
	// Do not clean first: a symlink before '..' must still be rejected.
	for _, component := range components[:len(components)-1] {
		if component == "" {
			continue
		}
		next, e := unix.Openat(int(f.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if e != nil {
			return leaf{}, fmt.Errorf("open parent component: %w: %w", ErrInvalidPath, e)
		}
		g := os.NewFile(uintptr(next), component)
		if e = f.Close(); e != nil {
			return leaf{}, errors.Join(e, g.Close())
		}
		f = g
	}
	id, err := identify(f)
	if err != nil {
		return leaf{}, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return leaf{}, fmt.Errorf("diagnostic path: %w", err)
	}
	lookupPath := path
	if !filepath.IsAbs(path) {
		cwd, e := os.Getwd()
		if e != nil {
			return leaf{}, fmt.Errorf("path working directory: %w", e)
		}
		lookupPath = cwd + "/" + path
	}
	return leaf{parent: &directory{f, id, filepath.Dir(abs)}, name: name, path: abs, lookupPath: lookupPath}, nil
}

func observe(l *leaf) (_ []byte, err error) {
	// O_NONBLOCK avoids hanging on a FIFO before its file type is checked.
	fd, err := unix.Openat(int(l.parent.file.Fd()), l.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open leaf: %w: %w", ErrInvalidPath, err)
	}
	f := os.NewFile(uintptr(fd), l.name)
	defer func() { err = errors.Join(err, f.Close()) }()
	id, err := identify(f)
	if err != nil {
		return nil, err
	}
	if id.Mode&unix.S_IFMT != unix.S_IFREG || id.Nlink != 1 {
		return nil, ErrInvalidPath
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read leaf: %w", err)
	}
	after, err := identify(f)
	if err != nil {
		return nil, err
	}
	if after != id || int64(len(b)) != id.Size {
		return nil, fmt.Errorf("leaf changed during snapshot: %w", ErrInvalidPath)
	}
	h := sha256.Sum256(b)
	l.id, l.digest, l.present = id, hex.EncodeToString(h[:]), true
	return b, nil
}

func filesystem(d *directory) (_ string, err error) {
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(d.file.Fd()), &st); err != nil {
		return "", fmt.Errorf("filesystem descriptor: %w", err)
	}
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return "", fmt.Errorf("read mount information: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 7 {
			continue
		}
		id, e := strconv.ParseUint(fields[0], 10, 64)
		if e != nil || id != d.id.MountID {
			continue
		}
		for i := 6; i+1 < len(fields); i++ {
			if fields[i] != "-" {
				continue
			}
			fs := fields[i+1]
			if (fs == "ext4" && st.Type == unix.EXT4_SUPER_MAGIC) || (fs == "btrfs" && st.Type == unix.BTRFS_SUPER_MAGIC) {
				return fs, nil
			}
			return "", fmt.Errorf("filesystem %s: %w", fs, ErrUnsupportedFilesystem)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan mount information: %w", err)
	}
	return "", fmt.Errorf("mount not identified: %w", ErrUnsupportedFilesystem)
}
