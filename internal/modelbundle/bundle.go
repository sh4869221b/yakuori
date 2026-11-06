// Package modelbundle stores the files of a PLaMo 3 checkpoint in one deterministic artifact.
package modelbundle

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const Magic = "YAKPLM3\n"
const LicenseSHA256 = "d9979039b8f1a96fa81e1be61c4550d06f8542b25119e3f765c7c011a68597fb"

var files = []struct {
	source, name string
	limit        int64
}{
	{"config.json", "config.json", 1 << 20},
	{"generation_config.json", "generation_config.json", 1 << 20},
	{"tokenizer_config.json", "tokenizer_config.json", 2 << 20},
	{"tokenizer.jsonl", "tokenizer.jsonl", 32 << 20},
	{"chat_template.jinja", "chat_template.jinja", 128 << 10},
	{"LICENSE/en", "LICENSE.txt", 64 << 10},
	{"model.safetensors", "model.safetensors", (8 << 30) - 1},
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Pack copies only the inference files, using pinned directory descriptors and no symlink traversal.
func Pack(ctx context.Context, dst io.Writer, source string) (err error) {
	root, err := unix.Open(source, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unix.Close(root)) }()
	if _, err = io.WriteString(dst, Magic); err != nil {
		return err
	}
	writer := tar.NewWriter(dst)
	for _, spec := range files {
		f, openErr := openSource(root, spec.source)
		if openErr != nil {
			return fmt.Errorf("PLaMo checkpoint missing or unsafe %s: %w", spec.source, openErr)
		}
		err = func() (err error) {
			defer func() { err = errors.Join(err, f.Close()) }()
			info, err := f.Stat()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > spec.limit {
				return fmt.Errorf("invalid PLaMo checkpoint file %s", spec.source)
			}
			if err = writer.WriteHeader(&tar.Header{Name: spec.name, Mode: 0400, Size: info.Size(), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
				return err
			}
			_, err = io.CopyN(writer, contextReader{ctx, f}, info.Size())
			return err
		}()
		if err != nil {
			return err
		}
	}
	return writer.Close()
}

func openSource(root int, name string) (*os.File, error) {
	dir := root
	if name == "LICENSE/en" {
		fd, err := unix.Openat(root, "LICENSE", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		defer unix.Close(fd)
		dir = fd
		name = "en"
	}
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func Is(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	buf := make([]byte, len(Magic))
	n, err := io.ReadFull(f, buf)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return false, nil
	}
	return n == len(Magic) && string(buf) == Magic, err
}

// Unpack accepts the exact uncompressed format and returns a private read-only snapshot.
// The caller removes it after both loaders have consumed it; Linux keeps mapped weights alive after unlink.
func Unpack(ctx context.Context, path, parent string) (snapshot string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Join(err, f.Close())
		if err != nil && snapshot != "" {
			err = errors.Join(err, os.RemoveAll(snapshot))
			snapshot = ""
		}
	}()
	magic := make([]byte, len(Magic))
	if _, err = io.ReadFull(f, magic); err != nil {
		return "", err
	}
	if string(magic) != Magic {
		return "", errors.New("unsupported model bundle")
	}
	snapshot, err = os.MkdirTemp(parent, "yakuori-plamo3-*")
	if err != nil {
		return "", err
	}
	reader := tar.NewReader(contextReader{ctx, f})
	for _, spec := range files {
		h, err := reader.Next()
		if err != nil {
			return snapshot, fmt.Errorf("read model bundle: %w", err)
		}
		if h.Name != spec.name || h.Typeflag != tar.TypeReg || h.Size <= 0 || h.Size > spec.limit {
			return snapshot, errors.New("unsupported model bundle entry")
		}
		out, err := os.OpenFile(filepath.Join(snapshot, spec.name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
		if err != nil {
			return snapshot, err
		}
		_, copyErr := io.CopyN(out, reader, h.Size)
		if err = errors.Join(copyErr, out.Close()); err != nil {
			return snapshot, err
		}
	}
	if _, err = reader.Next(); !errors.Is(err, io.EOF) {
		return snapshot, errors.New("model bundle has additional entries")
	}
	if err = validate(snapshot); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func validate(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return err
	}
	var cfg struct {
		ModelType string `json:"model_type"`
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.ModelType != "plamo3" {
		return errors.New("model bundle requires a PLaMo 3 config")
	}
	license, err := os.ReadFile(filepath.Join(dir, "LICENSE.txt"))
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(license)) != LicenseSHA256 {
		return errors.New("unrecognized PLaMo Community License")
	}
	return nil
}
