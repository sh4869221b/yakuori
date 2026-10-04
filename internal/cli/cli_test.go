package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/publication"
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

func TestParseLocalizeOutputModes(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want publication.Options
	}{
		{"create", []string{"--output", "out", "source"}, publication.Options{Source: "source", Output: "out", Mode: publication.Create}},
		{"replace output", []string{"--output", "out", "--replace-output", "source"}, publication.Options{Source: "source", Output: "out", Mode: publication.Replace}},
		{"in place", []string{"--in-place", "source"}, publication.Options{Source: "source", Output: "source", Mode: publication.InPlace}},
		{"end of options", []string{"--output", "out", "--", "source"}, publication.Options{Source: "source", Output: "out", Mode: publication.Create}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, help, err := parseLocalize(tt.args)
			if err != nil || help != "" || got != tt.want {
				t.Fatalf("options=%+v help=%q error=%v", got, help, err)
			}
		})
	}
}

func TestParseLocalizeRejectsInvalidArguments(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want error
	}{
		{"in place with output", []string{"--in-place", "--output", "out", "source"}, errInvalidLocalizeArguments},
		{"in place with replace", []string{"--in-place", "--output", "out", "--replace-output", "source"}, errInvalidLocalizeArguments},
		{"replace without output", []string{"--replace-output", "source"}, errInvalidLocalizeArguments},
		{"missing output mode", []string{"source"}, errInvalidLocalizeArguments},
		{"missing source", []string{"--output", "out"}, errInvalidLocalizeArguments},
		{"extra source", []string{"--output", "out", "source", "extra"}, errInvalidLocalizeArguments},
		{"empty output", []string{"--output", "", "source"}, errInvalidLocalizeArguments},
		{"empty source", []string{"--output", "out", ""}, errInvalidLocalizeArguments},
		{"unknown flag", []string{"--private-flag", "private-value", "--output", "out", "source"}, errInvalidLocalizeArguments},
		{"same lexical path", []string{"--output", "./source", "source"}, errSameLocalizePath},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, help, err := parseLocalize(tt.args)
			if !errors.Is(err, tt.want) || help != "" {
				t.Fatalf("help=%q error=%v, want error %v", help, err, tt.want)
			}
			var out, diag bytes.Buffer
			calls := 0
			code := run(append([]string{"localize"}, tt.args...), &out, &diag, func() (config.Paths, error) {
				calls++
				return config.Paths{}, nil
			})
			wantDiagnostic := "yakuori: invalid localize arguments; use --help\n"
			if errors.Is(tt.want, errSameLocalizePath) {
				wantDiagnostic = "yakuori: source and output are the same path; use --in-place\n"
			}
			if code != ExitUsage || out.Len() != 0 || diag.String() != wantDiagnostic || strings.Contains(diag.String(), "private-") || calls != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q resolver calls=%d", code, out.String(), diag.String(), calls)
			}
		})
	}
}

func TestRunLocalizeHelpUsesFlagUsage(t *testing.T) {
	for _, helpFlag := range []string{"-h", "--help"} {
		t.Run(helpFlag, func(t *testing.T) {
			var out, diag bytes.Buffer
			calls := 0
			code := run([]string{"localize", helpFlag}, &out, &diag, func() (config.Paths, error) {
				calls++
				return config.Paths{}, nil
			})
			if code != ExitOK || !strings.Contains(out.String(), "Usage: yakuori localize") || diag.Len() != 0 || calls != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q resolver calls=%d", code, out.String(), diag.String(), calls)
			}
		})
	}
}

func TestRunLocalizeRejectsSamePathWithoutEchoingPaths(t *testing.T) {
	var out, diag bytes.Buffer
	code := run([]string{"localize", "--output", "./private-source", "private-source"}, &out, &diag, nil)
	if code != ExitUsage || out.Len() != 0 || !strings.Contains(diag.String(), "--in-place") || strings.Contains(diag.String(), "private-source") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), diag.String())
	}
}

func TestRunLocalizeKeepsPipelineUnimplementedWithoutFileAccess(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "private-source.txt")
	output := filepath.Join(dir, "private-output.txt")
	if err := os.WriteFile(source, []byte("original source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte("existing output"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"create with missing source", []string{"localize", "--output", filepath.Join(dir, "new-output.txt"), filepath.Join(dir, "missing-source.txt")}},
		{"replace absent output", []string{"localize", "--output", filepath.Join(dir, "new-output.txt"), "--replace-output", filepath.Join(dir, "missing-source.txt")}},
		{"replace existing output", []string{"localize", "--output", output, "--replace-output", source}},
		{"in place", []string{"localize", "--in-place", source}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, diag bytes.Buffer
			calls := 0
			code := run(tt.args, &out, &diag, func() (config.Paths, error) {
				calls++
				return config.Paths{}, nil
			})
			if code != ExitFailure || out.Len() != 0 || diag.String() != "yakuori: localize pipeline is not implemented\n" || calls != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q resolver calls=%d", code, out.String(), diag.String(), calls)
			}
		})
	}
	if got, err := os.ReadFile(source); err != nil || string(got) != "original source" {
		t.Fatalf("source=%q error=%v", got, err)
	}
	if got, err := os.ReadFile(output); err != nil || string(got) != "existing output" {
		t.Fatalf("output=%q error=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new-output.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new output stat error=%v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("directory entries=%v error=%v", entries, err)
	}
}
