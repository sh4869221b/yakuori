//go:build linux && pipelinefaults

package localize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/publication"
	"golang.org/x/sys/unix"
)

// allow: SIZE_OK - Task5 owns exactly this file; the publication matrix, its
// per-Run stimuli and independent expectations form one test-only proof unit.
type pipelinePublicationFaultRow struct {
	name, phase, operation, stimulus string
	inPlace                          bool
}

type pipelinePublicationFault struct {
	h              *pipelineHarness
	row            pipelinePublicationFaultRow
	cancel         context.CancelFunc
	hits           int
	triggers       int
	closePending   *os.File
	directory      *os.File
	stage          pipelineHarnessFile
	external       *pipelineHarnessFile
	expectedEvents []pipelineHarnessRecordEvent
	shortWrite     []byte
	shortWritten   int
}

// Close deliberately closes the actual retained run-directory descriptor first.
// The real Run.Close must report os.ErrClosed while releasing its other FDs.
// This is an actual close error, not a synthetic error after successful Close.
type pipelinePublicationFaultRun struct {
	publicationRun
	fault *pipelinePublicationFault
}

func (r *pipelinePublicationFaultRun) Close() error {
	if r.fault.row.name == "run-close-error" {
		if r.fault.directory == nil {
			r.fault.h.t.Fatal("run Close fault has no actual run-directory FD")
		}
		if err := r.fault.directory.Close(); err != nil {
			r.fault.h.t.Fatal(err)
		}
		r.fault.inject()
	}
	return r.publicationRun.Close()
}

func TestPipelinePublicationFailures(t *testing.T) {
	rows := []pipelinePublicationFaultRow{{name: "healthy"}}
	for _, phase := range []string{"prepared", "source-backed-up", "published"} {
		for _, operation := range []string{"write", "sync", "close", "install-rename", "directory-sync"} {
			stimuli := []string{"error", "cancel-before", "cancel-after"}
			if operation == "write" {
				stimuli = append(stimuli, "short-write")
			}
			for _, stimulus := range stimuli {
				rows = append(rows, pipelinePublicationFaultRow{
					name:  phase + "-" + operation + "-" + stimulus,
					phase: phase, operation: operation, stimulus: stimulus,
					inPlace: phase == "source-backed-up",
				})
			}
		}
	}
	rows = append(rows,
		pipelinePublicationFaultRow{name: "backup-collision", inPlace: true},
		pipelinePublicationFaultRow{name: "backup-rename-error", inPlace: true},
		pipelinePublicationFaultRow{name: "publish-rename-error"},
		pipelinePublicationFaultRow{name: "cancel-before-backup", inPlace: true},
		pipelinePublicationFaultRow{name: "cancel-after-backup", inPlace: true},
		pipelinePublicationFaultRow{name: "cancel-before-publish"},
		pipelinePublicationFaultRow{name: "cancel-after-publish"},
		pipelinePublicationFaultRow{name: "restore-success", inPlace: true},
		pipelinePublicationFaultRow{name: "restore-rename-error", inPlace: true},
		pipelinePublicationFaultRow{name: "restore-external-file", inPlace: true},
		pipelinePublicationFaultRow{name: "run-close-error"},
	)
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			for _, kind := range []string{"text", "w3"} {
				t.Run(kind, func(t *testing.T) {
					for _, mode := range pipelineFixtureModes() {
						if row.inPlace && mode.mode != publication.InPlace {
							continue // These modes have no backup or restoration operation.
						}
						t.Run(mode.name, func(t *testing.T) {
							// Given: authored source, complete D0/D1 and pre-run identities.
							h := newPipelineHarness(t, pipelineFixtureSpec(kind, mode), true)
							ctx, cancel := context.WithCancel(t.Context())
							defer cancel()
							fault := &pipelinePublicationFault{h: h, row: row, cancel: cancel}
							want := fault.expectation()
							pipelineFixtureSaveJSON(t, "publication-expected.json", want)
							pipelineFixtureSaveJSON(t, "publication-expected-events.json", fault.expectedEvents)
							// When: the real Core, Store and Linux publication run execute.
							fault.execute(ctx)
							// Then: physical state, real operation history and causes agree.
							fault.check(want)
						})
					}
				})
			}
		})
	}
}

func (f *pipelinePublicationFault) inject() {
	f.hits++
	f.h.injected = append(f.h.injected, f.row.name)
	if f.hits != 1 {
		f.h.t.Fatalf("fault injected %d times, want exactly one", f.hits)
	}
}

func (f *pipelinePublicationFault) execute(ctx context.Context) {
	h := f.h
	h.requestedBoundary, h.requestedOccurrence, h.stimulus = f.row.name, 1, f.row.stimulus
	h.memory = &tmExecution{store: h.fixture.store, scope: h.fixture.scope}
	h.afterStageHook = func() error {
		if err := h.afterStage(); err != nil {
			return err
		}
		var err error
		f.stage, err = pipelineHarnessReadFile(h.ops.paths.Stage)
		if err != nil {
			return err
		}
		if f.row.name == "backup-collision" {
			f.external = f.createExternal(h.ops.paths.Backup)
			f.inject()
		}
		return nil
	}
	prepare := func(ctx context.Context, options publication.Options) (publicationRun, error) {
		run, err := h.prepare(ctx, options)
		if err != nil {
			return nil, err
		}
		real := run.(*pipelineHarnessRun).publicationRun.(*publication.Run)
		real.SetPipelineFaultHooks(f.hooks())
		return &pipelinePublicationFaultRun{publicationRun: run, fault: f}, nil
	}
	h.result, h.runErr = h.core.fileWithTM(ctx, h.options, h.adapter, h.fixture.profile, nil, h.memory, prepare)
}

func (f *pipelinePublicationFault) createExternal(path string) *pipelineHarnessFile {
	t := f.h.t
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	n, writeErr := file.Write(f.h.fixture.input.source)
	if err := errors.Join(writeErr, file.Close()); err != nil || n != len(f.h.fixture.input.source) {
		t.Fatalf("external fixture write=%d error=%v", n, err)
	}
	captured, err := pipelineHarnessReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Dev == f.h.beforeSource.Dev && captured.Inode == f.h.beforeSource.Inode {
		t.Fatal("same-byte external fixture did not get a distinct inode")
	}
	return &captured
}

func (f *pipelinePublicationFault) matches(phase, operation string) bool {
	return f.row.phase == phase && f.row.operation == operation
}

// before/after surround actual I/O. Cancellation never fabricates a failed
// successful rename, and record Close-after is delayed until its FD is closed.
func (f *pipelinePublicationFault) before(phase, operation string) bool {
	if !f.matches(phase, operation) {
		return false
	}
	switch f.row.stimulus {
	case "error", "short-write":
		f.inject()
		return true
	case "cancel-before":
		f.inject()
		f.cancel()
	}
	return false
}

func (f *pipelinePublicationFault) after(phase, operation string) {
	if f.matches(phase, operation) && f.row.stimulus == "cancel-after" {
		f.inject()
		f.cancel()
	}
}

func (f *pipelinePublicationFault) hooks() publication.PipelineFaultHooks {
	h, o := f.h, f.h.ops
	hooks := o.hooks()
	write, syncFile, closeFile, rename := hooks.RecordWrite, hooks.RecordSync, hooks.RecordClose, hooks.Rename
	hooks.RecordWrite = func(file *os.File, raw []byte) (int, error) {
		record, err := pipelineHarnessDecodeRecord(raw)
		if err != nil {
			return 0, err
		}
		target, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
		if err != nil || filepath.Dir(target) != filepath.Dir(o.paths.Record) || filepath.Base(target) != file.Name() {
			h.t.Fatalf("record Write FD target=%q error=%v", target, err)
		}
		o.lastPhase, o.lastRecordName = record.Phase, file.Name()
		if f.before(record.Phase, "write") {
			h.trace.mark(pipelineBoundaryRecordWriteBefore)
			o.event(record.Phase, "write")
			if f.row.stimulus == "short-write" {
				f.shortWrite = slices.Clone(raw)
				n, err := file.Write(raw[:len(raw)-1])
				f.shortWritten = n
				return n, err
			}
			return 0, unix.EIO
		}
		n, err := write(file, raw)
		if err == nil {
			f.after(record.Phase, "write")
		}
		return n, err
	}
	hooks.RecordSync = func(file *os.File) error {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		phase, operation := o.phase(file.Name()), "sync"
		if info.IsDir() {
			phase, operation, f.directory = o.lastPhase, "directory-sync", file
		}
		target, pathErr := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
		wantPath := filepath.Join(filepath.Dir(o.paths.Record), file.Name())
		if info.IsDir() {
			wantPath = filepath.Dir(o.paths.Record)
		}
		if pathErr != nil || target != wantPath {
			h.t.Fatalf("record %s FD target=%q, want %q: %v", operation, target, wantPath, pathErr)
		}
		if f.before(phase, operation) {
			h.trace.mark(pipelineBoundaryRecordSyncBefore)
			o.event(phase, operation)
			return unix.EIO
		}
		err = syncFile(file)
		if err == nil {
			f.after(phase, operation)
			last := "prepared"
			if h.mode == publication.InPlace {
				last = "source-backed-up"
			}
			if operation == "directory-sync" && ((f.row.name == "cancel-before-backup" && phase == "prepared") ||
				(f.row.name == "cancel-before-publish" && phase == last)) {
				f.inject()
				f.cancel()
			}
		}
		return err
	}
	hooks.RecordClose = func(file *os.File) error {
		phase := o.phase(file.Name())
		target, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
		if err != nil || target != filepath.Join(filepath.Dir(o.paths.Record), file.Name()) {
			h.t.Fatalf("record Close FD target=%q: %v", target, err)
		}
		failed := f.before(phase, "close")
		err = closeFile(file) // Retain the actual FD; the bridge must close it.
		if failed {
			return errors.Join(err, unix.EIO)
		}
		if f.matches(phase, "close") && f.row.stimulus == "cancel-after" {
			f.closePending = file
		}
		return err
	}
	hooks.Rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
		fromParent, toParent := filepath.Dir(h.options.Source), filepath.Dir(h.options.Output)
		if from == "stage" || strings.HasPrefix(from, ".record-") {
			fromParent = filepath.Dir(o.paths.Record)
		}
		if strings.HasPrefix(from, ".record-") {
			toParent = fromParent
		}
		for _, endpoint := range []struct {
			fd   int
			path string
		}{{fromFD, fromParent}, {toFD, toParent}} {
			path, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", endpoint.fd))
			if err != nil || path != endpoint.path {
				h.t.Fatalf("rename FD target=%q, want %q: %v", path, endpoint.path, err)
			}
		}
		if f.closePending != nil {
			if _, err := f.closePending.Stat(); !errors.Is(err, os.ErrClosed) {
				h.t.Fatalf("cancel-after Close did not follow actual FD close: %v", err)
			}
			f.after(o.phase(from), "close")
			f.closePending = nil
		}
		if strings.HasPrefix(from, ".record-") {
			phase := o.phase(from)
			if f.before(phase, "install-rename") {
				closed := o.recordFDs[len(o.recordFDs)-1]
				if _, err := closed.Stat(); !errors.Is(err, os.ErrClosed) {
					h.t.Fatalf("record install error preceded actual Close: %v", err)
				}
				h.trace.mark(pipelineBoundaryRecordCloseAfter)
				o.event(phase, "close-after")
				h.trace.mark(pipelineBoundaryRecordInstallBefore)
				o.event(phase, "install-before")
				return unix.EIO
			}
			err := rename(fromFD, from, toFD, to, flags)
			if err == nil {
				f.after(phase, "install-rename")
			}
			return err
		}
		if from == "source" && f.row.name == "backup-rename-error" {
			h.trace.mark(pipelineBoundaryBackupRenameBefore)
			f.inject()
			return unix.EIO
		}
		if strings.HasPrefix(from, ".yakuori-backup-") && f.row.name == "restore-rename-error" {
			h.trace.mark(pipelineBoundaryBackupRenameBefore)
			f.inject()
			return unix.EIO
		}
		if from == "stage" && (f.row.name == "publish-rename-error" || strings.HasPrefix(f.row.name, "restore-")) {
			if _, err := o.stageFD.Stat(); !errors.Is(err, os.ErrClosed) {
				h.t.Fatalf("publish error preceded actual stage Close: %v", err)
			}
			o.stageFDClosed = true
			h.trace.mark(pipelineBoundaryStageCloseAfter)
			h.trace.mark(pipelineBoundaryPublishRenameBefore)
			switch f.row.name {
			case "restore-rename-error":
				f.triggers++
			case "restore-external-file":
				f.triggers++
				f.external = f.createExternal(h.options.Source)
				f.inject()
			case "restore-success":
				f.triggers++
				f.inject()
			default:
				f.inject()
			}
			return unix.EIO
		}
		err := rename(fromFD, from, toFD, to, flags)
		if err == nil && ((from == "source" && f.row.name == "cancel-after-backup") ||
			(from == "stage" && f.row.name == "cancel-after-publish")) {
			f.inject()
			f.cancel()
		}
		return err
	}
	return hooks
}

func (f *pipelinePublicationFault) expectation() pipelineHarnessExpectation {
	h, row := f.h, f.row
	state, phase, operation := publication.Published, "published", "publish-rename"
	records := []string{"prepared", "published"}
	backupBefore, backupAfter, publishBefore, publishAfter := 0, 0, 1, 1
	if h.mode == publication.InPlace {
		records = []string{"prepared", "source-backed-up", "published"}
		backupBefore, backupAfter = 1, 1
	}
	failedRecord := row.stimulus == "error" || row.stimulus == "short-write"
	if row.phase == "prepared" {
		state, phase, operation = publication.NotPublished, "prepared", "none"
		records = []string{"prepared"}
		backupBefore, backupAfter, publishBefore, publishAfter = 0, 0, 0, 0
		if failedRecord && row.operation != "directory-sync" {
			phase, operation = "", ""
		}
	}
	if row.phase == "source-backed-up" {
		state, phase, operation = publication.Restored, "restored", "restore-rename"
		records = []string{"prepared", "source-backed-up", "restored"}
		backupBefore, backupAfter, publishBefore, publishAfter = 2, 2, 0, 0
	}
	if row.phase == "published" && failedRecord && row.operation != "directory-sync" {
		phase, operation = "prepared", "none"
		if h.mode == publication.InPlace {
			phase, operation = "source-backed-up", "backup-rename"
		}
	}
	switch row.name {
	case "backup-collision":
		state, phase, operation, records = publication.NotPublished, "", "", nil
		backupBefore, backupAfter, publishBefore, publishAfter = 0, 0, 0, 0
	case "backup-rename-error", "cancel-before-backup":
		state, phase, operation, records = publication.NotPublished, "prepared", "none", []string{"prepared"}
		backupBefore, backupAfter, publishBefore, publishAfter = 0, 0, 0, 0
		if row.name == "backup-rename-error" {
			backupBefore = 1
		}
	case "publish-rename-error", "restore-success", "restore-rename-error", "restore-external-file", "cancel-after-backup", "cancel-before-publish":
		state, phase, operation, records = publication.NotPublished, "prepared", "none", []string{"prepared"}
		publishAfter = 0
		if strings.HasPrefix(row.name, "cancel-") {
			publishBefore = 0
		}
		if h.mode == publication.InPlace {
			state, phase, operation = publication.Restored, "restored", "restore-rename"
			records = []string{"prepared", "source-backed-up", "restored"}
			backupBefore, backupAfter = 2, 2
		}
		if row.name == "restore-rename-error" || row.name == "restore-external-file" {
			state, phase, operation = publication.SourceBackedUp, "source-backed-up", "backup-rename"
			records = []string{"prepared", "source-backed-up"}
			backupAfter = 1
			if row.name == "restore-external-file" {
				backupBefore = 1
			}
		}
	}
	want := h.commitStateExpectation(pipelineCommitStateCase{name: "publication", state: state, phase: phase, operation: operation})
	want.Case = h.t.Name()
	want.RecordInstalled, want.RecordFDClosed = phase != "", len(records) != 0
	want.SourcePreserved = h.mode != publication.InPlace || state == publication.NotPublished || state == publication.Restored
	want.OutputPreserved = state == publication.NotPublished && want.OutputPresent || state == publication.Restored
	switch state {
	case publication.NotPublished:
		want.Outcome = "not-published"
	case publication.Restored:
		want.Outcome = "restored"
	case publication.SourceBackedUp:
		want.Outcome = "source-backed-up"
		want.OutputPresent = row.name == "restore-external-file"
		want.OutputBytes = slices.Clone(h.fixture.input.source)
	case publication.Published:
		want.Outcome = "file-published"
	}
	if row.name == "backup-collision" {
		want.BackupPresent = true
	}
	for _, boundary := range []string{
		pipelineBoundaryRecordWriteBefore, pipelineBoundaryRecordWriteAfter,
		pipelineBoundaryRecordSyncBefore, pipelineBoundaryRecordSyncAfter,
		pipelineBoundaryRecordCloseBefore, pipelineBoundaryRecordCloseAfter,
		pipelineBoundaryRecordInstallBefore, pipelineBoundaryRecordInstallAfter,
	} {
		want.RequiredBoundaries[boundary] = 0
	}
	for _, recordPhase := range records {
		failure := ""
		if recordPhase == row.phase && failedRecord {
			failure = row.operation
		}
		// Independent literal operation prefixes: even Write/Sync failures close.
		events := []string{"write", "sync", "close", "close-after", "install-before", "install", "directory-sync"}
		switch failure {
		case "write":
			events = []string{"write", "close"}
		case "sync", "close":
			events = []string{"write", "sync", "close"}
		case "install-rename":
			events = []string{"write", "sync", "close", "close-after", "install-before"}
		}
		for _, event := range events {
			f.expectedEvents = append(f.expectedEvents, pipelineHarnessRecordEvent{Phase: recordPhase, Operation: event, Occurrence: 1})
			switch event {
			case "write":
				want.RequiredBoundaries[pipelineBoundaryRecordWriteBefore]++
				if failure != "write" {
					want.RequiredBoundaries[pipelineBoundaryRecordWriteAfter]++
				}
			case "sync", "directory-sync":
				want.RequiredBoundaries[pipelineBoundaryRecordSyncBefore]++
				if failure != event {
					want.RequiredBoundaries[pipelineBoundaryRecordSyncAfter]++
				}
			case "close":
				want.RequiredBoundaries[pipelineBoundaryRecordCloseBefore]++
			case "close-after":
				want.RequiredBoundaries[pipelineBoundaryRecordCloseAfter]++
			case "install-before":
				want.RequiredBoundaries[pipelineBoundaryRecordInstallBefore]++
			case "install":
				want.RequiredBoundaries[pipelineBoundaryRecordInstallAfter]++
			}
		}
	}
	want.RequiredBoundaries[pipelineBoundaryBackupRenameBefore] = backupBefore
	want.RequiredBoundaries[pipelineBoundaryBackupRenameAfter] = backupAfter
	want.RequiredBoundaries[pipelineBoundaryPublishRenameBefore] = publishBefore
	want.RequiredBoundaries[pipelineBoundaryPublishRenameAfter] = publishAfter
	want.RequiredBoundaries[pipelineBoundaryStageCloseAfter] = publishBefore
	return want
}

func (f *pipelinePublicationFault) check(want pipelineHarnessExpectation) {
	h, t := f.h, f.h.t
	hits, triggers := 1, 0
	if f.row.name == "healthy" {
		hits = 0
	}
	if strings.HasPrefix(f.row.name, "restore-") {
		triggers = 1
	}
	if f.hits != hits || f.triggers != triggers || f.closePending != nil {
		t.Fatalf("unhit/double fault: hits=%d/%d triggers=%d/%d pending-close=%t error=%v unsupported_filesystem=%t", f.hits, hits, f.triggers, triggers, f.closePending != nil, h.runErr, errors.Is(h.runErr, publication.ErrUnsupportedFilesystem))
	}
	cause := error(unix.EIO)
	switch {
	case f.row.name == "healthy":
		cause = nil
	case f.row.name == "backup-collision":
		cause = unix.EEXIST
	case f.row.name == "run-close-error":
		cause = os.ErrClosed
	case strings.HasPrefix(f.row.stimulus, "cancel-"), strings.HasPrefix(f.row.name, "cancel-"):
		cause = context.Canceled
	case f.row.stimulus == "short-write":
		cause = io.ErrShortWrite
	}
	if (cause == nil && h.runErr != nil) || (cause != nil && !errors.Is(h.runErr, cause)) {
		t.Fatalf("cause=%v, want %v", h.runErr, cause)
	}
	var recovery *publication.RecoveryError
	wantRecovery := f.row.name == "restore-rename-error" || f.row.name == "restore-external-file"
	if errors.As(h.runErr, &recovery) != wantRecovery {
		t.Fatalf("RecoveryError=%t, want %t: %v", recovery != nil, wantRecovery, h.runErr)
	}
	got, err := h.snapshot(want)
	if err != nil {
		t.Fatal(err)
	}
	pipelineFixtureSaveState(t, t.Name()+"/publication", want, got, got.Rows)
	if !slices.Equal(got.RecordEvents, f.expectedEvents) {
		t.Fatalf("record operations=%+v, want %+v", got.RecordEvents, f.expectedEvents)
	}
	if problems := pipelineHarnessJudge(want, got); len(problems) != 0 {
		t.Fatalf("publication-state: %v", problems)
	}
	if f.directory != nil {
		if _, err := f.directory.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("Run.Close retained the real run-directory FD: %v", err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(h.ops.paths.Record))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".record-") {
			continue
		}
		file, err := pipelineHarnessReadFile(filepath.Join(filepath.Dir(h.ops.paths.Record), entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		pipelineFixtureSaveJSON(t, "temporary-"+entry.Name()+".json", file)
		pipelineFixtureSave(t, "temporary-"+entry.Name()+".bin", file.Bytes)
		if f.row.stimulus == "short-write" && entry.Name() == ".record-"+f.row.phase {
			if f.shortWritten != len(f.shortWrite)-1 || !slices.Equal(file.Bytes, f.shortWrite[:len(f.shortWrite)-1]) {
				t.Fatalf("short Write actual=%d persisted=%d expected=%d", f.shortWritten, len(file.Bytes), len(f.shortWrite)-1)
			}
		}
	}
	stage := got.Files["stage"]
	if h.result.Publication.State == publication.Published {
		stage = got.Files["output"]
	}
	if problems := pipelineHarnessCheckCaptured("translated-stage-identity", stage, &f.stage, h.fixture.input.target); len(problems) != 0 {
		t.Fatalf("stage identity: %v", problems)
	}
	if want.BackupPresent && f.row.name != "backup-collision" {
		if problems := pipelineHarnessCheckCaptured("original-backup-identity", got.Files["backup"], &h.beforeSource, h.fixture.input.source); len(problems) != 0 {
			t.Fatalf("backup identity: %v", problems)
		}
	}
	if f.external != nil {
		label := "output"
		if f.row.name == "backup-collision" {
			label = "backup"
		}
		if problems := pipelineHarnessCheckCaptured("external-preserved", got.Files[label], f.external, h.fixture.input.source); len(problems) != 0 {
			t.Fatalf("external file: %v", problems)
		}
		pipelineFixtureSaveJSON(t, "external-before-recovery.json", f.external)
	}
	pipelineFixtureAssertCalls(t, h.fixture, pipelineFixtureAuthoredCalls(h.fixture.input))
	if !pipelineLockAvailable(t, filepath.Dir(h.options.Source)) {
		t.Fatal("Run.Close did not release the real publication lock")
	}
	var sequence int
	var database, path string
	if err := h.fixture.db.QueryRow("PRAGMA database_list").Scan(&sequence, &database, &path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pipelineFixtureSave(t, "publication-tm.db", raw)
	pipelineFixtureSaveJSON(t, "publication-fault.json", struct {
		Row           string              `json:"row"`
		Hits          int                 `json:"hits"`
		Triggers      int                 `json:"triggers"`
		StageBefore   pipelineHarnessFile `json:"stage_before"`
		ShortWritten  int                 `json:"short_written"`
		FullWriteSize int                 `json:"full_write_size"`
	}{f.row.name, f.hits, f.triggers, f.stage, f.shortWritten, len(f.shortWrite)})
	if f.row.name == "healthy" {
		// These names are literals, not the mutation helper's own declaration.
		required := []string{
			"record-source-dev", "record-source-inode", "record-source-mount",
			"record-source-identity-size", "record-source-mode", "record-source-nlink",
			"record-stage-dev", "record-stage-inode", "record-stage-mount",
			"record-stage-identity-size", "record-stage-mode", "record-stage-nlink",
			"installed-bit", "missing-close", "missing-close-fd",
			"sentinel-schema", "sentinel-source", "sentinel-profile", "sentinel-text",
		}
		var rejected []string
		for _, mutation := range pipelineCommitStateMutations(got) {
			if !slices.Contains(required, mutation.name) {
				continue
			}
			bad := got.clone()
			mutation.apply(&bad)
			if mutation.record {
				encoded, err := json.Marshal(bad.Record)
				if err != nil {
					t.Fatal(err)
				}
				file := bad.Files["record"]
				file.Bytes, file.Size = encoded, int64(len(encoded))
				bad.Files["record"] = file
			}
			problems := pipelineHarnessJudge(want, bad)
			if len(problems) == 0 {
				t.Fatalf("sensitivity accepted %s", mutation.name)
			}
			rejected = append(rejected, mutation.name)
			t.Logf("sensitivity-reject %s: %v", mutation.name, problems)
		}
		slices.Sort(required)
		slices.Sort(rejected)
		if !slices.Equal(required, rejected) {
			t.Fatalf("sensitivity names=%v, want %v", rejected, required)
		}
		pipelineFixtureSaveJSON(t, "sensitivity-rejected.json", rejected)
	}
	t.Logf("publication-proof row=%s hits=%d triggers=%d state=%s record=%s cause=%v", f.row.name, f.hits, f.triggers, h.result.Publication.State, want.RecordPhase, h.runErr)
}
