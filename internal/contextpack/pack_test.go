package contextpack

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

const minimalCanonical = `{"schema":"yakuori.context/v1","scope":{"game_namespace":"example-game"},"source_language":"en","target_language":"ja"}`

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name + "-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustRead(t *testing.T, data []byte) *Document {
	t.Helper()
	doc, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func encode(t *testing.T, p Pack) []byte {
	t.Helper()
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestCanonicalV1(t *testing.T) {
	for _, tt := range []struct{ name, fingerprint string }{
		{"minimal", "3170144643af3dff1ba01bef0ee04a54b4969f408459bfd0bf35126f417208bb"},
		{"dialogue", "9fe066dac144f707c4abf9d882457db0bce5139c55b86da8c7799e894ebbaa66"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := mustRead(t, fixture(t, tt.name))
			if got := fmt.Sprintf("%x", doc.Fingerprint); got != tt.fingerprint {
				t.Fatalf("fingerprint = %s, want %s", got, tt.fingerprint)
			}
			if again := mustRead(t, doc.Canonical); !reflect.DeepEqual(again, doc) {
				t.Fatal("canonical round trip changed the pack, canonical bytes or fingerprint")
			}
			if bytes.HasSuffix(doc.Canonical, []byte{'\n'}) {
				t.Fatal("canonical bytes ended with a newline")
			}
		})
	}
	emptyLists := strings.Replace(minimalCanonical, `"game_namespace":"example-game"`, `"game_namespace":"example-game","artifacts":[]`, 1)
	emptyLists = strings.Replace(emptyLists, `"target_language":"ja"`, `"target_language":"ja","world":[],"characters":[],"scenes":[],"units":[],"glossary_refs":[]`, 1)
	if got := mustRead(t, []byte(emptyLists)); string(got.Canonical) != minimalCanonical {
		t.Fatalf("empty-list canonical form = %s", got.Canonical)
	}
}

func TestCanonicalSetsAndProvenance(t *testing.T) {
	input := fixture(t, "dialogue")
	doc := mustRead(t, input)
	p := mustRead(t, input).Pack
	slices.Reverse(p.Scope.Artifacts)
	slices.Reverse(p.Characters)
	slices.Reverse(p.Scenes)
	slices.Reverse(p.Units)
	slices.Reverse(p.World[0].Artifacts)
	slices.Reverse(p.World[0].Tags)
	slices.Reverse(p.World[0].Provenance.Sources)
	slices.Reverse(p.Scenes[1].SpeakerIDs)
	permuted := mustRead(t, encode(t, p))
	if permuted.Fingerprint != doc.Fingerprint || !bytes.Equal(permuted.Canonical, doc.Canonical) {
		t.Fatal("set ordering and alternative JSON escaping changed the canonical result")
	}

	w := doc.Pack.World[0]
	if w.Text != "港の記録 <log> & notes\r\nCafe\u0301\u2028夕方" ||
		!bytes.Contains(doc.Canonical, []byte(`港の記録 \u003clog\u003e \u0026 notes\r\nCafé\u2028夕方`)) {
		t.Fatal("canonical form changed world text or its v1 escaping")
	}
	manual := w.Provenance
	if manual.Method != "manual" || manual.Version != "1" || manual.Status != "confirmed" || !*manual.UserEdited ||
		len(manual.Sources) != 2 || manual.Sources[0].Kind != "artifact" || value(manual.Sources[0].ArtifactNamespace) != "mod-a" ||
		manual.Sources[1].Kind != "manual" || value(manual.Sources[1].Reference) != "design-notes#coast" {
		t.Fatal("manual confirmations, edit state or source links were lost")
	}
	candidate := doc.Pack.Scenes[1].Provenance
	if candidate.Method != "llm" || candidate.Status != "candidate" || *candidate.UserEdited || len(candidate.Sources) != 0 {
		t.Fatal("unsourced LLM candidate was changed")
	}
	a, b, c := doc.Pack.Units[0], doc.Pack.Units[1], doc.Pack.Units[2]
	if a.RawSourceDigest != c.RawSourceDigest || a.StableID == c.StableID || value(a.SceneID) == value(c.SceneID) ||
		b.ArtifactNamespace == c.ArtifactNamespace || b.StableID != c.StableID || b.RawSourceDigest != c.RawSourceDigest {
		t.Fatal("fixture no longer distinguishes same-source scenes and artifact snapshots")
	}

	for _, tt := range []struct {
		name string
		edit func(*Pack)
	}{
		{"language case", func(p *Pack) { p.SourceLanguage = "EN" }},
		{"line endings", func(p *Pack) { p.World[0].Text = strings.ReplaceAll(p.World[0].Text, "\r\n", "\n") }},
		{"unicode composition", func(p *Pack) { p.World[0].Text = strings.ReplaceAll(p.World[0].Text, "e\u0301", "é") }},
		{"artifact digest", func(p *Pack) { p.Scope.Artifacts[0].OriginalArtifactDigest = strings.Repeat("a", 64) }},
		{"unit digest", func(p *Pack) { p.Units[0].RawSourceDigest = strings.Repeat("b", 64) }},
		{"source", func(p *Pack) { *p.World[0].Provenance.Sources[1].Reference = "design-notes#shore" }},
		{"confirmation", func(p *Pack) { p.World[0].Provenance.Status = "candidate" }},
		{"user edit", func(p *Pack) { *p.World[0].Provenance.UserEdited = false }},
		{"scene link", func(p *Pack) { *p.Units[0].SceneID = "port" }},
		{"glossary reference", func(p *Pack) { p.GlossaryRefs[0].Reference = "other.json" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := mustRead(t, input).Pack
			tt.edit(&p)
			if got := mustRead(t, encode(t, p)); got.Fingerprint == doc.Fingerprint {
				t.Fatal("meaningful change kept the fingerprint")
			}
		})
	}
}

func TestStrictJSON(t *testing.T) {
	for _, tt := range []struct{ name, input, reason string }{
		{"root kind", `[]`, "expected a JSON object"},
		{"truncated", `{"schema":`, "invalid JSON syntax"},
		{"duplicate", `{"schema":"x","schema":"x"}`, "invalid JSON syntax"},
		{"escaped nested duplicate", `{"scope":{"game_namespace":"one","game_\u006eamespace":"two"}}`, "invalid JSON syntax"},
		{"trailing root", minimalCanonical + `{}`, "trailing data"},
		{"trailing garbage", minimalCanonical + `?`, "trailing data"},
		{"unknown member", `{"hidden":"unpublished secret"}`, "unknown JSON member"},
		{"case insensitive alias", `{"Schema":"x"}`, "unknown JSON member"},
		{"type", `{"scope":false}`, "unexpected JSON type"},
		{"nested type", `{"units":[{"provenance":{"user_edited":"true"}}]}`, "unexpected JSON type"},
		{"null field", `{"units":null}`, "JSON null"},
		{"null entry", `{"units":[null]}`, "JSON null"},
		{"unpaired surrogate", `{"secret":"\ud800"}`, "invalid JSON syntax"},
		{"invalid UTF-8", "{\"secret\":\"\xff\"}", "invalid JSON syntax"},
		{"unknown version", strings.Replace(minimalCanonical, "/v1", "/v2", 1), "unsupported context pack schema"},
		{"missing scope", `{"schema":"yakuori.context/v1","source_language":"en","target_language":"ja"}`, "invalid scope.game_namespace"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := Read(strings.NewReader(tt.input))
			if doc != nil || err == nil || !strings.Contains(err.Error(), tt.reason) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("document accepted: %t, error = %v, want %q", doc != nil, err, tt.reason)
			}
		})
	}
}

func TestLanguagesV1(t *testing.T) {
	for _, tag := range []string{"en", "en-US", "EN-us", "ja", "zh-Hant", "zh-Hant-TW", "es-419"} {
		doc := mustRead(t, []byte(strings.ReplaceAll(minimalCanonical, `"en"`, `"`+tag+`"`)))
		if doc.Pack.SourceLanguage != tag {
			t.Fatalf("language = %q, want %q", doc.Pack.SourceLanguage, tag)
		}
	}
	for _, tag := range []string{"", "not-a-language", "jp", "en_US", " en", "en--US", "en-US-US", "en-u-ca-gregory", "en-a-foo-a-bar", "x-local", "sl-rozaj", "sl-rozaj-rozaj"} {
		input := strings.Replace(minimalCanonical, `"ja"`, `"`+tag+`"`, 1)
		if doc, err := Read(strings.NewReader(input)); doc != nil || err == nil || !strings.Contains(err.Error(), "invalid target_language") {
			t.Fatalf("accepted invalid language %q: %v", tag, err)
		}
	}
	mustRead(t, []byte(strings.Replace(minimalCanonical, `"ja"`, `"en"`, 1)))
}

func TestReferencesAndScope(t *testing.T) {
	input := fixture(t, "dialogue")
	for _, tt := range []struct {
		name, reason string
		edit         func(*Pack)
	}{
		{"blank game", "scope.game_namespace", func(p *Pack) { p.Scope.GameNamespace = " " }},
		{"bad artifact digest", "artifact namespace or digest", func(p *Pack) { p.Scope.Artifacts[0].OriginalArtifactDigest = strings.Repeat("G", 64) }},
		{"zero digest", "artifact namespace or digest", func(p *Pack) { p.Scope.Artifacts[0].OriginalArtifactDigest = strings.Repeat("0", 64) }},
		{"duplicate artifact snapshot", "duplicate artifact namespace", func(p *Pack) { p.Scope.Artifacts[1].ArtifactNamespace = "mod-a" }},
		{"duplicate ID across kinds", "duplicate record ID", func(p *Pack) { p.World[0].ID = "port" }},
		{"duplicate artifact applicability", "duplicate artifact reference", func(p *Pack) { p.World[0].Artifacts = []string{"mod-a", "mod-a"} }},
		{"unresolved artifact applicability", "unresolved artifact reference", func(p *Pack) { p.World[0].Artifacts = []string{"unknown"} }},
		{"duplicate tag", "duplicate tag", func(p *Pack) { p.World[0].Tags = []string{"coast", "coast"} }},
		{"blank character name", "invalid character name", func(p *Pack) { p.Characters[0].Name = new("") }},
		{"blank character description", "invalid character description", func(p *Pack) { p.Characters[0].Description = new(" ") }},
		{"blank scene summary", "invalid scene summary", func(p *Pack) { p.Scenes[0].Summary = new("") }},
		{"duplicate speaker", "duplicate scene speaker", func(p *Pack) { p.Scenes[0].SpeakerIDs = []string{"captain", "captain"} }},
		{"wrong speaker kind", "unresolved scene speaker", func(p *Pack) { p.Scenes[0].SpeakerIDs = []string{"the-coast"} }},
		{"speaker coverage with no units", "scene scope exceeds speaker scope", func(p *Pack) {
			p.Units = nil
			p.Characters[0].Provenance = p.Characters[1].Provenance
			p.Characters[0].Artifacts = []string{"mod-a"}
		}},
		{"unresolved unit artifact", "unresolved artifact namespace", func(p *Pack) { p.Units[0].ArtifactNamespace = "unknown" }},
		{"missing stable id", "stable unit identity or digest", func(p *Pack) { p.Units[0].StableID = "" }},
		{"upper digest", "stable unit identity or digest", func(p *Pack) { p.Units[0].RawSourceDigest = strings.Repeat("B", 64) }},
		{"duplicate stable alias", "duplicate stable unit", func(p *Pack) {
			p.Units[0].StableID = p.Units[2].StableID
			p.Units[0].RawSourceDigest = strings.Repeat("a", 64)
		}},
		{"wrong scene kind", "out-of-scope unit scene", func(p *Pack) { *p.Units[0].SceneID = "captain" }},
		{"pack-only scene", "out-of-scope unit scene", func(p *Pack) { p.Scenes[0].Artifacts = nil }},
		{"other artifact scene", "out-of-scope unit scene", func(p *Pack) { *p.Units[1].SceneID = "port" }},
		{"empty scene link", "out-of-scope unit scene", func(p *Pack) { *p.Units[0].SceneID = "" }},
		{"unit speaker scope", "out-of-scope unit speaker", func(p *Pack) { p.Units[1].SpeakerID = new("navigator"); p.Characters[1].Artifacts = []string{"mod-a"} }},
		{"contradictory speaker", "unit speaker conflicts", func(p *Pack) { *p.Units[0].SpeakerID = "navigator" }},
		{"wrong previous kind", "unresolved previous unit", func(p *Pack) { p.Units[0].PreviousUnitID = new("deck") }},
		{"previous crosses scenes", "previous unit crosses artifact or scene scope", func(p *Pack) { p.Units[0].PreviousUnitID = new("port-greeting") }},
		{"previous crosses artifacts", "previous unit crosses artifact or scene scope", func(p *Pack) { p.Units[0].PreviousUnitID = new("mod-b-greeting") }},
		{"self previous", "previous unit cycle", func(p *Pack) { p.Units[0].PreviousUnitID = new(p.Units[0].ID) }},
		{"longer cycle", "previous unit cycle", func(p *Pack) { p.Units[2].PreviousUnitID = new(p.Units[3].ID) }},
		{"unknown glossary schema", "invalid glossary schema or reference", func(p *Pack) { p.GlossaryRefs[0].Schema = "other" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := mustRead(t, input).Pack
			tt.edit(&p)
			if doc, err := Read(bytes.NewReader(encode(t, p))); doc != nil || err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("document accepted: %t, error = %v, want %q", doc != nil, err, tt.reason)
			}
		})
	}
	t.Run("metadata-only references", func(t *testing.T) {
		p := mustRead(t, input).Pack
		p.Characters[0].Name = nil
		p.Characters[0].Description = nil
		p.Characters[0].Provenance.Status = "candidate"
		p.Scenes[0].Summary = nil
		p.Scenes[0].Provenance.Status = "candidate"
		doc := mustRead(t, encode(t, p))
		if !reflect.DeepEqual(doc.Pack, p) {
			t.Fatal("metadata-only records or their unit links changed")
		}
		mustRead(t, doc.Canonical)
	})
	p := mustRead(t, input).Pack
	p.Scenes[0].SpeakerIDs = nil
	// Leaving the scene's speakers unspecified still permits a unit speaker.
	// Its provenance may link back to the same unit as evidence.
	p.Units[0].Provenance.Sources = []Source{{Kind: "unit", UnitID: new(p.Units[0].ID)}}
	mustRead(t, encode(t, p))
}

func TestInvalidProvenance(t *testing.T) {
	input := fixture(t, "dialogue")
	for _, tt := range []struct {
		name, reason string
		edit         func(*Provenance)
	}{
		{"unknown method", "unknown creation method", func(p *Provenance) { p.Method = "adapter" }},
		{"missing version", "missing version or user_edited", func(p *Provenance) { p.Version = "" }},
		{"missing edit state", "missing version or user_edited", func(p *Provenance) { p.UserEdited = nil }},
		{"unknown state", "unknown confirmation status", func(p *Provenance) { p.Status = "accepted" }},
		{"unsupported confirmation", "confirmed record requires a source", func(p *Provenance) { p.Method = "llm"; p.Sources = nil }},
		{"duplicate evidence", "duplicate source", func(p *Provenance) { p.Sources = append(p.Sources, p.Sources[0]) }},
		{"unknown source kind", "invalid source kind or fields", func(p *Provenance) { p.Sources[0].Kind = "hidden" }},
		{"mismatched field", "invalid source kind or fields", func(p *Provenance) { p.Sources[0].MetadataKey = new("") }},
		{"missing metadata key", "invalid source kind or fields", func(p *Provenance) { p.Sources[0].Kind = "metadata" }},
		{"missing source artifact", "unresolved source artifact", func(p *Provenance) { *p.Sources[0].ArtifactNamespace = "unknown" }},
		{"wrong source unit kind", "unresolved source unit", func(p *Provenance) { p.Sources[0] = Source{Kind: "unit", UnitID: new("port")} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := mustRead(t, input).Pack
			tt.edit(&p.World[0].Provenance)
			if doc, err := Read(bytes.NewReader(encode(t, p))); doc != nil || err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("document accepted: %t, error = %v, want %q", doc != nil, err, tt.reason)
			}
		})
	}
}

func TestBounds(t *testing.T) {
	t.Run("input size", func(t *testing.T) {
		padded := minimalCanonical + strings.Repeat(" ", maxBytes-len(minimalCanonical))
		mustRead(t, []byte(padded))
		source := strings.NewReader(padded + "    ")
		if doc, err := Read(source); doc != nil || err == nil || source.Len() != 3 {
			t.Fatalf("document accepted: %t, err = %v, unread bytes = %d, want 3", doc != nil, err, source.Len())
		}
	})
	t.Run("array entries across levels", func(t *testing.T) {
		p := mustRead(t, []byte(minimalCanonical)).Pack
		tags := make([]string, maxEntries)
		for i := range tags {
			tags[i] = fmt.Sprint(i)
		}
		p.World = []World{{ID: "example", Text: "example", Tags: tags[:maxEntries-1], Provenance: Provenance{Method: "llm", Version: "1", Status: "candidate", UserEdited: new(false)}}}
		mustRead(t, encode(t, p))
		p.World[0].Tags = tags
		if doc, err := Read(bytes.NewReader(encode(t, p))); doc != nil || err == nil || !strings.Contains(err.Error(), "array entries exceed limit") {
			t.Fatalf("document accepted: %t, err = %v", doc != nil, err)
		}
	})
	t.Run("decoded string size", func(t *testing.T) {
		p := mustRead(t, []byte(minimalCanonical)).Pack
		p.Scope.GameNamespace = strings.Repeat("é", maxStringBytes/2)
		atLimit := bytes.ReplaceAll(encode(t, p), []byte("é"), []byte(`\u00e9`))
		mustRead(t, atLimit)
		p.Scope.GameNamespace += "x"
		if doc, err := Read(bytes.NewReader(encode(t, p))); doc != nil || err == nil || !strings.Contains(err.Error(), "JSON string exceeds byte limit") {
			t.Fatalf("document accepted: %t, err = %v", doc != nil, err)
		}
	})
	t.Run("depth", func(t *testing.T) {
		nested := func(n int) []byte {
			return []byte(`{"unknown":` + strings.Repeat("[", n-1) + "0" + strings.Repeat("]", n-1) + "}")
		}
		if err := checkJSON(nested(maxDepth)); err != nil {
			t.Fatal(err)
		}
		if err := checkJSON(nested(maxDepth + 1)); err == nil || !strings.Contains(err.Error(), "JSON nesting exceeds limit") {
			t.Fatalf("nesting error = %v", err)
		}
	})
	t.Run("canonical size after escaping", func(t *testing.T) {
		p := mustRead(t, []byte(minimalCanonical)).Pack
		for i := range 22 {
			p.World = append(p.World, World{ID: fmt.Sprint(i), Text: strings.Repeat("<", maxStringBytes), Provenance: Provenance{Method: "llm", Version: "1", Status: "candidate", UserEdited: new(false)}})
		}
		if doc, err := Read(bytes.NewReader(encode(t, p))); doc != nil || err == nil || err.Error() != "canonical JSON exceeds byte limit" {
			t.Fatalf("document accepted: %t, err = %v", doc != nil, err)
		}
	})
}

func TestReadSanitizesIOError(t *testing.T) {
	doc, err := Read(iotest.ErrReader(errors.New("secret input")))
	if doc != nil || err == nil || err.Error() != "read failed" {
		t.Fatalf("document accepted: %t, err = %v", doc != nil, err)
	}
}
