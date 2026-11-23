package dictionary

import (
	"strings"
	"testing"
)

func TestReadRejectsInvalidOrOversizedDocuments(t *testing.T) {
	empty := `{"version":1,"entries":[]}`
	tests := []struct {
		name, input, reason string
	}{
		{"unknown version", `{"version":2,"entries":[]}`, "unsupported dictionary version"},
		{"missing entries", `{"version":1}`, "missing dictionary entries"},
		{"malformed", `{"version":1,"entries":[}`, "invalid dictionary JSON"},
		{"duplicate key", `{"version":1,"entries":[{"license":{"name":"private text","name":"private text"}}]}`, "invalid dictionary JSON"},
		{"invalid UTF-8", "{\"version\":1,\"entries\":[{\"source\":\"\xff\"}]}", "invalid dictionary JSON"},
		{"unpaired surrogate", `{"version":1,"entries":[{"source":"\ud800"}]}`, "invalid dictionary JSON"},
		{"unknown field", `{"version":1,"entries":[{"private text":"private text"}]}`, "invalid dictionary member"},
		{"wrong type", `{"version":"1","entries":[]}`, "invalid dictionary member"},
		{"null", `{"version":1,"entries":null}`, "unexpected dictionary null"},
		{"trailing JSON", empty + empty, "trailing data"},
		{"byte limit", strings.Repeat(" ", maxBytes+1), "exceeds byte limit"},
		{"entry limit", `{"version":1,"entries":[` + strings.Repeat("{},", maxEntries) + "{}]}", "exceeds entry limit"},
		{"depth limit", `{"version":1,"entries":` + strings.Repeat("[", maxDepth) + strings.Repeat("]", maxDepth) + "}", "exceeds nesting limit"},
		{"string limit", `{"version":1,"entries":[{"source":"` + strings.Repeat("界", maxStringBytes/3+1) + `"}]}`, "exceeds string byte limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Read(strings.NewReader(tt.input))
			if err == nil || !strings.Contains(err.Error(), tt.reason) || strings.Contains(err.Error(), "private text") {
				t.Fatalf("Read error = %v, want %q without input text", err, tt.reason)
			}
		})
	}

	doc, err := Read(strings.NewReader(empty))
	if err != nil || doc.Version != Version || len(doc.Entries) != 0 {
		t.Fatalf("empty v1 document = %#v, %v", doc, err)
	}
}
