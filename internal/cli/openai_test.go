package cli

import (
	"bytes"
	"context"
	"github.com/sh4869221b/yakuori/internal/config"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAIArgumentsAndMissingKey(t *testing.T) {
	args := []string{"translate", "--source-language", "en", "--target-language", "ja", "--game", "qa", "--artifact", "sample", "--backend", "openai", "--openai-model", "gpt-4o-2024-08-06", "source.txt"}
	cmd, _, err := parseTranslation(args[0], args[1:])
	if err != nil || cmd.backend != "openai" || cmd.openaiModel != "gpt-4o-2024-08-06" {
		t.Fatal("OpenAI arguments rejected")
	}
	t.Setenv("OPENAI_API_KEY", "")
	var out, diagnostic bytes.Buffer
	status := runWithTranslationOpener(context.Background(), args, &out, &diagnostic, func() (config.Paths, error) { return config.Paths{}, nil }, nil, nil)
	if status != ExitFailure || out.Len() != 0 {
		t.Fatal("missing key accepted")
	}
}

type openaiTransport func(*http.Request) (*http.Response, error)

func (f openaiTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestCLIRemoteSuccess(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "yakuori"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "test-key")
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(name, dir)
	}
	previous := http.DefaultTransport
	defer func() { http.DefaultTransport = previous }()
	http.DefaultTransport = openaiTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"object":"response.input_tokens","input_tokens":7}`
		if r.URL.Path == "/v1/responses" {
			body = `{"status":"completed","model":"gpt-4o-2024-08-06","usage":{"input_tokens":7,"output_tokens":2},"output":[{"type":"message","content":[{"type":"output_text","text":"translated"}]}]}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	var out, diagnostic bytes.Buffer
	status := Run([]string{"translate", "--source-language", "en", "--target-language", "ja", "--game", "test", "--artifact", "test", "--backend", "openai", "--openai-model", "gpt-4o-2024-08-06", source}, &out, &diagnostic)
	if status != ExitOK || out.String() != "translated" {
		t.Fatalf("CLI failure: exit %d diagnostic %s", status, diagnostic.String())
	}
}
