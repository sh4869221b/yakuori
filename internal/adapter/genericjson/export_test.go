package genericjson

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func approved(t *testing.T, session unit.Session, texts ...string) (validate.Profile, []validate.AcceptedTranslation) {
	t.Helper()
	profile, err := validate.NewProfile([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	accepted := make([]validate.AcceptedTranslation, len(texts))
	for i, u := range session.Units() {
		accepted[i], err = validate.Validate(session, u.ID(), profile, texts[i])
		if err != nil {
			t.Fatal(err)
		}
	}
	return profile, accepted
}

func TestGenericJSONFinalComparison(t *testing.T) {
	const aInput = `{"id":"a","source":"{x} one","translation":"stale","protection":[{"start":0,"end":3,"kind":"placeholder","ordered":false}],"context":{"speaker":"keeper","scene":"inn"}}`
	const bInput = `{"id":"b","source":"{x} one","protection":[{"start":0,"end":3,"kind":"placeholder","ordered":false}],"context":{"speaker":"guard","scene":"gate"}}`
	const aOutput = `{"id":"a","source":"{x} one","translation":"{x} 一","protection":[{"start":0,"end":3,"kind":"placeholder","ordered":false}],"context":{"scene":"inn","speaker":"keeper"}}`
	const bOutput = `{"id":"b","source":"{x} one","translation":"{x} 二","protection":[{"start":0,"end":3,"kind":"placeholder","ordered":false}],"context":{"scene":"gate","speaker":"guard"}}`
	const good = header + "[" + aOutput + "," + bOutput + "]}"
	reformatted := strings.Replace(good, `"scene":"inn","speaker":"keeper"`, `"speaker":"keeper", "scene":"inn"`, 1)
	reformatted = " \n" + strings.Replace(reformatted, "一", `\u4e00`, 1) + "\t"

	a := setup(t, config.DefaultLimits())
	session := importOK(t, a, []byte(header+"["+aInput+","+bInput+"]}"))
	profile, accepted := approved(t, session, "{x} 一", "{x} 二")
	slices.Reverse(accepted)

	var out bytes.Buffer
	manifest, err := a.Export(&out, session, profile, accepted)
	if err != nil || out.String() != good || manifest.CheckInput(session) != nil {
		t.Fatalf("export = %q, err = %v", &out, err)
	}
	expected, err := artifact.PrepareExport(session, profile, accepted)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name, wire string
		observe    error
		compare    error
	}{
		{"published", out.String(), nil, nil},
		{"equivalent-format", reformatted, nil, nil},
		{"missing-unit", header + "[" + aOutput + "]}", nil, artifact.ErrFinalMismatch},
		{"duplicate-id", strings.Replace(good, `"id":"b"`, `"id":"a"`, 1), unit.ErrDuplicateUnit, nil},
		{"missing-id", strings.Replace(good, `"id":"b",`, "", 1), errRequired, nil},
		{"extra-unit", header + "[" + aOutput + "," + bOutput + `,{"id":"c","source":"","translation":""}]}`, nil, artifact.ErrFinalMismatch},
		{"foreign-id", strings.Replace(good, `"id":"b"`, `"id":"c"`, 1), nil, artifact.ErrFinalMismatch},
		{"reordered", header + "[" + bOutput + "," + aOutput + "]}", nil, artifact.ErrFinalMismatch},
		{"source", strings.Replace(good, `"source":"{x} one"`, `"source":"{x} two"`, 1), nil, artifact.ErrFinalMismatch},
		{"swapped-translations", header + "[" + strings.Replace(aOutput, "一", "二", 1) + "," + strings.Replace(bOutput, "二", "一", 1) + "]}", nil, artifact.ErrFinalMismatch},
		{"protection", strings.Replace(good, `"ordered":false`, `"ordered":true`, 1), nil, artifact.ErrFinalMismatch},
		{"protection-undeclared-literal", strings.Replace(good, "{x} one", "{x} one {x}", 1), protect.ErrInvalidSource, nil},
		{"protection-invalid-position", strings.Replace(good, `"end":3`, `"end":99`, 1), unit.ErrInvalidProtectionSpan, nil},
		{"speaker", strings.Replace(good, "keeper", "guard", 1), nil, artifact.ErrFinalMismatch},
		{"scene", strings.Replace(good, "gate", "inn", 1), nil, artifact.ErrFinalMismatch},
		{"scope", strings.Replace(good, "sample-game", "other-game", 1), nil, artifact.ErrFinalMismatch},
		{"artifact", strings.Replace(good, "dialogue", "other", 1), nil, artifact.ErrFinalMismatch},
		{"source-language", strings.Replace(good, `"en"`, `"de"`, 1), nil, artifact.ErrFinalMismatch},
		{"target-language", strings.Replace(good, `"ja"`, `"de"`, 1), nil, artifact.ErrFinalMismatch},
		{"schema", strings.Replace(good, "yakuori.generic-json", "foreign.generic-json", 1), errVersion, nil},
		{"version", strings.Replace(good, `"version":1`, `"version":2`, 1), errVersion, nil},
		{"missing-translation", strings.Replace(good, `"translation":"{x} 一",`, "", 1), errRequired, nil},
		{"translation-nul", strings.Replace(good, "一", `\u0000`, 1), errJSON, nil},
		{"translation-lone-surrogate", strings.Replace(good, "一", `\ud800`, 1), errJSON, nil},
		{"translation-invalid-utf8", strings.Replace(good, "一", "\xff", 1), errJSON, nil},
		{"malformed-json", good[:len(good)-1], errJSON, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			observed, err := a.Observe(strings.NewReader(test.wire))
			if !errors.Is(err, test.observe) {
				t.Fatalf("Observe = %v, want %v", err, test.observe)
			}
			if err != nil {
				return
			}
			if err := artifact.CompareFinal(manifest, expected, observed); !errors.Is(err, test.compare) {
				t.Fatalf("CompareFinal = %v, want %v", err, test.compare)
			}
		})
	}
}

func TestGenericJSONEmptyAndAbsentFields(t *testing.T) {
	const source = header + `[{"id":"a","source":"","translation":"stale","protection":[],"context":{}},{"id":"b","source":""}]}`
	const target = header + `[{"id":"a","source":"","translation":"","protection":[],"context":{}},{"id":"b","source":"","translation":""}]}`
	a := setup(t, config.DefaultLimits())
	session := importOK(t, a, []byte(source))
	profile, accepted := approved(t, session, "", "")
	var out bytes.Buffer
	manifest, err := a.Export(&out, session, profile, accepted)
	if err != nil || out.String() != target {
		t.Fatalf("export = %q, err = %v", &out, err)
	}
	expected, err := artifact.PrepareExport(session, profile, accepted)
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []struct {
		output string
		want   error
	}{
		{target, nil},
		{strings.Replace(target, `,"protection":[]`, "", 1), artifact.ErrFinalMismatch},
		{strings.Replace(target, `,"context":{}`, "", 1), artifact.ErrFinalMismatch},
	} {
		observed, err := a.Observe(strings.NewReader(attempt.output))
		if err != nil {
			t.Fatal(err)
		}
		if err := artifact.CompareFinal(manifest, expected, observed); !errors.Is(err, attempt.want) {
			t.Fatalf("CompareFinal = %v, want %v", err, attempt.want)
		}
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestGenericJSONExportLimitsAndBinding(t *testing.T) {
	const input = header + `[{"id":"a","source":"x"}]}`
	const emptyOutput = header + `[{"id":"a","source":"x","translation":""}]}`
	limits := config.DefaultLimits()
	// Tabs occupy one decoded byte and two encoded bytes. Only the second
	// boundary reaches ArtifactBytes while remaining below UnitTextBytes.
	const tabs = 600
	exactWire := strings.Repeat("\t", tabs) + strings.Repeat("x", limits.ArtifactBytes-len(emptyOutput)-2*tabs)
	a := setup(t, limits)
	session := importOK(t, a, []byte(input))
	for _, test := range []struct {
		name, text string
		want       error
	}{
		{"exact-unit-text", strings.Repeat("x", limits.UnitTextBytes), nil},
		{"unit-text-exceeded", strings.Repeat("x", limits.UnitTextBytes+1), config.ErrLimitExceeded},
		{"exact-encoded-size", exactWire, nil},
		{"encoded-size-exceeded", exactWire + "x", config.ErrLimitExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile, accepted := approved(t, session, test.text)
			var out bytes.Buffer
			_, err := a.Export(&out, session, profile, accepted)
			if !errors.Is(err, test.want) || (err != nil && out.Len() != 0) {
				t.Fatalf("export bytes = %d, err = %v, want %v", out.Len(), err, test.want)
			}
			if err != nil {
				return
			}
			observed, err := a.Observe(bytes.NewReader(out.Bytes()))
			if err != nil || observed.Units[0].Text != test.text {
				t.Fatalf("Observe error = %v", err)
			}
			if test.name == "exact-encoded-size" {
				if out.Len() != limits.ArtifactBytes {
					t.Fatalf("encoded bytes = %d, want %d", out.Len(), limits.ArtifactBytes)
				}
				if _, err := a.Observe(strings.NewReader(out.String() + " ")); !errors.Is(err, config.ErrLimitExceeded) {
					t.Fatalf("oversized Observe error = %v", err)
				}
			}
		})
	}
	profile, accepted := approved(t, session, "x")
	foreign := importOK(t, setup(t, limits), []byte(input))
	var out bytes.Buffer
	if _, err := a.Export(&out, foreign, profile, accepted); !errors.Is(err, unit.ErrInvalidSession) || out.Len() != 0 {
		t.Fatalf("foreign import exported bytes=%d, err=%v", out.Len(), err)
	}
	if _, err := a.Export(&out, session, profile, nil); !errors.Is(err, artifact.ErrIncompleteExport) || out.Len() != 0 {
		t.Fatalf("incomplete export returned bytes=%d, err=%v", out.Len(), err)
	}
	if _, err := a.Export(shortWriter{}, session, profile, accepted); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short writer export error = %v", err)
	}
}
