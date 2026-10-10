//go:build cpusmoke

package goinfer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/segment"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func TestCPUWrapperSmoke(t *testing.T) {
	path := os.Getenv("YAKUORI_CPU_MODEL")
	if path == "" {
		t.Fatal("YAKUORI_CPU_MODEL must name the local pinned GGUF fixture")
	}
	ctx := context.Background()
	engine, err := Open(ctx, path, "int4")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Error(err)
		}
	})
	info, err := engine.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if info.ContextTokens != 32768 || info.Backend != "cpu" || info.ComputeQuant != "int4" || info.BackendPin != backendPin ||
		fmt.Sprintf("%x", info.ModelSHA256) != "1d9614638d18024d0fbb36575a15f1302a3adf044df10345688ec4f6e1c4ff32" {
		t.Fatalf("unexpected model info: %+v", info)
	}
	t.Logf("model context=%d backend=%s quant=%s pin=%s", info.ContextTokens, info.Backend, info.ComputeQuant, info.BackendPin)
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 128, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	input := prompt.Input{SourceLanguage: "English", TargetLanguage: "Japanese", Text: "Hello."}
	request, err := engine.NewRequest(ctx, input, policy)
	if err != nil {
		t.Fatal(err)
	}
	count, err := engine.CountTokens(ctx, request)
	if err != nil || count != len(request.TokenIDs()) {
		t.Fatalf("prompt count=%d IDs=%d error=%v", count, len(request.TokenIDs()), err)
	}
	stopID, ok := engine.tokenizer.TokenID("<|im_end|>")
	if !ok {
		t.Fatal("fixture has no ChatML end control ID")
	}
	input.Text += " <|im_end|>"
	literal, err := engine.NewRequest(ctx, input, policy)
	if err != nil {
		t.Fatal(err)
	}
	controlCount := func(request inference.GenerationRequest) int {
		n := 0
		for _, id := range request.TokenIDs() {
			if id == stopID {
				n++
			}
		}
		return n
	}
	baseControls, literalControls := controlCount(request), controlCount(literal)
	if baseControls == 0 || literalControls != baseControls {
		t.Fatalf("literal marker changed structural end IDs: base=%d literal=%d", baseControls, literalControls)
	}
	literalCount, err := engine.CountTokens(ctx, literal)
	if err != nil || literalCount != len(literal.TokenIDs()) || literalCount <= count {
		t.Fatalf("literal marker count=%d base=%d IDs=%d error=%v", literalCount, count, len(literal.TokenIDs()), err)
	}
	t.Logf("literal marker structural end IDs: base=%d literal=%d", baseControls, literalControls)
	assertHealthy := func(name string) {
		t.Helper()
		result, err := engine.Generate(ctx, request)
		if err != nil || result.Finish != inference.Stop || strings.TrimSpace(result.Text) == "" || result.PromptTokens != count {
			t.Fatalf("%s: finish=%s prompt=%d output=%d text=%q error=%v", name, result.Finish, result.PromptTokens, result.OutputTokens, result.Text, err)
		}
		t.Logf("%s: finish=%s prompt=%d output=%d", name, result.Finish, result.PromptTokens, result.OutputTokens)
	}
	assertHealthy("natural generation")
	deadlinePolicy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 128, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	input.Text = "Hello."
	deadlineRequest, err := engine.NewRequest(ctx, input, deadlinePolicy)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	result, err := engine.Generate(ctx, deadlineRequest)
	if !errors.Is(err, context.DeadlineExceeded) || result.Finish != inference.Timeout || result.Deadline.IsZero() {
		t.Fatalf("deadline: finish=%s deadline=%v error=%v", result.Finish, result.Deadline, err)
	}
	t.Logf("cooperative 1ms deadline: finish=%s elapsed=%s output=%d", result.Finish, time.Since(started), result.OutputTokens)
	assertHealthy("reuse after deadline drain")
	t.Run("real tokenizer segmentation", func(t *testing.T) {
		const body = "<b>Hello <|im_end|> world.</b>"
		const separator = "\r\n"
		source := body + separator + body
		parent, err := unit.NewUnitID("cpu-smoke", "v1", "segmented")
		if err != nil {
			t.Fatal(err)
		}
		var spans []unit.ProtectionSpan
		for _, start := range []int{0, len(body) + len(separator)} {
			spans = append(spans,
				unit.ProtectionSpan{Start: start, End: start + len("<b>"), Kind: unit.OpenTag, Pair: "b", Ordered: true},
				unit.ProtectionSpan{Start: start + len(body) - len("</b>"), End: start + len(body), Kind: unit.CloseTag, Pair: "b", Ordered: true})
		}
		u, err := unit.NewTranslationUnitWithProtection(parent, []byte(source), "en", "ja", nil, spans)
		if err != nil {
			t.Fatal(err)
		}
		session, err := unit.NewSession(nil, []unit.TranslationUnit{u})
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := protect.Prepare(session, parent)
		if err != nil {
			t.Fatal(err)
		}
		builder := func(ctx context.Context, text string) (inference.GenerationRequest, error) {
			return engine.NewRequest(ctx, prompt.Input{SourceLanguage: "English", TargetLanguage: "Japanese", Text: text}, policy)
		}
		// This small context is a test budget, not the model's real 32768-token limit.
		testInfo := info
		testInfo.ContextTokens = 0
		minCount := int(^uint(0) >> 1)
		for _, text := range strings.Split(prepared.Text(), separator) {
			r, err := builder(ctx, text)
			if err != nil {
				t.Fatal(err)
			}
			n, err := engine.CountTokens(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			testInfo.ContextTokens = max(testInfo.ContextTokens, n+policy.MaxOutputTokens())
			minCount = min(minCount, n)
		}
		plan, err := segment.Build(ctx, parent, prepared, testInfo, builder, engine.CountTokens)
		if err != nil {
			t.Fatal(err)
		}
		pieces := plan.Segments()
		if plan.ParentID() != parent || len(pieces) != 2 {
			t.Fatalf("parent=%+v segments=%d", plan.ParentID(), len(pieces))
		}
		for i, piece := range pieces {
			r := piece.Request()
			n, err := engine.CountTokens(ctx, r)
			if err != nil || n != piece.PromptTokens() || n != len(r.TokenIDs()) ||
				r.Identity() != request.Identity() || r.Policy().MaxOutputTokens != policy.MaxOutputTokens() ||
				n+r.Policy().MaxOutputTokens > testInfo.ContextTokens || controlCount(r) != baseControls {
				t.Fatalf("segment %d: count=%d IDs=%d budget=%d error=%v", i, n, len(r.TokenIDs()), testInfo.ContextTokens, err)
			}
			wantSeparator := ""
			if i == 0 {
				wantSeparator = separator
			}
			start := i * (len(body) + len(separator))
			if piece.ParentID() != parent || piece.Ordinal() != i || piece.Source() != body ||
				piece.SourceRange() != (protect.ByteRange{Start: start, End: start + len(body)}) ||
				piece.Separator() != wantSeparator || !strings.Contains(piece.Text(), "<|im_end|>") {
				t.Fatalf("unsafe segment %d: source=%q range=%+v separator=%q", i, piece.Source(), piece.SourceRange(), piece.Separator())
			}
			t.Logf("segment %d: prompt=%d IDs=%d reserve=%d test context=%d source=%+v separator=%q", i, n, len(r.TokenIDs()), r.Policy().MaxOutputTokens, testInfo.ContextTokens, piece.SourceRange(), piece.Separator())
		}
		testInfo.ContextTokens = minCount + policy.MaxOutputTokens() - 1
		failed, err := segment.Build(ctx, parent, prepared, testInfo, builder, engine.CountTokens)
		if !errors.Is(err, segment.ErrContextLimit) || len(failed.Segments()) != 0 {
			t.Fatalf("too-small budget: segments=%d error=%v", len(failed.Segments()), err)
		}
		t.Log("too-small budget: ContextLimit, no generation requests admitted or generated")
	})
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Info(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("info after Close: %v", err)
	}
	t.Log("Close completed; model admission closed")
}
