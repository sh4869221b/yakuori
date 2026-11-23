package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/dictionary"
)

func TestExample(t *testing.T) {
	input, err := os.Open("testdata/example.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()

	var output, report bytes.Buffer
	if err := run([]string{"example"}, input, &output, &report); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{
		"line 16: String@Partial (lockedTrans)",
		"line 22: String@Partial (incompleteTrans)",
		"verified 3 distinct reference IDs across 2 source strings",
	} {
		if !strings.Contains(report.String(), message) {
			t.Errorf("missing report message %q", message)
		}
	}
	doc, err := dictionary.Read(&output)
	if err != nil {
		t.Fatal(err)
	}
	const namespace = `["yakuori/xtranslator-example","example-game","Example.esp","en","ja",`
	want := []struct{ unit, source, target string }{
		{`["0","EX_Notice","MESG:DESC"]`, "  Lamps & {count}\n<lit>  ", "  ランプと {count}\n<lit>  "},
		{`["0","EX_Notice","MESG:DESC","0"]`, "  Lamps & {count}\n<lit>  ", "  明かりと {count}\n<lit>  "},
		{`["1","EX_Question","MESG:DESC"]`, "Who keeps the lantern?", ""},
	}
	if len(doc.Entries) != len(want) {
		t.Fatalf("got %d entries, want %d", len(doc.Entries), len(want))
	}
	for i, expected := range want {
		entry := doc.Entries[i]
		if entry.ID != namespace+expected.unit[1:] || entry.Unit.StableID != expected.unit ||
			entry.Source != expected.source || entry.Target != expected.target {
			t.Errorf("entry %d did not preserve identity or text", i+1)
		}
		if entry.Context != (dictionary.Context{}) || entry.Confirmation != dictionary.Unconfirmed {
			t.Errorf("entry %d gained context or confirmation", i+1)
		}
	}
}

func TestUnsupportedExport(t *testing.T) {
	input, err := os.ReadFile("testdata/example.xml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, old, replacement, diagnostic string }{
		{"version", "<Version>2", "<Version>3", "line 7: Params/Version"},
		{"unsupported attribute", `List="0"`, `List="0" sID="private-text"`, "line 10: unsupported attribute on String"},
		{"unsupported context", "</Dest>\n    </String>", "</Dest><FuzInfo>private-text</FuzInfo>\n    </String>", "line 14: expected end of String"},
		{"duplicate identity", `<REC id="0">`, "<REC>", "line 16: String identity is duplicated"},
		{"directive", "<SSTXMLRessources>", "<!DOCTYPE SSTXMLRessources><SSTXMLRessources>", "line 2: unsupported XML directive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			xml := strings.Replace(string(input), tc.old, tc.replacement, 1)
			var output, report bytes.Buffer
			err := run([]string{"example"}, strings.NewReader(xml), &output, &report)
			if err == nil || !strings.Contains(err.Error(), tc.diagnostic) {
				t.Fatalf("got %v, want %q", err, tc.diagnostic)
			}
			if output.Len() != 0 || strings.Contains(err.Error()+report.String(), "private-text") {
				t.Fatal("invalid input exposed output or input text")
			}
		})
	}
}
