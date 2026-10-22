package cli

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func w3Count(n int) []byte {
	if n < 64 {
		return []byte{byte(n)}
	}
	return []byte{byte(n&63) | 64, byte(n >> 6)}
}

// w3Table authors minimal valid tables independently of the production parser.
func w3Table(language string, texts []string) []byte {
	mask, prefix, suffix := uint32(0x59825646), uint16(0x5483), uint16(0x4893)
	if language == "en" {
		mask, prefix, suffix = 0x79321793, 0x4397, 0x5139
	}
	units := make([][]uint16, len(texts))
	offsets := make([]uint32, len(texts))
	var payload uint32
	for i, text := range texts {
		units[i] = utf16.Encode([]rune(text))
		offsets[i] = payload
		payload += uint32(len(units[i]) + 1)
	}
	raw := []byte{'R', 'T', 'S', 'W'}
	raw = binary.LittleEndian.AppendUint32(raw, 162)
	raw = binary.LittleEndian.AppendUint16(raw, prefix)
	raw = append(raw, w3Count(len(texts))...)
	for i := range texts {
		raw = binary.LittleEndian.AppendUint32(raw, uint32(i+1)^mask)
		raw = binary.LittleEndian.AppendUint32(raw, offsets[i])
		raw = binary.LittleEndian.AppendUint32(raw, uint32(len(units[i])))
	}
	keys := 0
	if len(texts) > 0 {
		keys = 1
	}
	raw = append(raw, w3Count(keys)...)
	if keys == 1 {
		raw = binary.LittleEndian.AppendUint32(raw, 0x12345678)
		raw = binary.LittleEndian.AppendUint32(raw, 1^mask)
	}
	raw = append(raw, w3Count(int(payload))...)
	body := make([]byte, 2*int(payload))
	for i, entry := range units {
		state := uint16(mask >> 8)
		for j, unit := range entry {
			binary.LittleEndian.PutUint16(body[2*(int(offsets[i])+j):], unit^uint16(uint32(len(entry)+1)*uint32(state)))
			state = state<<1 | state>>15
		}
	}
	return binary.LittleEndian.AppendUint16(append(raw, body...), suffix)
}

func writeW3File(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func w3DispatchFixture(t *testing.T) (string, config.Paths, *translationEngine) {
	t.Helper()
	dir := t.TempDir()
	_, paths, engine := translationFixtureAt(t, dir, "")
	return dir, paths, engine
}

func TestW3StringsCLIArguments(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		for _, tt := range []struct {
			name, format, sourceLanguage string
			args                         []string
			mode                         publication.Mode
		}{
			{"en create", "w3strings", "en", []string{"--format", "w3strings", "--source-language", "en", "--target-language", "ja", "--game", "witcher3", "--artifact", "table", "--output", "out", "source"}, publication.Create},
			{"ja create", "w3strings", "ja", []string{"--format", "w3strings", "--source-language", "ja", "--target-language", "ja", "--game", "witcher3", "--artifact", "table", "--output", "out", "source"}, publication.Create},
			{"ja replace", "w3strings", "ja", []string{"--format", "w3strings", "--source-language", "ja", "--target-language", "ja", "--game", "witcher3", "--artifact", "table", "--output", "out", "--replace-output", "source"}, publication.Replace},
			{"en in place", "w3strings", "en", []string{"--format", "w3strings", "--source-language", "en", "--target-language", "ja", "--game", "witcher3", "--artifact", "table", "--in-place", "source"}, publication.InPlace},
			{"text create", "text", "en", []string{"--format", "text", "--source-language", "en", "--target-language", "ja", "--game", "witcher3", "--artifact", "table", "--output", "out", "source"}, publication.Create},
		} {
			t.Run(tt.name, func(t *testing.T) {
				command, help, err := parseTranslation("localize", tt.args)
				if err != nil || help != "" {
					t.Fatalf("help=%q err=%v", help, err)
				}
				if command.format != tt.format || command.sourceLanguage != tt.sourceLanguage || command.targetLanguage != "ja" ||
					command.options.Mode != tt.mode || command.options.Source != "source" {
					t.Fatalf("command=%+v", command)
				}
			})
		}
		t.Run("translate stays text", func(t *testing.T) {
			command, help, err := parseTranslation("translate", []string{"--source-language", "fr", "--target-language", "de", "--game", "g", "--artifact", "a", "source"})
			if err != nil || help != "" || command.format != "text" || command.sourceLanguage != "fr" || command.targetLanguage != "de" {
				t.Fatalf("command=%+v help=%q err=%v", command, help, err)
			}
		})
	})
	t.Run("rejected", func(t *testing.T) {
		for _, tt := range []struct {
			name string
			args []string
		}{
			{"unsupported format", []string{"--format", "binary", "--output", "out", "source"}},
			{"unsupported source language", []string{"--format", "w3strings", "--source-language", "fr", "--output", "out", "source"}},
			{"unsupported target language", []string{"--format", "w3strings", "--target-language", "en", "--output", "out", "source"}},
			{"empty game namespace", []string{"--format", "w3strings", "--game", "", "--output", "out", "source"}},
			{"empty artifact namespace", []string{"--format", "w3strings", "--artifact", "", "--output", "out", "source"}},
			{"unknown backend", []string{"--format", "w3strings", "--backend", "automatic", "--output", "out", "source"}},
			{"in place with output", []string{"--format", "w3strings", "--in-place", "--output", "out", "source"}},
			{"in place with replace", []string{"--format", "w3strings", "--in-place", "--output", "out", "--replace-output", "source"}},
			{"replace without output", []string{"--format", "w3strings", "--replace-output", "source"}},
			{"same path", []string{"--format", "w3strings", "--output", "./source", "source"}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				args := append([]string{"localize", "--source-language", "en", "--target-language", "ja", "--game", "witcher3", "--artifact", "table"}, tt.args...)
				var out, diag bytes.Buffer
				resolves, opens := 0, 0
				code := runWithTranslationOpener(context.Background(), args, &out, &diag,
					func() (config.Paths, error) { resolves++; return config.Paths{}, errors.New("private resolver") },
					nil,
					func(context.Context, string) (string, inference.Engine, error) {
						opens++
						return "", nil, errors.New("private opener")
					})
				if code != ExitUsage || resolves != 0 || opens != 0 || out.Len() != 0 || strings.Contains(diag.String(), "private") {
					t.Fatalf("code=%d resolves=%d opens=%d stdout=%q stderr=%q", code, resolves, opens, out.String(), diag.String())
				}
			})
		}
	})
}

func TestW3StringsCLIDispatch(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		for _, tt := range []struct {
			name, header, sourceLanguage, backend string
			mode                                  publication.Mode
		}{
			{"en create cpu", "en", "en", "cpu", publication.Create},
			{"ja create cpu", "jp", "ja", "cpu", publication.Create},
			{"ja replace cuda", "jp", "ja", "cuda", publication.Replace},
			{"en in place cpu", "en", "en", "cpu", publication.InPlace},
		} {
			t.Run(tt.name, func(t *testing.T) {
				dir, paths, engine := w3DispatchFixture(t)
				source := filepath.Join(dir, "source.w3strings")
				raw := w3Table(tt.header, []string{"Alpha.", "Beta."})
				writeW3File(t, source, raw)
				output := filepath.Join(dir, "output.w3strings")
				args := []string{"localize", "--format", "w3strings", "--source-language", tt.sourceLanguage, "--target-language", "ja", "--game", "witcher3", "--artifact", "table", "--backend", tt.backend}
				switch tt.mode {
				case publication.Create:
					args = append(args, "--output", output)
				case publication.Replace:
					writeW3File(t, output, w3Table(tt.header, []string{"Old."}))
					args = append(args, "--output", output, "--replace-output")
				case publication.InPlace:
					output = source
					args = append(args, "--in-place")
				}
				args = append(args, source)
				var out, diag bytes.Buffer
				code := runWithTranslationOpener(context.Background(), args, &out, &diag,
					func() (config.Paths, error) { return paths, nil }, nil, engine.open)
				if code != ExitOK || out.Len() != 0 || diag.Len() != 0 {
					t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), diag.String())
				}
				if engine.closed != 1 || engine.generated != 2 || engine.info.Backend != tt.backend {
					t.Fatalf("closed=%d generated=%d info=%+v", engine.closed, engine.generated, engine.info)
				}
				rows := translationRows(t, paths)
				if len(rows) != 2 || rows[0] != "訳文" || rows[1] != "訳文" {
					t.Fatalf("tm rows=%q", rows)
				}
				published, err := os.ReadFile(output)
				if err != nil {
					t.Fatal(err)
				}
				adapter, err := w3strings.NewForSource(tt.sourceLanguage, config.DefaultLimits())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := adapter.Import(raw, validate.Profile{}); err != nil {
					t.Fatal(err)
				}
				observed, err := adapter.Observe(bytes.NewReader(published))
				if err != nil {
					t.Fatalf("observe published artifact: %v", err)
				}
				if len(observed.Units) != 2 {
					t.Fatalf("observed units=%d", len(observed.Units))
				}
				for _, unit := range observed.Units {
					if unit.Text != "訳文" || unit.TargetLanguage != "ja" {
						t.Fatalf("observed unit=%+v", unit)
					}
				}
				t.Logf("mode=%s backend=%s generated=%d closed=%d tm_rows=%d observed_units=%d", tt.mode, tt.backend, engine.generated, engine.closed, len(rows), len(observed.Units))
			})
		}
		t.Run("text localize", func(t *testing.T) {
			dir, paths, engine := w3DispatchFixture(t)
			source := filepath.Join(dir, "source.txt")
			if err := os.WriteFile(source, []byte("Hello.\n"), 0600); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(dir, "output.txt")
			args := translationArgs("localize", "--output", output, source)
			var out, diag bytes.Buffer
			code := runWithTranslationOpener(context.Background(), args, &out, &diag,
				func() (config.Paths, error) { return paths, nil }, nil, engine.open)
			if code != ExitOK || out.Len() != 0 || diag.Len() != 0 || engine.generated != 1 {
				t.Fatalf("code=%d stdout=%q stderr=%q generated=%d", code, out.String(), diag.String(), engine.generated)
			}
			published, err := os.ReadFile(output)
			if err != nil || string(published) != "訳文" {
				t.Fatalf("published=%q err=%v", published, err)
			}
			if rows := translationRows(t, paths); len(rows) != 1 || rows[0] != "訳文" {
				t.Fatalf("tm rows=%q", rows)
			}
		})
		t.Run("translate", func(t *testing.T) {
			dir, paths, engine := w3DispatchFixture(t)
			source := filepath.Join(dir, "source.txt")
			if err := os.WriteFile(source, []byte("Hello.\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var out, diag bytes.Buffer
			code := runWithTranslationOpener(context.Background(), translationArgs("translate", source), &out, &diag,
				func() (config.Paths, error) { return paths, nil }, nil, engine.open)
			if code != ExitOK || out.String() != "訳文" || diag.Len() != 0 || engine.generated != 1 {
				t.Fatalf("code=%d stdout=%q stderr=%q generated=%d", code, out.String(), diag.String(), engine.generated)
			}
			if _, err := os.Lstat(filepath.Join(dir, "output.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("translate wrote a file: %v", err)
			}
		})
	})
	t.Run("rejected", func(t *testing.T) {
		t.Run("syntax before opener", func(t *testing.T) {
			dir, paths, engine := w3DispatchFixture(t)
			source := filepath.Join(dir, "source.w3strings")
			writeW3File(t, source, w3Table("en", []string{"Alpha."}))
			output := filepath.Join(dir, "output.w3strings")
			args := []string{"localize", "--format", "w3strings", "--source-language", "en", "--target-language", "en", "--game", "witcher3", "--artifact", "table", "--output", output, source}
			var out, diag bytes.Buffer
			resolves, opens := 0, 0
			code := runWithTranslationOpener(context.Background(), args, &out, &diag,
				func() (config.Paths, error) { resolves++; return paths, nil }, nil,
				func(ctx context.Context, backend string) (string, inference.Engine, error) {
					opens++
					return engine.open(ctx, backend)
				})
			if code != ExitUsage || resolves != 0 || opens != 0 || engine.generated != 0 || out.Len() != 0 {
				t.Fatalf("code=%d resolves=%d opens=%d generated=%d stdout=%q stderr=%q", code, resolves, opens, engine.generated, out.String(), diag.String())
			}
			if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected syntax published: %v", err)
			}
		})
		for _, tt := range []struct {
			name string
			raw  []byte
		}{
			{"header mismatch", w3Table("en", []string{"Alpha.", "Beta."})},
			{"empty table header mismatch", w3Table("en", nil)},
		} {
			t.Run(tt.name, func(t *testing.T) {
				dir, paths, engine := w3DispatchFixture(t)
				source := filepath.Join(dir, "source.w3strings")
				writeW3File(t, source, tt.raw)
				output := filepath.Join(dir, "output.w3strings")
				args := []string{"localize", "--format", "w3strings", "--source-language", "ja", "--target-language", "ja", "--game", "witcher3", "--artifact", "table", "--output", output, source}
				var out, diag bytes.Buffer
				code := runWithTranslationOpener(context.Background(), args, &out, &diag,
					func() (config.Paths, error) { return paths, nil }, nil, engine.open)
				if code != ExitFailure || out.Len() != 0 || diag.String() != "yakuori: localize import failed; TMCommitted=false; publication=not-published\n" {
					t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), diag.String())
				}
				if engine.generated != 0 || engine.closed != 1 {
					t.Fatalf("generated=%d closed=%d", engine.generated, engine.closed)
				}
				if rows := translationRows(t, paths); len(rows) != 0 {
					t.Fatalf("tm rows=%q", rows)
				}
				if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("mismatch published: %v", err)
				}
				if current, err := os.ReadFile(source); err != nil || !bytes.Equal(current, tt.raw) {
					t.Fatalf("source changed: err=%v", err)
				}
			})
		}
	})
}
