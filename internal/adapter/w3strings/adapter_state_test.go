package w3strings

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"unicode/utf16"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func TestImportPrivateState(t *testing.T) {
	t.Run("input-and-returned-buffers", func(t *testing.T) {
		raw := testFile("jp", []testString{
			{10, 8, []uint16{'é'}}, {30, 1, utf16.Encode([]rune("A😀"))}, {20, 6, nil},
		}, []testKey{{15, 30}, {0, 30}}, 12)
		original := bytes.Clone(raw)
		a := New()
		session := importSession(t, a, raw)
		wantState := mustParse(t, original)
		wantKeys := relations(t, a, session)

		clear(raw)
		artifactCopy := session.Artifact()
		clear(artifactCopy)
		units := session.Units()
		sourceCopy := units[0].Source()
		clear(sourceCopy)
		units[0] = unit.TranslationUnit{}
		keys := relations(t, a, session)
		keys[0].Key, keys[0].UnitID = "changed", unit.UnitID{}
		keyBytes := []byte(keys[1].Key)
		clear(keyBytes)

		if !bytes.Equal(session.Artifact(), original) || !reflect.DeepEqual(a.source, wantState) ||
			!reflect.DeepEqual(relations(t, a, session), wantKeys) || string(session.Units()[0].Source()) != "é" {
			t.Fatal("input or accessor mutation reached private import state")
		}
		if !reflect.DeepEqual(a.source.physical, []int{1, 2, 0}) ||
			!reflect.DeepEqual(a.source.gaps, []payloadGap{{0, 1}, {5, 1}, {7, 1}, {10, 2}}) {
			t.Fatal("private physical layout/gaps changed")
		}
	})
	t.Run("foreign-and-replaced-import", func(t *testing.T) {
		raw := testFile("en", []testString{{7, 0, []uint16{'A'}}}, []testKey{{0, 7}}, 2)
		a := New()
		first := importSession(t, a, raw)
		foreign, err := unit.NewSession(first.Artifact(), first.Units())
		if err != nil {
			t.Fatal(err)
		}
		otherAdapter := New()
		other := importSession(t, otherAdapter, raw)
		for _, session := range []unit.Session{{}, foreign, other} {
			if keys, err := a.KeyRelations(session); !errors.Is(err, unit.ErrInvalidSession) || keys != nil {
				t.Fatalf("foreign/zero import accepted: %v", err)
			}
		}
		copyOfFirst := first
		if len(relations(t, a, copyOfFirst)) != 1 {
			t.Fatal("copy of current import rejected")
		}

		second := importSession(t, a, raw)

		if first.SameImport(second) || !a.session.SameImport(second) {
			t.Fatal("new successful import reused stale session")
		}
		if keys, err := a.KeyRelations(first); !errors.Is(err, unit.ErrInvalidSession) || keys != nil {
			t.Fatalf("replaced import still accepted: %v", err)
		}
		if len(relations(t, a, second)) != 1 {
			t.Fatal("current import lost key")
		}
	})
	t.Run("failed-import-is-atomic", func(t *testing.T) {
		good := testFile("en", []testString{{7, 0, []uint16{'A'}}}, []testKey{{19, 7}}, 2)
		late := testFile("jp", []testString{{1, 0, []uint16{'B'}}, {2, 2, []uint16{0}}}, nil, 4)
		for _, bad := range [][]byte{nil, good[:len(good)-1], late} {
			a := New()
			first := importSession(t, a, good)
			source, keys := a.source, relations(t, a, first)

			rejected, err := a.Import(bad, validate.Profile{})

			if err == nil || rejected.Check() == nil || !a.session.SameImport(first) ||
				a.source != source || !reflect.DeepEqual(relations(t, a, first), keys) {
				t.Fatalf("failed import partially replaced prior state: %v", err)
			}
		}
	})
	t.Run("failure-before-first-import", func(t *testing.T) {
		a := New()

		session, err := a.Import(nil, validate.Profile{})

		if err == nil || session.Check() == nil || a.session.Check() == nil || a.source != nil || a.keys != nil {
			t.Fatal("failed initial import retained state")
		}
		if keys, err := a.KeyRelations(session); !errors.Is(err, unit.ErrInvalidSession) || keys != nil {
			t.Fatal("failed session gained accessor access")
		}
	})
}

func TestImportLimits(t *testing.T) {
	raw := testFile("jp", []testString{{1, 0, []uint16{'A'}}, {2, 2, utf16.Encode([]rune("😀é"))}}, nil, 6)
	for _, tc := range []struct {
		name   string
		actual int
		set    func(*config.Limits, int)
	}{
		{"artifact bytes", len(raw), func(l *config.Limits, n int) { l.ArtifactBytes = n }},
		{"units", 2, func(l *config.Limits, n int) { l.Units = n }},
		{"unit text bytes", 6, func(l *config.Limits, n int) { l.UnitTextBytes = n }},
		{"total text bytes", 7, func(l *config.Limits, n int) { l.TotalTextBytes = n }},
	} {
		for _, delta := range []int{-1, 0, 1} {
			t.Run(fmt.Sprintf("%s/maximum=%d", tc.name, tc.actual+delta), func(t *testing.T) {
				l := researchLimits()
				tc.set(&l, tc.actual+delta)
				a, err := NewWithLimits(l)
				if err != nil {
					t.Fatal(err)
				}
				before := importSession(t, a, testFile("en", []testString{{9, 0, nil}}, nil, 1))
				source := a.source

				session, err := a.Import(raw, validate.Profile{})

				if delta < 0 {
					var limit *config.LimitError
					if session.Check() == nil || !errors.Is(err, config.ErrLimitExceeded) || !errors.As(err, &limit) ||
						limit.Limit != tc.name || limit.Actual != tc.actual || limit.Maximum != tc.actual-1 ||
						!a.session.SameImport(before) || a.source != source {
						t.Fatalf("wrong rejection or partial retention: %v", err)
					}
					relations(t, a, before)
				} else if err != nil || session.UnitCount() != 2 || !a.session.SameImport(session) {
					t.Fatalf("exact UTF-8 input budget rejected: %v", err)
				}
			})
		}
	}
	t.Run("constructors-and-defaults", func(t *testing.T) {
		for _, l := range []config.Limits{{}, func() config.Limits {
			l := config.DefaultLimits()
			l.GenerationTimeout = 0
			return l
		}()} {
			if a, err := NewWithLimits(l); !errors.Is(err, config.ErrInvalidLimits) || a != nil {
				t.Fatalf("invalid limits accepted: %v", err)
			}
		}
		a := New()
		if a.limits != config.DefaultLimits() {
			t.Fatal("constructor expanded default limits")
		}
		session, err := a.Import(fixture(t, "en.w3strings"), validate.Profile{})
		if !errors.Is(err, config.ErrLimitExceeded) || session.Check() == nil || a.source != nil || a.session.Check() == nil {
			t.Fatalf("research fixture accepted under defaults: %v", err)
		}
	})
}
