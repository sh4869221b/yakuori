package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/contextselection"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func TestEngineRejectsResolvedContextTranslation(t *testing.T) {
	e, err := NewEngine("gpt-4o-2024-08-06", "test-key")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.client.http.Transport = transport(func(*http.Request) (*http.Response, error) {
		t.Fatal("resolved context reached the remote API")
		return nil, nil
	})
	selection, err := contextselection.New([]contextselection.Item{{Category: contextselection.World, Text: "Snow."}})
	if err != nil {
		t.Fatal(err)
	}
	limits := config.DefaultLimits()
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, limits.MaxOutputTokens, limits.RequestTimeout)
	if err != nil {
		t.Fatal(err)
	}
	input := prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: "Hello", ResolvedContext: selection}
	if _, err = e.NewRequest(t.Context(), input, policy); !errors.Is(err, ErrAPI) {
		t.Fatalf("NewRequest accepted context: %v", err)
	}
	input.ResolvedContext = contextselection.ResolvedContext{}
	plain, err := e.NewRequest(t.Context(), input, policy)
	if err != nil {
		t.Fatal(err)
	}
	identity := plain.Identity()
	identity.Prompt, _ = prompt.ResolvedContextIdentity(identity.Prompt)
	for _, prepared := range []inference.PreparedRequest{
		{Identity: plain.Identity(), ResolvedContext: selection},
		{Identity: identity},
	} {
		prepared.RenderedPrompt = plain.RenderedPrompt()
		r, err := inference.NewGenerationRequest(prepared, policy)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.CountTokens(t.Context(), r); !errors.Is(err, ErrAPI) {
			t.Fatalf("CountTokens accepted context: %v", err)
		}
		if _, err := e.Generate(t.Context(), r); !errors.Is(err, ErrAPI) {
			t.Fatalf("Generate accepted context: %v", err)
		}
	}
}

func TestEngineProfileAndGeneration(t *testing.T) {
	e, err := NewEngine("gpt-4o-2024-08-06", "test-key")
	if err != nil {
		t.Fatal(err)
	}
	e.client.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		body := `{"object":"response.input_tokens","input_tokens":7}`
		if r.URL.Path == "/v1/responses" {
			body = `{"status":"completed","model":"gpt-4o-2024-08-06","usage":{"input_tokens":7,"output_tokens":2},"output":[{"type":"message","content":[{"type":"output_text","text":"translated"}]}]}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	ctx := context.Background()
	limits := config.DefaultLimits()
	p, _ := inference.NewGenerationPolicy(inference.PolicySchemaV1, limits.MaxOutputTokens, limits.RequestTimeout)
	request, err := e.NewRequest(ctx, prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: "private"}, p)
	if err != nil {
		t.Fatal(err)
	}
	info, err := e.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := localize.NewCanonicalInferenceProfile(info, request, localize.InferenceProfileInput{ModelID: info.RemoteModel, Glossary: identity.GlossaryInput{Mode: "none"}, Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Digest() == ([32]byte{}) {
		t.Fatal("missing profile")
	}
	n, err := e.CountTokens(ctx, request)
	if err != nil || n != 7 {
		t.Fatal("count failed")
	}
	out, err := e.Generate(ctx, request)
	if err != nil || out.PromptTokens != n || out.Text != "translated" {
		t.Fatal("generation failed")
	}
	if request.Policy().SeedFixed || request.Policy().SamplingMode != "provider-default" {
		t.Fatal("incorrect policy")
	}
}
func TestModelMustBeSnapshot(t *testing.T) {
	if _, err := NewEngine("gpt-4o", "test-key"); err == nil {
		t.Fatal("accepted mutable alias")
	}
}

func TestEngineCancellationFinish(t *testing.T) {
	e, _ := NewEngine("gpt-4o-2024-08-06", "test-key")
	ctx, cancel := context.WithCancel(context.Background())
	e.client.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: cancelReader{cancel: cancel}, Request: r}, nil
	})
	p, _ := inference.NewGenerationPolicy(inference.PolicySchemaV1, 10, config.DefaultLimits().RequestTimeout)
	request, err := e.NewRequest(ctx, prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: "source"}, p)
	if err != nil {
		t.Fatal(err)
	}
	result, err := e.Generate(ctx, request)
	if err != context.Canceled || result.Finish != inference.Canceled {
		t.Fatal("cancellation classification lost")
	}
}

func TestSegmentedRemoteTokenCounts(t *testing.T) {
	const model = "gpt-4o-2024-08-06"
	e, err := NewEngine(model, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	e.info.ContextTokens = 10
	var calls []string
	var mismatch bool
	e.client.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		first, second := bytes.Contains(data, []byte("one.")), bytes.Contains(data, []byte("two!"))
		n, translated := 8, "一。"
		if first && second {
			n = 9
		} else if second {
			translated = "二！"
		}
		calls = append(calls, r.URL.Path)
		body := fmt.Sprintf(`{"object":"response.input_tokens","input_tokens":%d}`, n)
		if r.URL.Path == "/v1/responses" {
			if first == second {
				t.Fatalf("expected exactly one segment in generation request")
			}
			if mismatch {
				n++
			}
			body = fmt.Sprintf(`{"status":"completed","model":%q,"usage":{"input_tokens":%d,"output_tokens":2},"output":[{"type":"message","content":[{"type":"output_text","text":%q}]}]}`, model, n, translated)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	ctx := context.Background()
	limits := config.DefaultLimits()
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 2, limits.RequestTimeout)
	if err != nil {
		t.Fatal(err)
	}
	build := func(ctx context.Context, _ unit.UnitID, text string) (inference.GenerationRequest, error) {
		return e.NewRequest(ctx, prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: text}, policy)
	}
	req, err := build(ctx, unit.UnitID{}, "")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := localize.NewCanonicalInferenceProfile(e.info, req, localize.InferenceProfileInput{ModelID: model, Glossary: identity.GlossaryInput{Mode: "none"}, Limits: limits, Segmentation: true})
	if err != nil {
		t.Fatal(err)
	}
	core := localize.NewSegmentedCore(e, build)
	store, err := tm.Create(ctx, filepath.Join(t.TempDir(), "tm.sqlite"), limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope := tm.Scope{GameNamespace: "test", ArtifactNamespace: "segmented"}
	adapter := textadapter.New("en", "ja")
	var out bytes.Buffer
	_, err = core.TextWithTM(ctx, []byte("one.\ntwo!"), adapter, profile, store, scope, &out)
	if err != nil || out.String() != "一。\n二！" {
		t.Fatalf("segmented output=%q, error=%v", out.String(), err)
	}
	if got := strings.Join(calls, " "); got != "/v1/responses/input_tokens /v1/responses/input_tokens /v1/responses/input_tokens /v1/responses /v1/responses" {
		t.Fatalf("planning/generation order: %s", got)
	}
	mismatch = true
	scope.ArtifactNamespace = "mismatched-usage"
	out.Reset()
	if _, err := core.TextWithTM(ctx, []byte("one.\ntwo!"), adapter, profile, store, scope, &out); err == nil || out.Len() != 0 {
		t.Fatalf("generation usage mismatch published: %q, error=%v", out.String(), err)
	}
}

func TestRemoteTMAndUsageMismatch(t *testing.T) {
	ctx := context.Background()
	limits := config.DefaultLimits()
	store, err := tm.Create(ctx, filepath.Join(t.TempDir(), "tm.sqlite"), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	calls := 0
	wrong := false
	previousProfile := false
	run := func(model string) (string, error) {
		e, err := NewEngine(model, "test-key")
		if err != nil {
			return "", err
		}
		defer e.Close()
		if previousProfile {
			e.info.BackendPin = "responses-v1"
		}
		e.client.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
			body := `{"object":"response.input_tokens","input_tokens":7}`
			if r.URL.Path == "/v1/responses" {
				calls++
				count := "7"
				if wrong {
					count = "8"
				}
				text := "translated"
				if previousProfile {
					text = "Preparing translation. translated"
				}
				body = `{"status":"completed","model":"` + model + `","usage":{"input_tokens":` + count + `,"output_tokens":2},"output":[{"type":"message","content":[{"type":"output_text","text":"` + text + `"}]}]}`
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})
		p, _ := inference.NewGenerationPolicy(inference.PolicySchemaV1, limits.MaxOutputTokens, limits.RequestTimeout)
		build := func(ctx context.Context, _ unit.UnitID, text string) (inference.GenerationRequest, error) {
			return e.NewRequest(ctx, prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: text}, p)
		}
		req, err := build(ctx, unit.UnitID{}, "")
		if err != nil {
			return "", err
		}
		info, err := e.Info(ctx)
		if err != nil {
			return "", err
		}
		profile, err := localize.NewCanonicalInferenceProfile(info, req, localize.InferenceProfileInput{ModelID: model, Glossary: identity.GlossaryInput{Mode: "none"}, Limits: limits})
		if err != nil {
			return "", err
		}
		var out bytes.Buffer
		_, err = localize.NewCore(localize.NewInferenceEngine(e, build)).TextWithTM(ctx, []byte("source"), textadapter.New("en", "ja"), profile, store, tm.Scope{GameNamespace: "test", ArtifactNamespace: "test"}, &out)
		return out.String(), err
	}
	model := "gpt-4o-2024-08-06"
	previousProfile = true
	if out, err := run(model); err != nil || out != "Preparing translation. translated" || calls != 1 {
		t.Fatalf("seed previous profile: output=%q, calls=%d, error=%v", out, calls, err)
	}
	previousProfile = false
	if out, err := run(model); err != nil || out != "translated" || calls != 2 {
		t.Fatalf("previous profile reused: output=%q, calls=%d, error=%v", out, calls, err)
	}
	if out, err := run(model); err != nil || out != "translated" || calls != 2 {
		t.Fatalf("cache not reused: %v", err)
	}
	if out, err := run("gpt-4o-2024-11-20"); err != nil || out != "translated" || calls != 3 {
		t.Fatalf("snapshot isolation failed: %v", err)
	}
	wrong = true
	if out, err := run("gpt-4o-2025-01-01"); err == nil || out != "" {
		t.Fatal("usage mismatch was published")
	}
}
