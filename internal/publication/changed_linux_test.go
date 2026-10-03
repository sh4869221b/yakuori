//go:build linux

package publication

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestChangedIdentity(t *testing.T) {
	for _, name := range []string{"source-inode", "output-inode", "stage-inode", "source-bytes", "output-bytes", "stage-bytes", "source-mode", "stage-link", "source-absent", "output-alias", "source-symlink", "parent", "run-directory", "original-dotdot", "during-record"} {
		t.Run(name, func(t *testing.T) {
			d := publicationFixture(t)
			work := filepath.Join(d, "work")
			if err := os.Mkdir(work, 0700); err != nil {
				t.Fatal(err)
			}
			source, output := filepath.Join(work, "source"), filepath.Join(work, "output")
			putFile(t, source, "original")
			putFile(t, output, "existing")
			lookup := source
			if name == "original-dotdot" {
				if err := os.Mkdir(filepath.Join(work, "walk"), 0700); err != nil {
					t.Fatal(err)
				}
				lookup = work + "/walk/../source"
			}
			r, err := Prepare(context.Background(), Options{lookup, output, Replace})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeRun(t, r) })
			stageBytes(t, r, "translated")
			stage := r.stage.path
			sourceWant, outputWant, stageWant := "original", "existing", "translated"
			rename := func(from, to string) {
				t.Helper()
				if err := os.Rename(from, to); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "source-inode", "output-inode", "stage-inode":
				path, bytes := source, "original"
				if name == "output-inode" {
					path, bytes = output, "existing"
				}
				if name == "stage-inode" {
					path, bytes = stage, "translated"
				}
				rename(path, path+"-saved")
				putFile(t, path, bytes)
			case "source-bytes":
				sourceWant = "mutated!"
				putFile(t, source, sourceWant)
			case "output-bytes":
				outputWant = "otherold"
				putFile(t, output, outputWant)
			case "stage-bytes":
				stageWant = "modified!!"
				putFile(t, stage, stageWant)
			case "source-mode":
				if err := os.Chmod(source, 0400); err != nil {
					t.Fatal(err)
				}
			case "stage-link":
				if err := os.Link(stage, filepath.Join(work, "stage-alias")); err != nil {
					t.Fatal(err)
				}
			case "source-absent":
				rename(source, source+"-saved")
				source += "-saved"
			case "output-alias":
				rename(output, output+"-saved")
				if err := os.Link(source, output); err != nil {
					t.Fatal(err)
				}
				outputWant = "original"
			case "source-symlink":
				rename(source, source+"-saved")
				if err := os.Symlink(source+"-saved", source); err != nil {
					t.Fatal(err)
				}
			case "parent":
				moved := filepath.Join(d, "moved")
				rename(work, moved)
				if err := os.Mkdir(work, 0700); err != nil {
					t.Fatal(err)
				}
				putFile(t, source, "newsource")
				putFile(t, output, "newoutput")
				sourceWant, outputWant = "newsource", "newoutput"
				stage = filepath.Join(moved, filepath.Base(r.runDir.path), r.stage.name)
			case "run-directory":
				moved := filepath.Join(work, "run-moved")
				rename(r.runDir.path, moved)
				if err := os.Mkdir(r.runDir.path, 0700); err != nil {
					t.Fatal(err)
				}
				stage = filepath.Join(moved, r.stage.name)
			case "original-dotdot":
				walk := filepath.Join(work, "walk")
				rename(walk, walk+"-saved")
				if err := os.Symlink(walk+"-saved", walk); err != nil {
					t.Fatal(err)
				}
			case "during-record":
				sourceWant = "mutated!"
				r.recordSync = func(f *os.File) error {
					if f == r.runDir.file {
						putFile(t, source, sourceWant)
					}
					return f.Sync()
				}
			}
			result, err := r.Publish(context.Background())
			if !errors.Is(err, ErrChanged) || result.State != NotPublished {
				t.Fatalf("changed path published: %+v %v", result, err)
			}
			wantBytes(t, source, sourceWant)
			wantBytes(t, output, outputWant)
			wantBytes(t, stage, stageWant)
		})
	}
}
