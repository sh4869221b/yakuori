package cli

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
)

type brokenWriter struct{ short bool }

func (w brokenWriter) Write(p []byte) (int, error) {
	if w.short {
		return 0, nil
	}
	return 0, errors.New("broken")
}

func TestContracts(t *testing.T) {
	for _, tt := range []struct {
		name       string
		args       []string
		code       int
		out        string
		diagnostic bool
	}{
		{"help", []string{"help"}, 0, usage, false},
		{"long help", []string{"--help"}, 0, usage, false},
		{"short help", []string{"-h"}, 0, usage, false},
		{"missing", nil, 2, "", true},
		{"invalid option", []string{"--secret-text"}, 2, "", true},
		{"unknown command", []string{"secret-text"}, 2, "", true},
		{"extra args", []string{"doctor", "secret-text"}, 2, "", true},
		{"doctor", []string{"doctor"}, 0, "config: /c\ndata: /d\ncache: /k\nstate: /s\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, diag bytes.Buffer
			calls := 0
			code := run(tt.args, &out, &diag, func() (config.Paths, error) {
				calls++
				return config.Paths{Config: "/c", Data: "/d", Cache: "/k", State: "/s"}, nil
			})
			if code != tt.code || out.String() != tt.out || (diag.Len() > 0) != tt.diagnostic {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), diag.String())
			}
			if strings.Contains(diag.String(), "secret-text") {
				t.Fatal("diagnostic leaked argument")
			}
			if tt.name != "doctor" && calls != 0 {
				t.Fatal("unexpected config access")
			}
		})
	}
}

func TestInjectedError(t *testing.T) {
	var out, diag bytes.Buffer
	code := run([]string{"doctor"}, &out, &diag, func() (config.Paths, error) { return config.Paths{}, errors.New("fake failure") })
	if code != ExitFailure || out.Len() != 0 || !strings.Contains(diag.String(), "fake failure") {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), diag.String())
	}
}

func TestWriterFailures(t *testing.T) {
	for _, w := range []io.Writer{brokenWriter{}, brokenWriter{short: true}} {
		var diag bytes.Buffer
		if got := run([]string{"--help"}, w, &diag, nil); got != ExitFailure || diag.Len() == 0 {
			t.Fatalf("got %d, stderr %q", got, diag.String())
		}
	}
	if got := run(nil, io.Discard, brokenWriter{}, nil); got != ExitUsage {
		t.Fatalf("got %d", got)
	}
}
