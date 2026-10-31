//go:build linux && pipelinefaults

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/inference/goinfer"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/models"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/publication"
	"golang.org/x/sys/unix"
)

// These callbacks belong to one Engine, not a package hook. Task6 can replace
// Generate with a controlled worker without changing the real CLI composition.
type cliPrecommitEngine struct {
	*translationEngine
	OpenFunc     selectedEngineOpener
	InfoFunc     func(context.Context) (inference.ModelInfo, error)
	RequestFunc  func(context.Context, prompt.Input, inference.GenerationPolicy) (inference.GenerationRequest, error)
	CountFunc    func(context.Context, inference.GenerationRequest) (int, error)
	GenerateFunc func(context.Context, inference.GenerationRequest) (inference.GenerationResult, error)
	CloseFunc    func() error
	Calls        []cliPrecommitCall
	Injected     []string
}

type cliPrecommitRequest struct {
	Text     string
	Tokens   []int
	Identity inference.RequestIdentity
	Policy   inference.EffectivePolicy
}

type cliPrecommitCall struct {
	Operation                      string
	Backend                        string
	Input                          string
	SourceLanguage, TargetLanguage string
	Request                        *cliPrecommitRequest
	Info                           *inference.ModelInfo
	Result                         *inference.GenerationResult
	Count                          int
	Error                          string
}

func cliPrecommitRequestOf(r inference.GenerationRequest) *cliPrecommitRequest {
	return &cliPrecommitRequest{Text: r.RenderedPrompt(), Tokens: r.TokenIDs(), Identity: r.Identity(), Policy: r.Policy()}
}

func (e *cliPrecommitEngine) Open(ctx context.Context, backend string) (string, inference.Engine, error) {
	call := cliPrecommitCall{Operation: "open", Backend: backend}
	var id string
	var engine inference.Engine
	var err error
	if e.OpenFunc != nil {
		id, engine, err = e.OpenFunc(ctx, backend)
	} else {
		id, _, err = e.translationEngine.open(ctx, backend)
		if err == nil {
			engine = e
		}
	}
	if err != nil {
		call.Error = err.Error()
	}
	e.Calls = append(e.Calls, call)
	return id, engine, err
}

func (e *cliPrecommitEngine) Info(ctx context.Context) (inference.ModelInfo, error) {
	var info inference.ModelInfo
	var err error
	if e.InfoFunc != nil {
		info, err = e.InfoFunc(ctx)
	} else {
		info, err = e.translationEngine.Info(ctx)
	}
	call := cliPrecommitCall{Operation: "info", Backend: e.info.Backend, Info: &info}
	if err != nil {
		call.Error = err.Error()
	}
	e.Calls = append(e.Calls, call)
	return info, err
}

func (e *cliPrecommitEngine) NewRequest(ctx context.Context, input prompt.Input, policy inference.GenerationPolicy) (inference.GenerationRequest, error) {
	var r inference.GenerationRequest
	var err error
	if e.RequestFunc != nil {
		r, err = e.RequestFunc(ctx, input, policy)
	} else {
		r, err = e.translationEngine.NewRequest(ctx, input, policy)
	}
	call := cliPrecommitCall{Operation: "body-request", Backend: e.info.Backend,
		Input: input.Text, SourceLanguage: input.SourceLanguage, TargetLanguage: input.TargetLanguage}
	if input.Text == "" {
		call.Operation = "profile-request"
	}
	if err != nil {
		call.Error = err.Error()
	} else {
		call.Request = cliPrecommitRequestOf(r)
	}
	e.Calls = append(e.Calls, call)
	return r, err
}

func (e *cliPrecommitEngine) CountTokens(ctx context.Context, r inference.GenerationRequest) (int, error) {
	var n int
	var err error
	if e.CountFunc != nil {
		n, err = e.CountFunc(ctx, r)
	} else {
		n, err = e.translationEngine.CountTokens(ctx, r)
	}
	call := cliPrecommitCall{Operation: "count", Backend: e.info.Backend, Request: cliPrecommitRequestOf(r), Count: n}
	if err != nil {
		call.Error = err.Error()
	}
	e.Calls = append(e.Calls, call)
	return n, err
}

func (e *cliPrecommitEngine) Generate(ctx context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
	var result inference.GenerationResult
	var err error
	if e.GenerateFunc != nil {
		e.generated++
		result, err = e.GenerateFunc(ctx, r)
	} else {
		result, err = e.translationEngine.Generate(ctx, r)
	}
	call := cliPrecommitCall{Operation: "generate", Backend: e.info.Backend, Request: cliPrecommitRequestOf(r), Result: &result}
	if err != nil {
		call.Error = err.Error()
	}
	e.Calls = append(e.Calls, call)
	return result, err
}

func (e *cliPrecommitEngine) Close() error {
	err := e.translationEngine.Close()
	if e.CloseFunc != nil {
		err = errors.Join(err, e.CloseFunc())
	}
	call := cliPrecommitCall{Operation: "close", Backend: e.info.Backend}
	if err != nil {
		call.Error = err.Error()
	}
	e.Calls = append(e.Calls, call)
	return err
}

type cliPrecommitFile struct {
	Exists                      bool
	Bytes                       []byte
	DeviceMajor, DeviceMinor    uint32
	Inode, MountID, Nlink, Size uint64
	Mode                        uint16
}

func cliPrecommitCapture(t *testing.T, path string) cliPrecommitFile {
	t.Helper()
	var st unix.Statx_t
	err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &st)
	if errors.Is(err, os.ErrNotExist) {
		return cliPrecommitFile{}
	}
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return cliPrecommitFile{Exists: true, Bytes: raw, DeviceMajor: st.Dev_major, DeviceMinor: st.Dev_minor,
		Inode: st.Ino, MountID: st.Mnt_id, Nlink: uint64(st.Nlink), Size: st.Size, Mode: st.Mode}
}

type cliPrecommitFixture struct {
	Dir, Output       string
	Args              []string
	Command           translationCommand
	Paths             config.Paths
	Info              inference.ModelInfo
	Source, OldOutput cliPrecommitFile
	Target            []byte
	Texts             []string
	D0, D1            []translationTMRow
	Registry          map[string]cliPrecommitFile
}

func newCLIPrecommitFixture(t *testing.T, backend, surface, row string) cliPrecommitFixture {
	t.Helper()
	dir := t.TempDir()
	command, paths, base := translationFixtureAt(t, dir, "Hello.\n")
	if os.Geteuid() == 0 || !translationPublicationSupported(t, dir) {
		t.Fatal("qualified nonroot ext4/Btrfs required; this is not an unsupported-FS selector")
	}
	base.info.RequestedBackend, base.info.Backend, base.info.Prefill.Path = backend, backend, backend
	if backend == "cuda" {
		base.info.KVPrecision, base.info.CUDABackendPin = "fp16", "cuda-test-v1"
		base.info.ResidentActive, base.info.ResidentContextCap = true, 4096
	}
	f := cliPrecommitFixture{Dir: dir, Paths: paths, Info: base.info, Texts: []string{"Hello.\n"},
		Target: []byte(cliAuthoredTranslation), Registry: map[string]cliPrecommitFile{}}
	action, format := "localize", "text"
	if surface == "text-stdout-none" {
		action = "translate"
	}
	f.Output = filepath.Join(dir, "output")
	if surface == "w3-file-replace-existing" {
		format = "w3strings"
		f.Texts = []string{"Alpha.", "Beta."}
		writeW3File(t, command.options.Source, w3Table("en", f.Texts))
		f.Target = w3Table("jp", []string{cliAuthoredTranslation, cliAuthoredTranslation})
	}
	f.Args = []string{action, "--source-language", "en", "--target-language", "ja", "--game", "qa", "--artifact", "sample", "--backend", backend}
	if action == "localize" {
		f.Args = append(f.Args, "--format", format, "--output", f.Output, "--replace-output")
		old := []byte("old output\n")
		if format == "w3strings" {
			old = w3Table("en", []string{"Old."})
		}
		writeW3File(t, f.Output, old)
	}
	f.Args = append(f.Args, command.options.Source)
	var err error
	f.Command, _, err = parseTranslation(action, f.Args[1:])
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(row, "model-") {
		f.Paths = modelFixture(t)
		if row != "model-unselected" {
			code, _, diag := storageRun([]string{"models", "use", "test-model"}, f.Paths)
			if code != ExitOK || diag != "" {
				t.Fatalf("model selection code=%d diagnostic=%q", code, diag)
			}
			model := filepath.Join(f.Paths.Data, "models", "test-model", "model.gguf")
			if row == "model-missing" {
				if err := os.Remove(model); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFixture(t, model, "changed-model-text")
			}
		}
		for _, path := range []string{
			filepath.Join(f.Paths.Data, "registry.json"), filepath.Join(f.Paths.Config, "config.json"),
			filepath.Join(f.Paths.Data, "models", "test-model", "model.gguf"),
		} {
			f.Registry[path] = cliPrecommitCapture(t, path)
		}
	}
	f.Source = cliPrecommitCapture(t, f.Command.options.Source)
	f.OldOutput = cliPrecommitCapture(t, f.Output)

	// The seed goes through the real text Adapter/Core/Store/Run with its own
	// engine and input. Target-call histories therefore start at acquisition.
	seedCommand := f.Command
	seedCommand.action, seedCommand.format = "translate", "text"
	seedCommand.options.Source = filepath.Join(dir, "sentinel.txt")
	writeFixture(t, seedCommand.options.Source, "Sentinel source.\n")
	seedEngine := &translationEngine{info: f.Info, text: "Sentinel translation.\n", finish: inference.Stop}
	key := expectedTranslationKey(t, translationTMInput{command: seedCommand, modelID: "selected-model", source: []byte("Sentinel source.\n")}, seedEngine)
	f.D0 = []translationTMRow{{Schema: key.Schema(), Source: key.SourceBytes(), Profile: key.ProfileBytes(), Text: "Sentinel translation.\n"}}
	var seedOut bytes.Buffer
	if result, err := seedCommand.execute(context.Background(), &seedOut, f.Paths, seedEngine.open); err != nil || !result.TMCommitted || seedOut.String() != "Sentinel translation.\n" {
		t.Fatalf("valid sentinel seed: result=%+v error=%v stdout=%q", result, err, seedOut.String())
	}
	if actual := readTranslationTM(t, f.Paths); !reflect.DeepEqual(actual, f.D0) {
		t.Fatalf("authored D0 mismatch: actual=%+v expected=%+v", actual, f.D0)
	}
	wantEngine := &translationEngine{info: f.Info}
	f.D1 = slices.Clone(f.D0)
	if format == "w3strings" {
		f.D1 = append(f.D1, expectedW3TranslationRows(t, w3TMInput{
			Raw: w3Table("en", []string{"Alpha.", "Beta."}), SourceLanguage: "en", ModelID: "selected-model",
			Game: "qa", Artifact: "sample", Keys: map[string][][]byte{"1": {{0x78, 0x56, 0x34, 0x12}}},
			Texts: map[string]string{"1": cliAuthoredTranslation, "2": cliAuthoredTranslation},
		}, wantEngine)...)
	} else {
		key := expectedTranslationKey(t, translationTMInput{command: f.Command, modelID: "selected-model", source: []byte("Hello.\n")}, wantEngine)
		f.D1 = append(f.D1, translationTMRow{Schema: key.Schema(), Source: key.SourceBytes(), Profile: key.ProfileBytes(), Text: cliAuthoredTranslation})
	}
	slices.SortFunc(f.D1, func(a, b translationTMRow) int {
		if n := bytes.Compare(a.Source, b.Source); n != 0 {
			return n
		}
		return bytes.Compare(a.Profile, b.Profile)
	})
	return f
}

type cliPrecommitExpectation struct {
	Phase, Cause                                   string
	Code, Generated, Closed, Runs, Records, Writes int
	Rows                                           []translationTMRow
	Output, Stdout                                 []byte
	PreserveOutput                                 bool
	Calls                                          []cliPrecommitCall
	Injected                                       []string
}

// Expected histories are authored from the fixture and fixed path contract,
// never sliced from the actual engine's reached history.
func (f cliPrecommitFixture) expectation(row string, hit bool, fault error) cliPrecommitExpectation {
	x := cliPrecommitExpectation{Rows: f.D0, Output: f.OldOutput.Bytes, PreserveOutput: true, Code: 1}
	isFile := f.Command.action == "localize"
	call := func(op string) cliPrecommitCall { return cliPrecommitCall{Operation: op, Backend: f.Info.Backend} }
	request := func(text string) *cliPrecommitRequest {
		return &cliPrecommitRequest{Text: text, Tokens: []int{1},
			Identity: inference.RequestIdentity{ModelSHA256: f.Info.ModelSHA256, Tokenizer: f.Info.Tokenizer,
				Template: f.Info.Template, PromptSchema: f.Info.Prompt.Schema, Prompt: f.Info.Prompt, Thinking: f.Info.Thinking},
			Policy: inference.EffectivePolicy{Schema: 1, MaxOutputTokens: config.DefaultLimits().MaxOutputTokens,
				RequestTimeout: config.DefaultLimits().RequestTimeout, SeedFixed: true, StopIDs: []int{42}}}
	}
	x.Calls = append(x.Calls, call("open"))
	if strings.HasPrefix(row, "model-") || row == "open-error" || row == "open-cancel" || row == "unsupported" || row == "residency-vram-decline" {
		x.Phase, x.Cause = "model", row
		if !strings.HasPrefix(row, "model-") {
			x.Injected = []string{row}
			x.Calls[0].Error = fault.Error()
		}
		return x
	}
	x.Closed = 1
	info := f.Info
	i := call("info")
	i.Info = &info
	x.Calls = append(x.Calls, i)
	if row == "info-error" || row == "info-cancel" {
		x.Phase, x.Cause, x.Injected = "profile", row, []string{row}
		x.Calls[1].Info, x.Calls[1].Error = &inference.ModelInfo{}, fault.Error()
		x.Calls = append(x.Calls, call("close"))
		return x
	}
	r := call("profile-request")
	r.SourceLanguage, r.TargetLanguage, r.Request = "en", "ja", request("")
	if row == "profile-request-error" || row == "profile-request-cancel" {
		x.Phase, x.Cause, x.Injected = "profile", row, []string{row}
		r.Request, r.Error = nil, fault.Error()
		x.Calls = append(x.Calls, r, call("close"))
		return x
	}
	x.Calls = append(x.Calls, r)
	if row == "missing-record" {
		x.Phase, x.Cause, x.Runs = "prepare", "missing-record", 1
		x.Calls = append(x.Calls, call("close"))
		return x
	}
	if isFile {
		x.Runs = 1
	}
	x.Calls = append(x.Calls, i)
	for _, text := range f.Texts {
		r := call("body-request")
		r.Input, r.SourceLanguage, r.TargetLanguage, r.Request = text, "en", "ja", request(text)
		if row == "body-request-error" {
			x.Phase, x.Cause, x.Injected = "plan", row, []string{row}
			r.Request, r.Error = nil, fault.Error()
			x.Calls = append(x.Calls, r, call("close"))
			return x
		}
		x.Calls = append(x.Calls, r)
		c := call("count")
		c.Request, c.Count = request(text), 1
		if row == "count-error" {
			x.Phase, x.Cause, x.Injected = "plan", row, []string{row}
			c.Count, c.Error = 0, fault.Error()
			x.Calls = append(x.Calls, c, call("close"))
			return x
		}
		x.Calls = append(x.Calls, c)
	}
	if !hit {
		for _, text := range f.Texts {
			g := call("generate")
			g.Request = request(text)
			g.Result = &inference.GenerationResult{Text: cliAuthoredTranslation, Finish: inference.Stop, PromptTokens: 1}
			x.Generated++
			if row == "partial-plus-error" {
				x.Phase, x.Cause, x.Injected = "generate", row, []string{row}
				g.Result.Text, g.Error = "private partial result", fault.Error()
				x.Calls = append(x.Calls, g, call("close"))
				return x
			}
			x.Calls = append(x.Calls, g)
		}
	}
	x.Calls = append(x.Calls, call("close"))
	x.Code, x.Rows = 0, f.D1
	if isFile {
		x.Records, x.Output, x.PreserveOutput = 1, f.Target, false
		if hit {
			x.Records, x.Runs = 2, 2
		}
	} else {
		x.Stdout, x.Writes = []byte(cliAuthoredTranslation), 1
	}
	return x
}

func (f cliPrecommitFixture) engine(row string, cancel context.CancelFunc, fault error) *cliPrecommitEngine {
	e := &cliPrecommitEngine{translationEngine: &translationEngine{info: f.Info, text: cliAuthoredTranslation, finish: inference.Stop}}
	inject := func(ctx context.Context) error {
		e.Injected = append(e.Injected, row)
		if strings.HasSuffix(row, "-cancel") {
			cancel()
			return ctx.Err()
		}
		return fault
	}
	switch row {
	case "open-error", "open-cancel", "unsupported", "residency-vram-decline":
		e.OpenFunc = func(ctx context.Context, _ string) (string, inference.Engine, error) { return "", nil, inject(ctx) }
	case "model-unselected", "model-missing", "model-mismatch":
		e.OpenFunc = models.New(f.Paths).OpenSelected
	case "info-error", "info-cancel":
		e.InfoFunc = func(ctx context.Context) (inference.ModelInfo, error) { return inference.ModelInfo{}, inject(ctx) }
	case "profile-request-error", "profile-request-cancel", "body-request-error":
		e.RequestFunc = func(ctx context.Context, input prompt.Input, policy inference.GenerationPolicy) (inference.GenerationRequest, error) {
			if (input.Text == "") == strings.HasPrefix(row, "profile-") {
				return inference.GenerationRequest{}, inject(ctx)
			}
			return e.translationEngine.NewRequest(ctx, input, policy)
		}
	case "count-error":
		e.CountFunc = func(ctx context.Context, _ inference.GenerationRequest) (int, error) { return 0, inject(ctx) }
	case "partial-plus-error":
		e.GenerateFunc = func(ctx context.Context, _ inference.GenerationRequest) (inference.GenerationResult, error) {
			return inference.GenerationResult{Text: "private partial result", Finish: inference.Stop, PromptTokens: 1}, inject(ctx)
		}
	}
	return e
}

type cliPrecommitState struct {
	DispatchReturnCode        *int             `json:"dispatch_return_code"`
	ExitObserved              bool             `json:"exit_observed"`
	Result                    *localize.Result `json:"execute_result"`
	Phase, Cause, Error       string
	ErrorUnit                 string
	Source, Output            cliPrecommitFile
	Rows                      []translationTMRow
	Stdout, Diagnostic        []byte
	Writes, Generated, Closed int
	Calls                     []cliPrecommitCall
	Injected                  []string
	Runs                      []string
	Records                   []cliPublishedRecord
	Stages, Backups           []string
	Registry                  map[string]cliPrecommitFile
	RegistrySnapshots         []string
}

func (f cliPrecommitFixture) observe(t *testing.T, e *cliPrecommitEngine, out, diag *cliReachedWriter) cliPrecommitState {
	t.Helper()
	s := cliPrecommitState{Source: cliPrecommitCapture(t, f.Command.options.Source), Output: cliPrecommitCapture(t, f.Output),
		Rows: readTranslationTM(t, f.Paths), Stdout: bytes.Clone(out.target.Bytes()), Diagnostic: bytes.Clone(diag.target.Bytes()),
		Writes: out.attempts, Generated: e.generated, Closed: e.closed, Calls: e.Calls, Injected: e.Injected,
		Registry: map[string]cliPrecommitFile{}}
	var err error
	s.Runs, err = filepath.Glob(filepath.Join(f.Dir, ".yakuori-run-*"))
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range s.Runs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			switch entry.Name() {
			case "stage":
				s.Stages = append(s.Stages, filepath.Join(dir, entry.Name()))
			case "record.json":
				raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				var record cliPublishedRecord
				if err := json.Unmarshal(raw, &record); err != nil {
					t.Fatal(err)
				}
				s.Records = append(s.Records, record)
			default:
				t.Errorf("unexpected publication artifact: %s", filepath.Join(dir, entry.Name()))
			}
		}
	}
	s.Backups, err = filepath.Glob(filepath.Join(f.Dir, ".yakuori-backup-*"))
	if err != nil {
		t.Fatal(err)
	}
	for path := range f.Registry {
		s.Registry[path] = cliPrecommitCapture(t, path)
	}
	if len(f.Registry) != 0 {
		entries, err := os.ReadDir(config.ResolveStoragePaths(f.Paths).SnapshotDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			s.RegistrySnapshots = append(s.RegistrySnapshots, entry.Name())
		}
	}
	return s
}

func cliPrecommitCause(t *testing.T, err error, x cliPrecommitExpectation, fault error) (string, string) {
	t.Helper()
	if x.Phase == "" {
		if err != nil {
			t.Errorf("unexpected execute error: %v", err)
		}
		return "", ""
	}
	var failure *localize.Error
	if !errors.As(err, &failure) || failure.Phase != x.Phase {
		t.Errorf("typed phase: error=%v expected=%s", err, x.Phase)
		return "", ""
	}
	ok := false
	switch x.Cause {
	case "model-unselected":
		ok = failure.Err.Error() == "no model selected"
	case "model-missing":
		var pathError *os.PathError
		ok = errors.Is(err, os.ErrNotExist) && errors.As(err, &pathError)
	case "model-mismatch":
		ok = failure.Err.Error() == `model "test-model" identity mismatch`
	case "missing-record":
		var recovery *publication.RecoveryError
		ok = errors.As(err, &recovery) && recovery.Reason == "invalid or missing operation record" && errors.Is(err, os.ErrNotExist)
	default:
		ok = errors.Is(err, fault)
	}
	if !ok {
		t.Errorf("typed cause: error=%v expected=%s", err, x.Cause)
		return failure.Phase, "unexpected"
	}
	return failure.Phase, x.Cause
}

func (f cliPrecommitFixture) check(t *testing.T, x cliPrecommitExpectation, s cliPrecommitState) {
	t.Helper()
	if !reflect.DeepEqual(s.Source, f.Source) || !reflect.DeepEqual(s.Registry, f.Registry) {
		t.Error("precommit-source-or-registry-identity: bytes or physical identity changed")
	}
	if len(s.RegistrySnapshots) != 0 {
		t.Errorf("Registry refusal acquired model snapshots: %v", s.RegistrySnapshots)
	}
	if !reflect.DeepEqual(s.Rows, x.Rows) {
		t.Errorf("precommit-d0/d1: actual=%+v expected=%+v", s.Rows, x.Rows)
	}
	if !bytes.Equal(s.Output.Bytes, x.Output) || s.Output.Exists != f.OldOutput.Exists || x.PreserveOutput && !reflect.DeepEqual(s.Output, f.OldOutput) {
		t.Error("precommit-output-preserved: bytes/presence/identity mismatch")
	}
	if !bytes.Equal(s.Stdout, x.Stdout) || s.Writes != x.Writes {
		t.Errorf("precommit-stdout: bytes=%q writes=%d expected=%q/%d", s.Stdout, s.Writes, x.Stdout, x.Writes)
	}
	if s.Generated != x.Generated || s.Closed != x.Closed || !reflect.DeepEqual(s.Injected, x.Injected) {
		t.Errorf("precommit-lifecycle: generate=%d close=%d injected=%v expected=%d/%d/%v", s.Generated, s.Closed, s.Injected, x.Generated, x.Closed, x.Injected)
	}
	if len(s.Runs) != x.Runs || len(s.Records) != x.Records || len(s.Stages) != 0 || len(s.Backups) != 0 {
		t.Errorf("precommit-publication: runs=%d records=%d stages=%v backups=%v expected=%d/%d/0/0", len(s.Runs), len(s.Records), s.Stages, s.Backups, x.Runs, x.Records)
	}
	for _, record := range s.Records {
		if record.SchemaVersion != 1 || record.Phase != "published" || record.LastCompletedOperation != "publish-rename" {
			t.Errorf("published record not installed: %+v", record)
		}
	}
	if s.DispatchReturnCode != nil {
		if *s.DispatchReturnCode != x.Code || (x.Code == 0) != (len(s.Diagnostic) == 0) {
			t.Errorf("dispatch code/diagnostic=%d/%q expected=%d", *s.DispatchReturnCode, s.Diagnostic, x.Code)
		}
	} else if s.Phase != x.Phase || s.Cause != x.Cause {
		t.Errorf("execute phase/cause=%s/%s expected=%s/%s", s.Phase, s.Cause, x.Phase, x.Cause)
	}
	if s.Result != nil {
		wantUnit := ""
		if x.Phase == "plan" || x.Phase == "generate" {
			wantUnit = "document"
			if f.Command.format == "w3strings" {
				wantUnit = "1"
			}
		}
		if s.ErrorUnit != wantUnit {
			t.Errorf("typed error unit=%q expected=%q", s.ErrorUnit, wantUnit)
		}
		wantPublication := publication.State("")
		if f.Command.action == "localize" && (x.Phase == "" || x.Phase == "plan" || x.Phase == "generate" || x.Phase == "prepare") {
			wantPublication = publication.NotPublished
			if x.Code == 0 {
				wantPublication = publication.Published
			}
		}
		if s.Result.TMCommitted != (x.Code == 0) || s.Result.BytesWritten != len(x.Stdout) || s.Result.Publication.State != wantPublication {
			t.Errorf("execute result=%+v expected committed=%t bytes=%d publication=%s", *s.Result, x.Code == 0, len(x.Stdout), wantPublication)
		}
	}
	for _, private := range append(slices.Clone(f.Texts), "private", "Sentinel source.") {
		if bytes.Contains(s.Diagnostic, []byte(private)) {
			t.Errorf("diagnostic leaked body/prompt: %q", s.Diagnostic)
		}
	}
	actualCalls := slices.Clone(s.Calls)
	if strings.HasPrefix(x.Cause, "model-") && len(actualCalls) == 1 {
		// Registry has unexported/untyped refusal errors. execute-cause checks
		// errors.Is/As where supported; dispatch is not a typed-error oracle.
		if actualCalls[0].Error == "" {
			t.Error("real Registry opener did not refuse")
		}
		actualCalls[0].Error = ""
	}
	if !reflect.DeepEqual(actualCalls, x.Calls) {
		t.Errorf("precommit-history: actual=%+v expected=%+v", actualCalls, x.Calls)
	}
}

func (f cliPrecommitFixture) save(t *testing.T, arm string, x cliPrecommitExpectation, s cliPrecommitState) {
	t.Helper()
	pipelineCLISaveJSON(t, arm+"/expected.json", x)
	pipelineCLISaveJSON(t, arm+"/state.json", s)
	pipelineCLISaveJSON(t, arm+"/before.json", struct {
		Source, Output cliPrecommitFile
		Registry       map[string]cliPrecommitFile
	}{f.Source, f.OldOutput, f.Registry})
	pipelineCLISave(t, arm+"/stdout.bin", s.Stdout)
	pipelineCLISave(t, arm+"/diagnostic.bin", s.Diagnostic)
	pipelineCLISave(t, arm+"/source.bin", s.Source.Bytes)
	pipelineCLISave(t, arm+"/output.bin", s.Output.Bytes)
	pipelineCLISave(t, arm+"/logical-rows.log", cliLogicalRows(s.Rows))
	db, err := os.ReadFile(config.ResolveStoragePaths(f.Paths).TMFile)
	if err != nil {
		t.Fatal(err)
	}
	pipelineCLISave(t, arm+"/tm.sqlite", db)
	for i, dir := range s.Runs {
		for _, name := range []string{"stage", "record.json"} {
			path := filepath.Join(dir, name)
			raw, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			pipelineCLISave(t, fmt.Sprintf("%s/run-%d/%s", arm, i, name), raw)
		}
	}
}

func TestPipelinePrecommitCLI(t *testing.T) {
	for _, backend := range []string{"cpu", "cuda"} {
		for _, surface := range []string{"text-file-replace-existing", "text-stdout-none", "w3-file-replace-existing"} {
			rows := []string{"happy", "open-error", "info-error", "profile-request-error", "body-request-error", "count-error", "partial-plus-error",
				"open-cancel", "info-cancel", "profile-request-cancel", "model-unselected", "model-missing", "model-mismatch"}
			if backend == "cuda" {
				rows = append(rows, "unsupported", "residency-vram-decline")
			}
			for _, row := range rows {
				for _, entry := range []string{"dispatch", "execute-cause"} {
					t.Run(backend+"-"+surface+"-"+row+"-"+entry, func(t *testing.T) {
						// Given: independent fixtures for both entry points, authored
						// D0/D1, real source/output identities and a single opener.
						f := newCLIPrecommitFixture(t, backend, surface, row)
						fault := errors.New("private precommit backend failure")
						switch row {
						case "open-cancel", "info-cancel", "profile-request-cancel":
							fault = context.Canceled
						case "unsupported":
							fault = goinfer.ErrUnsupportedBackend
						case "residency-vram-decline":
							fault = goinfer.ErrCUDAResidency
						}
						for pass := 0; pass < 2; pass++ {
							selected, expectedRow, hit := row, row, false
							if pass == 1 && !strings.HasPrefix(row, "model-") {
								selected, expectedRow, hit = "happy", "happy", row == "happy"
								if f.Command.action == "localize" && (row == "body-request-error" || row == "count-error" || row == "partial-plus-error") {
									expectedRow = "missing-record"
								}
							}
							x := f.expectation(expectedRow, hit, fault)
							ctx, cancel := context.WithCancel(context.Background())
							e := f.engine(selected, cancel, fault)
							out, diag := &cliReachedWriter{}, &cliReachedWriter{}
							var code *int
							var result *localize.Result
							var executeErr error
							// When: dispatch is real NotifyContext -> execute -> Core;
							// the other fresh fixture preserves the typed API result.
							if entry == "dispatch" {
								value := runWithTranslationOpener(ctx, f.Args, out, diag, func() (config.Paths, error) { return f.Paths, nil }, nil, e.Open)
								code = &value
							} else {
								value, err := f.Command.execute(ctx, out, f.Paths, e.Open)
								result, executeErr = &value, err
							}
							cancel()
							s := f.observe(t, e, out, diag)
							s.DispatchReturnCode, s.Result = code, result
							if entry == "execute-cause" {
								s.Phase, s.Cause = cliPrecommitCause(t, executeErr, x, fault)
								var failure *localize.Error
								if errors.As(executeErr, &failure) {
									s.ErrorUnit = failure.UnitID.StableID()
								}
								if executeErr != nil {
									s.Error = executeErr.Error()
								}
							}
							// Then: compare physical state and all four SQLite
							// columns before any rerun; preserve actuals even on RED.
							arm := []string{"initial", "rerun"}[pass]
							f.save(t, arm, x, s)
							f.check(t, x, s)
							dispatch := "not-applicable"
							if code != nil {
								dispatch = fmt.Sprint(*code)
							}
							t.Logf("%s: dispatch_return_code=%s exit_observed=false phase=%s cause=%s generated=%d closed=%d stdout_bytes=%d rows=%d runs=%d records=%d",
								arm, dispatch, s.Phase, s.Cause, s.Generated, s.Closed, len(s.Stdout), len(s.Rows), len(s.Runs), len(s.Records))
						}
					})
				}
			}
		}
	}
}
