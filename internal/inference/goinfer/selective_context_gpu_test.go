//go:build gpu && cgo && linux && amd64

package goinfer

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/townsendmerino/goinfer/tokenizer"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

func TestSelectiveContextGPU(t *testing.T) {
	model := os.Getenv("YAKUORI_CONTEXT_GPU_MODEL")
	if model == "" {
		t.Skip("set YAKUORI_CONTEXT_GPU_MODEL to the pinned Index-Translate-2B Q4_K_M GGUF")
	}
	const reuseKnob = "GOINFER_NO_RESIDENT_REUSE"
	t.Setenv(reuseKnob, "1")
	var expectedSHA [32]byte
	const modelSHA = "044b313d29342bd3b2c77cbb64023ca0d209bd9b3247763f9d162767ef2d746a"
	if n, err := hex.Decode(expectedSHA[:], []byte(modelSHA)); err != nil || n != len(expectedSHA) {
		t.Fatalf("invalid model pin: decoded %d bytes: %v", n, err)
	}
	ctx := t.Context()
	engine, err := OpenWithOptions(ctx, model, Options{
		Backend: "webgpu", ComputeQuant: "int4", ContextTokens: 4096, ExpectedModelSHA256: &expectedSHA,
	})
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
	if info.Backend != "webgpu" || !info.ResidentActive || info.ComputeQuant != "int4" ||
		info.KVPrecision != "f32" || info.ContextTokens != 4096 || info.Template.Source != indexChatTemplate {
		t.Fatalf("unexpected model path: backend=%s resident=%t quant=%s KV=%s context=%d template=%s",
			info.Backend, info.ResidentActive, info.ComputeQuant, info.KVPrecision, info.ContextTokens, info.Prompt.Variant)
	}
	if value, ok := engine.model.Knob(reuseKnob); !ok || value != "1" {
		t.Fatalf("model did not disable resident KV reuse: %s=%q", reuseKnob, value)
	}
	t.Logf("model=%x backend=%s path=%s GPU=%q driver=%q pin=%q WebGPU pin=%q context=%d KV=%s prefill=%+v resident_reuse=off",
		info.ModelSHA256, info.Backend, info.DecodePath, info.GPUName, info.GPUDriver,
		info.BackendPin, info.WebGPUBackendPin, info.ContextTokens, info.KVPrecision, info.Prefill)

	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 128, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	warmup, err := engine.NewRequest(ctx, prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: "The library is open."}, policy)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("policy=%+v", warmup.Policy())
	result, err := engine.Generate(ctx, warmup)
	if err != nil || result.Finish != inference.Stop || strings.TrimSpace(result.Text) == "" {
		t.Fatalf("warmup: finish=%s text=%q error=%v", result.Finish, result.Text, err)
	}

	fixtures := []struct {
		id, source, reference string
	}{
		{"speaker-formal", "I will handle it.", "The speaker is a newly assigned interpreter addressing a visiting diplomat at their first official meeting."},
		{"speaker-familiar", "I will handle it.", "The speaker is talking privately to a longtime teammate they joke with every day."},
		{"scene-magic", "The seal is broken.", "The party stands outside a cursed vault. A spell cast centuries ago kept the door shut."},
		{"scene-letter", "The seal is broken.", "The letter was delivered in an envelope. The red wax impression on the flap has a crack."},
		{"neighbor-sports", "Keep it away from the bat.", "Previous line: Leave the helmet by home plate.\nNext line: Put the other thing with the gloves and the baseballs."},
		{"neighbor-animal", "Keep it away from the bat.", "Previous line: The rescued animal is hanging upside down in the pen.\nNext line: The vet will examine its wings next."},
	}
	for round := range 2 {
		for i, fixture := range fixtures {
			input := prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: fixture.source}
			a, err := engine.NewRequest(ctx, input, policy)
			if err != nil {
				t.Fatalf("%s A: %v", fixture.id, err)
			}
			b := selectiveContextRequest(t, engine, input, policy, a, fixture.reference)
			requests := []inference.GenerationRequest{a, b}
			labels := []string{"A", "B"}
			if (i+round)%2 != 0 {
				requests[0], requests[1] = requests[1], requests[0]
				labels[0], labels[1] = labels[1], labels[0]
			}
			for position, request := range requests {
				count, countErr := engine.CountTokens(ctx, request)
				start := time.Now()
				result, err := engine.Generate(ctx, request)
				elapsed := time.Since(start)
				t.Logf("round=%d fixture=%s position=%d variant=%s prompt=%d counted=%d output=%d elapsed=%s finish=%s text=%q error=%v",
					round+1, fixture.id, position+1, labels[position], result.PromptTokens, count, result.OutputTokens, elapsed, result.Finish, result.Text, err)
				if countErr != nil || count <= 0 || count != result.PromptTokens {
					t.Errorf("%s %s: inconsistent prompt token count: %v", fixture.id, labels[position], countErr)
				}
				if err != nil || result.Finish != inference.Stop || strings.TrimSpace(result.Text) == "" {
					t.Errorf("%s %s: did not produce a nonempty stopped translation", fixture.id, labels[position])
				}
			}
		}
	}
}

func selectiveContextRequest(t *testing.T, engine *Engine, input prompt.Input, policy inference.GenerationPolicy, base inference.GenerationRequest, reference string) inference.GenerationRequest {
	t.Helper()
	const sourceHeader = "\n\n【源文】\n"
	user := "user\n" + strings.TrimSpace(prompt.BuildIndex(input).User)
	if strings.Count(user, sourceHeader) != 1 {
		t.Fatal("expected one Index source block")
	}
	contextBlock := "\n\n【参考信息】\n以下内容仅供参考，只翻译源文，不要翻译参考信息。\n" + reference
	spans := base.Spans()
	segments := make([]tokenizer.Segment, len(spans))
	var rendered strings.Builder
	matched := 0
	for i, span := range spans {
		if !span.Special && span.Text == user {
			span.Text = strings.Replace(span.Text, sourceHeader, contextBlock+sourceHeader, 1)
			spans[i] = span
			matched++
		}
		rendered.WriteString(span.Text)
		segments[i] = tokenizer.Segment{Text: span.Text, Special: span.Special}
	}
	if matched != 1 {
		t.Fatalf("expected one Index user span, found %d", matched)
	}
	ids, err := engine.tokenizer.EncodeSegments(segments, false)
	if err != nil {
		t.Fatal(err)
	}
	// The base identity admits this experiment to the same engine. It is not a TM profile.
	request, err := inference.NewGenerationRequest(inference.PreparedRequest{
		RenderedPrompt: rendered.String(), Spans: spans, TokenIDs: ids,
		Identity: base.Identity(), StopIDs: base.Policy().StopIDs, Terms: base.Terms(),
	}, policy)
	if err != nil {
		t.Fatal(err)
	}
	return request
}
