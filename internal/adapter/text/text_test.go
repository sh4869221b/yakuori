package text

import (
	"bytes"
	"testing"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func TestTextRoundTrip(t *testing.T) {
	for _, tt := range []struct{ source, target, sourceLanguage, targetLanguage string }{
		{"Hello\r\nWorld", "こんにちは\r\n世界", "en", "ja"},
		{"日本語", "Deutsch", "ja", "de"},
		{"", "", "en", "fr"},
	} {
		t.Run(tt.targetLanguage, func(t *testing.T) {
			profile, err := validate.NewProfile([32]byte{1})
			if err != nil {
				t.Fatal(err)
			}
			a := New(tt.sourceLanguage, tt.targetLanguage)
			session, err := a.Import([]byte(tt.source), profile)
			if err != nil {
				t.Fatal(err)
			}
			units := session.Units()
			if len(units) != 1 {
				t.Fatalf("unit count = %d", len(units))
			}
			u := units[0]
			if u.ID().StableID() != "document" || u.ID().Adapter() != "text" || u.ID().FormatSchema() != "text-v1" ||
				string(u.Source()) != tt.source || u.SourceLanguage() != tt.sourceLanguage || u.TargetLanguage() != tt.targetLanguage {
				t.Fatalf("unit = %+v", u)
			}
			accepted, err := validate.Validate(session, u.ID(), profile, tt.target)
			if err != nil {
				t.Fatal(err)
			}
			translations := []validate.AcceptedTranslation{accepted}
			var buffer bytes.Buffer
			manifest, err := a.Export(&buffer, session, profile, translations)
			if err != nil || buffer.String() != tt.target {
				t.Fatalf("export = %q, error = %v", buffer.String(), err)
			}
			if err := manifest.CheckInput(session); err != nil {
				t.Fatal(err)
			}
			expected, err := artifact.PrepareExport(session, profile, translations)
			if err != nil {
				t.Fatal(err)
			}
			final, err := a.Observe(bytes.NewReader(buffer.Bytes()))
			if err != nil || len(final.Keys) != 0 {
				t.Fatalf("observation = %+v, error = %v", final, err)
			}
			if err := artifact.CompareFinal(manifest, expected, final); err != nil {
				t.Fatal(err)
			}
		})
	}
}
