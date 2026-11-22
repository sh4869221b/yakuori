package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestClient(t *testing.T) {
	client, err := New("test-key")
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.openai.com" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatal("incorrect request destination or authorization")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal("invalid request JSON")
		}
		if payload["model"] != "snapshot" || payload["input"] == nil {
			t.Fatal("missing request parameters")
		}
		var body string
		switch r.URL.Path {
		case "/v1/responses/input_tokens":
			body = `{"object":"response.input_tokens","input_tokens":4}`
		case "/v1/responses":
			if payload["store"] != false || payload["max_output_tokens"] != float64(5) {
				t.Fatal("invalid generation policy")
			}
			body = `{"status":"completed","model":"snapshot","usage":{"input_tokens":4,"output_tokens":3},"output":[{"type":"message","content":[{"type":"output_text","text":"translation"}]}]}`
		default:
			t.Fatal("unexpected endpoint")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	req := Request{Model: "snapshot", Input: []Message{{Role: "user", Content: "private"}}, MaxOutputTokens: 5, Timeout: time.Second}
	n, err := client.Count(context.Background(), req)
	if err != nil || n != 4 {
		t.Fatalf("count = %d, err = %v", n, err)
	}
	result, err := client.Generate(context.Background(), req)
	if err != nil || result.Text != "translation" || result.InputTokens != 4 {
		t.Fatal("invalid response")
	}
}
func TestRefusalFailsClosed(t *testing.T) {
	client, _ := New("test-key")
	client.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"status":"completed","model":"snapshot","usage":{"input_tokens":4,"output_tokens":3},"output":[{"type":"message","content":[{"type":"refusal","refusal":"secret"}]}]}`)), Request: r}, nil
	})
	if _, err := client.Generate(context.Background(), Request{Model: "snapshot", Timeout: time.Second}); err != ErrAPI {
		t.Fatal("refusal accepted")
	}
}

func TestResponsePhases(t *testing.T) {
	const commentary = `{"type":"message","phase":"commentary","content":[{"type":"output_text","text":"Preparing translation."}]}`
	const final = `{"type":"message","phase":"final_answer","content":[{"type":"output_text","text":"こん"},{"type":"output_text","text":"にち"}]}`
	const finalTail = `{"type":"message","phase":"final_answer","content":[{"type":"output_text","text":"は"}]}`
	const legacy = `{"type":"message","content":[{"type":"output_text","text":"旧"}]}`
	const legacyTail = `{"type":"message","phase":null,"content":[{"type":"output_text","text":"式"}]}`
	for _, tc := range []struct{ name, output, want string }{
		{"final", commentary + "," + final + "," + finalTail, "こんにちは"},
		{"legacy", legacy + "," + legacyTail, "旧式"},
		{"final with unphased", legacy + "," + commentary + "," + finalTail, "は"},
		{"commentary only", commentary, ""},
		{"commentary with unphased but no final", commentary + "," + legacy, ""},
		{"unknown phase", strings.Replace(final, "final_answer", "future", 1), ""},
		{"empty phase", strings.Replace(final, "final_answer", "", 1), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := New("test-key")
			client.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
				body := `{"status":"completed","model":"snapshot","usage":{"input_tokens":4,"output_tokens":3},"output":[` + tc.output + `]}`
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			got, err := client.Generate(context.Background(), Request{Model: "snapshot", Timeout: time.Second})
			if tc.want == "" {
				if err != ErrAPI || got != (Response{}) {
					t.Fatalf("got %+v, %v; want rejected response", got, err)
				}
			} else if err != nil || got.Text != tc.want {
				t.Fatalf("got %+v, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestFailureDoesNotExposeBody(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"http_error", 400, "private prompt and secret"},
		{"invalid_json", 200, "private prompt and secret"},
		{"oversized", 200, strings.Repeat("x", maxResponseBytes+1)},
		{"incomplete", 200, `{"status":"incomplete","usage":{"input_tokens":4,"output_tokens":3}}`},
		{"missing_usage", 200, `{"status":"completed","model":"snapshot","output":[{"type":"message","content":[{"type":"output_text","text":"translation"}]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := New("test-key")
			client.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})
			_, err := client.Generate(context.Background(), Request{Model: "snapshot", Timeout: time.Second})
			if err != ErrAPI {
				t.Fatal("failure was not redacted")
			}
		})
	}
}

type cancelReader struct{ cancel context.CancelFunc }

func (r cancelReader) Read([]byte) (int, error) {
	r.cancel()
	return 0, errors.New("transport private detail")
}
func (cancelReader) Close() error { return nil }
func TestCancelDuringBody(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, _ := New("test-key")
	client.http.Transport = transport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: cancelReader{cancel: cancel}, Request: r}, nil
	})
	_, err := client.Generate(ctx, Request{Timeout: time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("body cancellation lost")
	}
}
