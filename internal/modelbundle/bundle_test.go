package modelbundle

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	license, err := os.ReadFile("../../licenses/plamo-community/LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data := []byte("fixture")
		if file.name == "config.json" {
			data = []byte(`{"model_type":"plamo3"}`)
		}
		if file.name == "LICENSE.txt" {
			data = license
		}
		path := filepath.Join(dir, file.source)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestBundleSnapshot(t *testing.T) {
	source := fixture(t)
	var a, b bytes.Buffer
	if err := Pack(context.Background(), &a, source); err != nil {
		t.Fatal(err)
	}
	if err := Pack(context.Background(), &b, source); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("package identity is nondeterministic")
	}
	path := filepath.Join(t.TempDir(), "sealed.model")
	if err := os.WriteFile(path, a.Bytes(), 0400); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Unpack(context.Background(), path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(snapshot)
	if err := os.WriteFile(filepath.Join(source, "tokenizer.jsonl"), []byte("mutated"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(snapshot, "tokenizer.jsonl"))
	if err != nil || string(got) != "fixture" {
		t.Fatalf("mutable snapshot: %q %v", got, err)
	}
	for _, file := range files {
		info, err := os.Stat(filepath.Join(snapshot, file.name))
		if err != nil || info.Mode().Perm() != 0400 {
			t.Fatalf("snapshot mode: %v %v", info, err)
		}
	}
}

func TestBundleRejectsUnsafeSource(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "missing", "bad-license"} {
		t.Run(kind, func(t *testing.T) {
			source := fixture(t)
			target := filepath.Join(source, "tokenizer.jsonl")
			if kind == "bad-license" {
				target = filepath.Join(source, "LICENSE/en")
			}
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				if err := os.Symlink(filepath.Join(source, "config.json"), target); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(target, 0600); err != nil {
					t.Fatal(err)
				}
			case "bad-license":
				if err := os.WriteFile(target, []byte("other"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var buf bytes.Buffer
			err := Pack(context.Background(), &buf, source)
			if kind == "bad-license" {
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "bad.model")
				if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
				parent := t.TempDir()
				dir, err := Unpack(context.Background(), path, parent)
				entries, _ := os.ReadDir(parent)
				if err == nil || dir != "" || len(entries) != 0 {
					t.Fatalf("bad license accepted or leaked snapshot: %q %v %v", dir, err, entries)
				}
			} else if err == nil {
				t.Fatal("unsafe source accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Pack(ctx, io.Discard, fixture(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestBundleRejectsArchiveEntries(t *testing.T) {
	for _, header := range []*tar.Header{
		{Name: "../outside", Typeflag: tar.TypeReg, Size: 1},
		{Name: "config.json", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		{Name: "config.json", Typeflag: tar.TypeReg, Size: 2 << 20},
	} {
		t.Run(header.Name+string(header.Typeflag), func(t *testing.T) {
			var buf bytes.Buffer
			buf.WriteString(Magic)
			writer := tar.NewWriter(&buf)
			if err := writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "bad.model")
			if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			parent := t.TempDir()
			dir, err := Unpack(context.Background(), path, parent)
			entries, _ := os.ReadDir(parent)
			if err == nil || dir != "" || len(entries) != 0 {
				t.Fatalf("unsafe archive accepted or leaked: %q %v", dir, err)
			}
		})
	}
}
