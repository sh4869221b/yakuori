//go:build linux

package publication

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRecoveryRecordRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     Mode
		existing bool
	}{{"create", Create, false}, {"replace-existing", Replace, true}, {"replace-absent", Replace, false}, {"in-place", InPlace, false}} {
		t.Run(tc.name, func(t *testing.T) {
			r := publishFixture(t, tc.mode, tc.existing)
			var phases []recordPhase
			r.rename = func(a int, from string, b int, to string, flags uint) error {
				err := unix.Renameat2(a, from, b, to, flags)
				if err == nil && to == "record.json" {
					want := readOperationRecord(t, filepath.Join(r.runDir.path, "record.json"))
					got, readErr := readRecord(r.runDir)
					if readErr != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("writer record round trip: %+v %v", got, readErr)
					}
					phases = append(phases, got.Phase)
				}
				return err
			}
			result, err := r.Publish(context.Background())
			if err != nil || result.State != Published {
				t.Fatalf("writer publication: %+v %v", result, err)
			}
			wantPhases := []recordPhase{preparedPhase, publishedPhase}
			if r.mode == InPlace {
				wantPhases = []recordPhase{preparedPhase, backedUpPhase, publishedPhase}
			}
			if !reflect.DeepEqual(phases, wantPhases) {
				t.Fatalf("writer phases: %v; want %v", phases, wantPhases)
			}
			got, err := readRecord(r.runDir)
			if err != nil || got.Phase != publishedPhase {
				t.Fatalf("published writer record: %+v %v", got, err)
			}
			for _, phase := range []recordPhase{restoredPhase, abandonedPhase} {
				if phase == restoredPhase && r.mode != InPlace {
					continue
				}
				record := got
				record.Phase, record.LastCompletedOperation = phase, "reconcile-unpublished"
				if phase == restoredPhase {
					record.LastCompletedOperation = "restore-rename"
				}
				w := recordWriter{r.runDir, (*os.File).Write, (*os.File).Sync, (*os.File).Close, unix.Renameat2}
				if err := w.write(record, ".record-"+string(phase)); err != nil {
					t.Fatal(err)
				}
				read, err := readRecord(r.runDir)
				if err != nil || !reflect.DeepEqual(read, record) {
					t.Fatalf("terminal existing record round trip: %+v %v", read, err)
				}
			}
		})
	}
}

func TestRecoveryRecordRejectsInvalid(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*operationRecord)
	}{
		{"schema", func(r *operationRecord) { r.SchemaVersion = 2 }},
		{"mode", func(r *operationRecord) { r.Mode = "invalid" }},
		{"phase", func(r *operationRecord) { r.Phase = "invalid" }},
		{"operation", func(r *operationRecord) { r.LastCompletedOperation = "publish-rename" }},
		{"ordinary-backed-up", func(r *operationRecord) { r.Phase, r.LastCompletedOperation = backedUpPhase, "backup-rename" }},
		{"ordinary-restored", func(r *operationRecord) { r.Phase, r.LastCompletedOperation = restoredPhase, "restore-rename" }},
		{"run-id", func(r *operationRecord) { r.RunID = strings.Repeat("A", 32) }},
		{"run-id-mismatch", func(r *operationRecord) { r.RunID = strings.Repeat("0", 32) }},
		{"relative-path", func(r *operationRecord) { r.Source.Path = "source" }},
		{"unclean-path", func(r *operationRecord) { r.Source.Path += "/../source" }},
		{"null-byte-path", func(r *operationRecord) { r.Output.Path += "\x00" }},
		{"basename", func(r *operationRecord) { r.Output.Basename = "different" }},
		{"stage-escape", func(r *operationRecord) { r.Stage.Path = filepath.Join(filepath.Dir(r.Output.Path), "stage") }},
		{"stage-name", func(r *operationRecord) { r.Stage.Path += "-other"; r.Stage.Basename = "stage-other" }},
		{"stage-mount", func(r *operationRecord) { r.Stage.Identity.MountID++ }},
		{"parent-mode", func(r *operationRecord) { r.SourceParent.Mode = unix.S_IFREG | 0700 }},
		{"parent-inode", func(r *operationRecord) { r.OutputParent.Ino = 0 }},
		{"source-size", func(r *operationRecord) { r.Source.Size++ }},
		{"negative-size", func(r *operationRecord) { r.Stage.Size, r.Stage.Identity.Size = -1, -1 }},
		{"file-mode", func(r *operationRecord) { r.Source.Identity.Mode = unix.S_IFDIR | 0600 }},
		{"file-links", func(r *operationRecord) { r.Stage.Identity.Nlink = 2 }},
		{"file-inode", func(r *operationRecord) { r.Stage.Identity.Ino = 0 }},
		{"hash", func(r *operationRecord) { r.Source.SHA256 = strings.Repeat("A", 64) }},
		{"absent-with-file", func(r *operationRecord) { r.ExistingOutput.Absent = true }},
		{"present-without-file", func(r *operationRecord) { r.ExistingOutput.File = nil }},
		{"existing-path", func(r *operationRecord) { r.ExistingOutput.File.Path += "-other" }},
		{"normal-backup", func(r *operationRecord) { r.Backup = &r.Output }},
		{"create-existing", func(r *operationRecord) { r.Mode = Create }},
		{"in-place-path", func(r *operationRecord) { r.Mode = InPlace }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := publishFixture(t, Replace, true)
			if err := r.writeRecord(preparedPhase, "none"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(r.runDir.path, "record.json")
			record := readOperationRecord(t, path)
			tc.change(&record)
			b, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			putFile(t, path, string(b))
			assertRecordRejection(t, r, string(b), ErrInvalidRecord)
		})
	}
	for _, name := range []string{"broken", "array", "truncated", "trailing", "unknown", "duplicate", "nested-duplicate", "nested-unknown", "composite-key", "missing", "nested-missing", "case-key", "null-scalar", "field-type", "oversized", "symlink", "hardlink", "fifo", "directory", "permissions", "run-permissions", "backup-name", "in-place-output"} {
		t.Run(name, func(t *testing.T) {
			mode, existing := Replace, true
			if name == "backup-name" || name == "in-place-output" {
				mode = InPlace
			}
			r := publishFixture(t, mode, existing)
			if err := r.writeRecord(preparedPhase, "none"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(r.runDir.path, "record.json")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			content, wantErr := string(b), ErrInvalidRecord
			switch name {
			case "broken":
				content = `{"private-content":`
			case "array":
				content = "[]"
			case "truncated":
				content = content[:len(content)-1]
			case "trailing":
				content += " {}"
			case "unknown":
				content = strings.Replace(content, `"schema_version":`, `"private-content":1,"schema_version":`, 1)
			case "duplicate":
				content = strings.Replace(content, `"schema_version":`, `"schema_version":1,"schema_version":`, 1)
			case "nested-duplicate":
				content = strings.Replace(content, `"size":`, `"size":0,"size":`, 1)
			case "nested-unknown":
				content = strings.Replace(content, `"dev":`, `"private-content":0,"dev":`, 1)
			case "composite-key":
				content = strings.Replace(content, `"dev":`, `"dev ino":`, 1)
			case "missing":
				content = strings.Replace(content, `"schema_version":1,`, "", 1)
			case "nested-missing":
				content = strings.Replace(content, `,"nlink":1`, "", 1)
			case "case-key":
				content = strings.Replace(content, `"mode":`, `"Mode":`, 1)
			case "null-scalar":
				content = strings.Replace(content, `"schema_version":1`, `"schema_version":null`, 1)
			case "field-type":
				content = strings.Replace(content, `"schema_version":1`, `"schema_version":"private-content"`, 1)
			case "oversized":
				content = strings.Repeat(" ", 64*1024+1)
				wantErr = ErrRecordTooLarge
			case "backup-name", "in-place-output":
				record := readOperationRecord(t, path)
				if name == "backup-name" {
					record.Backup.Basename = "other"
				} else {
					record.ExistingOutput.File.Size++
				}
				b, err = json.Marshal(record)
				content = string(b)
			}
			if err != nil {
				t.Fatal(err)
			}
			putFile(t, path, content)
			switch name {
			case "symlink", "hardlink", "fifo", "directory":
				other := filepath.Join(r.runDir.path, "retained-record")
				if err = os.Rename(path, other); err != nil {
					t.Fatal(err)
				}
				switch name {
				case "symlink":
					err = os.Symlink(other, path)
				case "hardlink":
					err = os.Link(other, path)
				case "fifo":
					err = unix.Mkfifo(path, 0600)
				case "directory":
					err = os.Mkdir(path, 0600)
				}
			case "permissions":
				err = os.Chmod(path, 0644)
			case "run-permissions":
				err = os.Chmod(r.runDir.path, 0755)
			}
			if err != nil {
				t.Fatal(err)
			}
			if name == "fifo" || name == "directory" {
				content = ""
			}
			assertRecordRejection(t, r, content, wantErr)
			if name == "symlink" || name == "hardlink" || name == "fifo" || name == "directory" {
				wantBytes(t, filepath.Join(r.runDir.path, "retained-record"), string(b))
			}
		})
	}
}

func assertRecordRejection(t *testing.T, r *Run, recordBytes string, wantErr error) {
	t.Helper()
	path := filepath.Join(r.runDir.path, "record.json")
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, r.backupPath(), "retained backup")
	_, err = readRecord(r.runDir)
	if !errors.Is(err, wantErr) || strings.Contains(err.Error(), "private-content") {
		t.Fatalf("unsafe record accepted or content exposed: %v", err)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() {
		t.Fatalf("reader changed record: %v", err)
	}
	if recordBytes != "" {
		wantBytes(t, path, recordBytes)
	}
	wantBytes(t, r.source.path, "original")
	if r.mode != InPlace {
		wantBytes(t, r.output.path, "existing")
	}
	wantBytes(t, r.stage.path, "translated")
	wantBytes(t, r.backupPath(), "retained backup")
}
