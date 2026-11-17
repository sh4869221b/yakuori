package openai

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
)

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
	run := func(model string) (string, error) {
		e, err := NewEngine(model, "test-key")
		if err != nil {
			return "", err
		}
		e.client.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
			body := `{"object":"response.input_tokens","input_tokens":7}`
			if r.URL.Path == "/v1/responses" {
				calls++
				count := "7"
				if wrong {
					count = "8"
				}
				body = `{"status":"completed","model":"` + model + `","usage":{"input_tokens":` + count + `,"output_tokens":2},"output":[{"type":"message","content":[{"type":"output_text","text":"translated"}]}]}`
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
	if out, err := run(model); err != nil || out != "translated" || calls != 1 {
		t.Fatalf("first run failed: %v", err)
	}
	if out, err := run(model); err != nil || out != "translated" || calls != 1 {
		t.Fatalf("cache not reused: %v", err)
	}
	if out, err := run("gpt-4o-2024-11-20"); err != nil || out != "translated" || calls != 2 {
		t.Fatalf("snapshot isolation failed: %v", err)
	}
	wrong = true
	if out, err := run("gpt-4o-2025-01-01"); err == nil || out != "" {
		t.Fatal("usage mismatch was published")
	}
}
