//go:build offlinesmoke

package localize

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/inference/goinfer"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/unit"
)

// TestOfflineCoreMatrix closes GAP-4,5,6 for the CPU backend: the real goinfer
// engine behind the real InferenceEngine drives Core.Text and Core.File under a
// private XDG registry. Injected failures use the existing seams and are
// labelled; the cancellation and loader cases are real.
func TestOfflineCoreMatrix(t *testing.T) {
	model := offlineSmokeFixture(t)
	backend := offlineSmokeBackend(t)
	ctx := context.Background()
	registry, _ := offlineSmokeRegistry(t)
	if _, err := registry.Import(ctx, "qwen", model); err != nil {
		t.Fatalf("import: %v", err)
	}
	if err := registry.Use(ctx, "qwen"); err != nil {
		t.Fatalf("use: %v", err)
	}
	selected, opened, err := registry.OpenSelected(ctx, backend)
	if err != nil || opened == nil || selected != "qwen" {
		t.Fatalf("open selected: id=%q engine=%v err=%v", selected, opened != nil, err)
	}
	engine, ok := opened.(*goinfer.Engine)
	if !ok {
		t.Fatalf("registry returned %T instead of the real goinfer engine", opened)
	}
	engineClosed := false
	t.Cleanup(func() {
		if !engineClosed {
			_ = engine.Close()
		}
	})
	info, err := engine.Info(ctx)
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 128, config.DefaultLimits().RequestTimeout)
	if err != nil {
		t.Fatal(err)
	}
	build := func(ctx context.Context, _ unit.UnitID, text string) (inference.GenerationRequest, error) {
		return engine.NewRequest(ctx, prompt.Input{SourceLanguage: "English", TargetLanguage: "Japanese", Text: text}, policy)
	}
	base, err := build(ctx, unit.UnitID{}, offlineSmokeText)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	profile, err := NewCanonicalInferenceProfile(info, base, InferenceProfileInput{ModelID: "qwen", Glossary: identity.GlossaryInput{Mode: "none"}, Limits: config.DefaultLimits()})
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	core := NewCore(NewInferenceEngine(engine, build))

	t.Run("engine_request_count_generate", func(t *testing.T) {
		request, err := build(ctx, unit.UnitID{}, offlineSmokeText)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		count, err := engine.CountTokens(ctx, request)
		if err != nil || count <= 0 || count != len(request.TokenIDs()) {
			t.Fatalf("count=%d ids=%d err=%v", count, len(request.TokenIDs()), err)
		}
		result, err := engine.Generate(ctx, request)
		if err != nil || result.Finish != inference.Stop || result.PromptTokens != count || strings.TrimSpace(result.Text) == "" {
			t.Fatalf("generate: finish=%s prompt=%d output=%d err=%v", result.Finish, result.PromptTokens, result.OutputTokens, err)
		}
		t.Logf("case=engine_request_count_generate kind=real reached=generated prompt_tokens=%d output_tokens=%d finish=%s", count, result.OutputTokens, result.Finish)
	})

	t.Run("text_real_generation", func(t *testing.T) {
		tm := &fakeTM{}
		var out bytes.Buffer
		result, err := core.Text(ctx, []byte(offlineSmokeText), textadapter.New("en", "ja"), profile, tm, &out)
		if err != nil {
			t.Fatalf("text: %v", err)
		}
		if out.Len() == 0 || !result.TMCommitted || tm.calls != 1 || len(tm.rows) != 1 {
			t.Fatalf("text result=%+v bytes=%d commits=%d rows=%d", result, out.Len(), tm.calls, len(tm.rows))
		}
		offlineSmokeValidate(t, profile, out.String())
		t.Logf("case=text_real_generation kind=real reached=text_output bytes=%d tm_committed=%t", out.Len(), result.TMCommitted)
	})

	t.Run("file_real_publication", func(t *testing.T) {
		dir := t.TempDir()
		source, output := filepath.Join(dir, "source.txt"), filepath.Join(dir, "output.txt")
		if err := os.WriteFile(source, []byte(offlineSmokeText), 0o600); err != nil {
			t.Fatal(err)
		}
		tm := &fakeTM{}
		result, err := core.File(ctx, publication.Options{Source: source, Output: output, Mode: publication.Create}, textadapter.New("en", "ja"), profile, tm)
		if err != nil {
			t.Fatalf("file: %v", err)
		}
		if result.Publication.State != publication.Published || result.Publication.OutputPath == "" || !result.TMCommitted || tm.calls != 1 {
			t.Fatalf("file result=%+v commits=%d", result, tm.calls)
		}
		published, err := os.ReadFile(output)
		if err != nil || len(published) == 0 {
			t.Fatalf("published artifact: %d bytes, err=%v", len(published), err)
		}
		offlineSmokeValidate(t, profile, string(published))
		t.Logf("case=file_real_publication kind=real reached=published_file bytes=%d state=%s", len(published), result.Publication.State)
	})

	t.Run("model_missing_setup", func(t *testing.T) {
		empty, _ := offlineSmokeRegistry(t)
		id, opened, err := empty.OpenSelected(ctx, backend)
		if err == nil || opened != nil || id != "" || !strings.Contains(err.Error(), "no model selected") {
			t.Fatalf("missing model: id=%q engine=%v err=%v", id, opened != nil, err)
		}
		t.Logf("case=model_missing_setup kind=real reached=registry_setup error=%q", err)
	})

	t.Run("registered_digest_mismatch", func(t *testing.T) {
		other, otherStorage := offlineSmokeRegistry(t)
		if _, err := other.Import(ctx, "qwen", model); err != nil {
			t.Fatalf("import: %v", err)
		}
		if err := other.Use(ctx, "qwen"); err != nil {
			t.Fatalf("use: %v", err)
		}
		if err := os.WriteFile(filepath.Join(otherStorage.ModelsDir, "qwen", "model.gguf"), []byte("corrupted managed bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		id, opened, err := other.OpenSelected(ctx, backend)
		if err == nil || opened != nil || id != "" || !strings.Contains(err.Error(), "identity mismatch") {
			t.Fatalf("changed bytes: id=%q engine=%v err=%v", id, opened != nil, err)
		}
		t.Logf("case=registered_digest_mismatch kind=real reached=registry_verify error=%q", err)
	})

	t.Run("invalid_gguf_identity_matched", func(t *testing.T) {
		body := []byte(strings.Repeat("not-a-gguf-payload ", 200))
		path := filepath.Join(t.TempDir(), "invalid.gguf")
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(body)
		options := func(pin *[32]byte, snapshot string) goinfer.Options {
			return goinfer.Options{Backend: backend, ComputeQuant: "int4", ContextTokens: config.DefaultLimits().ContextTokens, ExpectedModelSHA256: pin, SnapshotDir: snapshot}
		}
		pinnedSnapshot, plainSnapshot := filepath.Join(t.TempDir(), "snap"), filepath.Join(t.TempDir(), "snap")
		for _, dir := range []string{pinnedSnapshot, plainSnapshot} {
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		pinned, err := goinfer.OpenWithOptions(ctx, path, options(&digest, pinnedSnapshot))
		if err == nil || pinned != nil {
			t.Fatalf("identity-matched invalid bytes were accepted: engine=%v err=%v", pinned != nil, err)
		}
		// Control: the same bytes fail with the identity check disabled, so the
		// rejection is the loader itself rather than the digest comparison.
		unpinned, controlErr := goinfer.OpenWithOptions(ctx, path, options(nil, plainSnapshot))
		if controlErr == nil || unpinned != nil {
			t.Fatalf("unpinned control accepted invalid bytes: engine=%v err=%v", unpinned != nil, controlErr)
		}
		t.Logf("case=invalid_gguf_identity_matched kind=real_native reached=loader_rejection pinned=%q control=%q", err, controlErr)
	})

	t.Run("injected_request_error", func(t *testing.T) {
		labelled := errors.New("injected request build failure")
		broken := NewCore(NewInferenceEngine(engine, func(context.Context, unit.UnitID, string) (inference.GenerationRequest, error) {
			return inference.GenerationRequest{}, labelled
		}))
		var out bytes.Buffer
		_, err := broken.Text(ctx, []byte(offlineSmokeText), textadapter.New("en", "ja"), profile, &fakeTM{}, &out)
		if !errors.Is(err, labelled) || out.Len() != 0 || offlineSmokePhase(t, err) != "plan" {
			t.Fatalf("injected request error: bytes=%d phase=%s err=%v", out.Len(), offlineSmokePhase(t, err), err)
		}
		t.Logf("case=injected_request_error kind=injected reached=plan error=%q chain=%q", err, offlineSmokeChain(err))
	})

	t.Run("injected_count_error", func(t *testing.T) {
		labelled := errors.New("injected count failure")
		broken := NewCore(NewInferenceEngine(offlineSmokeEngine{Engine: engine, countErr: labelled}, build))
		var out bytes.Buffer
		_, err := broken.Text(ctx, []byte(offlineSmokeText), textadapter.New("en", "ja"), profile, &fakeTM{}, &out)
		if !errors.Is(err, labelled) || out.Len() != 0 || offlineSmokePhase(t, err) != "plan" {
			t.Fatalf("injected count error: bytes=%d phase=%s err=%v", out.Len(), offlineSmokePhase(t, err), err)
		}
		t.Logf("case=injected_count_error kind=injected reached=plan error=%q chain=%q", err, offlineSmokeChain(err))
	})

	t.Run("injected_generate_error", func(t *testing.T) {
		labelled := errors.New("injected generate failure")
		broken := NewCore(NewInferenceEngine(offlineSmokeEngine{Engine: engine, generateErr: labelled}, build))
		var out bytes.Buffer
		_, err := broken.Text(ctx, []byte(offlineSmokeText), textadapter.New("en", "ja"), profile, &fakeTM{}, &out)
		if !errors.Is(err, labelled) || out.Len() != 0 || offlineSmokePhase(t, err) != "generate" {
			t.Fatalf("injected generate error: bytes=%d phase=%s err=%v", out.Len(), offlineSmokePhase(t, err), err)
		}
		t.Logf("case=injected_generate_error kind=injected reached=generate error=%q chain=%q", err, offlineSmokeChain(err))
	})

	t.Run("cancel_during_generate", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		broken := NewCore(NewInferenceEngine(offlineSmokeEngine{Engine: engine, cancel: cancel}, build))
		var out bytes.Buffer
		_, err := broken.Text(cancelled, []byte(offlineSmokeText), textadapter.New("en", "ja"), profile, &fakeTM{}, &out)
		if !errors.Is(err, context.Canceled) || out.Len() != 0 || offlineSmokePhase(t, err) != "generate" {
			t.Fatalf("cancelled generation: bytes=%d phase=%s err=%v", out.Len(), offlineSmokePhase(t, err), err)
		}
		cancel()
		t.Logf("case=cancel_during_generate kind=injected_trigger_real_cancel reached=generate error=%q chain=%q", err, offlineSmokeChain(err))
	})

	t.Run("cancel_before_any_work", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		var out bytes.Buffer
		_, err := core.Text(cancelled, []byte(offlineSmokeText), textadapter.New("en", "ja"), profile, &fakeTM{}, &out)
		if !errors.Is(err, context.Canceled) || out.Len() != 0 || offlineSmokePhase(t, err) != "input" {
			t.Fatalf("cancelled context: bytes=%d phase=%s err=%v", out.Len(), offlineSmokePhase(t, err), err)
		}
		t.Logf("case=cancel_before_any_work kind=real reached=input error=%q chain=%q", err, offlineSmokeChain(err))
	})

	t.Run("close_real_and_injected_error", func(t *testing.T) {
		labelled := errors.New("injected close failure")
		if err := (offlineSmokeEngine{Engine: engine, closeErr: labelled}).Close(); !errors.Is(err, labelled) {
			t.Fatalf("injected close error was not surfaced: %v", err)
		}
		engineClosed = true
		if _, err := engine.Info(ctx); err == nil {
			t.Fatal("real engine is still open after Close")
		}
		t.Logf("case=close_real_and_injected_error kind=injected reached=close error=%q", labelled)
	})
}
