package dictionary_test

import (
	"fmt"
	"os"

	"github.com/sh4869221b/yakuori/internal/dictionary"
)

func ExampleCatalog() {
	file, err := os.Open("testdata/neutral-v1.json")
	if err != nil {
		panic(err)
	}
	defer file.Close()

	var catalog dictionary.Catalog
	if _, err := catalog.Import(file); err != nil {
		panic(err)
	}
	if err := catalog.Review("inn-welcome", dictionary.Review{Decision: dictionary.Accept}); err != nil {
		panic(err)
	}
	candidate, err := catalog.Select(dictionary.Query{
		Game: "fixture-game", Artifact: "fixture-artifact", SourceLanguage: "en", TargetLanguage: "ja",
		Adapter: "witcher3-w3strings", FormatSchema: "w3strings-v162-en-jp-v1", StableID: "1",
		Source: "Welcome cat TOKEN", Scene: "inn", Speaker: "keeper",
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(candidate.Target)
	// Output: 宿へようこそ 猫 TOKEN
}
