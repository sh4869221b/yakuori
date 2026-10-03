package validate

import (
	"errors"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func TestContentRejectsAdditions(t *testing.T) {
	if Schema != "restored-content-v1" {
		t.Fatalf("schema = %q", Schema)
	}
	for _, tt := range []struct{ name, candidate string }{
		{"code fence", "```\n訳 {p}\n```"},
		{"code fence language", " \r\n```ja_1-test\r\n訳 {p}\r\n```\r\n "},
		{"sole JSON translation", `{"translation":"訳 {p}"}`},
		{"XML translation", "<translation>訳 {p}</translation>"},
		{"English prefix", "Translation: 訳 {p}"},
		{"long English prefix", "Here is the translation: 訳 {p}"},
		{"Japanese prefix", "翻訳: 訳 {p}"},
		{"long Japanese prefix", "以下が翻訳です: 訳 {p}"},
		{"LF repeated lines", "{p}\n訳\n訳\n訳"},
		{"mixed CRLF LF repeated lines", "{p}\r\n訳\r\n訳\n訳"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			session, id, profile := protectedFixture(t, "Hello {p}", []unit.ProtectionSpan{{Start: 6, End: 9, Kind: unit.Placeholder}})
			prepared, err := protect.Prepare(session, id)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := prepared.Restore(session, id, strings.ReplaceAll(tt.candidate, "{p}", "[[YAKUORI_0_0]]"))
			if err != nil || restored != tt.candidate {
				t.Fatalf("restored = %q, error = %v", restored, err)
			}
			for _, path := range []struct{ name, candidate string }{{"generated restored", restored}, {"TM direct", tt.candidate}} {
				t.Run(path.name, func(t *testing.T) {
					accepted, err := Validate(session, id, profile, path.candidate)
					if !errors.Is(err, ErrInvalidCandidate) || accepted != (AcceptedTranslation{}) {
						t.Fatalf("accepted = %#v, error = %v", accepted, err)
					}
				})
			}
		})
	}
}

func TestContentSourceExemptions(t *testing.T) {
	for _, tt := range []struct{ name, source, candidate string }{
		{"same fence class", "```en\nHello\n```", "```ja\nこんにちは\n```"},
		{"same JSON class", `{"translation":"Hello"}`, `{"translation":"こんにちは"}`},
		{"same XML class", "<translation>Hello</translation>", "<translation>こんにちは</translation>"},
		{"same English prefix", "Translation: Hello", "Translation: こんにちは"},
		{"same long English prefix", "Here is the translation: Hello", "Here is the translation: こんにちは"},
		{"same Japanese prefix", "翻訳: Hello", "翻訳: こんにちは"},
		{"same long Japanese prefix", "以下が翻訳です: Hello", "以下が翻訳です: こんにちは"},
		{"two repeated lines", "Hello", "訳\n訳"},
		{"source baseline three accepts three", "A\nA\nA", "訳\n訳\n訳"},
		{"source baseline CRLF", "A\r\nA\r\nA", "訳\n訳\n訳"},
		{"exact Unicode CRLF bytes", "Hello", " \r\nこんにちは e\u0301\r\n "},
		{"ordinary dialogue", "Hello", "I think this is the translation."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			session, ids, profile := fixture(t, tt.source)
			accepted, err := Validate(session, ids[0], profile, tt.candidate)
			if err != nil || accepted.Text() != tt.candidate {
				t.Fatalf("accepted = %q, error = %v", accepted.Text(), err)
			}
		})
	}
}

func TestContentFiniteBoundaries(t *testing.T) {
	for _, tt := range []struct{ name, candidate string }{
		{"incomplete code fence", "```\n訳"},
		{"non-ASCII language", "```日本語\n訳\n```"},
		{"language with space", "``` ja\n訳\n```"},
		{"different closing fence", "```\n訳\n```extra"},
		{"JSON duplicate members", `{"translation":"訳","translation":"別訳"}`},
		{"JSON extra member", `{"translation":"訳","other":"value"}`},
		{"JSON other key", `{"text":"訳"}`},
		{"JSON number value", `{"translation":3}`},
		{"JSON null value", `{"translation":null}`},
		{"JSON object value", `{"translation":{"text":"訳"}}`},
		{"JSON array", `["訳"]`},
		{"JSON string", `"訳"`},
		{"JSON trailing object", `{"translation":"訳"} {}`},
		{"XML attribute", `<translation lang="ja">訳</translation>`},
		{"partial XML wrapper", "<translation>訳"},
		{"lowercase prefix", "translation: 訳"},
		{"embedded prefix", "A Translation: 訳"},
		{"single-line repetition", "訳訳訳"},
		{"different line whitespace", "訳\n 訳\n訳"},
		{"blank line interrupts run", "訳\n訳\n \n訳"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			session, ids, profile := fixture(t, "Hello")
			accepted, err := Validate(session, ids[0], profile, tt.candidate)
			if err != nil || accepted.Text() != tt.candidate {
				t.Fatalf("accepted = %q, error = %v", accepted.Text(), err)
			}
		})
	}
	for _, tt := range []struct{ name, source, candidate string }{
		{"source baseline three rejects four", "A\nA\nA", "訳\n訳\n訳\n訳"},
		{"source baseline two rejects three", "A\nA", "訳\n訳\n訳"},
		{"wrapper class differs", "```\nHello\n```", `{"translation":"訳"}`},
		{"duplicate source members give no wrapper exemption", `{"translation":"Hello","translation":"Bye"}`, `{"translation":"訳"}`},
		{"prefix differs", "Translation: Hello", "翻訳: 訳"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			session, ids, profile := fixture(t, tt.source)
			prepared, err := protect.Prepare(session, ids[0])
			if err != nil {
				t.Fatal(err)
			}
			restored, err := prepared.Restore(session, ids[0], tt.candidate)
			if err != nil || restored != tt.candidate {
				t.Fatalf("restored = %q, error = %v", restored, err)
			}
			for _, path := range []struct{ name, candidate string }{{"generated restored", restored}, {"TM direct", tt.candidate}} {
				t.Run(path.name, func(t *testing.T) {
					accepted, err := Validate(session, ids[0], profile, path.candidate)
					if !errors.Is(err, ErrInvalidCandidate) || accepted != (AcceptedTranslation{}) {
						t.Fatalf("accepted = %#v, error = %v", accepted, err)
					}
				})
			}
		})
	}
}
