package w3strings

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/sh4869221b/yakuori/internal/artifact"
)

func TestExportBounds(t *testing.T) {
	for _, n := range []int{62, 63, 4094, 4095} {
		t.Run(fmt.Sprintf("payload-%d", n+1), func(t *testing.T) {
			raw := testFile("jp", []testString{{7, 0, []uint16{'A'}}}, nil, 2)
			a := New()
			session := importSession(t, a, raw)
			target := strings.Repeat("長", n)
			accepted := acceptedTexts(t, session, map[string]string{"7": target})
			var output bytes.Buffer

			manifest, err := a.Export(&output, session, exportProfile(t), accepted)

			if n == 4095 {
				if err == nil || output.Len() != 0 || !errors.Is(manifest.CheckInput(session), artifact.ErrInvalidManifest) {
					t.Fatal("payload 4096 returned bytes or a success manifest")
				}
				t.Log("payload4096 rejected before writing")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := testFile("jp", []testString{{7, 0, utf16.Encode([]rune(target))}}, nil, n+1)
			if !bytes.Equal(output.Bytes(), want) {
				t.Fatal("payload/count width boundary differs from independent bytes")
			}
			countBytes := []byte{63}
			if n+1 == 64 {
				countBytes = []byte{64, 1}
			} else if n+1 == 4095 {
				countBytes = []byte{127, 63}
			}
			if !bytes.Equal(output.Bytes()[24:24+len(countBytes)], countBytes) {
				t.Fatal("wrong minimal payload count at 63/64/4095 boundary")
			}
			// Task4 owns Observe. Exercise its existing bounded parser contract
			// here without imposing Import's bytes or UTF-8 limits on expansion.
			bmax := 10 + 1 + 12 + 1 + 2 + 2*4095 + 2
			observed, err := parse(output.Bytes(), bmax, nil)
			if err != nil || observed.entries[0].text != target {
				t.Fatalf("bounded output parser rejected legal expansion: %v", err)
			}
			if output.Len() <= a.limits.ArtifactBytes && n >= 2048 {
				t.Fatal("test did not exceed input artifact budget")
			}
			t.Logf("P=%d, encoded count=%x, output=%d; bounded output parse succeeds", n+1, countBytes, output.Len())
		})
	}
	t.Run("exact-input-budgets-do-not-limit-output", func(t *testing.T) {
		raw := testFile("en", []testString{{7, 0, []uint16{'A'}}, {8, 2, []uint16{'B'}}}, nil, 4)
		l := researchLimits()
		l.ArtifactBytes, l.Units, l.UnitTextBytes, l.TotalTextBytes = len(raw), 2, 1, 2
		a, err := NewWithLimits(l)
		if err != nil {
			t.Fatal(err)
		}
		session := importSession(t, a, raw)
		targets := map[string]string{"7": strings.Repeat("長", 2048), "8": "😀"}
		accepted := acceptedTexts(t, session, targets)
		var output bytes.Buffer

		_, err = a.Export(&output, session, exportProfile(t), accepted)

		if err != nil || output.Len() <= l.ArtifactBytes || len(targets["7"]) <= l.UnitTextBytes {
			t.Fatalf("input-only budget applied to legal Japanese expansion: %v", err)
		}
		bmax := 10 + 1 + 12*2 + 1 + 2 + 2*4095 + 2
		got, err := parse(output.Bytes(), bmax, nil)
		if err != nil || got.entries[0].text != targets["7"] || got.entries[1].text != targets["8"] {
			t.Fatalf("output observation parser applied input budget: %v", err)
		}
		t.Logf("Import=%d bytes, UTF8 budgets=1/2; output=%d bytes, texts=6144/4 bytes, P=2052", len(raw), output.Len())
	})
	t.Run("gaps-and-all-terminators-count", func(t *testing.T) {
		raw := testFile("jp", []testString{{7, 1, []uint16{'A'}}, {8, 4, nil}}, nil, 7)
		a := New()
		session := importSession(t, a, raw)
		accepted := acceptedTexts(t, session, map[string]string{"7": strings.Repeat("長", 4090), "8": ""})
		writer := &exportWriter{}

		manifest, err := a.Export(writer, session, exportProfile(t), accepted)

		if err == nil || writer.calls != 0 || !errors.Is(manifest.CheckInput(session), artifact.ErrInvalidManifest) {
			t.Fatal("P4096 including four gap units and two terminators reached writer")
		}
	})
	t.Run("supplementary-counts-as-two", func(t *testing.T) {
		raw := testFile("jp", []testString{{7, 0, []uint16{'A'}}}, nil, 2)
		a := New()
		session := importSession(t, a, raw)
		target := strings.Repeat("😀", 2047) + "長"
		accepted := acceptedTexts(t, session, map[string]string{"7": target})
		writer := &exportWriter{}

		_, err := a.Export(writer, session, exportProfile(t), accepted)

		if err == nil || writer.calls != 0 {
			t.Fatal("4096 UTF-16 payload units accepted as fewer Unicode scalars")
		}
	})
}

type exportWriter struct {
	calls int
	n     int
	err   error
}

func (w *exportWriter) Write(raw []byte) (int, error) {
	w.calls++
	if w.n < 0 {
		return len(raw), w.err
	}
	return w.n, w.err
}

func TestExportWriteErrors(t *testing.T) {
	injected := errors.New("injected writer failure")
	for _, tc := range []struct {
		name string
		n    int
		err  error
		want error
	}{
		{"error-before-bytes", 0, injected, injected},
		{"error-after-bytes", 5, injected, injected},
		{"error-with-full-count", -1, injected, injected},
		{"zero-short-write", 0, nil, io.ErrShortWrite},
		{"partial-short-write", 5, nil, io.ErrShortWrite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := New()
			session := importSession(t, a, testFile("jp", []testString{{7, 0, []uint16{'A'}}}, nil, 2))
			accepted := acceptedTexts(t, session, nil)
			writer := &exportWriter{n: tc.n, err: tc.err}

			manifest, err := a.Export(writer, session, exportProfile(t), accepted)

			if !errors.Is(err, tc.want) || writer.calls != 1 || !errors.Is(manifest.CheckInput(session), artifact.ErrInvalidManifest) {
				t.Fatalf("write error/short write lost or success manifest returned: %v", err)
			}
		})
	}
}
