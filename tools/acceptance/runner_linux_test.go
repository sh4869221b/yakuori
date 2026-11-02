//go:build linux

package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/publication"
	"golang.org/x/sys/unix"
)

type acceptanceFakeEngine struct{ info inference.ModelInfo }

func (e *acceptanceFakeEngine) Info(context.Context) (inference.ModelInfo, error) { return e.info, nil }
func (e *acceptanceFakeEngine) Close() error                                      { return nil }
func (e *acceptanceFakeEngine) CountTokens(_ context.Context, r inference.GenerationRequest) (int, error) {
	return len(r.TokenIDs()), nil
}
func (e *acceptanceFakeEngine) NewRequest(_ context.Context, input prompt.Input, policy inference.GenerationPolicy) (inference.GenerationRequest, error) {
	return inference.NewGenerationRequest(inference.PreparedRequest{RenderedPrompt: input.Text, TokenIDs: []int{1}, StopIDs: []int{42}, Terms: input.Terms, Identity: inference.RequestIdentity{ModelSHA256: e.info.ModelSHA256, Tokenizer: e.info.Tokenizer, Template: e.info.Template, PromptSchema: e.info.Prompt.Schema, Prompt: e.info.Prompt, Thinking: e.info.Thinking}}, policy)
}
func (e *acceptanceFakeEngine) Generate(_ context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
	text := "訳文"
	if r.RenderedPrompt() == "Long source" {
		text = strings.Repeat("長", 160)
	}
	return inference.GenerationResult{Text: text, Finish: inference.Stop, PromptTokens: len(r.TokenIDs())}, nil
}
func fakeOpener(context.Context, string) (string, inference.Engine, error) {
	info := inference.ModelInfo{ModelSHA256: [32]byte{1}, RequestedBackend: "cuda", Backend: "cuda", BackendPin: "goinfer-test", CUDABackendPin: "cuda-test", ComputeQuant: "int4", ContextTokens: 4096, ConfiguredContextTokens: 4096, ModelContextTokens: 4096, ResidentActive: true, ResidentContextCap: 4096, KVPrecision: "fp16", Tokenizer: inference.TokenizerIdentity{ModelSHA256: [32]byte{1}, BackendPin: "goinfer-test"}, Template: inference.TemplateIdentity{Family: "chatml", Source: "embedded", RendererVersion: "v1"}, PolicySchema: inference.PolicySchemaV1, Prompt: prompt.PlainIdentity(), Thinking: inference.ThinkingDisabled, Prefill: inference.PrefillPolicy{Path: "cuda", Fast: "off"}}
	return "test-model", &acceptanceFakeEngine{info: info}, nil
}

func TestAcceptanceWorkerHelper(t *testing.T) {
	if os.Getenv("YAKUORI_ACCEPTANCE_HELPER") != "1" {
		return
	}
	var req WorkerRequest
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		os.Exit(2)
	}
	result, err := RunWorker(context.Background(), req, fakeOpener)
	fd := os.NewFile(3, "result")
	if fd == nil || json.NewEncoder(fd).Encode(result) != nil {
		os.Exit(2)
	}
	fd.Close()
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestAcceptanceWorkerProtocol(t *testing.T) {
	// The publication implementation deliberately rejects unqualified mounts.
	// Use the repository mount where available rather than /tmp (often tmpfs).
	dir, err := os.MkdirTemp(".", ".acceptance-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		t.Fatal(err)
	}
	if stat.Type != unix.EXT4_SUPER_MAGIC && stat.Type != unix.BTRFS_SUPER_MAGIC {
		t.Skip("real publication requires ext4 or Btrfs; worker success was not exercised")
	}
	source, err := filepath.Abs("../w3spike/testdata/better-keybinds/en.w3strings")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	model := [32]byte{1}
	tmData := filepath.Join(dir, "tm")
	if err := os.Mkdir(tmData, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YAKUORI_ACCEPTANCE_HELPER", "1")
	req := WorkerRequest{RunID: "fixture-fresh", FixtureID: "fixture", Mode: "fresh", Source: source, Output: filepath.Join(dir, "fresh.w3strings"), SourceLanguage: "en", SourceSHA256: hex.EncodeToString(digest[:]), ModelID: "test-model", ModelSHA256: hex.EncodeToString(model[:]), Backend: "cuda", Game: "witcher3", Artifact: "fixture", TMData: tmData}
	fresh, err := executeWorker(context.Background(), os.Args[0], []string{"-test.run=^TestAcceptanceWorkerHelper$"}, req)
	if err != nil {
		t.Fatalf("worker: %v; phase=%s", err, fresh.FailurePhase)
	}
	if !fresh.Observation.FinalValidated || !fresh.Result.TMCommitted || fresh.Result.Publication.State != publication.Published || fresh.Observation.GenerateCalls != 19 || len(fresh.Observation.Units) != 19 {
		t.Fatalf("fresh result: %+v", fresh)
	}
	for _, u := range fresh.Observation.Units {
		if u.Kind != "generated" {
			t.Fatalf("unexpected fresh unit: %+v", u)
		}
	}
	req.RunID, req.Mode, req.Output = "fixture-reuse", "reuse", filepath.Join(dir, "reuse.w3strings")
	reuse, err := executeWorker(context.Background(), os.Args[0], []string{"-test.run=^TestAcceptanceWorkerHelper$"}, req)
	if err != nil {
		t.Fatalf("reuse worker: %v; phase=%s", err, reuse.FailurePhase)
	}
	if reuse.Details.Profile != fresh.Details.Profile || reuse.Observation.GenerateCalls != 0 || !reuse.Result.TMCommitted || reuse.Result.Publication.State != publication.Published {
		t.Fatalf("reuse result: %+v", reuse)
	}
	for _, u := range reuse.Observation.Units {
		if u.Kind != "tm" {
			t.Fatalf("unexpected reuse unit: %+v", u)
		}
	}
	later, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(raw, later) {
		t.Fatal("source changed")
	}

	recordsFile := filepath.Join(dir, "expected.records")
	if err := os.WriteFile(recordsFile, encodeOracleRecords(t, reuse.Observation.Units, reuse.Observation.Keys), 0600); err != nil {
		t.Fatal(err)
	}
	fakeReader := filepath.Join(dir, "oracle")
	script := fmt.Sprintf("#!/bin/sh\n[ \"$1\" = records ] && [ \"$2\" = %s ] || exit 1\ncp %s \"$3\"\n", strconv.Quote(req.Output), strconv.Quote(recordsFile))
	if err := os.WriteFile(fakeReader, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	result, err := FinalizeWorker(context.Background(), fakeReader, req, reuse, nil, filepath.Join(dir, "read.records"))
	if err != nil || result.Execution != "exited" || result.ExitCode == nil || *result.ExitCode != 0 || result.Comparison != "pass" {
		t.Fatalf("reader: %+v %v", result, err)
	}
	if result, err = FinalizeWorker(context.Background(), fakeReader, req, reuse, errors.New("child exit"), filepath.Join(dir, "not-read.records")); err == nil || result.Execution != "not_run" || reuse.Result.Publication.State != publication.Published {
		t.Fatal("child failure lost published state or reached reader")
	}
	if _, err := os.Stat(req.Output); err != nil {
		t.Fatal("published file lost after child failure")
	}
	if _, err := os.Stat(filepath.Join(dir, "not-read.records")); !os.IsNotExist(err) {
		t.Fatal("reader executed after child failure")
	}
	badUnits := append([]localize.AcceptanceUnit(nil), reuse.Observation.Units...)
	badUnits[0].Text = "不一致"
	if err := os.WriteFile(recordsFile, encodeOracleRecords(t, badUnits, reuse.Observation.Keys), 0600); err != nil {
		t.Fatal(err)
	}
	if result, err = FinalizeWorker(context.Background(), fakeReader, req, reuse, nil, filepath.Join(dir, "read.records")); err == nil || result.Comparison != "fail" || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatal("accepted mismatched reader text")
	}
	if result, err = FinalizeWorker(context.Background(), filepath.Join(dir, "missing"), req, reuse, nil, filepath.Join(dir, "read.records")); err == nil || result.Execution != "launch_failed" || result.ExitCode != nil || result.Comparison != "not_run" {
		t.Fatal("oracle launch failure misclassified")
	}
	if err := os.WriteFile(fakeReader, []byte("#!/bin/sh\nexit 37\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if result, err = FinalizeWorker(context.Background(), fakeReader, req, reuse, nil, filepath.Join(dir, "read.records")); err == nil || result.Execution != "exited" || result.ExitCode == nil || *result.ExitCode != 37 || result.Comparison != "not_run" {
		t.Fatal("oracle nonzero exit misclassified")
	}
	if err := os.WriteFile(fakeReader, []byte("#!/bin/sh\nkill -TERM $$\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if result, err = FinalizeWorker(context.Background(), fakeReader, req, reuse, nil, filepath.Join(dir, "read.records")); err == nil || result.Execution != "signaled" || result.Signal == "" || result.ExitCode != nil || result.Comparison != "not_run" {
		t.Fatal("oracle signal misclassified")
	}
	if oracle := os.Getenv("YAKUORI_TEST_ORACLE"); oracle != "" {
		if result, err = FinalizeWorker(context.Background(), oracle, req, reuse, nil, filepath.Join(dir, "real.records")); err != nil || result.Comparison != "pass" {
			t.Fatalf("real oracle: %+v %v", result, err)
		}
	}

	wrong := req
	wrong.RunID, wrong.Output, wrong.SourceSHA256 = "bad", filepath.Join(dir, "bad.w3strings"), strings.Repeat("0", 64)
	bad, err := executeWorker(context.Background(), os.Args[0], []string{"-test.run=^TestAcceptanceWorkerHelper$"}, wrong)
	if err == nil || bad.Observation.FinalValidated || bad.Result.TMCommitted || bad.Result.Publication.State == publication.Published {
		t.Fatalf("wrong source pin reached publication: %+v err=%v", bad, err)
	}
}

func TestCompareOracleFramingAndErrors(t *testing.T) {
	units := []localize.AcceptanceUnit{{ID: "2", Text: "行1\n行2|値"}, {ID: "9", Text: ""}}
	keys := []localize.AcceptanceKey{{ID: "2", Key: []byte{123, 0, 0, 0}}}
	good := encodeOracleRecords(t, units, keys)
	if err := CompareOracle(good, units, keys); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{bytes.Replace(good, []byte("jp"), []byte("en"), 1), bytes.Replace(good, []byte("行2"), []byte("行3"), 1), bytes.Replace(good, []byte{123, 0, 0, 0}, []byte{122, 0, 0, 0}, 1), append(bytes.Clone(good), 0)} {
		if err := CompareOracle(bad, units, keys); err == nil {
			t.Fatalf("accepted invalid oracle output: %q", bad)
		}
	}
	// The old unescaped CSV representation made these two different records
	// indistinguishable from two separate rows.
	actual := []localize.AcceptanceUnit{{ID: "1", Text: "A\n         2|00000000||B"}}
	expected := []localize.AcceptanceUnit{{ID: "1", Text: "A"}, {ID: "2", Text: "B"}}
	if err := CompareOracle(encodeOracleRecords(t, actual, nil), expected, nil); err == nil {
		t.Fatal("accepted ambiguous missing ID")
	}
	// The crate sorts key records by hash. Compare the full multiset, not the
	// last key in either ordering; a real hash-zero relation is distinct.
	keys30 := []localize.AcceptanceKey{{ID: "30", Key: []byte{0xef, 0xcd, 0xab, 0x89}}, {ID: "30", Key: []byte{0, 0, 0, 0}}}
	order := []localize.AcceptanceKey{keys30[1], keys30[0]}
	if err := CompareOracle(encodeOracleRecords(t, []localize.AcceptanceUnit{{ID: "30", Text: "訳"}}, order), []localize.AcceptanceUnit{{ID: "30", Text: "訳"}}, keys30); err != nil {
		t.Fatal(err)
	}
	if err := CompareOracle(encodeOracleRecords(t, []localize.AcceptanceUnit{{ID: "30", Text: "訳"}}, order[:1]), []localize.AcceptanceUnit{{ID: "30", Text: "訳"}}, keys30); err == nil {
		t.Fatal("missing multi-key relation accepted")
	}
}

func encodeOracleRecords(t *testing.T, units []localize.AcceptanceUnit, keys []localize.AcceptanceKey) []byte {
	t.Helper()
	var out bytes.Buffer
	out.WriteString("YAKW3O1\x00")
	write := func(n uint32) {
		if err := binary.Write(&out, binary.LittleEndian, n); err != nil {
			t.Fatal(err)
		}
	}
	write(162)
	write(2)
	out.WriteString("jp")
	write(uint32(len(units)))
	for _, unit := range units {
		id, err := strconv.ParseUint(unit.ID, 10, 32)
		if err != nil {
			t.Fatal(err)
		}
		write(uint32(id))
		write(uint32(len(unit.Text)))
		out.WriteString(unit.Text)
	}
	write(uint32(len(keys)))
	for _, key := range keys {
		id, err := strconv.ParseUint(key.ID, 10, 32)
		if err != nil || len(key.Key) != 4 {
			t.Fatal("invalid test key")
		}
		write(uint32(id))
		out.Write(key.Key)
	}
	return out.Bytes()
}

func TestPendingCriteriaAreNotExecuted(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	evidence := filepath.Join(t.TempDir(), "evidence")
	opts := RunOptions{Manifest: filepath.Join(root, "docs/evidence/acceptance/fixtures.json"), Criteria: filepath.Join(root, "docs/evidence/acceptance/criteria.json"), Root: root, Repository: root, Evidence: evidence, ModelID: "not-opened", ModelSHA256: strings.Repeat("0", 64), Game: "witcher3", Artifact: "test"}
	record, err := Run(context.Background(), opts)
	if err == nil || record.Status != "not_run" || record.FailurePhase != "readiness" || len(record.Runs) != 4 {
		t.Fatalf("pending criteria: %+v err=%v", record, err)
	}
	for _, entry := range record.Runs {
		if entry.Status != "not_run" || entry.WorkerExit != "not_run" || entry.FinalValidated != nil || entry.TMCommitted != nil || entry.GenerateCalls != nil || entry.FinalValidationStatus != "not_run" || entry.CommitStatus != "not_run" || entry.PublicationStatus != "not_run" {
			t.Fatalf("worker started: %+v", entry)
		}
	}
	data, err := os.ReadFile(filepath.Join(evidence, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved RunRecord
	if err := json.Unmarshal(data, &saved); err != nil || saved.Status != "not_run" || len(saved.Runs) != 4 {
		t.Fatalf("saved record: %+v err=%v", saved, err)
	}
}

func TestControlledRunnerRecord(t *testing.T) {
	// All manifest confirmation and build metadata below are synthetic test
	// values; this result is temporary and is not a GPU acceptance result.
	dir, err := os.MkdirTemp(".", ".acceptance-record-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		t.Fatal(err)
	}
	if stat.Type != unix.EXT4_SUPER_MAGIC && stat.Type != unix.BTRFS_SUPER_MAGIC {
		t.Skip("real publication requires ext4 or Btrfs")
	}
	fixturePath, err := filepath.Abs("testdata/small.w3strings")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.w3strings")
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	sourceSHA := sha256.Sum256(raw)
	fixture := Fixture{ID: "test-small", Kind: "mod", SourceURL: "test-only synthetic fixture", Revision: "test-only", License: "project-authored test", LocalPath: source, SHA256: hex.EncodeToString(sourceSHA[:]), Bytes: len(raw), Units: 3, TextBytes: 16, MaxTextBytes: 11, Format: "w3strings", FormatVersion: 162, Language: "en"}
	for _, name := range categories {
		id := "10"
		if name == "empty_text" {
			id = "20"
		}
		if name == "long_text" {
			id = "30"
		}
		fixture.Categories = append(fixture.Categories, Category{Name: name, UnitIDs: []string{id}, Evidence: "synthetic test case only"})
	}
	manifest := Manifest{Version: "test-manifest", Fixtures: []Fixture{fixture}}
	criteria := Criteria{Version: "test-criteria", ManifestVersion: manifest.Version, FixtureIDs: []string{fixture.ID}, Values: make(map[string]Criterion), Confirmation: Confirmation{Status: "confirmed", Date: "2026-01-01T00:00:00Z", Reviewer: "test-only", Decision: "approved", ManifestVersion: manifest.Version, CriteriaVersion: "test-criteria"}}
	for _, name := range criterionNames {
		criteria.Values[name] = Criterion{Value: "1", Rationale: "test only", Impact: "test only"}
	}
	for name, value := range map[string]string{"units": "3", "text_bytes": "16", "artifact_bytes": strconv.Itoa(len(raw))} {
		v := criteria.Values[name]
		v.Value = value
		criteria.Values[name] = v
	}
	manifestPath, criteriaPath := filepath.Join(dir, "manifest.json"), filepath.Join(dir, "criteria.json")
	for path, value := range map[string]any{manifestPath: manifest, criteriaPath: criteria} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	fakeRecords, fakeReader := filepath.Join(dir, "fake.records"), filepath.Join(dir, "fake-reader")
	if err := os.WriteFile(fakeReader, []byte(fmt.Sprintf("#!/bin/sh\ncp %s \"$3\"\n", strconv.Quote(fakeRecords))), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YAKUORI_ACCEPTANCE_HELPER", "1")
	testBuild := BuildInfo{Revision: "controlled-test-only", Modified: "false"}
	var capturedFresh WorkerResult
	launcher := func(ctx context.Context, req WorkerRequest) (WorkerResult, error) {
		result, err := executeWorker(ctx, os.Args[0], []string{"-test.run=^TestAcceptanceWorkerHelper$"}, req)
		if err != nil {
			return result, err
		}
		result.Build = testBuild
		if req.Mode == "fresh" {
			capturedFresh = result
			if oracle := os.Getenv("YAKUORI_TEST_ORACLE"); oracle != "" {
				checked, err := FinalizeWorker(ctx, oracle, req, result, nil, filepath.Join(dir, "small-real.records"))
				if err != nil || checked.Comparison != "pass" {
					t.Fatalf("small fixture real oracle: %+v err=%v", checked, err)
				}
			}
		}
		if err := os.WriteFile(fakeRecords, encodeOracleRecords(t, result.Observation.Units, result.Observation.Keys), 0600); err != nil {
			return result, err
		}
		return result, nil
	}
	builder := func(context.Context, string, string) (string, string, bool, error) {
		return fakeReader, "test-only", false, nil
	}
	model := [32]byte{1}
	opts := RunOptions{Manifest: manifestPath, Criteria: criteriaPath, Root: dir, Repository: dir, Evidence: filepath.Join(dir, "private-evidence"), ModelID: "test-model", ModelSHA256: hex.EncodeToString(model[:]), Game: "test", Artifact: "test", ModelPaths: config.Paths{Data: dir}}
	record, err := runWith(context.Background(), opts, launcher, builder, testBuild)
	if err != nil || record.Status != "pass" || len(record.Runs) != 2 {
		t.Fatalf("controlled run: %+v err=%v", record, err)
	}
	fresh, reuse := record.Runs[0], record.Runs[1]
	if fresh.GeneratedUnits == nil || *fresh.GeneratedUnits != 2 || fresh.UnchangedUnits == nil || *fresh.UnchangedUnits != 1 || fresh.GenerateCalls == nil || *fresh.GenerateCalls != 2 || reuse.ReusedUnits == nil || *reuse.ReusedUnits != 3 || reuse.GenerateCalls == nil || *reuse.GenerateCalls != 0 {
		t.Fatalf("wrong unit counts: fresh=%+v reuse=%+v", fresh, reuse)
	}
	for _, u := range capturedFresh.Observation.Units {
		if u.ID == "20" && (u.Text != "" || u.Kind != "unchanged") {
			t.Fatalf("empty unit: %+v", u)
		}
		if u.ID == "30" && u.Text != strings.Repeat("長", 160) {
			t.Fatalf("long unit length=%d", len(u.Text))
		}
	}
	for _, entry := range record.Runs {
		if entry.Oracle.Comparison != "pass" || entry.SourcePath != source || entry.OutputSHA256 == "" || entry.OutputPath == "" {
			t.Fatalf("record references: %+v", entry)
		}
		foundEmpty, foundLong := false, false
		for _, u := range entry.Units {
			for _, cat := range u.Categories {
				if u.ID == "20" && cat == "empty_text" {
					foundEmpty = true
				}
				if u.ID == "30" && cat == "long_text" {
					foundLong = true
				}
			}
		}
		if !foundEmpty || !foundLong {
			t.Fatalf("category references missing: %+v", entry.Units)
		}
		if _, err := os.Stat(filepath.Join(opts.Evidence, entry.OutputPath)); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(opts.Evidence, "run.json"))
	if err != nil || bytes.Contains(data, []byte("Long source")) || bytes.Contains(data, []byte(strings.Repeat("長", 160))) {
		t.Fatal("private text leaked into run record")
	}

	opts.Evidence = filepath.Join(dir, "source-hash-failure")
	mutatingLauncher := func(ctx context.Context, req WorkerRequest) (WorkerResult, error) {
		result, err := launcher(ctx, req)
		if err == nil {
			err = os.WriteFile(source, []byte("changed after publication"), 0600)
		}
		return result, err
	}
	failed, err := runWith(context.Background(), opts, mutatingLauncher, builder, testBuild)
	if err == nil || failed.FailurePhase != "source_hash" || failed.Runs[0].Oracle.Comparison != "pass" {
		t.Fatalf("source hash phase: %+v err=%v", failed, err)
	}
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fakeReader, []byte(fmt.Sprintf("#!/bin/sh\ncp %s \"$3\" || exit 1\nrm \"$2\"\n", strconv.Quote(fakeRecords))), 0700); err != nil {
		t.Fatal(err)
	}
	opts.Evidence = filepath.Join(dir, "output-hash-failure")
	failed, err = runWith(context.Background(), opts, launcher, builder, testBuild)
	if err == nil || failed.FailurePhase != "output_hash" || failed.Runs[0].Oracle.Comparison != "pass" {
		t.Fatalf("output hash phase: %+v err=%v", failed, err)
	}
}

func TestOfflineOracleBuild(t *testing.T) {
	if os.Getenv("YAKUORI_TEST_BUILD_ORACLE") != "1" {
		t.Skip("explicit offline oracle QA requires the locked Rust dependencies")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	path, revision, _, err := buildOracle(context.Background(), repo, t.TempDir())
	if err != nil || revision == "" {
		t.Fatalf("build oracle: %v revision=%q", err, revision)
	}
	if stat, err := os.Stat(path); err != nil || stat.Mode()&0111 == 0 {
		t.Fatalf("built oracle is not executable: %v", err)
	}
}

func TestRealOracleMultipleKeys(t *testing.T) {
	oracle := os.Getenv("YAKUORI_TEST_ORACLE")
	if oracle == "" {
		t.Skip("explicit independent oracle QA")
	}
	// Same literal structure used by the W3 adapter tests: source order puts
	// 89abcdef before zero, and both keys belong to ID 30.
	raw, err := hex.DecodeString("52545357a2000000835403" + "4c5682590a00000001000000" + "585682590100000002000000" + "525682590500000003000000" + "02efcdab89585682590000000058568259" + "0e11114387450e000022220009ed12322500003333ef040000444455559348")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	source, output := filepath.Join(dir, "source.w3strings"), filepath.Join(dir, "records")
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if outputBytes, err := exec.Command(oracle, "records", source, output).CombinedOutput(); err != nil {
		t.Fatalf("oracle: %v %s", err, outputBytes)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	units := []localize.AcceptanceUnit{{ID: "10", Text: "C"}, {ID: "30", Text: "AB"}, {ID: "20", Text: "XYZ"}}
	keys := []localize.AcceptanceKey{{ID: "30", Key: []byte{0xef, 0xcd, 0xab, 0x89}}, {ID: "30", Key: []byte{0, 0, 0, 0}}}
	if err := CompareOracle(data, units, keys); err != nil {
		t.Fatal(err)
	}
}
