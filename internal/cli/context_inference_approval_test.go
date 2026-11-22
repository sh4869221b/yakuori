package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sh4869221b/yakuori/internal/inference"
)

func TestContextInferenceApproval(t *testing.T) {
	dir := t.TempDir()
	engine := &fakeContextEngine{
		count: 400,
		result: inference.GenerationResult{
			Text: inferredDialogue, Finish: inference.Stop, OutputTokens: 147,
		},
	}
	code, stdout, stderr := runContextInference(t, t.Context(), dir, engine)
	if code != ExitOK || stderr != "" {
		t.Fatalf("build exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	candidatePath, candidate := readBuiltContext(t, stdout)
	expected := candidate.Pack
	if len(expected.World) != 1 || len(expected.GlossaryRefs) != 1 {
		t.Fatalf("expected a generated world entry and glossary reference")
	}
	selected := expected.World[0]
	if p := selected.Provenance; p.Status != "candidate" || p.Generation == nil || len(p.Sources) != 2 {
		t.Fatalf("generated world entry lacks candidate status or provenance: %+v", p)
	}
	if expected.GlossaryRefs[0].Provenance.Status != "candidate" {
		t.Fatal("generated glossary reference lacks candidate status")
	}

	basePath := filepath.Join(dir, "base.json")
	base := []byte(`{"schema":"yakuori.context/v1","scope":{"game_namespace":"sample-game"},"source_language":"en","target_language":"ja"}`)
	if err := os.WriteFile(basePath, base, 0600); err != nil {
		t.Fatal(err)
	}
	approvedPath := filepath.Join(dir, "approved.json")
	var diag bytes.Buffer
	args := approvalArgs(basePath, approvedPath, candidatePath, selected.ID)
	if code := runApproval(t, t.Context(), args, io.Discard, &diag); code != ExitOK || diag.Len() != 0 {
		t.Fatalf("approve exit=%d stderr=%q", code, diag.String())
	}

	expected.World[0].Provenance.Status = "confirmed"
	approved := readApprovedPack(t, approvedPath)
	if !reflect.DeepEqual(approved, expected) {
		t.Fatal("approval should preserve all generated records and provenance except the selected status")
	}
	if !bytes.Equal(approvalBytes(t, candidatePath), candidate.Canonical) {
		t.Fatal("approval changed the generated candidate file")
	}
}
