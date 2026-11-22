package contextpack

import (
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/unit"
)

func TestInferenceMaterialByteLimit(t *testing.T) {
	scope := BuildScope{GameNamespace: "game", ArtifactNamespace: "artifact", SourceLanguage: "en", TargetLanguage: "ja"}
	id, err := unit.NewUnitID("adapter", "v1", "large")
	if err != nil {
		t.Fatal(err)
	}
	u, err := unit.NewTranslationUnit(id, []byte(strings.Repeat("<", inferBytes/6+1)), scope.SourceLanguage, scope.TargetLanguage, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := unit.NewSession([]byte("import"), []unit.TranslationUnit{u})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Build(session, scope)
	if err != nil {
		t.Fatal(err)
	}
	evidence, data, err := selectInferenceMaterial(doc.Pack, session)
	if err == nil || evidence != nil || data != nil || !strings.Contains(err.Error(), "exceeds byte budget") {
		t.Fatalf("unexpected oversized data selection: evidence=%d data=%d error=%v", len(evidence), len(data), err)
	}
}

func TestGenerationOriginRequiresLLMAndUnitSource(t *testing.T) {
	makePack := func(t *testing.T) Pack {
		t.Helper()
		p := mustRead(t, fixture(t, "dialogue")).Pack
		provenance := &p.Scenes[1].Provenance
		provenance.Generation = &GenerationOrigin{
			ModelID: "local-model", ModelSHA256: strings.Repeat("ab", 32),
			PromptVersion: "context-infer-v1", TemplateFamily: "chatml", TemplateSource: "gguf:tokenizer.chat_template",
			TemplateRendererVersion: "renderer1", PolicyVersion: "context-infer-policy-v1",
		}
		provenance.Sources = []Source{{Kind: "unit", UnitID: &p.Units[0].ID}}
		return p
	}
	p := makePack(t)
	// Explicit review can change state and retain the original generation identity.
	p.Scenes[1].Provenance.Status = "confirmed"
	p.Scenes[1].Provenance.UserEdited = new(true)
	doc := mustRead(t, encode(t, p))
	if got := doc.Pack.Scenes[1].Provenance.Generation; got == nil || *got != *p.Scenes[1].Provenance.Generation {
		t.Fatal("generation origin was lost when reloading a reviewed record")
	}
	for _, tc := range []struct {
		name string
		edit func(*Provenance)
	}{
		{"manual", func(p *Provenance) { p.Method = "manual" }},
		{"no unit source", func(p *Provenance) { p.Sources = nil }},
		{"invalid model ID", func(p *Provenance) { p.Generation.ModelID = "../escape" }},
		{"invalid model digest", func(p *Provenance) { p.Generation.ModelSHA256 = strings.Repeat("0", 64) }},
		{"missing template source", func(p *Provenance) { p.Generation.TemplateSource = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := makePack(t)
			tc.edit(&p.Scenes[1].Provenance)
			if doc, err := Read(strings.NewReader(string(encode(t, p)))); doc != nil || err == nil ||
				!strings.Contains(err.Error(), "generation requires valid identity and unit evidence") {
				t.Fatalf("invalid generation origin accepted: %v", err)
			}
		})
	}
}
