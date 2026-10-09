package validate

import (
	"reflect"
	"testing"

	"github.com/sh4869221b/yakuori/internal/unit"
)

func TestReviewFindings(t *testing.T) {
	for _, tt := range []struct {
		name, source, candidate string
		want                    []ReviewCode
	}{
		{"unchanged name", "Geralt", "Geralt", []ReviewCode{SourceIdentical, PossibleProperName, TargetLanguageUncertain}},
		{"two-word Unicode name", "Éowyn O'Neill", "Éowyn O'Neill", []ReviewCode{SourceIdentical, PossibleProperName, TargetLanguageUncertain}},
		{"hyphen and combining mark", "E\u0301owyn-Elf", "E\u0301owyn-Elf", []ReviewCode{SourceIdentical, PossibleProperName, TargetLanguageUncertain}},
		{"unchanged lowercase", "hello", "hello", []ReviewCode{SourceIdentical, TargetLanguageUncertain}},
		{"unchanged multiline", "Geralt\r\nRivia", "Geralt\r\nRivia", []ReviewCode{SourceIdentical, TargetLanguageUncertain}},
		{"three words", "Geralt Of Rivia", "Geralt Of Rivia", []ReviewCode{SourceIdentical, TargetLanguageUncertain}},
		{"name with digit", "Geralt2", "Geralt2", []ReviewCode{SourceIdentical, TargetLanguageUncertain}},
		{"changed English-only text", "Hello", "Welcome", []ReviewCode{TargetLanguageUncertain}},
		{"Hiragana", "Hello", "こんにちは", nil},
		{"Katakana", "Hello", "ハロー", nil},
		{"Han", "Hello", "挨拶", nil},
		{"unchanged Japanese text", "こんにちは", "こんにちは", []ReviewCode{SourceIdentical}},
		{"empty", "", "", nil},
		{"whitespace-only", " \r\n", "\t", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			session, ids, profile := fixture(t, tt.source)
			accepted, err := Validate(session, ids[0], profile, tt.candidate)
			if err != nil || accepted.Text() != tt.candidate {
				t.Fatalf("accepted = %q, error = %v", accepted.Text(), err)
			}
			if err := CheckBinding(session, ids[0], profile, accepted); err != nil {
				t.Fatal(err)
			}
			var codes []ReviewCode
			for _, finding := range accepted.ReviewFindings() {
				codes = append(codes, finding.Code)
				if finding.Reason == "" {
					t.Fatal("finding reason is empty")
				}
			}
			if !reflect.DeepEqual(codes, tt.want) {
				t.Fatalf("codes = %v, want %v", codes, tt.want)
			}
		})
	}
}

func TestReviewProtectedPayload(t *testing.T) {
	for _, tt := range []struct {
		name, source, candidate string
		spans                   []unit.ProtectionSpan
		want                    []ReviewCode
	}{
		{"protected only", " {名前}\r\n", " {名前}\r\n", []unit.ProtectionSpan{{Start: 1, End: 9, Kind: unit.Placeholder}}, nil},
		{"protected script does not prove language", "Hi {名前}", "Hello {名前}", []unit.ProtectionSpan{{Start: 3, End: 11, Kind: unit.Placeholder}}, []ReviewCode{TargetLanguageUncertain}},
		{"nonprotected Japanese payload", "Hi {名前}", "こんにちは {名前}", []unit.ProtectionSpan{{Start: 3, End: 11, Kind: unit.Placeholder}}, nil},
		{"payload inspection is nonrecursive", "{p} あx Hello", "あ{p}x あx Welcome", []unit.ProtectionSpan{{Start: 0, End: 3, Kind: unit.Placeholder}, {Start: 4, End: 8, Kind: unit.Placeholder}}, nil},
		{"protected unchanged payload", "Geralt {p}", "Geralt {p}", []unit.ProtectionSpan{{Start: 7, End: 10, Kind: unit.Placeholder}}, []ReviewCode{SourceIdentical, TargetLanguageUncertain}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			session, id, profile := protectedFixture(t, tt.source, tt.spans)
			accepted, err := Validate(session, id, profile, tt.candidate)
			if err != nil || accepted.Text() != tt.candidate {
				t.Fatalf("accepted = %q, error = %v", accepted.Text(), err)
			}
			var codes []ReviewCode
			for _, finding := range accepted.ReviewFindings() {
				codes = append(codes, finding.Code)
			}
			if !reflect.DeepEqual(codes, tt.want) {
				t.Fatalf("codes = %v, want %v", codes, tt.want)
			}
		})
	}
}

func TestReviewFindingsSnapshot(t *testing.T) {
	session, ids, profile := fixture(t, "Geralt")
	accepted, err := Validate(session, ids[0], profile, "Geralt")
	if err != nil {
		t.Fatal(err)
	}
	copy := accepted
	want := accepted.ReviewFindings()
	returned := accepted.ReviewFindings()
	returned[0].Code = "modified"
	returned[1].Reason = "modified"
	returned = append(returned, ReviewFinding{Code: "extra"})
	if accepted != copy || !reflect.DeepEqual(accepted.ReviewFindings(), want) {
		t.Fatal("returned findings changed accepted translation")
	}
	if got := (AcceptedTranslation{}).ReviewFindings(); len(got) != 0 {
		t.Fatalf("zero accepted findings = %v", got)
	}
}

func TestReviewOtherTargetLanguage(t *testing.T) {
	for _, target := range []string{"en", "JA", "ja-JP"} {
		t.Run(target, func(t *testing.T) {
			id, err := unit.NewUnitID("fixture", "v1", target)
			if err != nil {
				t.Fatal(err)
			}
			u, err := unit.NewTranslationUnit(id, []byte("Hello"), "en", target, nil)
			if err != nil {
				t.Fatal(err)
			}
			session, err := unit.NewSession([]byte("artifact"), []unit.TranslationUnit{u})
			if err != nil {
				t.Fatal(err)
			}
			profile, err := NewProfile([32]byte{1})
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := Validate(session, id, profile, "Welcome")
			if err != nil || len(accepted.ReviewFindings()) != 0 {
				t.Fatalf("findings = %v, error = %v", accepted.ReviewFindings(), err)
			}
		})
	}
}
