package goinfer

import (
	"context"
	"fmt"
	"strings"

	"github.com/townsendmerino/goinfer/chat"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

func (e *Engine) NewRequest(ctx context.Context, input prompt.Input, policy inference.GenerationPolicy) (inference.GenerationRequest, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return inference.GenerationRequest{}, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return inference.GenerationRequest{}, err
	}
	if policy.Schema() != inference.PolicySchemaV1 {
		return inference.GenerationRequest{}, inference.ErrInvalidPolicy
	}
	messages := prompt.Build(input)
	segments := e.template.RenderSegments(messages.System, []chat.Turn{{Role: "user", Content: messages.User}})
	ids, err := e.tokenizer.EncodeSegments(segments, false)
	if err != nil {
		return inference.GenerationRequest{}, fmt.Errorf("encode request: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return inference.GenerationRequest{}, err
	}
	var rendered strings.Builder
	spans := make([]inference.PromptSpan, len(segments))
	for i, segment := range segments {
		rendered.WriteString(segment.Text)
		spans[i] = inference.PromptSpan{Text: segment.Text, Special: segment.Special}
	}
	return inference.NewGenerationRequest(inference.PreparedRequest{
		RenderedPrompt: rendered.String(), Spans: spans, TokenIDs: ids,
		Identity: e.identity(), StopIDs: e.stopIDs,
	}, policy)
}

func (e *Engine) CountTokens(ctx context.Context, request inference.GenerationRequest) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return 0, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if request.Identity() != e.identity() {
		return 0, ErrForeignRequest
	}
	return len(request.TokenIDs()), nil
}
