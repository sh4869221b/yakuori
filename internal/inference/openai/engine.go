package openai

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sync"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

var snapshotName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*-[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

type Engine struct {
	client *Client
	info   inference.ModelInfo
	mu     sync.Mutex
	closed bool
}

func NewEngine(model, key string) (*Engine, error) {
	if !snapshotName.MatchString(model) {
		return nil, ErrAPI
	}
	client, err := New(key)
	if err != nil {
		return nil, err
	}
	// Bump the profile pin so previously saved commentary cannot be reused from TM.
	return &Engine{client: client, info: inference.ModelInfo{RemoteModel: model, Backend: "openai", RequestedBackend: "openai", BackendPin: "responses-v2", ComputeQuant: "provider-managed", KVPrecision: "not-applicable", Tokenizer: inference.TokenizerIdentity{BackendPin: "openai-server"}, Prefill: inference.PrefillPolicy{Path: "none"}, Thinking: "provider-default", ContextTokens: config.DefaultLimits().ContextTokens, PolicySchema: inference.PolicySchemaV1, Prompt: prompt.PlainIdentity(), Template: inference.TemplateIdentity{Family: "openai-responses", Source: "openai", RendererVersion: "v1"}}}, nil
}
func (e *Engine) Info(ctx context.Context) (inference.ModelInfo, error) {
	if err := ctx.Err(); err != nil {
		return inference.ModelInfo{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return inference.ModelInfo{}, ErrAPI
	}
	return e.info, nil
}
func (e *Engine) Close() error { e.mu.Lock(); defer e.mu.Unlock(); e.closed = true; return nil }
func (e *Engine) NewRequest(ctx context.Context, input prompt.Input, policy inference.GenerationPolicy) (inference.GenerationRequest, error) {
	if _, err := e.Info(ctx); err != nil {
		return inference.GenerationRequest{}, err
	}
	messages := prompt.Build(input)
	data, err := json.Marshal([]Message{{Role: "system", Content: messages.System}, {Role: "user", Content: messages.User}})
	if err != nil {
		return inference.GenerationRequest{}, ErrAPI
	}
	return inference.NewGenerationRequest(inference.PreparedRequest{RenderedPrompt: string(data), Identity: inference.RequestIdentity{RemoteModel: e.info.RemoteModel, Template: e.info.Template, PromptSchema: prompt.SchemaV1, Prompt: e.info.Prompt, Thinking: e.info.Thinking, Tokenizer: e.info.Tokenizer}, Terms: input.Terms}, policy)
}
func (e *Engine) request(ctx context.Context, r inference.GenerationRequest) (Request, error) {
	if _, err := e.Info(ctx); err != nil {
		return Request{}, err
	}
	id, p := r.Identity(), r.Policy()
	if id.RemoteModel != e.info.RemoteModel || id.Template != e.info.Template || id.Prompt != e.info.Prompt || p.Schema != inference.PolicySchemaV1 || p.SamplingMode != "provider-default" {
		return Request{}, ErrAPI
	}
	var input []Message
	if json.Unmarshal([]byte(r.RenderedPrompt()), &input) != nil || len(input) != 2 || input[0].Role != "system" || input[1].Role != "user" {
		return Request{}, ErrAPI
	}
	return Request{Model: e.info.RemoteModel, Input: input, MaxOutputTokens: p.MaxOutputTokens, Timeout: p.RequestTimeout}, nil
}
func (e *Engine) CountTokens(ctx context.Context, r inference.GenerationRequest) (int, error) {
	q, err := e.request(ctx, r)
	if err != nil {
		return 0, err
	}
	return e.client.Count(ctx, q)
}
func (e *Engine) Generate(ctx context.Context, r inference.GenerationRequest) (inference.GenerationResult, error) {
	q, err := e.request(ctx, r)
	if err != nil {
		return failure(r, err)
	}
	response, err := e.client.Generate(ctx, q)
	if err != nil {
		return failure(r, err)
	}
	return inference.GenerationResult{Text: response.Text, Finish: inference.Stop, PromptTokens: response.InputTokens, OutputTokens: response.OutputTokens, Policy: r.Policy()}, nil
}

func failure(r inference.GenerationRequest, err error) (inference.GenerationResult, error) {
	result := inference.GenerationResult{Finish: inference.DecodeError, Policy: r.Policy()}
	if errors.Is(err, context.Canceled) {
		result.Finish = inference.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		result.Finish = inference.Timeout
	}
	return result, err
}
