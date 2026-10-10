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
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Info(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("info after Close: %v", err)
	}
	t.Log("Close completed; model admission closed")
}
