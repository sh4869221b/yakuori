package localize_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

func inferenceProfileFixture() (inference.ModelInfo, inference.PreparedRequest, localize.InferenceProfileInput) {
	info := inference.ModelInfo{
		ModelSHA256: [32]byte{1}, Backend: "cpu", BackendPin: "goinfer-v1", CUDABackendPin: "unused", ComputeQuant: "int4", ContextTokens: 8192,
		Tokenizer: inference.TokenizerIdentity{ModelSHA256: [32]byte{1}, BackendPin: "goinfer-v1"}, Template: inference.TemplateIdentity{Family: "chatml", Source: "embedded template", RendererVersion: "goinfer-v1"},
		PolicySchema: inference.PolicySchemaV1, Prompt: prompt.PlainIdentity(), Thinking: inference.ThinkingDisabled,
		Prefill: inference.PrefillPolicy{Path: "cpu", CPUFastAttention: true, Fast: "off"},
	}
	prepared := inference.PreparedRequest{RenderedPrompt: "source a", TokenIDs: []int{1, 2}, StopIDs: []int{42}, Identity: inference.RequestIdentity{
		ModelSHA256: info.ModelSHA256, Tokenizer: info.Tokenizer, Template: info.Template, PromptSchema: info.Prompt.Schema, Prompt: info.Prompt, Thinking: info.Thinking,
	}}
	return info, prepared, localize.InferenceProfileInput{ModelID: "model", Glossary: identity.GlossaryInput{Mode: "none"}, Limits: config.DefaultLimits(), Segmentation: true}
}

func profileRequest(t *testing.T, prepared inference.PreparedRequest, output int, timeout time.Duration) inference.GenerationRequest {
	t.Helper()
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, output, timeout)
	if err != nil {
		t.Fatal(err)
	}
	request, err := inference.NewGenerationRequest(prepared, policy)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestCanonicalInferenceProfileEffectiveConditions(t *testing.T) {
	info, prepared, input := inferenceProfileFixture()
	request := profileRequest(t, prepared, 128, time.Second)
	base, err := localize.NewCanonicalInferenceProfile(info, request, input)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*inference.ModelInfo, *inference.PreparedRequest, *localize.InferenceProfileInput){
		"model ID": func(_ *inference.ModelInfo, _ *inference.PreparedRequest, in *localize.InferenceProfileInput) {
			in.ModelID = "other"
		},
		"model bytes": func(i *inference.ModelInfo, p *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			i.ModelSHA256[0]++
			i.Tokenizer.ModelSHA256 = i.ModelSHA256
			p.Identity.ModelSHA256 = i.ModelSHA256
			p.Identity.Tokenizer = i.Tokenizer
		},
		"release": func(i *inference.ModelInfo, _ *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			i.BackendPin += "2"
		},
		"compute": func(i *inference.ModelInfo, _ *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			i.ComputeQuant = "int8"
		},
		"tokenizer": func(i *inference.ModelInfo, p *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			i.Tokenizer.BackendPin += "2"
			p.Identity.Tokenizer = i.Tokenizer
		},
		"template": func(i *inference.ModelInfo, p *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			i.Template.Source += "2"
			p.Identity.Template = i.Template
		},
		"Index variant": func(i *inference.ModelInfo, p *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			i.Prompt = prompt.IndexIdentity()
			p.Identity.Prompt = i.Prompt
		},
		"thinking prefix": func(i *inference.ModelInfo, p *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			i.DisabledThinkingPrefix = "<think>\n\n</think>\n\n"
			p.Identity.DisabledThinkingPrefix = i.DisabledThinkingPrefix
		},
		"CPU prefill": func(i *inference.ModelInfo, _ *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			i.Prefill.CPUFastAttention = false
		},
		"effective context": func(i *inference.ModelInfo, _ *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			i.ContextTokens = 3072
		},
		"Core context": func(_ *inference.ModelInfo, _ *inference.PreparedRequest, in *localize.InferenceProfileInput) {
			in.Limits.ContextTokens = 3072
		},
		"segmentation disabled": func(_ *inference.ModelInfo, _ *inference.PreparedRequest, in *localize.InferenceProfileInput) {
			in.Segmentation = false
		},
		"segmentation bytes": func(_ *inference.ModelInfo, _ *inference.PreparedRequest, in *localize.InferenceProfileInput) {
			in.Limits.UnitTextBytes++
		},
		"segmentation count": func(_ *inference.ModelInfo, _ *inference.PreparedRequest, in *localize.InferenceProfileInput) {
			in.Limits.Segments++
		},
		"glossary": func(_ *inference.ModelInfo, _ *inference.PreparedRequest, in *localize.InferenceProfileInput) {
			in.Glossary = identity.GlossaryInput{Mode: "provided", Schema: "v1", Entries: []byte{0, 255}}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			i, p, in := inferenceProfileFixture()
			change(&i, &p, &in)
			got, err := localize.NewCanonicalInferenceProfile(i, profileRequest(t, p, 128, time.Second), in)
			if err != nil || got == base {
				t.Fatalf("profile changed=%t error=%v", got != base, err)
			}
		})
	}
	for _, output := range []int{127, 129} {
		got, err := localize.NewCanonicalInferenceProfile(info, profileRequest(t, prepared, output, time.Second), input)
		if err != nil || got == base {
			t.Fatalf("output policy changed=%t error=%v", got != base, err)
		}
	}
	got, err := localize.NewCanonicalInferenceProfile(info, profileRequest(t, prepared, 128, 2*time.Second), input)
	if err != nil || got == base {
		t.Fatalf("timeout policy changed=%t error=%v", got != base, err)
	}
}

func TestCanonicalInferenceProfileUnusedConditions(t *testing.T) {
	info, prepared, input := inferenceProfileFixture()
	base, err := localize.NewCanonicalInferenceProfile(info, profileRequest(t, prepared, 128, time.Second), input)
	if err != nil {
		t.Fatal(err)
	}
	info.CUDABackendPin, info.RequestedBackend, info.ResidentDecline, info.KVPrecision = "other unused CUDA", "cuda", "diagnostic", "unused CUDA precision"
	info.ModelContextTokens, info.ConfiguredContextTokens, info.ResidentContextCap = 16384, 16384, 16384
	info.ContextTokens = 16384
	info.Prefill.Fast, info.Prefill.Floor, info.Prefill.Chunk = "both", 2048, 1024
	prepared.RenderedPrompt, prepared.TokenIDs = "different source/segment", []int{99}
	input.Limits.ArtifactBytes++
	input.Limits.GenerationTimeout++
	got, err := localize.NewCanonicalInferenceProfile(info, profileRequest(t, prepared, 128, time.Second), input)
	if err != nil || got != base {
		t.Fatalf("unused conditions changed profile=%t error=%v", got != base, err)
	}
}

func TestCanonicalInferenceProfileCUDA(t *testing.T) {
	info, prepared, input := inferenceProfileFixture()
	info.Backend, info.KVPrecision = "cuda", "f16"
	info.Prefill = inference.PrefillPolicy{Path: "cuda", Batched: true, BatchedCapability: true, Fast: "off", Chunk: 512}
	base, err := localize.NewCanonicalInferenceProfile(info, profileRequest(t, prepared, 128, time.Second), input)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*inference.ModelInfo){
		"KV":                 func(i *inference.ModelInfo) { i.KVPrecision = "f32" },
		"CUDA release":       func(i *inference.ModelInfo) { i.CUDABackendPin += "2" },
		"chunk without fast": func(i *inference.ModelInfo) { i.Prefill.Chunk++ },
		"static capability":  func(i *inference.ModelInfo) { i.Prefill.BatchedCapability = false },
	} {
		t.Run(name, func(t *testing.T) {
			i := info
			change(&i)
			got, err := localize.NewCanonicalInferenceProfile(i, profileRequest(t, prepared, 128, time.Second), input)
			if err != nil || got == base {
				t.Fatalf("profile changed=%t error=%v", got != base, err)
			}
		})
	}
}

func TestCanonicalInferenceProfileRejectsMismatch(t *testing.T) {
	for name, change := range map[string]func(*inference.ModelInfo, *inference.PreparedRequest, *localize.InferenceProfileInput){
		"model": func(_ *inference.ModelInfo, p *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			p.Identity.ModelSHA256[0]++
		},
		"tokenizer": func(_ *inference.ModelInfo, p *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			p.Identity.Tokenizer.BackendPin += "2"
		},
		"template": func(_ *inference.ModelInfo, p *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			p.Identity.Template.Source += "2"
		},
		"prompt": func(_ *inference.ModelInfo, p *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			p.Identity.Prompt = prompt.IndexIdentity()
		},
		"prefix": func(_ *inference.ModelInfo, p *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			p.Identity.DisabledThinkingPrefix = "changed"
		},
		"policy schema": func(i *inference.ModelInfo, _ *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			i.PolicySchema++
		},
		"unknown backend": func(i *inference.ModelInfo, _ *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			i.Backend = "other"
		},
		"missing CUDA precision": func(i *inference.ModelInfo, _ *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			i.Backend = "cuda"
			i.Prefill.Path = "cuda"
		},
		"unsupported context": func(_ *inference.ModelInfo, _ *inference.PreparedRequest, in *localize.InferenceProfileInput) {
			in.AdditionalContext = []byte("context")
		},
		"zero context cap": func(i *inference.ModelInfo, _ *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			i.ContextTokens = 0
		},
		"wrong prefill backend": func(i *inference.ModelInfo, _ *inference.PreparedRequest, _ *localize.InferenceProfileInput) {
			i.Prefill.Path = "cuda"
		},
	} {
		t.Run(name, func(t *testing.T) {
			info, prepared, input := inferenceProfileFixture()
			change(&info, &prepared, &input)
			_, err := localize.NewCanonicalInferenceProfile(info, profileRequest(t, prepared, 128, time.Second), input)
			if !errors.Is(err, localize.ErrInferenceProfile) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCanonicalInferenceProfileProjectionAndSnapshot(t *testing.T) {
	info, prepared, input := inferenceProfileFixture()
	input.Glossary = identity.GlossaryInput{Mode: "provided", Schema: "v1", Entries: []byte{0, 255}}
	request := profileRequest(t, prepared, 128, time.Second)
	profile, err := localize.NewCanonicalInferenceProfile(info, request, input)
	if err != nil {
		t.Fatal(err)
	}
	before := string(profile.CanonicalBytes())
	input.Glossary.Entries[0]++
	prepared.StopIDs[0]++
	effective, err := localize.EffectiveInferenceProfile(info, request, input.Limits, input.Segmentation)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := profile.CanonicalIdentity().MatchesEffective(effective); err != nil || !ok {
		t.Fatalf("match=%t error=%v", ok, err)
	}
	model, err := localize.EffectiveModelProfile(info, input.Limits, input.Segmentation)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := profile.CanonicalIdentity().MatchesModel(model); err != nil || !ok {
		t.Fatalf("model match=%t error=%v", ok, err)
	}
	if string(profile.CanonicalBytes()) != before {
		t.Fatal("canonical profile changed after caller mutation")
	}
}
