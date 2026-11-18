//go:build linux

package publication

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

var ErrRecordTooLarge = errors.New("publication record exceeds 64 KiB")

type recordPhase string

const (
	recordUTF8Schema  = 1
	recordBytesSchema = 2

	preparedPhase  recordPhase = "prepared"
	backedUpPhase  recordPhase = "source-backed-up"
	publishedPhase recordPhase = "published"
	restoredPhase  recordPhase = "restored"
	abandonedPhase recordPhase = "abandoned"
)

type recordPath struct {
	Path     string `json:"path"`
	Basename string `json:"basename"`
}

type recordFile struct {
	Path     string   `json:"path"`
	Basename string   `json:"basename"`
	Identity identity `json:"identity"`
	SHA256   string   `json:"sha256"`
	Size     int64    `json:"size"`
}

type operationRecord struct {
	SchemaVersion  int         `json:"schema_version"`
	RunID          string      `json:"run_id"`
	Mode           Mode        `json:"mode"`
	Source         recordFile  `json:"source"`
	Output         recordPath  `json:"output"`
	Stage          recordFile  `json:"stage"`
	Backup         *recordPath `json:"backup"`
	SourceParent   identity    `json:"source_parent"`
	OutputParent   identity    `json:"output_parent"`
	ExistingOutput struct {
		Absent bool        `json:"absent"`
		File   *recordFile `json:"file"`
	} `json:"existing_output"`
	Phase                  recordPhase `json:"phase"`
	LastCompletedOperation string      `json:"last_completed_operation"`
}

func recordedFile(l leaf) recordFile {
	return recordFile{l.path, l.name, l.id, l.digest, l.id.Size}
}

func (r *operationRecord) pathFields() []*string {
	fields := []*string{&r.Source.Path, &r.Source.Basename, &r.Output.Path, &r.Output.Basename, &r.Stage.Path, &r.Stage.Basename}
	if r.Backup != nil {
		fields = append(fields, &r.Backup.Path, &r.Backup.Basename)
	}
	if f := r.ExistingOutput.File; f != nil {
		fields = append(fields, &f.Path, &f.Basename)
	}
	return fields
}

func encodeRecord(record operationRecord) ([]byte, error) {
	if record.SchemaVersion != recordBytesSchema {
		return json.Marshal(record)
	}
	// Rewriting a recovery snapshot must not mutate its decoded paths.
	if record.Backup != nil {
		backup := *record.Backup
		record.Backup = &backup
	}
	if record.ExistingOutput.File != nil {
		file := *record.ExistingOutput.File
		record.ExistingOutput.File = &file
	}
	for _, field := range record.pathFields() {
		*field = base64.RawStdEncoding.EncodeToString([]byte(*field))
	}
	return json.Marshal(record)
}

func (r *Run) writeRecord(phase recordPhase, operation string) error {
	record := operationRecord{
		SchemaVersion: recordUTF8Schema, RunID: r.runID, Mode: r.mode,
		Source: recordedFile(r.source), Output: recordPath{r.output.path, r.output.name}, Stage: recordedFile(r.stage),
		SourceParent: r.source.parent.id, OutputParent: r.output.parent.id,
		Phase: phase, LastCompletedOperation: operation,
	}
	if r.mode == InPlace {
		record.Backup = &recordPath{r.backupPath(), r.backupName()}
	}
	record.ExistingOutput.Absent = !r.output.present
	if r.output.present {
		file := recordedFile(r.output)
		record.ExistingOutput.File = &file
	}
	for _, field := range record.pathFields() {
		// Both invalid UTF-8 and a literal replacement rune need a lossless record.
		if strings.ContainsRune(*field, utf8.RuneError) {
			record.SchemaVersion = recordBytesSchema
			break
		}
	}
	w := recordWriter{r.runDir, r.recordWrite, r.recordSync, r.recordClose, r.rename}
	return w.write(record, ".record-"+string(phase))
}

type recordWriter struct {
	dir       *directory
	writeFile func(*os.File, []byte) (int, error)
	syncFile  func(*os.File) error
	closeFile func(*os.File) error
	rename    renameFunc
}

func (w recordWriter) write(record operationRecord, name string) (err error) {
	b, err := encodeRecord(record)
	if err != nil {
		return fmt.Errorf("encode publication record: %w", err)
	}
	if len(b) > 64*1024 {
		return ErrRecordTooLarge
	}
	fd, err := unix.Openat(int(w.dir.file.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return fmt.Errorf("create record snapshot: %w", err)
	}
	f := os.NewFile(uintptr(fd), name)
	defer func() {
		if f != nil {
			err = errors.Join(err, w.closeFile(f))
		}
	}()
	n, err := w.writeFile(f, b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = w.syncFile(f)
	}
	err = errors.Join(err, w.closeFile(f))
	f = nil
	if err != nil {
		return fmt.Errorf("write/close publication record: %w", err)
	}
	fd = int(w.dir.file.Fd())
	if err = w.rename(fd, name, fd, "record.json", 0); err != nil {
		return fmt.Errorf("replace publication record: %w", err)
	}
	if err = w.syncFile(w.dir.file); err != nil {
		return fmt.Errorf("sync record directory: %w", err)
	}
	return nil
}

func (r *Run) backupName() string { return ".yakuori-backup-" + r.runID }

func (r *Run) backupPath() string { return filepath.Join(r.source.parent.path, r.backupName()) }
