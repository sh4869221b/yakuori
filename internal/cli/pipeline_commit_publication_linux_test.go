//go:build linux && pipelinefaults

package cli

// allow: SIZE_OK - Task6 permits one new file. This file owns the real CLI
// publication-exit scenario and its independent post-Wait oracle.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
	"golang.org/x/sys/unix"
	"modernc.org/sqlite"
)

const cliPublicationExitCollision = "owned prepared-record collision\n"

// These fields mirror the existing publication record contract; the digest is
// checked only because that persisted format already requires it.
type cliPublicationExitIdentity struct {
	Dev     uint64 `json:"dev"`
	Ino     uint64 `json:"ino"`
	MountID uint64 `json:"mount_id"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
	Nlink   uint64 `json:"nlink"`
}

type cliPublicationExitRecordFile struct {
	Path     string                     `json:"path"`
	Basename string                     `json:"basename"`
	Identity cliPublicationExitIdentity `json:"identity"`
	SHA256   string                     `json:"sha256"`
	Size     int64                      `json:"size"`
}

type cliPublicationExitRecordPath struct {
	Path     string `json:"path"`
	Basename string `json:"basename"`
}

type cliPublicationExitRecord struct {
	SchemaVersion  int                           `json:"schema_version"`
	RunID          string                        `json:"run_id"`
	Mode           publication.Mode              `json:"mode"`
	Source         cliPublicationExitRecordFile  `json:"source"`
	Output         cliPublicationExitRecordPath  `json:"output"`
	Stage          cliPublicationExitRecordFile  `json:"stage"`
	Backup         *cliPublicationExitRecordPath `json:"backup"`
	SourceParent   cliPublicationExitIdentity    `json:"source_parent"`
	OutputParent   cliPublicationExitIdentity    `json:"output_parent"`
	ExistingOutput struct {
		Absent bool                          `json:"absent"`
		File   *cliPublicationExitRecordFile `json:"file"`
	} `json:"existing_output"`
	Phase                  string `json:"phase"`
	LastCompletedOperation string `json:"last_completed_operation"`
}

type cliPublicationExitSchemaObject struct {
	Type, Name, Table string
	RootPage          int
	SQL               sql.NullString
}

type cliPublicationExitChildReport struct {
	Result                          localize.Result
	Phase, Error, Cause             string
	SQLiteCode                      int
	RecordExistsError, CloseIOError bool
	Generated, Drained, Closed      int
	Injected                        int
	BeforeRuns, NewRuns             []string
	RunDir                          string
	Collision                       cliPrecommitFile
	BeforeEngineClose               map[string]cliPrecommitFile
	BeforeEngineCloseRows           []translationTMRow
	OwnedFDsBeforeEngineClose       []string
	StreamEvents                    []string
	WriterAttempts, Delegated       int
	Diagnostic                      []byte
	WriterError                     string
	Calls                           []cliPrecommitCall
}

type cliPublicationExitEngine struct {
	*cliPrecommitEngine
	t       *testing.T
	request signalChildRequest
	sink    *signalNoticeSink
	control *os.File
	report  cliPublicationExitChildReport
	key     translationTMRow
}

func (e *cliPublicationExitEngine) Open(ctx context.Context, backend string) (string, inference.Engine, error) {
	id, _, err := e.cliPrecommitEngine.Open(ctx, backend)
	return id, e, err
}

func (e *cliPublicationExitEngine) Generate(ctx context.Context, request inference.GenerationRequest) (inference.GenerationResult, error) {
	e.generated++
	e.report.Generated++
	unit := e.report.Generated
	e.report.StreamEvents = append(e.report.StreamEvents, fmt.Sprintf("generate-%d", unit))
	if unit == 1 {
		dirs, err := filepath.Glob(filepath.Join(filepath.Dir(e.request.Args[len(e.request.Args)-1]), ".yakuori-run-*"))
		if err != nil {
			return inference.GenerationResult{}, err
		}
		for _, dir := range dirs {
			if !slices.Contains(e.report.BeforeRuns, dir) {
				e.report.NewRuns = append(e.report.NewRuns, dir)
			}
		}
		if len(e.report.NewRuns) != 1 {
			return inference.GenerationResult{}, fmt.Errorf("owned new run directories=%v", e.report.NewRuns)
		}
		e.report.RunDir = e.report.NewRuns[0]
		if err := e.inject(ctx); err != nil {
			return inference.GenerationResult{}, err
		}
		e.sink.emit(signalNotice{Case: e.request.Case, PID: os.Getpid(), PPID: os.Getppid(), Event: "generate-ready", Occurrence: 1})
		line, err := bufio.NewReader(e.control).ReadString('\n')
		if err != nil || line != "DRAIN_RELEASE\n" {
			return inference.GenerationResult{}, fmt.Errorf("publication release %q: %w", line, err)
		}
		e.sink.emit(signalNotice{Case: e.request.Case, PID: os.Getpid(), PPID: os.Getppid(), Event: "release-received", Occurrence: 1})
	}
	// The Engine consumes an actual unbuffered stream and joins its worker
	// before returning. Close therefore observes completed consumers, not a
	// flag set by a test after CLI dispatch has already returned.
	tokens := make(chan string)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(tokens)
		for _, token := range []string{"訳", "文", "。\n"} {
			select {
			case tokens <- token:
			case <-ctx.Done():
				return
			}
		}
	}()
	var text strings.Builder
	for token := range tokens {
		text.WriteString(token)
	}
	<-done
	e.report.Drained++
	e.report.StreamEvents = append(e.report.StreamEvents, fmt.Sprintf("drained-%d", unit))
	return inference.GenerationResult{Text: text.String(), Finish: inference.Stop,
		PromptTokens: len(request.TokenIDs()), OutputTokens: 3, Policy: request.Policy()}, ctx.Err()
}

func cliPublicationExitTrigger(key translationTMRow) string {
	return fmt.Sprintf("CREATE TRIGGER cli_publication_exit_reject BEFORE INSERT ON tm WHEN NEW.key_schema=%d AND NEW.source_identity=X'%x' AND NEW.translation_profile=X'%x' BEGIN SELECT RAISE(ABORT, 'cli-publication-exit-dml'); END",
		key.Schema, key.Source, key.Profile)
}

func (e *cliPublicationExitEngine) inject(ctx context.Context) (err error) {
	switch e.request.Row {
	case "normal", "published-close-error", "published-diagnostic-error":
		return nil
	case "commit-failure":
		db, err := sql.Open("sqlite", "file:"+config.ResolveStoragePaths(e.request.Paths).TMFile+"?mode=rw")
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, db.Close()) }()
		conn, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, conn.Close()) }()
		if _, err := conn.ExecContext(ctx, cliPublicationExitTrigger(e.key)); err != nil {
			return err
		}
		e.report.Injected++
	case "postcommit-prepublish":
		path := filepath.Join(e.report.RunDir, ".record-prepared")
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		n, writeErr := io.WriteString(file, cliPublicationExitCollision)
		err = errors.Join(writeErr, file.Close())
		if err != nil {
			return err
		}
		if n != len(cliPublicationExitCollision) {
			return io.ErrShortWrite
		}
		e.report.Collision = cliPrecommitCapture(e.t, path)
		e.report.Injected++
	default:
		return fmt.Errorf("unknown publication exit row %q", e.request.Row)
	}
	return nil
}

func (e *cliPublicationExitEngine) Close() error {
	e.sink.emit(signalNotice{Case: e.request.Case, PID: os.Getpid(), PPID: os.Getppid(), Event: "close-attempt", Occurrence: 1})
	if e.report.Generated == 0 || e.report.Drained != e.report.Generated {
		return errSignalEarlyClose
	}
	command, _, err := parseTranslation(e.request.Args[0], e.request.Args[1:])
	if err != nil {
		return err
	}
	e.report.BeforeEngineClose = map[string]cliPrecommitFile{
		"source": cliPrecommitCapture(e.t, command.options.Source),
		"output": cliPrecommitCapture(e.t, command.options.Output),
		"stage":  cliPrecommitCapture(e.t, filepath.Join(e.report.RunDir, "stage")),
		"record": cliPrecommitCapture(e.t, filepath.Join(e.report.RunDir, "record.json")),
		"backup": cliPrecommitCapture(e.t, filepath.Join(filepath.Dir(command.options.Source), ".yakuori-backup-"+strings.TrimPrefix(filepath.Base(e.report.RunDir), ".yakuori-run-"))),
	}
	e.report.BeforeEngineCloseRows = readTranslationTM(e.t, e.request.Paths)
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return err
	}
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue // ReadDir's own descriptor is already closed.
		}
		if err != nil {
			return err
		}
		if strings.HasPrefix(target, filepath.Dir(command.options.Source)+"/") {
			e.report.OwnedFDsBeforeEngineClose = append(e.report.OwnedFDsBeforeEngineClose, target)
		}
	}
	e.report.StreamEvents = append(e.report.StreamEvents, "close")
	err = e.cliPrecommitEngine.Close()
	e.report.Closed = e.closed
	e.sink.emit(signalNotice{Case: e.request.Case, PID: os.Getpid(), PPID: os.Getppid(), Event: "close-complete", Occurrence: 1})
	return err
}

func cliPublicationExitChild(t *testing.T, request signalChildRequest, sink *signalNoticeSink) (int, bool) {
	command, help, err := parseTranslation(request.Args[0], request.Args[1:])
	if err != nil || help != "" {
		t.Fatalf("publication child arguments: %v %q", err, help)
	}
	control := os.NewFile(4, "publication-control")
	if control == nil {
		t.Fatal("publication control descriptor unavailable")
	}
	defer func() {
		if err := control.Close(); err != nil {
			t.Error(err)
		}
	}()
	base := &translationEngine{info: request.Info, text: cliAuthoredTranslation, finish: inference.Stop}
	e := &cliPublicationExitEngine{cliPrecommitEngine: &cliPrecommitEngine{translationEngine: base},
		t: t, request: request, sink: sink, control: control}
	e.report.BeforeRuns, err = filepath.Glob(filepath.Join(filepath.Dir(command.options.Source), ".yakuori-run-*"))
	if err != nil {
		t.Fatal(err)
	}
	if command.format == "w3strings" {
		keys := expectedW3TranslationRows(t, w3TMInput{
			Raw: w3Table("en", []string{"Alpha.", "Beta."}), SourceLanguage: "en", ModelID: "selected-model",
			Game: "qa", Artifact: "sample", Keys: map[string][][]byte{"1": {{0x78, 0x56, 0x34, 0x12}}},
			Texts: map[string]string{"1": cliAuthoredTranslation, "2": cliAuthoredTranslation},
		}, &translationEngine{info: request.Info})
		e.key = keys[0]
	} else {
		key := expectedTranslationKey(t, translationTMInput{command: command, modelID: "selected-model", source: []byte("Hello.\n")}, &translationEngine{info: request.Info})
		e.key = translationTMRow{Schema: key.Schema(), Source: key.SourceBytes(), Profile: key.ProfileBytes()}
	}
	writer := &cliReachedWriter{}
	if request.Row == "published-close-error" || request.Row == "published-diagnostic-error" {
		e.CloseFunc = func() error { return syscall.EIO }
	}
	if request.Row == "published-diagnostic-error" {
		writer.err = syscall.ENOSPC
	}
	var executeErr error
	runner := func(ctx context.Context, options publication.Options) (localize.Result, error) {
		command.options = options
		e.report.Result, executeErr = command.execute(ctx, os.Stdout, request.Paths, e.Open)
		return e.report.Result, executeErr
	}
	// This is Run's actual dispatcher, with only the existing per-call opener
	// seam; Store, Core, Prepare, commit, publication and reportPipeline are real.
	code := runWithPipeline(t.Context(), request.Args, os.Stdout, io.MultiWriter(writer, os.Stderr),
		func() (config.Paths, error) { return request.Paths, nil }, runner)
	var failure *localize.Error
	if errors.As(executeErr, &failure) {
		e.report.Phase = failure.Phase
		e.report.Cause = signalErrorString(failure.Err)
	}
	e.report.Error = signalErrorString(executeErr)
	var sqlError *sqlite.Error
	if errors.As(executeErr, &sqlError) {
		e.report.SQLiteCode = sqlError.Code()
	}
	e.report.RecordExistsError = errors.Is(executeErr, os.ErrExist)
	e.report.CloseIOError = errors.Is(executeErr, syscall.EIO)
	e.report.WriterAttempts, e.report.Delegated = writer.attempts, writer.delegated
	e.report.Diagnostic, e.report.WriterError = writer.attempted, signalErrorString(writer.err)
	e.report.Calls = e.Calls
	raw, err := json.MarshalIndent(e.report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(command.options.Source), "cli-publication-child.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	sink.emit(signalNotice{Case: request.Case, PID: os.Getpid(), PPID: os.Getppid(), Event: "final", Occurrence: 1,
		Generated: e.generated, Closed: e.closed, TestFailed: t.Failed(), Result: &e.report.Result})
	return code, t.Failed()
}

type cliPublicationExitFixture struct {
	signalFixture
	Parent     cliPublicationExitIdentity
	Schema     []cliPublicationExitSchemaObject
	BeforeRuns []string
}

type cliPublicationExitState struct {
	AfterWait, RecordInstalled bool
	RunDir                     string
	Files                      map[string]cliPrecommitFile
	Rows                       []translationTMRow
	Schema                     []cliPublicationExitSchemaObject
	Record                     cliPublicationExitRecord
	RecordError                string
	Child                      cliPublicationExitChildReport
	Observation                signalObservation
}

func cliPublicationExitID(file cliPrecommitFile) cliPublicationExitIdentity {
	return cliPublicationExitIdentity{Dev: unix.Mkdev(file.DeviceMajor, file.DeviceMinor), Ino: file.Inode,
		MountID: file.MountID, Size: int64(file.Size), Mode: uint32(file.Mode), Nlink: file.Nlink}
}

func cliPublicationExitReadSchema(t *testing.T, paths config.Paths) []cliPublicationExitSchemaObject {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+config.ResolveStoragePaths(paths).TMFile+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	rows, err := db.Query("SELECT type, name, tbl_name, rootpage, sql FROM sqlite_schema ORDER BY type, name")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	result := []cliPublicationExitSchemaObject{}
	for rows.Next() {
		var object cliPublicationExitSchemaObject
		if err := rows.Scan(&object.Type, &object.Name, &object.Table, &object.RootPage, &object.SQL); err != nil {
			t.Fatal(err)
		}
		result = append(result, object)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func (f cliPublicationExitFixture) capture(t *testing.T, runDir string) cliPublicationExitState {
	t.Helper()
	id := strings.TrimPrefix(filepath.Base(runDir), ".yakuori-run-")
	state := cliPublicationExitState{RunDir: runDir, Rows: readTranslationTM(t, f.Paths),
		Schema: cliPublicationExitReadSchema(t, f.Paths), Files: map[string]cliPrecommitFile{
			"source":    cliPrecommitCapture(t, f.Command.options.Source),
			"output":    cliPrecommitCapture(t, f.OutputPath),
			"stage":     cliPrecommitCapture(t, filepath.Join(runDir, "stage")),
			"backup":    cliPrecommitCapture(t, filepath.Join(f.Dir, ".yakuori-backup-"+id)),
			"record":    cliPrecommitCapture(t, filepath.Join(runDir, "record.json")),
			"collision": cliPrecommitCapture(t, filepath.Join(runDir, ".record-prepared")),
			"sentinel":  cliPrecommitCapture(t, filepath.Join(f.Dir, "sentinel.txt")),
		}}
	if state.Files["record"].Exists {
		record, err := cliPublicationExitDecodeRecord(state.Files["record"].Bytes)
		state.Record, state.RecordError = record, signalErrorString(err)
		state.RecordInstalled = err == nil
	}
	return state
}

// Exact recursive key matching rejects duplicates, omissions, null scalars,
// unknown keys and case folding. Field order and JSON whitespace are irrelevant.
func cliPublicationExitJSONShape(raw, shape []byte) error {
	if bytes.Equal(shape, []byte("null")) {
		if !bytes.Equal(bytes.TrimSpace(raw), shape) {
			return publication.ErrInvalidRecord
		}
		return nil
	}
	if len(shape) == 0 || shape[0] != '{' {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return publication.ErrInvalidRecord
		}
		return nil
	}
	var expected map[string]json.RawMessage
	if err := json.Unmarshal(shape, &expected); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return publication.ErrInvalidRecord
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		want, exists := expected[key]
		if err != nil || !ok || !exists || seen[key] {
			return publication.ErrInvalidRecord
		}
		seen[key] = true
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return publication.ErrInvalidRecord
		}
		if err := cliPublicationExitJSONShape(value, want); err != nil {
			return err
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') || len(seen) != len(expected) {
		return publication.ErrInvalidRecord
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return publication.ErrInvalidRecord
	}
	return nil
}

func cliPublicationExitDecodeRecord(raw []byte) (cliPublicationExitRecord, error) {
	var record cliPublicationExitRecord
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&record); err != nil {
		return record, publication.ErrInvalidRecord
	}
	shape, err := json.Marshal(record)
	if err != nil {
		return record, err
	}
	return record, cliPublicationExitJSONShape(raw, shape)
}

func cliPublicationExitRecorded(path string, file cliPrecommitFile) cliPublicationExitRecordFile {
	return cliPublicationExitRecordFile{Path: path, Basename: filepath.Base(path), Identity: cliPublicationExitID(file),
		SHA256: fmt.Sprintf("%x", sha256.Sum256(file.Bytes)), Size: int64(len(file.Bytes))}
}

func (f cliPublicationExitFixture) judge(state cliPublicationExitState) []string {
	var problems []string
	check := func(ok bool, name string) {
		if !ok {
			problems = append(problems, name)
		}
	}
	row := f.Case.Row
	published := row == "normal" || row == "published-close-error" || row == "published-diagnostic-error"
	committed := row != "commit-failure"
	wantExit := 1
	if row == "normal" {
		wantExit = 0
	}
	o, child := state.Observation, state.Child
	check(state.AfterWait && o.WaitCompleted && o.Waits == 1 && o.Rescues == 0 && o.EOF && o.NoticeClosed && o.ControlClosed, "actual-wait")
	check(o.ExitCode == wantExit && !o.Signaled, "child-exit")
	check(len(o.Stdout) == 0, "stdout")
	check(slices.Equal(o.Events, []string{"generate-ready", "release-received", "close-attempt", "close-complete"}), "lifecycle")
	check(len(o.Notices) == 5 && o.Notices[len(o.Notices)-1].Event == "final" && !o.Notices[len(o.Notices)-1].TestFailed, "final-notice")
	wantGenerated := 1
	if f.Case.Format == "w3strings" {
		wantGenerated = 2
	}
	check(child.Generated == wantGenerated && child.Drained == wantGenerated && child.Closed == 1, "stream-drain-close")
	events := []string{"generate-1", "drained-1", "close"}
	if wantGenerated == 2 {
		events = []string{"generate-1", "drained-1", "generate-2", "drained-2", "close"}
	}
	check(slices.Equal(child.StreamEvents, events), "stream-order")
	check(len(child.OwnedFDsBeforeEngineClose) == 0, "publication-store-descriptors-closed")
	check(slices.Equal(child.BeforeRuns, f.BeforeRuns) && slices.Equal(child.NewRuns, []string{state.RunDir}) && child.RunDir == state.RunDir, "owned-run")
	wantState, wantPhase := publication.NotPublished, ""
	if published {
		wantState = publication.Published
	}
	injected := 0
	switch row {
	case "normal":
		check(child.Error == "", "normal-error")
	case "commit-failure":
		injected, wantPhase = 1, "commit"
		check(child.SQLiteCode == 1811 && strings.Contains(child.Cause, "cli-publication-exit-dml"), "real-dml-error")
	case "postcommit-prepublish":
		injected, wantPhase = 1, "publish"
		check(child.RecordExistsError, "record-eexist")
	case "published-close-error", "published-diagnostic-error":
		wantPhase = "close"
		check(child.CloseIOError, "real-engine-close-error")
	}
	check(child.Injected == injected, "injection-exactly-once")
	check(child.Phase == wantPhase && child.Result.TMCommitted == committed && child.Result.Publication.State == wantState, "caller-result")
	wantRows := f.D0
	if committed {
		wantRows = f.D1
	}
	check(reflect.DeepEqual(state.Rows, wantRows), "all-four-tm-columns")
	check(reflect.DeepEqual(child.BeforeEngineCloseRows, wantRows), "tm-before-engine-close")
	check(state.Files["sentinel"].Exists && string(state.Files["sentinel"].Bytes) == "Sentinel source.\n", "unrelated-sentinel-file")
	check(state.RecordInstalled == published && state.Files["record"].Exists == published && state.RecordError == "", "record-installed")
	check(!state.Files["backup"].Exists || published && f.Case.Mode == "in-place", "unexpected-backup")
	if published {
		check(state.Files["output"].Exists && bytes.Equal(state.Files["output"].Bytes, f.Target), "full-authored-output")
		check(!state.Files["stage"].Exists, "published-stage-absent")
		check(state.Files["output"].Inode != f.OldOutput.Inode, "stage-installed-not-old-output")
		if f.Case.Mode == "in-place" {
			check(reflect.DeepEqual(state.Files["source"], state.Files["output"]), "inplace-source-is-output")
			check(reflect.DeepEqual(state.Files["backup"], f.Source), "backup-full-identity-bytes")
		} else {
			check(reflect.DeepEqual(state.Files["source"], f.Source), "source-full-identity-bytes")
		}
		id := strings.TrimPrefix(filepath.Base(state.RunDir), ".yakuori-run-")
		want := cliPublicationExitRecord{SchemaVersion: 1, RunID: id, Mode: f.Command.options.Mode,
			Source:       cliPublicationExitRecorded(f.Command.options.Source, f.Source),
			Output:       cliPublicationExitRecordPath{Path: f.OutputPath, Basename: filepath.Base(f.OutputPath)},
			Stage:        cliPublicationExitRecorded(filepath.Join(state.RunDir, "stage"), state.Files["output"]),
			SourceParent: f.Parent, OutputParent: f.Parent, Phase: "published", LastCompletedOperation: "publish-rename"}
		// Digest expectations use the authored full target, never observed bytes.
		want.Stage.SHA256, want.Stage.Size = fmt.Sprintf("%x", sha256.Sum256(f.Target)), int64(len(f.Target))
		want.ExistingOutput.Absent = !f.OldOutput.Exists
		if f.OldOutput.Exists {
			file := cliPublicationExitRecorded(f.OutputPath, f.OldOutput)
			want.ExistingOutput.File = &file
		}
		if f.Case.Mode == "in-place" {
			path := filepath.Join(f.Dir, ".yakuori-backup-"+id)
			want.Backup = &cliPublicationExitRecordPath{Path: path, Basename: filepath.Base(path)}
		}
		check(reflect.DeepEqual(state.Record, want), "all-record-fields-identities")
		check(child.Result.Publication.RunID == id && child.Result.Publication.RecordPath == filepath.Join(state.RunDir, "record.json") &&
			child.Result.Publication.OutputPath == f.OutputPath, "published-result-paths")
	} else {
		check(reflect.DeepEqual(state.Files["source"], f.Source), "unpublished-source")
		check(reflect.DeepEqual(state.Files["output"], f.OldOutput), "unpublished-output")
		check(state.Files["stage"].Exists && bytes.Equal(state.Files["stage"].Bytes, f.Target), "unpublished-full-stage")
	}
	if row == "postcommit-prepublish" {
		check(state.Files["collision"].Exists && string(state.Files["collision"].Bytes) == cliPublicationExitCollision &&
			reflect.DeepEqual(state.Files["collision"], child.Collision), "collision-full-identity-bytes")
	} else {
		check(!state.Files["collision"].Exists, "unexpected-collision")
	}
	for _, name := range []string{"source", "output", "stage", "backup", "record"} {
		check(reflect.DeepEqual(child.BeforeEngineClose[name], state.Files[name]), "preserved-through-close-"+name)
	}
	wantAttempts := 1
	if row == "normal" {
		wantAttempts = 0
	}
	check(child.WriterAttempts == wantAttempts, "diagnostic-attempts")
	if row == "published-diagnostic-error" {
		check(child.WriterError == syscall.ENOSPC.Error() && child.Delegated == 0 && len(o.Stderr) == 0 && len(child.Diagnostic) > 0, "actual-diagnostic-writer-error")
	} else {
		check(child.WriterError == "" && child.Delegated == len(child.Diagnostic) && bytes.Equal(o.Stderr, child.Diagnostic), "actual-stderr")
	}
	wantSchema := slices.Clone(f.Schema)
	if row == "commit-failure" {
		var target translationTMRow
		for _, candidate := range f.D1 {
			if !slices.ContainsFunc(f.D0, func(old translationTMRow) bool { return reflect.DeepEqual(old, candidate) }) {
				target = candidate
				break
			}
		}
		wantSchema = append(wantSchema, cliPublicationExitSchemaObject{Type: "trigger", Name: "cli_publication_exit_reject", Table: "tm",
			SQL: sql.NullString{String: strings.TrimSuffix(cliPublicationExitTrigger(target), ";"), Valid: true}})
	}
	check(reflect.DeepEqual(state.Schema, wantSchema), "complete-schema")
	return problems
}

func (f cliPublicationExitFixture) save(t *testing.T, state cliPublicationExitState) {
	t.Helper()
	pipelineCLISaveJSON(t, "post-wait.json", state)
	pipelineCLISaveJSON(t, "expected.json", struct {
		Case              signalCase
		Source, OldOutput cliPrecommitFile
		Parent            cliPublicationExitIdentity
		Target            []byte
		D0, D1            []translationTMRow
	}{f.Case, f.Source, f.OldOutput, f.Parent, f.Target, f.D0, f.D1})
	pipelineCLISave(t, "input-source.bin", f.Source.Bytes)
	pipelineCLISave(t, "authored-target.bin", f.Target)
	if f.OldOutput.Exists {
		pipelineCLISave(t, "old-output.bin", f.OldOutput.Bytes)
	}
	pipelineCLISave(t, "stdout.bin", state.Observation.Stdout)
	pipelineCLISave(t, "stderr.bin", state.Observation.Stderr)
	for name, file := range state.Files {
		if file.Exists {
			pipelineCLISave(t, name+".bin", file.Bytes)
		}
	}
	pipelineCLISaveJSON(t, "schema-before-trigger-drop.json", state.Schema)
	dbPath := config.ResolveStoragePaths(f.Paths).TMFile
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		raw, err := os.ReadFile(dbPath + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		pipelineCLISave(t, "tm.sqlite"+suffix, raw)
	}
}

func TestPipelineCLIPublicationExit(t *testing.T) {
	for _, row := range []string{"normal", "commit-failure", "postcommit-prepublish", "published-close-error", "published-diagnostic-error"} {
		t.Run(row, func(t *testing.T) {
			for _, kind := range []string{"text", "w3"} {
				t.Run(kind, func(t *testing.T) {
					for _, mode := range []string{"create", "replace-absent", "replace-existing", "in-place"} {
						t.Run(mode, func(t *testing.T) {
							// Given: authored input/target, unrelated seeded TM row, real files.
							format := "text"
							if kind == "w3" {
								format = "w3strings"
							}
							f := cliPublicationExitFixture{signalFixture: newSignalFixture(t, t.TempDir(), signalCase{
								Backend: "cpu", Surface: kind + "-file-" + mode, Row: row, Format: format, Mode: mode})}
							f.Schema = cliPublicationExitReadSchema(t, f.Paths)
							var st unix.Statx_t
							if err := unix.Statx(unix.AT_FDCWD, f.Dir, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &st); err != nil {
								t.Fatal(err)
							}
							f.Parent = cliPublicationExitIdentity{Dev: unix.Mkdev(st.Dev_major, st.Dev_minor), Ino: st.Ino,
								MountID: st.Mnt_id, Size: int64(st.Size), Mode: uint32(st.Mode), Nlink: uint64(st.Nlink)}
							var err error
							f.BeforeRuns, err = filepath.Glob(filepath.Join(f.Dir, ".yakuori-run-*"))
							if err != nil {
								t.Fatal(err)
							}
							// When: subscribe before Start, release the first Generate, then Wait.
							child := startSignalChild(t, signalChildRequest{Case: t.Name(), Args: f.Args, Paths: f.Paths,
								Info: f.Info, Row: row, Mode: "publication-exit"}, signalWatchdog)
							if _, err := child.await("generate-ready"); err != nil {
								t.Fatal(err)
							}
							dirs, err := filepath.Glob(filepath.Join(f.Dir, ".yakuori-run-*"))
							if err != nil || len(dirs) != len(f.BeforeRuns)+1 {
								t.Fatalf("run inventory=%v before=%v error=%v", dirs, f.BeforeRuns, err)
							}
							var runDir string
							for _, dir := range dirs {
								if !slices.Contains(f.BeforeRuns, dir) {
									runDir = dir
								}
							}
							beforeWait := f.capture(t, runDir)
							pipelineCLISaveJSON(t, "before-wait.json", beforeWait)
							if err := child.sendControl("DRAIN_RELEASE"); err != nil {
								t.Fatal(err)
							}
							observation, err := child.finish()
							if err != nil {
								t.Fatal(err)
							}
							// Then: all final physical facts come from the parent after actual Wait.
							state := f.capture(t, runDir)
							state.AfterWait, state.Observation = observation.WaitCompleted, observation
							raw, err := os.ReadFile(filepath.Join(f.Dir, "cli-publication-child.json"))
							if err != nil {
								t.Fatal(err)
							}
							if err := json.Unmarshal(raw, &state.Child); err != nil {
								t.Fatal(err)
							}
							pipelineCLISave(t, "child-causal-report.json", raw)
							f.save(t, state)
							status, ok := child.cmd.ProcessState.Sys().(syscall.WaitStatus)
							if !ok {
								t.Fatal("actual WaitStatus unavailable")
							}
							pipelineCLISaveJSON(t, "wait.json", struct {
								PID, Status, Exit int
								Exited, Signaled  bool
								Waits, Rescues    int
							}{child.cmd.Process.Pid, int(status), status.ExitStatus(), status.Exited(), status.Signaled(), observation.Waits, observation.Rescues})
							if problems := f.judge(state); len(problems) != 0 {
								t.Fatalf("publication-exit oracle: %v; child=%+v", problems, state.Child)
							}
							f.sensitivity(t, state, beforeWait)
							// Raw DB/schema are already saved. Remove only this test's trigger;
							// unknown schema objects must never be normalized into a passing Open.
							if row == "commit-failure" {
								db, err := sql.Open("sqlite", "file:"+config.ResolveStoragePaths(f.Paths).TMFile+"?mode=rw")
								if err != nil {
									t.Fatal(err)
								}
								_, dropErr := db.Exec("DROP TRIGGER cli_publication_exit_reject")
								if err := errors.Join(dropErr, db.Close()); err != nil {
									t.Fatal(err)
								}
							}
							if after := cliPublicationExitReadSchema(t, f.Paths); !reflect.DeepEqual(after, f.Schema) {
								t.Fatalf("schema after owned trigger removal: %+v", after)
							}
							if after := readTranslationTM(t, f.Paths); !reflect.DeepEqual(after, state.Rows) {
								t.Fatal("rows changed while removing owned trigger")
							}
							store, err := tm.Open(t.Context(), config.ResolveStoragePaths(f.Paths).TMFile, config.DefaultLimits())
							if err != nil {
								t.Fatalf("fresh Store.Open after exact schema cleanup: %v", err)
							}
							if err := store.Close(); err != nil {
								t.Fatal(err)
							}
							pipelineCLISaveJSON(t, "after-trigger-drop.json", struct {
								Schema               []cliPublicationExitSchemaObject
								Rows                 []translationTMRow
								StoreOpenedAndClosed bool
							}{cliPublicationExitReadSchema(t, f.Paths), readTranslationTM(t, f.Paths), true})
							t.Logf("actual child exit=%d WaitStatus=%d publication=%s committed=%t generated/drained/closed=%d/%d/%d injected=%d",
								status.ExitStatus(), status, state.Child.Result.Publication.State, state.Child.Result.TMCommitted,
								state.Child.Generated, state.Child.Drained, state.Child.Closed, state.Child.Injected)
						})
					}
				})
			}
		})
	}
}

func (f cliPublicationExitFixture) sensitivity(t *testing.T, state, before cliPublicationExitState) {
	t.Helper()
	// Mutations retain the healthy counterpart and alter one named observation
	// at a time. No expected rows or bytes are taken from the actual DB/output.
	reject := func(name string, changed cliPublicationExitState) {
		t.Helper()
		problems := f.judge(changed)
		pipelineCLISaveJSON(t, "sensitivity/"+name+".json", struct {
			Problems []string
			State    cliPublicationExitState
		}{problems, changed})
		if len(problems) == 0 {
			t.Fatalf("sensitivity %s accepted wrong state", name)
		}
		t.Logf("REJECT %s: %v", name, problems)
	}
	clone := func() cliPublicationExitState {
		copy := state
		copy.Files = make(map[string]cliPrecommitFile, len(state.Files))
		for k, v := range state.Files {
			copy.Files[k] = v
		}
		return copy
	}
	bad := clone()
	bad.RecordInstalled = !bad.RecordInstalled
	reject("installed-bit", bad)
	bad = clone()
	bad.Child.Injected++
	reject("double-injection", bad)
	if state.Child.Injected > 0 {
		bad = clone()
		bad.Child.Injected = 0
		reject("unhit-injection", bad)
	}
	bad = clone()
	bad.Observation.Rescues = 1
	reject("rescued-child", bad)
	bad = clone()
	bad.AfterWait = false
	reject("missing-wait", bad)
	bad = clone()
	bad.Child.StreamEvents = slices.Clone(state.Child.StreamEvents)
	drained := slices.Index(bad.Child.StreamEvents, "drained-1")
	closed := slices.Index(bad.Child.StreamEvents, "close")
	bad.Child.StreamEvents[drained], bad.Child.StreamEvents[closed] = bad.Child.StreamEvents[closed], bad.Child.StreamEvents[drained]
	reject("close-before-drain-order-only", bad)
	if f.Case.Row == "commit-failure" || f.Case.Row == "postcommit-prepublish" {
		bad = clone()
		bad.Files["stage"] = before.Files["stage"]
		reject("stale-pre-wait-stage", bad)
	} else {
		bad = clone()
		bad.Files["output"] = before.Files["output"]
		reject("stale-pre-wait-output", bad)
		bad = clone()
		bad.Files["record"], bad.Record = before.Files["record"], before.Record
		reject("stale-pre-wait-record", bad)
	}
	if f.Case.Row != "commit-failure" {
		bad = clone()
		bad.Rows = before.Rows
		reject("stale-pre-wait-db", bad)
	}
	bad = clone()
	bad.Rows = slices.Clone(state.Rows)
	for i := range bad.Rows {
		if bad.Rows[i].Text == "Sentinel translation.\n" {
			bad.Rows[i].Text = "corrupted sentinel"
		}
	}
	reject("unrelated-sentinel", bad)
	short := f
	short.Target = bytes.Clone(f.Target[:len(f.Target)-1])
	problems := short.judge(state)
	pipelineCLISaveJSON(t, "sensitivity/truncated-expectation.json", problems)
	if len(problems) == 0 {
		t.Fatal("sensitivity truncated-expectation accepted")
	}
	t.Logf("REJECT truncated-expectation: %v", problems)
	if !state.RecordInstalled {
		return
	}
	raw := string(state.Files["record"].Bytes)
	for _, malformed := range []struct {
		name, raw string
	}{
		{"duplicate", `{"schema_version":1,` + raw[1:]},
		{"missing", strings.Replace(raw, `"schema_version":1,`, "", 1)},
		{"unknown", `{"unexpected":1,` + raw[1:]},
		{"case-folded", strings.Replace(raw, `"schema_version"`, `"Schema_version"`, 1)},
		{"null", strings.Replace(raw, `"schema_version":1`, `"schema_version":null`, 1)},
		{"trailing", raw + "{}"},
		{"truncated", raw[:len(raw)-1]},
	} {
		if malformed.raw == raw {
			t.Fatalf("malformed record control %s did not mutate", malformed.name)
		}
		_, err := cliPublicationExitDecodeRecord([]byte(malformed.raw))
		pipelineCLISave(t, "sensitivity/record-"+malformed.name+".bin", []byte(malformed.raw))
		if !errors.Is(err, publication.ErrInvalidRecord) {
			t.Fatalf("malformed record %s: %v", malformed.name, err)
		}
		t.Logf("REJECT malformed-record-%s: %v", malformed.name, err)
	}
	for _, role := range []string{"source", "stage", "source-parent", "output-parent", "existing-output"} {
		if role == "existing-output" && state.Record.ExistingOutput.File == nil {
			continue // A create/absent record structurally has no old file identity.
		}
		for _, field := range []string{"dev", "ino", "mount_id", "size", "mode", "nlink"} {
			bad = clone()
			var id *cliPublicationExitIdentity
			switch role {
			case "source":
				id = &bad.Record.Source.Identity
			case "stage":
				id = &bad.Record.Stage.Identity
			case "source-parent":
				id = &bad.Record.SourceParent
			case "output-parent":
				id = &bad.Record.OutputParent
			case "existing-output":
				file := *bad.Record.ExistingOutput.File
				bad.Record.ExistingOutput.File = &file
				id = &file.Identity
			}
			switch field {
			case "dev":
				id.Dev++
			case "ino":
				id.Ino++
			case "mount_id":
				id.MountID++
			case "size":
				id.Size++
			case "mode":
				id.Mode ^= 0100
			case "nlink":
				id.Nlink++
			}
			reject("record-"+role+"-"+field, bad)
		}
	}
}
