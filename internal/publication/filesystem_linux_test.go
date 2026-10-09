//go:build linux

package publication

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func externalPublicationFixture(t *testing.T, fromMount uint64) (string, *os.File, identity) {
	t.Helper()
	for _, base := range []string{"/var/tmp", ".", "/tmp"} {
		d, err := os.MkdirTemp(base, "yakuori-publication-mount-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.RemoveAll(d); err != nil {
				t.Error(err)
			}
		})
		f, err := os.Open(d)
		if err != nil {
			t.Fatal(err)
		}
		id, err := identify(f)
		if err != nil {
			f.Close()
			t.Fatal(err)
		}
		if id.MountID == fromMount {
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			continue
		}
		t.Cleanup(func() {
			if err := f.Close(); err != nil {
				t.Error(err)
			}
		})
		return d, f, id
	}
	t.Fatal("separate-mount scenario requires distinct mount IDs")
	return "", nil, identity{}
}

func TestPublicationPermissionFailure(t *testing.T) {
	for _, name := range []string{"replace", "backup", "in-place-publish"} {
		t.Run(name, func(t *testing.T) {
			mode := InPlace
			if name == "replace" {
				mode = Replace
			}
			r := publishFixture(t, mode, true)
			if os.Geteuid() == 0 {
				t.Fatal("real EACCES scenario requires non-root")
			}
			r.rename = func(a int, from string, b int, to string, flags uint) error {
				denied := (name == "backup" && to == r.backupName()) || (name != "backup" && from == r.stage.name)
				if !denied {
					return unix.Renameat2(a, from, b, to, flags)
				}
				if err := unix.Fchmod(b, 0500); err != nil {
					return err
				}
				err := unix.Renameat2(a, from, b, to, flags)
				return errors.Join(err, unix.Fchmod(b, 0700))
			}
			result, err := r.Publish(context.Background())
			state := NotPublished
			if name == "in-place-publish" {
				state = Restored
			}
			if !errors.Is(err, unix.EACCES) || result.State != state {
				t.Fatalf("permission result: %+v %v", result, err)
			}
			wantBytes(t, r.stage.path, "translated")
			wantBytes(t, r.source.path, "original")
			if mode == Replace {
				wantBytes(t, r.output.path, "existing")
			} else {
				wantAbsent(t, r.backupPath())
			}
		})
	}
}

func TestPublicationCrossMountFailure(t *testing.T) {
	for _, mode := range []Mode{Replace, InPlace} {
		t.Run(string(mode), func(t *testing.T) {
			r := publishFixture(t, mode, true)
			other, f, id := externalPublicationFixture(t, r.runDir.id.MountID)
			if id.MountID == r.runDir.id.MountID {
				t.Fatal("EXDEV scenario requires distinct mount IDs")
			}
			t.Logf("stage dev=%d mount_id=%d; destination dev=%d mount_id=%d", r.runDir.id.Dev, r.runDir.id.MountID, id.Dev, id.MountID)
			otherOutput := filepath.Join(other, "output")
			putFile(t, otherOutput, "other-existing")
			r.rename = func(a int, from string, b int, to string, flags uint) error {
				if from == r.stage.name {
					return unix.Renameat2(a, from, int(f.Fd()), "output", flags)
				}
				return unix.Renameat2(a, from, b, to, flags)
			}
			result, err := r.Publish(context.Background())
			state := NotPublished
			if mode == InPlace {
				state = Restored
			}
			if !errors.Is(err, unix.EXDEV) || result.State != state {
				t.Fatalf("cross mount: %+v %v", result, err)
			}
			wantBytes(t, otherOutput, "other-existing")
			wantBytes(t, r.stage.path, "translated")
			if mode == InPlace {
				wantBytes(t, r.source.path, "original")
				wantAbsent(t, result.BackupPath)
			} else {
				wantBytes(t, r.source.path, "original")
				wantBytes(t, r.output.path, "existing")
			}
		})
	}
}

func TestPublicationSourceAcrossMount(t *testing.T) {
	d := publicationFixture(t)
	output := filepath.Join(d, "output")
	l, err := openParent(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.parent.file.Close(); err != nil {
		t.Fatal(err)
	}
	other, _, id := externalPublicationFixture(t, l.parent.id.MountID)
	source := filepath.Join(other, "source")
	putFile(t, source, "original")
	r, err := Prepare(context.Background(), Options{source, output, Create})
	if err != nil {
		t.Fatal(err)
	}
	defer closeRun(t, r)
	if id.MountID == r.output.parent.id.MountID {
		t.Fatal("cross-mount source requires distinct mount IDs")
	}
	t.Logf("source mount_id=%d; output mount_id=%d", id.MountID, r.output.parent.id.MountID)
	stageBytes(t, r, "translated")
	result, err := r.Publish(context.Background())
	if err != nil || result.State != Published {
		t.Fatalf("cross mount source publish: %+v %v", result, err)
	}
	wantBytes(t, source, "original")
	wantBytes(t, output, "translated")
	wantAbsent(t, r.stage.path)
}
