package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/models"
)

// No fixture serves bytes with this SHA-256, so a pull in these tests always
// fails before registration.
const pullDigest = "1d9614638d18024d0fbb36575a15f1302a3adf044df10345688ec4f6e1c4ff32"

const pullDigestSource = "plan-pinned fixture digest"

type pullArguments struct {
	id, sha256, size, digestSource, url string
}

func (a pullArguments) args() []string {
	return []string{"models", "pull", "--id", a.id, "--sha256", a.sha256, "--size", a.size, "--digest-source", a.digestSource, a.url}
}

func validPullArguments(change func(*pullArguments)) []string {
	args := pullArguments{id: "fetched", sha256: pullDigest, size: "1024", digestSource: pullDigestSource, url: "https://example.invalid/model.gguf"}
	if change != nil {
		change(&args)
	}
	return args.args()
}

// countingListener closes every connection it accepts, so a pull against it
// always fails and only the connection attempt itself is observed.
func countingListener(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var connections atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})
	return "https://" + listener.Addr().String() + "/model.gguf", &connections
}

func TestPullCLIArguments(t *testing.T) {
	change := func(apply func(*pullArguments)) []string {
		return validPullArguments(apply)
	}
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"missing id", change(func(a *pullArguments) { a.id = "" })},
		{"relative id", change(func(a *pullArguments) { a.id = "../private-text" })},
		{"missing sha256", change(func(a *pullArguments) { a.sha256 = "" })},
		{"short sha256", change(func(a *pullArguments) { a.sha256 = pullDigest[:32] })},
		{"odd sha256", change(func(a *pullArguments) { a.sha256 = pullDigest[:63] })},
		{"uppercase sha256", change(func(a *pullArguments) { a.sha256 = strings.ToUpper(pullDigest) })},
		{"non hex sha256", change(func(a *pullArguments) { a.sha256 = strings.Repeat("z", 64) })},
		{"missing size", change(func(a *pullArguments) { a.size = "" })},
		{"zero size", change(func(a *pullArguments) { a.size = "0" })},
		{"negative size", change(func(a *pullArguments) { a.size = "-1" })},
		{"non numeric size", change(func(a *pullArguments) { a.size = "private-text" })},
		{"max int64 size", change(func(a *pullArguments) { a.size = "9223372036854775807" })},
		{"overflowing size", change(func(a *pullArguments) { a.size = "9223372036854775808" })},
		{"missing digest source", change(func(a *pullArguments) { a.digestSource = "" })},
		{"blank digest source", change(func(a *pullArguments) { a.digestSource = " \t " })},
		{"blank url", change(func(a *pullArguments) { a.url = "" })},
		{"plain http url", change(func(a *pullArguments) { a.url = "http://example.invalid/model.gguf" })},
		{"relative url", change(func(a *pullArguments) { a.url = "/model.gguf" })},
		{"userinfo url", change(func(a *pullArguments) { a.url = "https://user:private-text@example.invalid/model.gguf" })},
		{"fragment url", change(func(a *pullArguments) { a.url = "https://example.invalid/model.gguf#private-text" })},
		{"empty host url", change(func(a *pullArguments) { a.url = "https:///model.gguf" })},
		{"missing url", []string{"models", "pull", "--id", "fetched", "--sha256", pullDigest, "--size", "1024", "--digest-source", pullDigestSource}},
		{"extra url", append(validPullArguments(nil), "https://example.invalid/other.gguf")},
		{"flags after url", []string{"models", "pull", "https://example.invalid/model.gguf", "--id", "fetched", "--sha256", pullDigest, "--size", "1024", "--digest-source", pullDigestSource}},
		{"unknown flag", []string{"models", "pull", "--private-text", "private-value", "--id", "fetched", "--sha256", pullDigest, "--size", "1024", "--digest-source", pullDigestSource}},
		{"help with arguments", []string{"models", "pull", "--help", "https://example.invalid/model.gguf"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, diag bytes.Buffer
			calls := 0
			code := run(tt.args, &out, &diag, func() (config.Paths, error) {
				calls++
				return config.Paths{}, nil
			})
			if code != ExitUsage || out.Len() != 0 || diag.Len() == 0 || calls != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q resolver calls=%d", code, out.String(), diag.String(), calls)
			}
			if strings.Contains(diag.String(), "private-text") {
				t.Fatalf("diagnostic leaked argument text: %q", diag.String())
			}
		})
	}

	for _, args := range [][]string{{"models", "--help"}, {"models", "pull", "--help"}, {"models", "pull", "-h"}} {
		t.Run("help "+strings.Join(args, " "), func(t *testing.T) {
			var out, diag bytes.Buffer
			calls := 0
			code := run(args, &out, &diag, func() (config.Paths, error) {
				calls++
				return config.Paths{}, nil
			})
			if code != ExitOK || !strings.Contains(out.String(), "models pull") || diag.Len() != 0 || calls != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q resolver calls=%d", code, out.String(), diag.String(), calls)
			}
		})
	}
}

func TestPullCLIOutput(t *testing.T) {
	entry := models.Entry{SHA256: pullDigest, SizeBytes: 1024}

	requireRegistered := func(t *testing.T, diagnostic string) {
		t.Helper()
		if !strings.Contains(diagnostic, "registered") || strings.Contains(diagnostic, "not registered") {
			t.Fatalf("diagnostic does not state that the model is registered: %q", diagnostic)
		}
	}

	t.Run("registered result", func(t *testing.T) {
		var out, diag bytes.Buffer
		if code := reportPull(&out, &diag, "fetched", entry, nil); code != ExitOK {
			t.Fatalf("code=%d stderr=%q", code, diag.String())
		}
		want := fmt.Sprintf("pull: fetched\nmodel_sha256: %s\nsize_bytes: 1024\n", pullDigest)
		if out.String() != want || diag.Len() != 0 {
			t.Fatalf("stdout=%q stderr=%q", out.String(), diag.String())
		}
	})

	t.Run("failure after registration stays registered", func(t *testing.T) {
		var out, diag bytes.Buffer
		code := reportPull(&out, &diag, "fetched", entry, errors.New("private-text raw transport detail"))
		if code != ExitFailure || out.Len() == 0 || strings.Contains(diag.String(), "private-text") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), diag.String())
		}
		requireRegistered(t, diag.String())
	})

	t.Run("stdout failure after registration stays registered", func(t *testing.T) {
		var diag bytes.Buffer
		code := reportPull(brokenWriter{short: true}, &diag, "fetched", entry, nil)
		if code != ExitFailure {
			t.Fatalf("code=%d stderr=%q", code, diag.String())
		}
		requireRegistered(t, diag.String())
	})

	t.Run("failure before registration", func(t *testing.T) {
		var out, diag bytes.Buffer
		code := reportPull(&out, &diag, "fetched", models.Entry{}, errors.New("private-text"))
		if code != ExitFailure || out.Len() != 0 || strings.Contains(diag.String(), "registered") || strings.Contains(diag.String(), "private-text") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), diag.String())
		}
	})

	t.Run("acquisition notice precedes the connection", func(t *testing.T) {
		paths := modelFixture(t)
		source, connections := countingListener(t)
		var out, diag bytes.Buffer
		calls := 0
		code := run(validPullArguments(func(a *pullArguments) { a.url = source }), &out, &diag, func() (config.Paths, error) {
			calls++
			return paths, nil
		})
		if code != ExitFailure || out.Len() != 0 || calls != 1 || connections.Load() != 1 {
			t.Fatalf("code=%d stdout=%q resolver calls=%d connections=%d stderr=%q", code, out.String(), calls, connections.Load(), diag.String())
		}
		notice := diag.String()
		for _, value := range []string{source, pullDigest, "1024", pullDigestSource} {
			if !strings.Contains(notice, value) {
				t.Fatalf("acquisition notice omitted %q: %q", value, notice)
			}
		}
		diagnostic := strings.Index(notice, "yakuori: ")
		if diagnostic < 0 || diagnostic < strings.Index(notice, source) {
			t.Fatalf("failure diagnostic did not follow the acquisition notice: %q", notice)
		}
		if !strings.Contains(notice, "unreachable") {
			t.Fatalf("failure was not classified: %q", notice)
		}
	})

	t.Run("metadata write failure blocks the connection", func(t *testing.T) {
		paths := modelFixture(t)
		source, connections := countingListener(t)
		var out bytes.Buffer
		calls := 0
		code := run(validPullArguments(func(a *pullArguments) { a.url = source }), &out, brokenWriter{}, func() (config.Paths, error) {
			calls++
			return paths, nil
		})
		if code != ExitFailure || out.Len() != 0 || calls != 1 || connections.Load() != 0 {
			t.Fatalf("code=%d stdout=%q resolver calls=%d connections=%d", code, out.String(), calls, connections.Load())
		}
	})

	t.Run("cancelled pull keeps the operational exit status", func(t *testing.T) {
		paths := modelFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var out, diag bytes.Buffer
		code := runWithPipeline(ctx, validPullArguments(nil), &out, &diag, func() (config.Paths, error) { return paths, nil }, nil)
		if code != ExitFailure || out.Len() != 0 || !strings.Contains(diag.String(), "cancelled") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), diag.String())
		}
	})
}
