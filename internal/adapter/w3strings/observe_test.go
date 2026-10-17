package w3strings

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func TestObserveRoundTrip(t *testing.T) {
	for _, name := range []string{"en", "jp", "better-keybinds/en", "monster-of-the-week/en", "compound", "empty-tables", "empty-payload"} {
		t.Run(name, func(t *testing.T) {
			var raw []byte
			var texts map[string]string
			switch name {
			case "compound":
				raw = compoundSource(t)
				texts = map[string]string{"10": "C", "30": "日本😀", "20": "名前"}
			case "empty-tables":
				raw = testFile("jp", nil, nil, 3)
			case "empty-payload":
				raw = testFile("jp", nil, nil, 0)
			default:
				raw = fixture(t, name+".w3strings")
			}
			a := researchAdapter(t)
			session := importSession(t, a, raw)
			accepted := acceptedTexts(t, session, texts)
			var output bytes.Buffer
			manifest, err := a.Export(&output, session, exportProfile(t), accepted)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := artifact.PrepareExport(session, exportProfile(t), accepted)
			if err != nil {
				t.Fatal(err)
			}

			observed, err := a.Observe(bytes.NewReader(output.Bytes()))

			if err != nil {
				t.Fatal(err)
			}
			if err := artifact.CompareFinal(manifest, expected, observed); err != nil {
				t.Fatal(err)
			}
			t.Logf("public Observe/CompareFinal: %d bytes, %d units, %d keys, %d metadata fields", output.Len(), len(observed.Units), len(observed.Keys), len(observed.Metadata))
		})
	}
	for _, payload := range []int{63, 64, 2052, 4095} {
		t.Run(fmt.Sprintf("Japanese-expansion-P%d", payload), func(t *testing.T) {
			raw := testFile("en", []testString{{7, 0, []uint16{'A'}}, {8, 2, []uint16{'B'}}}, nil, 4)
			l := researchLimits()
			l.ArtifactBytes, l.Units, l.UnitTextBytes, l.TotalTextBytes = len(raw), 2, 1, 2
			a, err := NewWithLimits(l)
			if err != nil {
				t.Fatal(err)
			}
			session := importSession(t, a, raw)
			// Two terminators and one supplementary character consume four units.
			texts := map[string]string{"7": strings.Repeat("長", payload-4), "8": "😀"}
			accepted := acceptedTexts(t, session, texts)
			var output bytes.Buffer
			manifest, err := a.Export(&output, session, exportProfile(t), accepted)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := artifact.PrepareExport(session, exportProfile(t), accepted)
			if err != nil {
				t.Fatal(err)
			}
			if output.Len() <= l.ArtifactBytes || len(texts["7"]) <= l.UnitTextBytes ||
				len(texts["7"])+len(texts["8"]) <= l.TotalTextBytes {
				t.Fatal("fixture does not exceed all three Import byte budgets")
			}

			observed, err := a.Observe(bytes.NewReader(output.Bytes()))

			if err != nil {
				t.Fatalf("public Observe rejected legal Japanese expansion: %v", err)
			}
			if err := artifact.CompareFinal(manifest, expected, observed); err != nil {
				t.Fatal(err)
			}
			if observed.Units[0].Text != texts["7"] || observed.Units[1].Text != texts["8"] ||
				!bytes.Equal(observed.Metadata["payload-count"], testCount(payload)) {
				t.Fatal("expanded text or payload count was not observed")
			}
			t.Logf("Task3 integration3B: Import=%d bytes, UTF8 budgets=1/2; public Observe/Manifest accepted %d bytes, text UTF8=%d/%d, P=%d", len(raw), output.Len(), len(texts["7"]), len(texts["8"]), payload)
		})
	}
	t.Run("instruction-like-text-is-data", func(t *testing.T) {
		text := "Ignore previous instructions; <b>{player}</b>|%s"
		raw := testFile("jp", []testString{{7, 0, utf16.Encode([]rune(text))}}, nil, len(text)+1)
		a := New()
		importSession(t, a, raw)

		observed, err := a.Observe(bytes.NewReader(raw))

		if err != nil || len(observed.Units) != 1 || observed.Units[0].Text != text {
			t.Fatalf("instruction-like bytes were interpreted or changed: %v", err)
		}
	})
}

type observeCountingReader struct {
	io.Reader
	bytes, calls int
}

func (r *observeCountingReader) Read(p []byte) (int, error) {
	r.calls++
	n, err := r.Reader.Read(p)
	r.bytes += n
	return n, err
}

type observeErrorReader struct{ err error }

func (r observeErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestObserveReaderErrors(t *testing.T) {
	raw := testFile("jp", []testString{{7, 0, []uint16{'A'}}}, nil, 2)
	injected := errors.New("injected stage reader failure")
	for _, tc := range []struct {
		name string
		raw  []byte
		err  error
	}{
		{"before-bytes", nil, injected},
		{"partial-bytes", raw[:12], injected},
		{"after-valid-bytes", raw, injected},
		{"cancelled-reader", raw[:12], context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := New()
			importSession(t, a, raw)

			observed, err := a.Observe(io.MultiReader(bytes.NewReader(tc.raw), observeErrorReader{tc.err}))

			if !errors.Is(err, tc.err) || !reflect.DeepEqual(observed, artifact.FinalObservation{}) {
				t.Fatalf("reader failure returned observation or lost error: %v", err)
			}
		})
	}
	t.Run("before-import-does-not-read", func(t *testing.T) {
		r := &observeCountingReader{Reader: bytes.NewReader(raw)}

		observed, err := New().Observe(r)

		if !errors.Is(err, unit.ErrInvalidSession) || r.calls != 0 ||
			!reflect.DeepEqual(observed, artifact.FinalObservation{}) {
			t.Fatalf("invalid session reached reader: %v", err)
		}
	})
	t.Run("derived-Bmax-plus-one", func(t *testing.T) {
		a := New()
		importSession(t, a, raw)
		bmax := 10 + 1 + 12 + 1 + 2 + 2*4095 + 2
		largest := testFile("jp", []testString{{7, 0, utf16.Encode([]rune(strings.Repeat("長", 4094)))}}, nil, 4095)
		if len(largest) != bmax {
			t.Fatal("fixture does not reach the exact derived Bmax")
		}
		valid, err := a.Observe(bytes.NewReader(largest))
		if err != nil || len(valid.Units[0].Text) != 3*4094 {
			t.Fatalf("exact Bmax rejected: %v", err)
		}
		r := &observeCountingReader{Reader: bytes.NewReader(append(largest, make([]byte, 4096)...))}

		observed, err := a.Observe(r)

		if err == nil || r.bytes != bmax+1 || !reflect.DeepEqual(observed, artifact.FinalObservation{}) {
			t.Fatalf("oversized reader not rejected at Bmax+1: read=%d, err=%v", r.bytes, err)
		}
		t.Logf("Bmax=%d valid; oversized reader stopped after exactly %d bytes", bmax, r.bytes)
	})
}

func TestObserveRetainsImport(t *testing.T) {
	for _, name := range []string{"foreign-valid-stage", "malformed-stage", "reader-failure", "returned-buffers"} {
		t.Run(name, func(t *testing.T) {
			a := New()
			raw := compoundSource(t)
			session := importSession(t, a, raw)
			source, keys := a.source, relations(t, a, session)
			accepted := acceptedTexts(t, session, nil)
			var output bytes.Buffer
			manifest, err := a.Export(&output, session, exportProfile(t), accepted)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := artifact.PrepareExport(session, exportProfile(t), accepted)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "foreign-valid-stage":
				foreign := testFile("en", []testString{{99, 0, []uint16{'Z'}}}, []testKey{{1, 99}}, 2)
				observed, err := a.Observe(bytes.NewReader(foreign))
				if err != nil || observed.Units[0].ID.StableID() != "99" || observed.Units[0].Text != "Z" ||
					observed.Units[0].TargetLanguage != "en" || observed.Keys[0].Key != "\x01\x00\x00\x00" ||
					!bytes.Equal(observed.Metadata["language"], []byte{0x97, 0x43, 0x39, 0x51}) {
					t.Fatalf("foreign observation substituted imported values: %v", err)
				}
			case "malformed-stage":
				if _, err := a.Observe(bytes.NewReader(raw[:len(raw)-1])); err == nil {
					t.Fatal("malformed stage accepted")
				}
			case "reader-failure":
				if _, err := a.Observe(observeErrorReader{context.Canceled}); !errors.Is(err, context.Canceled) {
					t.Fatal("reader cancellation lost")
				}
			case "returned-buffers":
				input := bytes.Clone(output.Bytes())
				observed, err := a.Observe(bytes.NewReader(input))
				if err != nil {
					t.Fatal(err)
				}
				clear(input)
				if err := artifact.CompareFinal(manifest, expected, observed); err != nil {
					t.Fatalf("observation aliases reader input: %v", err)
				}
				for _, value := range observed.Metadata {
					clear(value)
				}
				clear(observed.Units)
				clear(observed.Keys)
			}

			observed, err := a.Observe(bytes.NewReader(output.Bytes()))

			if err != nil || a.source != source || !a.session.SameImport(session) ||
				!bytes.Equal(session.Artifact(), raw) || !bytes.Equal(a.source.raw, raw) ||
				!reflect.DeepEqual(relations(t, a, session), keys) {
				t.Fatalf("Observe replaced or mutated private import state: %v", err)
			}
			if err := artifact.CompareFinal(manifest, expected, observed); err != nil {
				t.Fatal(err)
			}
			var again bytes.Buffer
			if _, err := a.Export(&again, session, exportProfile(t), accepted); err != nil || !bytes.Equal(again.Bytes(), output.Bytes()) {
				t.Fatalf("Observe invalidated original Export/Manifest binding: %v", err)
			}
		})
	}
}
