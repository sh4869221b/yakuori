package artifact_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

var protectedOutputs = []fakeRecord{
	{"dialogue", " \r\n<b>こんにちは {p}</b>\r\n "},
	{"empty", ""},
	{"protected-only", " {p}\r\n"},
	{"name", "Geralt"},
}

func protectedAdapterFixture(t *testing.T) *fakeAdapter {
	t.Helper()
	profile, err := validate.NewProfile([32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	raw := encodeFake(fakeImage{
		adapter: "fixture", schema: "v1", language: "en",
		records:  []fakeRecord{{"dialogue", "<b>Hello {p}</b>"}, {"empty", ""}, {"protected-only", " {p}\r\n"}, {"name", "Geralt"}},
		keys:     []fakeKey{{"dialogue", "dialogue-key"}, {"empty", "empty-key"}, {"protected-only", "protected-key"}, {"name", "name-key"}},
		metadata: map[string][]byte{"header": {7}, "language": []byte("en")},
	})
	a, err := importFake(raw, profile, map[string][]unit.ProtectionSpan{
		"dialogue": {
			{Start: 0, End: 3, Kind: unit.OpenTag, Pair: "b", Ordered: true},
			{Start: 9, End: 12, Kind: unit.Placeholder},
			{Start: 12, End: 16, Kind: unit.CloseTag, Pair: "b", Ordered: true},
		},
		"protected-only": {{Start: 1, End: 4, Kind: unit.Placeholder}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func protectedAccepted(t *testing.T, a *fakeAdapter, generated bool) []validate.AcceptedTranslation {
	t.Helper()
	var accepted []validate.AcceptedTranslation
	for i, u := range a.session.Units() {
		prepared, err := protect.Prepare(a.session, u.ID())
		if err != nil {
			t.Fatal(err)
		}
		candidate := protectedOutputs[i].text
		generate := u.ID().StableID() == "dialogue" || u.ID().StableID() == "name"
		if prepared.GenerationRequired() != generate {
			t.Fatalf("%s generation required = %v", u.ID().StableID(), prepared.GenerationRequired())
		}
		if !prepared.GenerationRequired() {
			candidate = string(u.Source())
		} else if generated {
			tokenized := candidate
			if u.ID().StableID() == "dialogue" {
				tokenized = " \r\n[[YAKUORI_0_0]]こんにちは [[YAKUORI_0_1]][[YAKUORI_0_2]]\r\n "
			}
			candidate, err = prepared.Restore(a.session, u.ID(), tokenized)
			if err != nil {
				t.Fatal(err)
			}
		}
		translation, err := validate.Validate(a.session, u.ID(), a.profile, candidate)
		if err != nil || translation.Text() != protectedOutputs[i].text {
			t.Fatalf("%s accepted = %q, error = %v", u.ID().StableID(), translation.Text(), err)
		}
		findings := translation.ReviewFindings()
		if u.ID().StableID() == "name" {
			if len(findings) != 3 || findings[0].Code != validate.SourceIdentical || findings[1].Code != validate.PossibleProperName || findings[2].Code != validate.TargetLanguageUncertain {
				t.Fatalf("name findings = %v", findings)
			}
		} else if len(findings) != 0 {
			t.Fatalf("%s unexpected findings = %v", u.ID().StableID(), findings)
		}
		accepted = append(accepted, translation)
	}
	return accepted
}

func TestAdapterProtectedBoundaryRoundTrip(t *testing.T) {
	for _, generated := range []bool{true, false} {
		name := "TM direct"
		if generated {
			name = "generated restored"
		}
		t.Run(name, func(t *testing.T) {
			a := protectedAdapterFixture(t)
			accepted := protectedAccepted(t, a, generated)
			stage, expected, err := a.stage(a.session, a.profile, accepted)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.checkStage(stage, expected); err != nil {
				t.Fatal(err)
			}
			actual, err := decodeFake(stage)
			if err != nil {
				t.Fatal(err)
			}
			if a.exports != 1 || expected.Len() != 4 || actual.language != "ja" || !slices.Equal(actual.records, protectedOutputs) {
				t.Fatalf("exports = %d, count = %d, actual stage = %+v", a.exports, expected.Len(), actual)
			}
		})
	}
}

func TestAdapterProtectedBoundaryRejectsInvalid(t *testing.T) {
	for _, tt := range []struct{ name, candidate string }{
		{"missing tag", "こんにちは {p}</b>"},
		{"content wrapper", "```ja\n<b>こんにちは {p}</b>\n```"},
		{"explanation prefix", "翻訳: <b>こんにちは {p}</b>"},
		{"repeated content", "<b>こんにちは {p}</b>\n訳\n訳\n訳"},
		{"payload loss", "<b> \r\n{p}</b>"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, generated := range []bool{true, false} {
				name := "TM direct"
				if generated {
					name = "generated restored"
				}
				t.Run(name, func(t *testing.T) {
					a := protectedAdapterFixture(t)
					accepted := protectedAccepted(t, a, generated)
					id := a.session.Units()[0].ID()
					candidate := tt.candidate
					if generated {
						prepared, err := protect.Prepare(a.session, id)
						if err != nil {
							t.Fatal(err)
						}
						tokenized := strings.NewReplacer("<b>", "[[YAKUORI_0_0]]", "{p}", "[[YAKUORI_0_1]]", "</b>", "[[YAKUORI_0_2]]").Replace(candidate)
						candidate, err = prepared.Restore(a.session, id, tokenized)
						structureFailure := tt.name == "missing tag" || tt.name == "payload loss"
						if structureFailure {
							if !errors.Is(err, protect.ErrInvalidCandidate) || candidate != "" {
								t.Fatalf("restored = %q, error = %v", candidate, err)
							}
						} else if err != nil || candidate != tt.candidate {
							t.Fatalf("restored = %q, error = %v", candidate, err)
						}
					}
					translation, err := validate.Validate(a.session, id, a.profile, candidate)
					if !errors.Is(err, validate.ErrInvalidCandidate) || translation != (validate.AcceptedTranslation{}) {
						t.Fatalf("accepted = %#v, error = %v", translation, err)
					}
					accepted[0] = translation
					stage, expected, err := a.stage(a.session, a.profile, accepted)
					if err == nil || stage != nil || expected.Len() != 0 || a.exports != 0 {
						t.Fatalf("invalid candidate reached writer: calls=%d, bytes=%d, count=%d, error=%v", a.exports, len(stage), expected.Len(), err)
					}
				})
			}
		})
	}
	for _, omitted := range []int{1, 2} {
		t.Run("missing "+protectedOutputs[omitted].id, func(t *testing.T) {
			a := protectedAdapterFixture(t)
			accepted := protectedAccepted(t, a, false)
			accepted = slices.Delete(accepted, omitted, omitted+1)
			stage, expected, err := a.stage(a.session, a.profile, accepted)
			if !errors.Is(err, artifact.ErrIncompleteExport) || stage != nil || expected.Len() != 0 || a.exports != 0 {
				t.Fatalf("incomplete export reached writer: calls=%d, bytes=%d, count=%d, error=%v", a.exports, len(stage), expected.Len(), err)
			}
		})
	}
}
