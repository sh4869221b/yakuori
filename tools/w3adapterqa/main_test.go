package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func TestRun(t *testing.T) {
	for _, name := range []string{"translated", "jp-noop"} {
		t.Run(name, func(t *testing.T) {
			input, csv := "../w3spike/testdata/en.w3strings", "../../internal/adapter/w3strings/testdata/translated-jp.csv"
			if name == "jp-noop" {
				input, csv = "../w3spike/testdata/jp.w3strings", "../w3spike/testdata/jp.csv"
			}
			before := readFile(t, input)
			output := filepath.Join(t.TempDir(), "output.w3strings")

			if err := run([]string{"--input", input, "--translations", csv, "--output", output}); err != nil {
				t.Fatal(err)
			}

			raw := readFile(t, output)
			if !bytes.Equal(before, readFile(t, input)) {
				t.Fatal("source changed")
			}
			if name == "jp-noop" {
				if !bytes.Equal(raw, before) {
					t.Fatal("Japanese no-op changed bytes")
				}
				return
			}
			limits := config.DefaultLimits()
			limits.ArtifactBytes, limits.Units = 65536, 4095
			limits.UnitTextBytes, limits.TotalTextBytes, limits.Segments = 16384, 32768, 4095
			a, err := w3strings.NewWithLimits(limits)
			if err != nil {
				t.Fatal(err)
			}
			session, err := a.Import(raw, validate.Profile{})
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{
				"1001": "短い訳文😀", "1002": "", "1003": strings.Repeat("長", 2050),
				"4294967295": "  <b>{player}</b>|%s  ",
			}
			for _, u := range session.Units() {
				text, exists := want[u.ID().StableID()]
				if !exists || string(u.Source()) != text || u.SourceLanguage() != "ja" {
					t.Fatal("saved output ID/text/language mismatch")
				}
				delete(want, u.ID().StableID())
			}
			if len(want) != 0 {
				t.Fatal("missing output IDs")
			}
			t.Log("closed/reopened production file matched four fixed translations, including empty, pipe, spaces and 2050 UTF16 units")
		})
	}
}

func TestRunRejectsInvalid(t *testing.T) {
	validSource := readFile(t, "../w3spike/testdata/en.w3strings")
	validCSV := readFile(t, "../../internal/adapter/w3strings/testdata/translated-jp.csv")
	for _, name := range []string{"malformed", "missing-ID", "extra-ID", "duplicate-ID", "invalid-ID", "columns", "invalid-text", "existing-output", "input-as-output", "missing-argument"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			input, csv, output := filepath.Join(dir, "input"), filepath.Join(dir, "translations"), filepath.Join(dir, "output")
			source, translations := bytes.Clone(validSource), bytes.Clone(validCSV)
			switch name {
			case "malformed":
				source = source[:len(source)-1]
			case "missing-ID":
				translations = bytes.ReplaceAll(translations, []byte("1002|87654321||\n"), nil)
			case "extra-ID":
				translations = append(translations, []byte("42|00000000||extra\n")...)
			case "duplicate-ID":
				translations = append(translations, []byte(" 1001|12345678||duplicate\n")...)
			case "invalid-ID":
				translations = []byte("no-ID|00000000||text\n")
			case "columns":
				translations = []byte("1001|text\n")
			case "invalid-text":
				translations = bytes.Replace(translations, []byte("短い訳文😀"), []byte{0xff}, 1)
			case "existing-output":
				writeFile(t, output, []byte("existing"))
			case "input-as-output":
				output = input
			}
			writeFile(t, input, source)
			writeFile(t, csv, translations)
			args := []string{"--input", input, "--translations", csv, "--output", output}
			if name == "missing-argument" {
				args = nil
			}

			if err := run(args); err == nil {
				t.Fatal("invalid run succeeded")
			}

			if !bytes.Equal(source, readFile(t, input)) || !bytes.Equal(translations, readFile(t, csv)) {
				t.Fatal("input or CSV changed on rejection")
			}
			switch name {
			case "input-as-output":
			case "existing-output":
				if string(readFile(t, output)) != "existing" {
					t.Fatal("existing output changed")
				}
			default:
				if _, err := os.Lstat(output); !os.IsNotExist(err) {
					t.Fatalf("rejected output exists: %v", err)
				}
			}
			t.Log("rejected; source/CSV/existing output preserved and no new output")
		})
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeFile(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
