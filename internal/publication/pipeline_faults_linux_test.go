//go:build linux && pipelinefaults

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

type bridgeOperation struct {
	RunID     string `json:"run_id"`
	Operation string `json:"operation"`
	Name      string `json:"name"`
	Bytes     int    `json:"bytes,omitempty"`
	Error     string `json:"error,omitempty"`
}

type bridgeRecorder struct {
	runID        string
	operations   []bridgeOperation
	stageSyncs   int
	stageCloses  int
	renames      int
	recordWrites int
	recordSyncs  int
	recordCloses int
	stageFD      *os.File
	recordFDs    []*os.File
}

func writeError(operation string, err error) string {
	if err == nil {
		return ""
	}
	return operation + ": " + err.Error()
}

func (b *bridgeRecorder) hooks() PipelineFaultHooks {
	return PipelineFaultHooks{
		SyncStage: func(f *os.File) error {
			b.stageSyncs++
			b.stageFD = f
			err := f.Sync()
			b.operations = append(b.operations, bridgeOperation{RunID: b.runID, Operation: "sync-stage", Name: f.Name(), Error: writeError("sync-stage", err)})
			return err
		},
		CloseStage: func(f *os.File) error {
			b.stageCloses++
			b.operations = append(b.operations, bridgeOperation{RunID: b.runID, Operation: "close-stage-hook", Name: f.Name()})
			return nil
		},
		Rename: func(fromFD int, from string, toFD int, to string, flags uint) error {
			b.renames++
			err := unix.Renameat2(fromFD, from, toFD, to, flags)
			b.operations = append(b.operations, bridgeOperation{RunID: b.runID, Operation: "rename", Name: from + "->" + to, Error: writeError("rename", err)})
			return err
		},
		RecordWrite: func(f *os.File, p []byte) (int, error) {
			b.recordWrites++
			b.recordFDs = append(b.recordFDs, f)
			n, err := f.Write(p)
			b.operations = append(b.operations, bridgeOperation{RunID: b.runID, Operation: "record-write", Name: f.Name(), Bytes: n, Error: writeError("record-write", err)})
			return n, err
		},
		RecordSync: func(f *os.File) error {
			b.recordSyncs++
			err := f.Sync()
			b.operations = append(b.operations, bridgeOperation{RunID: b.runID, Operation: "record-sync", Name: f.Name(), Error: writeError("record-sync", err)})
			return err
		},
		RecordClose: func(f *os.File) error {
			b.recordCloses++
			b.operations = append(b.operations, bridgeOperation{RunID: b.runID, Operation: "record-close-hook", Name: f.Name()})
			return nil
		},
	}
}

func bridgeRun(t *testing.T, mode Mode, existing bool) (*Run, Options) {
	t.Helper()
	d := t.TempDir()
	source, output := filepath.Join(d, "source"), filepath.Join(d, "output")
	putFile(t, source, "original")
	if mode == InPlace {
		output = source
	} else if existing {
		putFile(t, output, "existing")
	}
	parent, err := openParent(source)
	if err != nil {
		t.Fatal(err)
	}
	qualified, fsErr := filesystem(parent.parent)
	if err := parent.parent.file.Close(); err != nil {
		t.Fatal(err)
	}
	if fsErr != nil {
		t.Fatalf("qualified ext4/Btrfs root required for the positive bridge scenario: %v", fsErr)
	}
	options := Options{Source: source, Output: output, Mode: mode}
	r, err := Prepare(context.Background(), options)
	if err != nil {
		t.Fatalf("real Prepare on %s: %v", qualified, err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("close run: %v", err)
		}
	})
	return r, options
}

func wantClosedDescriptor(t *testing.T, f *os.File) {
	t.Helper()
	if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("descriptor %s still open: %v", f.Name(), err)
	}
}

func saveBridgeEvidence(t *testing.T, name string, value any) {
	t.Helper()
	dir := os.Getenv("YAKUORI_PIPELINE_EVIDENCE")
	if dir == "" {
		return
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineFaultBridge(t *testing.T) {
	t.Run("delegates", testPipelineFaultBridgeDelegates)
	t.Run("close-error", testPipelineFaultBridgeCloseError)
}

func testPipelineFaultBridgeDelegates(t *testing.T) {
	r, options := bridgeRun(t, Replace, true)
	recorder := &bridgeRecorder{runID: r.runID}
	paths := r.SetPipelineFaultHooks(recorder.hooks())
	if want := filepath.Join(r.runDir.path, "stage"); paths.Stage != want {
		t.Fatalf("stage path = %q; want this run's %q", paths.Stage, want)
	}
	if want := filepath.Join(r.runDir.path, "record.json"); paths.Record != want {
		t.Fatalf("record path = %q; want this run's %q", paths.Record, want)
	}
	if paths.Backup != "" {
		t.Fatalf("replace run reported backup path %q", paths.Backup)
	}
	if again := r.SetPipelineFaultHooks(PipelineFaultHooks{}); again != paths {
		t.Fatalf("nil-only install changed paths: %+v", again)
	}

	stageBytes(t, r, "translated")
	if recorder.stageSyncs != 1 || recorder.stageCloses != 1 {
		t.Fatalf("stage hooks = sync %d close %d; want 1/1", recorder.stageSyncs, recorder.stageCloses)
	}
	if recorder.stageFD == nil {
		t.Fatal("sync hook never received the stage descriptor")
	}
	wantClosedDescriptor(t, recorder.stageFD)

	result, err := r.Publish(context.Background())
	if err != nil || result.State != Published {
		t.Fatalf("publish through hooks: %+v %v", result, err)
	}
	if recorder.recordWrites != 2 || recorder.recordSyncs != 4 || recorder.recordCloses != 2 || recorder.renames != 3 {
		t.Fatalf("record/rename hooks = write %d sync %d close %d rename %d; want 2/4/2/3",
			recorder.recordWrites, recorder.recordSyncs, recorder.recordCloses, recorder.renames)
	}
	wantBytes(t, options.Output, "translated")
	wantBytes(t, options.Source, "original")
	wantAbsent(t, filepath.Join(r.runDir.path, "stage"))
	record := readOperationRecord(t, result.RecordPath)
	if record.RunID != r.runID || record.Phase != publishedPhase || record.Stage.SHA256 != r.stage.digest {
		t.Fatalf("record through hooks: %+v", record)
	}
	for _, f := range recorder.recordFDs {
		wantClosedDescriptor(t, f)
	}

	independentOps := len(recorder.operations)
	other, otherOptions := bridgeRun(t, Create, false)
	otherPaths := other.SetPipelineFaultHooks(PipelineFaultHooks{})
	if want := filepath.Join(other.runDir.path, "stage"); otherPaths.Stage != want {
		t.Fatalf("independent stage path = %q; want %q", otherPaths.Stage, want)
	}
	stageBytes(t, other, "second")
	otherResult, err := other.Publish(context.Background())
	if err != nil || otherResult.State != Published {
		t.Fatalf("independent run default publish: %+v %v", otherResult, err)
	}
	wantBytes(t, otherOptions.Output, "second")
	if len(recorder.operations) != independentOps {
		t.Fatalf("run %s hooks received operations from run %s", r.runID, other.runID)
	}

	inPlace, _ := bridgeRun(t, InPlace, false)
	inPlacePaths := inPlace.SetPipelineFaultHooks(PipelineFaultHooks{})
	if inPlacePaths.Backup != inPlace.backupPath() {
		t.Fatalf("in-place backup path = %q; want this run's %q", inPlacePaths.Backup, inPlace.backupPath())
	}
	if inPlacePaths.Stage != filepath.Join(inPlace.runDir.path, "stage") || inPlacePaths.Record != filepath.Join(inPlace.runDir.path, "record.json") {
		t.Fatalf("in-place run paths escape its private run: %+v", inPlacePaths)
	}

	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	wantClosedDescriptor(t, r.runDir.file)
	wantClosedDescriptor(t, r.output.parent.file)
	wantClosedDescriptor(t, r.source.parent.file)
	if err := r.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}

	saveBridgeEvidence(t, "operations.json", map[string]any{
		"run_id":           r.runID,
		"mode":             r.mode,
		"paths":            paths,
		"operations":       recorder.operations,
		"stage_syncs":      recorder.stageSyncs,
		"stage_closes":     recorder.stageCloses,
		"record_writes":    recorder.recordWrites,
		"record_syncs":     recorder.recordSyncs,
		"record_closes":    recorder.recordCloses,
		"renames":          recorder.renames,
		"record_phase":     record.Phase,
		"record_operation": record.LastCompletedOperation,
		"output_bytes":     "translated",
		"source_bytes":     "original",
		"independent_run": map[string]any{
			"run_id":          other.runID,
			"stage_path":      otherPaths.Stage,
			"hook_operations": 0,
			"output_bytes":    "second",
		},
	})
}

func testPipelineFaultBridgeCloseError(t *testing.T) {
	r, options := bridgeRun(t, Replace, true)
	injected := errors.New("bridge close injection")
	var hooked *os.File
	closeCalls := 0
	paths := r.SetPipelineFaultHooks(PipelineFaultHooks{
		CloseStage: func(f *os.File) error {
			closeCalls++
			hooked = f
			return injected
		},
	})
	checked := false
	err := r.Stage(context.Background(), func(w io.Writer) error {
		_, err := io.WriteString(w, "translated")
		return err
	}, func(io.Reader) error {
		checked = true
		return nil
	})
	if !errors.Is(err, injected) {
		t.Fatalf("stage error lost the injected cause: %v", err)
	}
	if closeCalls != 1 || hooked == nil {
		t.Fatalf("close hook calls = %d, descriptor set = %v; want 1/true", closeCalls, hooked != nil)
	}
	wantClosedDescriptor(t, hooked)
	if r.validated || checked {
		t.Fatal("failed close reached stage validation")
	}
	wantBytes(t, paths.Stage, "translated")
	wantBytes(t, options.Source, "original")
	wantBytes(t, options.Output, "existing")
	result, publishErr := r.Publish(context.Background())
	if !errors.Is(publishErr, ErrInvalidPublish) || result.State != NotPublished {
		t.Fatalf("publish after failed close: %+v %v", result, publishErr)
	}

	recordRun, recordOptions := bridgeRun(t, Replace, true)
	recordInjected := errors.New("bridge record close injection")
	var recordFD *os.File
	recordCloseCalls := 0
	recordRun.SetPipelineFaultHooks(PipelineFaultHooks{
		RecordClose: func(f *os.File) error {
			recordCloseCalls++
			recordFD = f
			return recordInjected
		},
	})
	stageBytes(t, recordRun, "translated")
	result, publishErr = recordRun.Publish(context.Background())
	if !errors.Is(publishErr, recordInjected) || result.State != NotPublished {
		t.Fatalf("publish with failing record close: %+v %v", result, publishErr)
	}
	if recordCloseCalls != 1 || recordFD == nil {
		t.Fatalf("record close hook calls = %d, descriptor set = %v; want 1/true", recordCloseCalls, recordFD != nil)
	}
	wantClosedDescriptor(t, recordFD)
	wantAbsent(t, filepath.Join(recordRun.runDir.path, "record.json"))
	wantBytes(t, recordRun.stage.path, "translated")
	wantBytes(t, recordOptions.Source, "original")
	wantBytes(t, recordOptions.Output, "existing")
}
