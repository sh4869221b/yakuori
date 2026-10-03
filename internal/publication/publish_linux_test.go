//go:build linux

package publication

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func publishFixture(t *testing.T, mode Mode, existing bool) *Run {
	t.Helper()
	d := publicationFixture(t)
	work := filepath.Join(d, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	source, output := filepath.Join(work, "source"), filepath.Join(work, "output")
	putFile(t, source, "original")
	if mode == InPlace {
		output = source
	} else if existing {
		putFile(t, output, "existing")
	}
	r, err := Prepare(context.Background(), Options{source, output, mode})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeRun(t, r) })
	stageBytes(t, r, "translated")
	return r
}

func stageBytes(t *testing.T, r *Run, bytes string) {
	t.Helper()
	err := r.Stage(context.Background(), func(w io.Writer) error { _, err := io.WriteString(w, bytes); return err }, func(rd io.Reader) error {
		b, err := io.ReadAll(rd)
		if err != nil {
			return err
		}
		if string(b) != bytes {
			t.Fatalf("validator bytes %q; want %q", b, bytes)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func readOperationRecord(t *testing.T, path string) operationRecord {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record operationRecord
	if err := json.Unmarshal(b, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestPublishModes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     Mode
		existing bool
	}{{"create", Create, false}, {"replace-existing", Replace, true}, {"replace-absent", Replace, false}, {"in-place", InPlace, false}} {
		t.Run(tc.name, func(t *testing.T) {
			r := publishFixture(t, tc.mode, tc.existing)
			var old *os.File
			if tc.existing {
				var err error
				old, err = os.Open(r.output.path)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := old.Close(); err != nil {
						t.Error(err)
					}
				}()
			}
			result, err := r.Publish(context.Background())
			if err != nil || result.State != Published {
				t.Fatalf("publish: %+v %v", result, err)
			}
			wantBytes(t, r.output.path, "translated")
			wantAbsent(t, r.stage.path)
			if tc.mode == InPlace {
				wantBytes(t, result.BackupPath, "original")
				if result.BackupPath != r.backupPath() {
					t.Fatal("backup diagnostic path mismatch")
				}
			} else {
				wantBytes(t, r.source.path, "original")
				if result.BackupPath != "" {
					t.Fatal("ordinary mode returned backup")
				}
			}
			if old != nil {
				b, err := io.ReadAll(old)
				if err != nil || string(b) != "existing" {
					t.Fatalf("old reader changed: %q %v", b, err)
				}
			}
			record := readOperationRecord(t, result.RecordPath)
			if record.SchemaVersion != 1 || record.RunID != result.RunID || record.Mode != tc.mode || record.Phase != publishedPhase || record.LastCompletedOperation != "publish-rename" {
				t.Fatalf("wrong completed record: %+v", record)
			}
			if record.Source.Identity != r.source.id || record.Stage.Identity != r.stage.id || record.Source.SHA256 != r.source.digest || record.Stage.SHA256 != r.stage.digest || record.Source.Size != 8 || record.Stage.Size != 10 {
				t.Fatal("record lost initial file identity")
			}
			if record.Source.Path != r.source.path || record.Source.Basename != r.source.name || record.Output.Path != r.output.path || record.Stage.Path != r.stage.path {
				t.Fatal("record paths mismatch")
			}
			if record.ExistingOutput.Absent == r.output.present || (record.ExistingOutput.File != nil) != r.output.present {
				t.Fatal("record absence is ambiguous")
			}
			if (record.Backup != nil) != (tc.mode == InPlace) {
				t.Fatal("record backup is ambiguous")
			}
			st, err := os.Stat(result.RecordPath)
			if err != nil || st.Mode().Perm() != 0600 {
				t.Fatalf("record mode: %v %v", st, err)
			}
		})
	}
	t.Run("same-source-stage-bytes", func(t *testing.T) {
		d := publicationFixture(t)
		source := filepath.Join(d, "source")
		putFile(t, source, "original")
		r, err := Prepare(context.Background(), Options{source, source, InPlace})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { closeRun(t, r) })
		stageBytes(t, r, "original")
		result, err := r.Publish(context.Background())
		if err != nil || result.State != Published {
			t.Fatalf("equal bytes failed: %+v %v", result, err)
		}
		wantBytes(t, source, "original")
		wantBytes(t, result.BackupPath, "original")
		record := readOperationRecord(t, result.RecordPath)
		if record.Source.SHA256 != record.Stage.SHA256 || record.Source.Identity.Ino == record.Stage.Identity.Ino || record.Phase != publishedPhase || record.Backup.Path == record.Output.Path {
			t.Fatal("equal bytes confused stage and original")
		}
	})
}

func TestPublishRefusal(t *testing.T) {
	for _, mode := range []Mode{Create, Replace} {
		t.Run(string(mode)+"-absent-destination-race", func(t *testing.T) {
			r := publishFixture(t, mode, false)
			r.rename = func(a int, from string, b int, to string, flags uint) error {
				if from == r.stage.name {
					putFile(t, r.output.path, "racing-output")
				}
				return unix.Renameat2(a, from, b, to, flags)
			}
			result, err := r.Publish(context.Background())
			if !errors.Is(err, unix.EEXIST) || result.State != NotPublished {
				t.Fatalf("absent destination was overwritten: %+v %v", result, err)
			}
			wantBytes(t, r.source.path, "original")
			wantBytes(t, r.output.path, "racing-output")
			wantBytes(t, r.stage.path, "translated")
		})
	}
	for _, name := range []string{"unstaged", "failed-stage", "zero", "nil", "closed", "repeat-success", "repeat-failure"} {
		t.Run(name, func(t *testing.T) {
			r, source, output := stageFixture(t)
			want := NotPublished
			switch name {
			case "failed-stage":
				if err := r.Stage(context.Background(), func(io.Writer) error { return io.ErrUnexpectedEOF }, func(io.Reader) error { return nil }); err == nil {
					t.Fatal("failure setup validated")
				}
			case "zero":
				r = &Run{}
			case "nil":
				r = nil
			case "closed":
				closeRun(t, r)
			case "repeat-success", "repeat-failure":
				stageBytes(t, r, "translated")
				if name == "repeat-failure" {
					r.recordWrite = func(*os.File, []byte) (int, error) { return 0, io.ErrUnexpectedEOF }
				} else {
					want = Published
				}
				first, err := r.Publish(context.Background())
				if first.State != want || (err != nil) != (name == "repeat-failure") {
					t.Fatalf("first publish: %+v %v", first, err)
				}
			}
			result, err := r.Publish(context.Background())
			if !errors.Is(err, ErrInvalidPublish) || result.State != want {
				t.Fatalf("invalid publish admitted: %+v %v", result, err)
			}
			wantBytes(t, source, "original")
			if want == Published {
				wantBytes(t, output, "translated")
			} else {
				wantBytes(t, output, "existing")
			}
		})
	}
}
