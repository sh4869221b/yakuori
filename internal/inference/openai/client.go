// Package openai implements a bounded, stateless Responses API transport.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

var ErrAPI = errors.New("openai request failed")

const origin = "https://api.openai.com/v1"
const maxResponseBytes = 4 << 20

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Request struct {
	Model           string        `json:"model"`
	Input           []Message     `json:"input"`
	MaxOutputTokens int           `json:"max_output_tokens,omitempty"`
	Store           bool          `json:"store"`
	Timeout         time.Duration `json:"-"`
}
type Client struct {
	key  string
	http *http.Client
}

// New fixes the API origin and refuses redirects so credentials cannot be forwarded.
func New(key string) (*Client, error) {
	if key == "" {
		return nil, ErrAPI
	}
	return &Client{key: key, http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) post(ctx context.Context, path string, in any, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return ErrAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+path, bytes.NewReader(body))
	if err != nil {
		return ErrAPI
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrAPI
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ErrAPI
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrAPI
	}
	if len(b) > maxResponseBytes {
		return ErrAPI
	}
	if json.Unmarshal(b, out) != nil {
		return ErrAPI
	}
	return nil
}
func (c *Client) Count(ctx context.Context, request Request) (int, error) {
	if request.Timeout <= 0 {
		return 0, ErrAPI
	}
	ctx, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()
	var response struct {
		Object      string `json:"object"`
		InputTokens int    `json:"input_tokens"`
	}
	if err := c.post(ctx, "/responses/input_tokens", struct {
		Model string    `json:"model"`
		Input []Message `json:"input"`
	}{request.Model, request.Input}, &response); err != nil {
		return 0, err
	}
	if response.Object != "response.input_tokens" || response.InputTokens <= 0 {
		return 0, ErrAPI
	}
	return response.InputTokens, nil
}

type Response struct {
	Text                      string
	InputTokens, OutputTokens int
}

func (c *Client) Generate(ctx context.Context, request Request) (Response, error) {
	if request.Timeout <= 0 {
		return Response{}, ErrAPI
	}
	ctx, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()
	var response struct {
		Status string `json:"status"`
		Model  string `json:"model"`
		Usage  *struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
		Output []struct {
			Type    string  `json:"type"`
			Phase   *string `json:"phase"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	request.Store = false
	if err := c.post(ctx, "/responses", request, &response); err != nil {
		return Response{}, err
	}
	if response.Status != "completed" || response.Model != request.Model || response.Usage == nil || response.Usage.Input <= 0 || response.Usage.Output <= 0 {
		return Response{}, ErrAPI
	}
	// A phased response needs an explicit final answer. Older responses omit
	// phase (or send null), so only fall back when all messages are unphased.
	phased := false
	for _, item := range response.Output {
		if item.Type != "message" || item.Phase == nil {
			continue
		}
		switch *item.Phase {
		case "commentary", "final_answer":
			phased = true
		default:
			return Response{}, ErrAPI
		}
	}
	var text strings.Builder
	for _, item := range response.Output {
		if item.Type != "message" {
			continue
		}
		answer := !phased || (item.Phase != nil && *item.Phase == "final_answer")
		for _, part := range item.Content {
			if part.Type != "output_text" {
				return Response{}, ErrAPI
			}
			if answer {
				text.WriteString(part.Text)
			}
		}
	}
	if text.Len() == 0 {
		return Response{}, ErrAPI
	}
	return Response{Text: text.String(), InputTokens: response.Usage.Input, OutputTokens: response.Usage.Output}, nil
}
