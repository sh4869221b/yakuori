//go:build linux

package publication

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var ErrRecordTooLarge = errors.New("publication record exceeds 64 KiB")

type recordPhase string

const (
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

func (r *Run) writeRecord(phase recordPhase, operation string) error {
	record := operationRecord{
		SchemaVersion: 1, RunID: r.runID, Mode: r.mode,
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
	b, err := json.Marshal(record)
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
