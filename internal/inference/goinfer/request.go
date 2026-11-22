package goinfer

import (
	"context"
	"fmt"
	"strings"

	"github.com/townsendmerino/goinfer/chat"
	"github.com/townsendmerino/goinfer/tokenizer"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

const (
	thinkingOpen           = "<think>"
	thinkingClose          = "</think>"
	thinkingSeparator      = "\n\n"
	disabledThinkingPrefix = thinkingOpen + thinkingSeparator + thinkingClose + thinkingSeparator
)

func (e *Engine) NewRequest(ctx context.Context, input prompt.Input, policy inference.GenerationPolicy) (inference.GenerationRequest, error) {
	return e.prepareRequest(ctx, inference.PreparedRequest{Identity: e.identity(), StopIDs: e.stopIDs, Terms: input.Terms}, policy, func() ([]tokenizer.Segment, error) {
		if e.info.Template.Family == plamo3Family {
			return plamo3Segments(input)
		}
		messages := prompt.Build(input)
		if e.info.Template.Source == indexChatTemplate {
			messages = prompt.BuildIndex(input)
			messages.System = strings.TrimSpace(messages.System)
			messages.User = strings.TrimSpace(messages.User)
		}
		return e.chatSegments(messages.System, messages.User), nil
	})
}

// NewContextRequest renders the separate Context Pack extraction prompt using
// the local ChatML template and its literal user turn.
func (e *Engine) NewContextRequest(ctx context.Context, data string, policy inference.GenerationPolicy) (inference.GenerationRequest, error) {
	return e.prepareRequest(ctx, inference.PreparedRequest{Identity: e.contextIdentity(), StopIDs: e.stopIDs}, policy, func() ([]tokenizer.Segment, error) {
		if e.info.Template.Family != "chatml" {
			return nil, ErrUnknownTemplate
		}
		task := prompt.ContextIdentity()
		return e.chatSegments(task.SystemTemplate, fmt.Sprintf(task.UserTemplate, data)), nil
	})
}

func (e *Engine) chatSegments(system, user string) []tokenizer.Segment {
	segments := e.template.RenderSegments(system, []chat.Turn{{Role: "user", Content: user}})
	if e.info.Template.Source == indexChatTemplate {
		// The official disabled-thinking generation prefix closes an empty block.
		segments = append(segments,
			tokenizer.Segment{Text: thinkingOpen, Special: true},
			tokenizer.Segment{Text: thinkingSeparator},
			tokenizer.Segment{Text: thinkingClose, Special: true},
			tokenizer.Segment{Text: thinkingSeparator})
	}
	return segments
}

func (e *Engine) prepareRequest(ctx context.Context, prepared inference.PreparedRequest, policy inference.GenerationPolicy, render func() ([]tokenizer.Segment, error)) (inference.GenerationRequest, error) {
	e.mu.Lock()
	closing := e.closing
	e.mu.Unlock()
	if closing {
		return inference.GenerationRequest{}, ErrClosed
	}
	e.tokenizerMu.Lock()
	defer e.tokenizerMu.Unlock()
	e.mu.Lock()
	closing = e.closing
	e.mu.Unlock()
	if closing {
		return inference.GenerationRequest{}, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return inference.GenerationRequest{}, err
	}
	if policy.Schema() != inference.PolicySchemaV1 {
		return inference.GenerationRequest{}, inference.ErrInvalidPolicy
	}
	segments, err := render()
	if err != nil {
		return inference.GenerationRequest{}, err
	}
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
	prepared.RenderedPrompt, prepared.Spans, prepared.TokenIDs = rendered.String(), spans, ids
	return inference.NewGenerationRequest(prepared, policy)
}

func (e *Engine) CountTokens(ctx context.Context, request inference.GenerationRequest) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing {
		return 0, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !e.acceptsIdentity(request.Identity()) {
		return 0, ErrForeignRequest
	}
	return len(request.TokenIDs()), nil
}

func (e *Engine) contextIdentity() inference.RequestIdentity {
	id := e.identity()
	id.Prompt = prompt.ContextIdentity()
	id.PromptSchema = id.Prompt.Schema
	return id
}

func (e *Engine) acceptsIdentity(id inference.RequestIdentity) bool {
	return id == e.identity() || e.info.Template.Family == "chatml" && id == e.contextIdentity()
}
