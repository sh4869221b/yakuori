package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/publication"
)

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
		{"doctor", []string{"doctor"}, 0, "config: /c\ndata: /d\ncache: /k\nstate: /s\n" +
			"config directory: /c [missing]\ndata directory: /d [missing]\ncache directory: /k [missing]\nstate directory: /s [missing]\n" +
			"config file: /c/config.json [missing]\nregistry file: /d/registry.json [missing]\nmodels directory: /d/models [missing]\n" +
			"TM file: /d/tm.sqlite [missing]\nsnapshot directory: /k/model-snapshots [missing]\nregistry lock: /d/registry.lock [missing]\nselected model: none\n", false},
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
			got, help, err := parseTranslation("localize", translationArgs("localize", tt.args...)[1:])
			if err != nil || help != "" || got.options != tt.want {
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
			args := translationArgs("localize", tt.args...)
			_, help, err := parseTranslation("localize", args[1:])
			if !errors.Is(err, tt.want) || help != "" {
				t.Fatalf("help=%q error=%v, want error %v", help, err, tt.want)
			}
			var out, diag bytes.Buffer
			calls := 0
			code := run(args, &out, &diag, func() (config.Paths, error) {
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

func TestRunLocalizeRejectsUnselectedModelWithoutChangingFiles(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "private-source.txt")
	output := filepath.Join(dir, "private-output.txt")
	writeFixture(t, source, "original source")
	writeFixture(t, output, "existing output")
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
			code := run(translationArgs("localize", tt.args[1:]...), &out, &diag, func() (config.Paths, error) {
				calls++
				return config.Paths{Config: filepath.Join(dir, "absent-config"), Data: filepath.Join(dir, "absent-data")}, nil
			})
			if code != ExitFailure || out.Len() != 0 || diag.String() != "yakuori: localize model failed; TMCommitted=false\n" || calls != 1 {
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

func testTranslationArguments(t *testing.T) {
	for _, action := range []string{"translate", "localize"} {
		for _, args := range [][]string{
			{}, {"--help"}, {"--source-language", "en", "source"},
			translationArgs(action, "--backend", "automatic", "source")[1:],
			translationArgs(action, "-")[1:],
			translationArgs(action, "source", "--backend", "cuda")[1:],
			translationArgs(action, "--game", "", "source")[1:],
			translationArgs(action, "--artifact", "", "source")[1:],
			translationArgs(action, "--target-language", "", "source")[1:],
			translationArgs(action, "--source-language", "", "source")[1:],
			translationArgs(action, "--format", "w3strings", "source")[1:],
		} {
			var out, diag bytes.Buffer
			calls := 0
			// Given an inaccessible storage resolver, syntax/help must finish before I/O.
			code := run(append([]string{action}, args...), &out, &diag, func() (config.Paths, error) { calls++; return config.Paths{}, errors.New("private") })
			want := ExitUsage
			if len(args) == 1 && args[0] == "--help" {
				want = ExitOK
			}
			if code != want || calls != 0 || strings.Contains(diag.String(), "private") {
				t.Fatalf("args=%v code=%d calls=%d diag=%q", args, code, calls, diag.String())
			}
		}
	}
}

func testTranslationFiles(t *testing.T) {
	for _, mode := range []publication.Mode{publication.Create, publication.Replace, publication.InPlace} {
		t.Run(string(mode), func(t *testing.T) {
			command, paths, engine := translationFixture(t, "Hello.\n")
			source := command.options.Source
			output := filepath.Join(filepath.Dir(source), "output.txt")
			if mode == publication.Replace {
				writeFixture(t, output, "old output")
			}
			if mode == publication.InPlace {
				output = source
			}
			command.action, command.options = "localize", publication.Options{Source: source, Output: output, Mode: mode}
			supported := translationPublicationSupported(t, filepath.Dir(output))
			engine.closeErr = errors.New("private close after publication")
			var out, diag bytes.Buffer
			result, err := command.execute(context.Background(), &out, paths, engine.open)
			if !supported {
				checkTranslationFilesystemRefusal(t, result, err)
				if reportPipeline(&diag, result, err) != ExitFailure || out.Len() != 0 || strings.Contains(diag.String(), "private") ||
					engine.generated != 0 || engine.closed != 1 || len(translationRows(t, paths)) != 0 {
					t.Fatalf("result=%+v err=%v generated=%d closed=%d diag=%q", result, err, engine.generated, engine.closed, diag.String())
				}
				if raw, err := os.ReadFile(source); err != nil || string(raw) != "Hello.\n" {
					t.Fatalf("source=%q err=%v", raw, err)
				}
				switch mode {
				case publication.Replace:
					if raw, err := os.ReadFile(output); err != nil || string(raw) != "old output" {
						t.Fatalf("output=%q err=%v", raw, err)
					}
				case publication.Create:
					if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("output stat=%v", err)
					}
				}
				return
			}
			if reportPipeline(&diag, result, err) != 1 || !result.TMCommitted || result.Publication.State != publication.Published || out.Len() != 0 || strings.Contains(diag.String(), "private") {
				t.Fatalf("result=%+v err=%v diag=%q", result, err, diag.String())
			}
			raw, err := os.ReadFile(output)
			if err != nil || string(raw) != engine.text {
				t.Fatalf("output=%q err=%v", raw, err)
			}
			if mode == publication.InPlace {
				raw, err := os.ReadFile(result.Publication.BackupPath)
				if err != nil || string(raw) != "Hello.\n" {
					t.Fatalf("backup=%q err=%v", raw, err)
				}
			}
			if len(translationRows(t, paths)) != 1 {
				t.Fatal("missing committed row")
			}
			if engine.generated != 1 || engine.closed != 1 {
				t.Fatalf("generated=%d closed=%d", engine.generated, engine.closed)
			}
			t.Logf("mode=%s generated=1 tm_rows=1 publication=published closed=1 backup=%q", mode, result.Publication.BackupPath)
		})
	}
}
