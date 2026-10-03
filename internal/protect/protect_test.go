package protect_test

import (
	"testing"

	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/unit"
)

var dialogueSpans = []unit.ProtectionSpan{
	{Start: 0, End: 3, Kind: unit.OpenTag, Pair: "b", Ordered: true},
	{Start: 9, End: 17, Kind: unit.Placeholder},
	{Start: 17, End: 21, Kind: unit.CloseTag, Pair: "b", Ordered: true},
	{Start: 22, End: 24, Kind: unit.Placeholder},
}

func TestProtectionRoundTrip(t *testing.T) {
	tests := []struct {
		name, source, prepared, candidate, want string
		spans                                   []unit.ProtectionSpan
		generate                                bool
	}{
		{
			name: "Japanese dialogue and unordered placeholder movement", source: "<b>Hello {player}</b> %s", spans: dialogueSpans, generate: true,
			prepared:  "[[YAKUORI_0_0]]Hello [[YAKUORI_0_1]][[YAKUORI_0_2]] [[YAKUORI_0_3]]",
			candidate: " \r\n[[YAKUORI_0_3]][[YAKUORI_0_0]]こんにちは [[YAKUORI_0_1]][[YAKUORI_0_2]]\r\n ", want: " \r\n%s<b>こんにちは {player}</b>\r\n ",
		},
		{
			name: "repeated placeholder", source: "{p} and {p}", spans: []unit.ProtectionSpan{{Start: 0, End: 3, Kind: unit.Placeholder}, {Start: 8, End: 11, Kind: unit.Placeholder}}, generate: true,
			prepared: "[[YAKUORI_0_0]] and [[YAKUORI_0_1]]", candidate: "[[YAKUORI_0_1]] と [[YAKUORI_0_0]]", want: "{p} と {p}",
		},
		{
			name: "nested and repeated same-label tags", source: "<b>A<b>B</b>C</b><b>D</b>", generate: true,
			spans: []unit.ProtectionSpan{
				{Start: 0, End: 3, Kind: unit.OpenTag, Pair: "b", Ordered: true}, {Start: 4, End: 7, Kind: unit.OpenTag, Pair: "b", Ordered: true},
				{Start: 8, End: 12, Kind: unit.CloseTag, Pair: "b", Ordered: true}, {Start: 13, End: 17, Kind: unit.CloseTag, Pair: "b", Ordered: true},
				{Start: 17, End: 20, Kind: unit.OpenTag, Pair: "b", Ordered: true}, {Start: 21, End: 25, Kind: unit.CloseTag, Pair: "b", Ordered: true},
			},
			prepared:  "[[YAKUORI_0_0]]A[[YAKUORI_0_1]]B[[YAKUORI_0_2]]C[[YAKUORI_0_3]][[YAKUORI_0_4]]D[[YAKUORI_0_5]]",
			candidate: "[[YAKUORI_0_0]]あ[[YAKUORI_0_1]]い[[YAKUORI_0_2]]う[[YAKUORI_0_3]][[YAKUORI_0_4]]え[[YAKUORI_0_5]]", want: "<b>あ<b>い</b>う</b><b>え</b>",
		},
		{
			name: "original complete and incomplete token-like text", source: "[[YAKUORI_0_7]] [[YAKUORI_1_broken {p}", generate: true,
			spans:     []unit.ProtectionSpan{{Start: 35, End: 38, Kind: unit.Placeholder}},
			prepared:  "[[YAKUORI_0_7]] [[YAKUORI_1_broken [[YAKUORI_2_0]]",
			candidate: "[[YAKUORI_0_7]] [[YAKUORI_1_broken 日本語 [[YAKUORI_2_0]]", want: "[[YAKUORI_0_7]] [[YAKUORI_1_broken 日本語 {p}",
		},
		{
			name: "token-shaped protected literal", source: "Hi [[YAKUORI_0_7]]", generate: true,
			spans:    []unit.ProtectionSpan{{Start: 3, End: 18, Kind: unit.Placeholder}},
			prepared: "Hi [[YAKUORI_1_0]]", candidate: "こんにちは [[YAKUORI_1_0]]", want: "こんにちは [[YAKUORI_0_7]]",
		},
		{name: "empty", source: "", prepared: "", candidate: "", want: ""},
		{
			name: "protected only with exact whitespace", source: " {p}\r\n", spans: []unit.ProtectionSpan{{Start: 1, End: 4, Kind: unit.Placeholder}},
			prepared: " [[YAKUORI_0_0]]\r\n", candidate: " [[YAKUORI_0_0]]\r\n", want: " {p}\r\n",
		},
		{name: "no spans", source: "Hello", prepared: "Hello", candidate: "こんにちは", want: "こんにちは", generate: true},
		{name: "whitespace-only no-span foundation bytes", source: " \r\n", prepared: " \r\n", candidate: "\t", want: "\t", generate: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session, id := fixtureSession(t, tt.source, tt.spans)
			prepared, err := protect.Prepare(session, id)
			if err != nil {
				t.Fatal(err)
			}
			if prepared.Text() != tt.prepared || prepared.GenerationRequired() != tt.generate {
				t.Fatalf("prepared = %q, generation = %v; want %q, %v", prepared.Text(), prepared.GenerationRequired(), tt.prepared, tt.generate)
			}
			restored, err := prepared.Restore(session, id, tt.candidate)
			if err != nil || restored != tt.want {
				t.Fatalf("restored = %q, error = %v; want %q", restored, err, tt.want)
			}
			if err := prepared.CheckRestored(session, id, tt.want); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func fixtureSession(t *testing.T, source string, spans []unit.ProtectionSpan) (unit.Session, unit.UnitID) {
	t.Helper()
	id, err := unit.NewUnitID("fixture", "v1", "dialogue")
	if err != nil {
		t.Fatal(err)
	}
	u, err := unit.NewTranslationUnitWithProtection(id, []byte(source), "en", "ja", nil, spans)
	if err != nil {
		t.Fatal(err)
	}
	session, err := unit.NewSession([]byte("original artifact"), []unit.TranslationUnit{u})
	if err != nil {
		t.Fatal(err)
	}
	return session, id
}
