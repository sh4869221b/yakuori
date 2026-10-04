//go:build linux

package publication

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

var ErrInvalidRecord = errors.New("invalid publication record")

// readRecord uses the retained private directory, never paths from the JSON.
func readRecord(dir *directory) (_ operationRecord, err error) {
	id, err := identify(dir.file)
	if err != nil {
		return operationRecord{}, err
	}
	if id.Mode != unix.S_IFDIR|0700 || !sameDirectory(id, dir.id) {
		return operationRecord{}, fmt.Errorf("private run directory: %w", ErrInvalidRecord)
	}
	fd, err := unix.Openat(int(dir.file.Fd()), "record.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return operationRecord{}, fmt.Errorf("open record: %w: %w", ErrInvalidRecord, err)
	}
	f := os.NewFile(uintptr(fd), "record.json")
	defer func() { err = errors.Join(err, f.Close()) }()
	id, err = identify(f)
	if err != nil {
		return operationRecord{}, err
	}
	if id.Mode != unix.S_IFREG|0600 || id.Nlink != 1 || id.MountID != dir.id.MountID || id.Size < 0 {
		return operationRecord{}, fmt.Errorf("private record file: %w", ErrInvalidRecord)
	}
	if id.Size > 64*1024 {
		return operationRecord{}, ErrRecordTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil {
		return operationRecord{}, fmt.Errorf("read record: %w", err)
	}
	if len(b) > 64*1024 {
		return operationRecord{}, ErrRecordTooLarge
	}
	after, err := identify(f)
	if err != nil {
		return operationRecord{}, err
	}
	if after != id || int64(len(b)) != id.Size {
		return operationRecord{}, fmt.Errorf("record changed during read: %w", ErrInvalidRecord)
	}
	return parseRecord(b, dir)
}

// Exact keys are checked before typed decoding: encoding/json alone accepts
// repeated keys, case-insensitive keys and missing scalar fields.
func recordFields(b []byte, keys string) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("record object expected: %w", ErrInvalidRecord)
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || !slices.Contains(strings.Fields(keys), key) {
			return nil, fmt.Errorf("unexpected record field: %w", ErrInvalidRecord)
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("repeated record field: %w", ErrInvalidRecord)
		}
		var value json.RawMessage
		if err = d.Decode(&value); err != nil {
			return nil, fmt.Errorf("malformed record value: %w", ErrInvalidRecord)
		}
		if bytes.Equal(value, []byte("null")) && key != "backup" && key != "file" {
			return nil, fmt.Errorf("null record value: %w", ErrInvalidRecord)
		}
		fields[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || len(fields) != len(strings.Fields(keys)) {
		return nil, fmt.Errorf("missing record field: %w", ErrInvalidRecord)
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("trailing record data: %w", ErrInvalidRecord)
	}
	return fields, nil
}

func parseRecord(b []byte, dir *directory) (operationRecord, error) {
	fields, err := recordFields(b, "schema_version run_id mode source output stage backup source_parent output_parent existing_output phase last_completed_operation")
	if err != nil {
		return operationRecord{}, err
	}
	existing, err := recordFields(fields["existing_output"], "absent file")
	if err != nil {
		return operationRecord{}, err
	}
	for _, value := range []json.RawMessage{fields["output"], fields["backup"]} {
		if !bytes.Equal(value, []byte("null")) {
			if _, err = recordFields(value, "path basename"); err != nil {
				return operationRecord{}, err
			}
		}
	}
	ids := []json.RawMessage{fields["source_parent"], fields["output_parent"]}
	for _, value := range []json.RawMessage{fields["source"], fields["stage"], existing["file"]} {
		if bytes.Equal(value, []byte("null")) {
			continue
		}
		file, e := recordFields(value, "path basename identity sha256 size")
		if e != nil {
			return operationRecord{}, e
		}
		ids = append(ids, file["identity"])
	}
	for _, value := range ids {
		if _, err = recordFields(value, "dev ino mount_id size mode nlink"); err != nil {
			return operationRecord{}, err
		}
	}
	var record operationRecord
	if err = json.Unmarshal(b, &record); err != nil {
		// Decoder errors can quote arbitrary JSON values; keep file bodies private.
		return operationRecord{}, fmt.Errorf("invalid record field type: %w", ErrInvalidRecord)
	}
	if err = validateRecord(record, dir); err != nil {
		return operationRecord{}, err
	}
	return record, nil
}

func validateRecord(r operationRecord, dir *directory) error {
	invalid := func(reason string) error { return fmt.Errorf("%s: %w", reason, ErrInvalidRecord) }
	if r.SchemaVersion != 1 || !recordHex(r.RunID, 32) {
		return invalid("record schema/run ID")
	}
	if !validRecordPath(r.Output) || !validRecordFile(r.Source) || !validRecordFile(r.Stage) {
		return invalid("record paths/files")
	}
	for _, id := range []identity{r.SourceParent, r.OutputParent} {
		if id.Mode&unix.S_IFMT != unix.S_IFDIR || id.Ino == 0 || id.MountID == 0 || id.Nlink == 0 || id.Size < 0 {
			return invalid("record parent identity")
		}
	}
	wantDir := filepath.Join(filepath.Dir(r.Output.Path), ".yakuori-run-"+r.RunID)
	if dir.path != wantDir || r.Stage.Path != filepath.Join(wantDir, "stage") || r.Stage.Basename != "stage" ||
		r.Stage.Identity.Dev != r.OutputParent.Dev || r.Stage.Identity.MountID != r.OutputParent.MountID ||
		dir.id.Dev != r.OutputParent.Dev || dir.id.MountID != r.OutputParent.MountID {
		return invalid("record run/stage location")
	}
	if r.ExistingOutput.Absent != (r.ExistingOutput.File == nil) {
		return invalid("record existing output presence")
	}
	if f := r.ExistingOutput.File; f != nil {
		if !validRecordFile(*f) || f.Path != r.Output.Path || f.Basename != r.Output.Basename ||
			f.Identity.Dev != r.OutputParent.Dev || f.Identity.MountID != r.OutputParent.MountID {
			return invalid("record existing output")
		}
	}
	samePath := sameDirectory(r.SourceParent, r.OutputParent) && r.Source.Basename == r.Output.Basename
	switch r.Mode {
	case InPlace:
		if !samePath || r.Source.Path != r.Output.Path || r.Backup == nil || !validRecordPath(*r.Backup) ||
			r.Backup.Path != filepath.Join(filepath.Dir(r.Source.Path), ".yakuori-backup-"+r.RunID) ||
			r.Backup.Basename != ".yakuori-backup-"+r.RunID || r.ExistingOutput.File == nil || *r.ExistingOutput.File != r.Source {
			return invalid("record in-place relation")
		}
	case Create, Replace:
		if samePath || r.Source.Path == r.Output.Path || r.Backup != nil || (r.Mode == Create && !r.ExistingOutput.Absent) {
			return invalid("record ordinary mode relation")
		}
	default:
		return invalid("record mode")
	}
	operation := ""
	switch r.Phase {
	case preparedPhase:
		operation = "none"
	case backedUpPhase, restoredPhase:
		if r.Mode != InPlace {
			return invalid("record phase/mode")
		}
		operation = "backup-rename"
		if r.Phase == restoredPhase {
			operation = "restore-rename"
		}
	case publishedPhase:
		operation = "publish-rename"
	case abandonedPhase:
		operation = "reconcile-unpublished"
	default:
		return invalid("record phase")
	}
	if r.LastCompletedOperation != operation {
		return invalid("record phase/operation")
	}
	return nil
}

func validRecordPath(p recordPath) bool {
	return filepath.IsAbs(p.Path) && filepath.Clean(p.Path) == p.Path && strings.IndexByte(p.Path, 0) < 0 &&
		p.Basename != "." && p.Basename != "/" && p.Basename == filepath.Base(p.Path)
}

func validRecordFile(f recordFile) bool {
	id := f.Identity
	return validRecordPath(recordPath{f.Path, f.Basename}) && recordHex(f.SHA256, 64) && f.Size >= 0 && f.Size == id.Size &&
		id.Mode&unix.S_IFMT == unix.S_IFREG && id.Nlink == 1 && id.Ino != 0 && id.MountID != 0
}

func recordHex(s string, length int) bool {
	return len(s) == length && strings.Trim(s, "0123456789abcdef") == ""
}
