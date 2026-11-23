package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/sh4869221b/yakuori/internal/dictionary"
)

type exportProfile struct {
	dictionaryID                   string
	scope                          dictionary.Scope
	sourceLanguage, targetLanguage string
	provenance                     string
}

const lootmanExport = "https://github.com/eideehi/fo4-lootman/blob/d0efa1e08ff20db7940f1ebeecdbc286097c1743/translation/Lootman_en_ja.xml"

var profiles = map[string]exportProfile{
	"example": {
		dictionaryID:   "yakuori/xtranslator-example",
		scope:          dictionary.Scope{Game: "example-game", Artifact: "Example.esp"},
		sourceLanguage: "en", targetLanguage: "ja",
		provenance: "Yakuori tools/xtranslator-example/testdata/example.xml",
	},
	"lootman": {
		dictionaryID:   "eideehi/fo4-lootman",
		scope:          dictionary.Scope{Game: "fallout4", Artifact: "LootMan.esp"},
		sourceLanguage: "en", targetLanguage: "ja",
		provenance: lootmanExport,
	},
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, input io.Reader, output, report io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: xtranslator-example example|lootman < input.xml")
	}
	profile, ok := profiles[args[0]]
	if !ok {
		return errors.New("usage: xtranslator-example example|lootman < input.xml")
	}
	doc, err := convert(input, report, profile)
	if err != nil {
		return err
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return errors.New("cannot encode neutral dictionary")
	}
	data = append(data, '\n')
	var catalog dictionary.Catalog
	if _, err := catalog.Import(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("neutral dictionary import: %w", err)
	}
	sources := make(map[string]struct{})
	for i, entry := range doc.Entries {
		query := dictionary.Query{
			Game: entry.Scope.Game, Artifact: entry.Scope.Artifact, Source: entry.Source,
			SourceLanguage: entry.SourceLanguage, TargetLanguage: entry.TargetLanguage,
		}
		expected := dictionary.ReviewedEntry{Entry: entry}
		if !slices.Contains(catalog.References(query), expected) {
			return fmt.Errorf("String %d: imported reference differs from XML conversion", i+1)
		}
		sources[entry.Source] = struct{}{}
	}
	if _, err := output.Write(data); err != nil {
		return errors.New("cannot write neutral dictionary")
	}
	fmt.Fprintf(report, "verified %d distinct reference IDs across %d source strings\n", len(doc.Entries), len(sources))
	return nil
}

func (p exportProfile) entry(identity []string, source, target string) dictionary.Entry {
	unitID, _ := json.Marshal(identity)
	entryID, _ := json.Marshal(append([]string{p.dictionaryID, p.scope.Game, p.scope.Artifact, p.sourceLanguage, p.targetLanguage}, identity...))
	return dictionary.Entry{
		ID: string(entryID), Kind: dictionary.Translation, Source: source, Target: target,
		SourceLanguage: p.sourceLanguage, TargetLanguage: p.targetLanguage, Provenance: p.provenance,
		License:      dictionary.License{Name: "MIT", Permission: dictionary.Allowed},
		Confirmation: dictionary.Unconfirmed,
		Scope:        p.scope,
		Unit:         dictionary.Unit{FormatSchema: "xtranslator-xml-v2", StableID: string(unitID)},
	}
}
