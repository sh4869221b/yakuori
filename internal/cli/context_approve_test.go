package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/contextpack"
)

const (
	contextApprovalBase     = "../contextpack/testdata/dialogue-v1.json"
	contextApprovalReviewed = "../contextpack/testdata/approval-reviewed-v1.json"
)

func TestContextApprove(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "approved.json")
	second := filepath.Join(dir, "merged-again.json")
	later := filepath.Join(dir, "later.json")
	selected := []string{"relief-captain", "mod-c-note"}
	var out, diag bytes.Buffer
	code := runApproval(t, context.Background(), approvalArgs(contextApprovalBase, first, contextApprovalReviewed, selected...), &out, &diag)
	if code != ExitOK || diag.Len() != 0 {
		t.Fatalf("approve exit=%d stdout=%q stderr=%q", code, out.String(), diag.String())
	}
	var validated bytes.Buffer
	code = runApproval(t, context.Background(), []string{"context", "validate", first}, &validated, &diag)
	if code != ExitOK || diag.Len() != 0 || validated.String() != out.String() {
		t.Fatalf("validate exit=%d stdout=%q stderr=%q", code, validated.String(), diag.String())
	}
	if info, err := os.Stat(first); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("approved file info=%v err=%v", info, err)
	}
	pack := readApprovedPack(t, first)
	if len(pack.Characters) != 3 || pack.Characters[0].ID != "captain" || *pack.Characters[0].Name != "Captain" ||
		pack.Characters[0].Provenance.Status != "confirmed" || pack.Characters[0].Provenance.Method != "manual" {
		t.Fatalf("existing characters = %+v", pack.Characters)
	}
	character := pack.Characters[2]
	p := character.Provenance
	if character.ID != "relief-captain" || *character.Name != "Captain" || *character.Description != "Leads the relief crew." ||
		p.Status != "confirmed" || p.Method != "extraction" || p.Version != "context-build-v1" || !*p.UserEdited || len(p.Sources) != 2 ||
		p.Sources[0].Kind != "metadata" || *p.Sources[0].ArtifactNamespace != "mod-c" ||
		*p.Sources[0].MetadataKey != "units/witcher3-w3strings/w3strings-v162-en-jp-v1/101/origin/speaker" ||
		p.Sources[1].Kind != "unit" || *p.Sources[1].UnitID != "mod-c-greeting" {
		t.Fatalf("approved character = %+v, provenance = %+v", character, p)
	}
	if len(pack.Scope.Artifacts) != 3 || pack.Scope.Artifacts[2].ArtifactNamespace != "mod-c" ||
		pack.Scope.Artifacts[2].OriginalArtifactDigest != strings.Repeat("5", 64) || len(pack.Units) != 5 ||
		pack.Units[2].ID != "mod-c-greeting" || pack.Units[2].ArtifactNamespace != "mod-c" ||
		pack.Units[2].StableID != "101" || pack.Units[2].RawSourceDigest != strings.Repeat("3", 64) ||
		pack.Units[3].ID != "port-greeting" || pack.Units[3].RawSourceDigest != pack.Units[2].RawSourceDigest ||
		*pack.Units[2].SpeakerID != "relief-captain" || *pack.Units[2].SceneID != "mod-c-port" ||
		pack.Units[2].Provenance.Status != "candidate" || pack.Units[4].Provenance.Status != "confirmed" {
		t.Fatalf("scope = %+v, units = %+v", pack.Scope, pack.Units)
	}
	if len(pack.World) != 3 || pack.World[0].ID != "mod-c-note" || pack.World[0].Provenance.Status != "confirmed" ||
		*pack.World[0].Provenance.Sources[0].UnitID != "mod-c-greeting" || pack.World[2].ID != "unverified-rumor" ||
		pack.World[2].Provenance.Status != "candidate" || pack.Scenes[1].Provenance.Status != "candidate" ||
		pack.GlossaryRefs[1].Provenance.Status != "candidate" {
		t.Fatalf("world = %+v, scenes = %+v, glossary = %+v", pack.World, pack.Scenes, pack.GlossaryRefs)
	}
	out.Reset()
	code = runApproval(t, context.Background(), approvalArgs(first, second, contextApprovalReviewed, selected...), &out, &diag)
	if code != ExitOK || diag.Len() != 0 || out.String() != validated.String() || !bytes.Equal(approvalBytes(t, first), approvalBytes(t, second)) {
		t.Fatalf("remerge exit=%d stdout=%q stderr=%q, expected identical merged bytes", code, out.String(), diag.String())
	}
	code = runApproval(t, context.Background(), approvalArgs(second, later, contextApprovalReviewed, "mod-c-port", "mod-c-greeting", "mod-c-terms"), io.Discard, &diag)
	if code != ExitOK || diag.Len() != 0 {
		t.Fatalf("later approval exit=%d stderr=%q", code, diag.String())
	}
	pack = readApprovedPack(t, later)
	if pack.World[0].Provenance.Status != "confirmed" || pack.Characters[2].Provenance.Status != "confirmed" ||
		pack.Scenes[1].Provenance.Status != "confirmed" || pack.Units[2].Provenance.Status != "confirmed" ||
		pack.GlossaryRefs[1].Provenance.Status != "confirmed" {
		t.Fatalf("later approval lost or failed to promote selected records: %+v", pack)
	}
}

func TestContextApproveRejectsBeforePublication(t *testing.T) {
	baseBytes := approvalBytes(t, contextApprovalBase)
	reviewed := string(approvalBytes(t, contextApprovalReviewed))
	for _, tt := range []struct {
		name, reviewed, output, reason string
		accept                         []string
		usage, canceled                bool
	}{
		{name: "missing selection", reviewed: reviewed, usage: true, reason: "invalid context approve arguments"},
		{name: "unknown ID", reviewed: reviewed, accept: []string{"secret-id"}, reason: "absent from reviewed pack"},
		{name: "base-only ID", reviewed: reviewed, accept: []string{"the-coast"}, reason: "absent from reviewed pack"},
		{name: "duplicate ID", reviewed: reviewed, accept: []string{"relief-captain", "relief-captain"}, reason: "duplicate accepted record ID"},
		{name: "missing source", reviewed: reviewed, accept: []string{"unverified-rumor"}, reason: "confirmed record requires a source"},
		{name: "invalid JSON", reviewed: `{"secret-key":"secret-content"}`, reason: "unknown JSON member"},
		{name: "invalid reference", reviewed: strings.Replace(reviewed, `"unit_id": "mod-c-greeting"`, `"unit_id": "secret-unit"`, 1), reason: "unresolved source unit"},
		{name: "scope conflict", reviewed: strings.Replace(reviewed, `"example-game"`, `"secret-game"`, 1), reason: "game namespace or languages do not match"},
		{name: "language conflict", reviewed: strings.Replace(reviewed, `"ja"`, `"fr"`, 1), reason: "game namespace or languages do not match"},
		{name: "artifact conflict", reviewed: strings.Replace(reviewed, strings.Repeat("1", 64), strings.Repeat("9", 64), 1), reason: "different original snapshots"},
		{name: "changed protected record", reviewed: strings.Replace(string(baseBytes), "Runs the ship.", "secret-override", 1), accept: []string{"captain"}, reason: "different record content"},
		{name: "cross-type ID conflict", reviewed: strings.Replace(reviewed, `"mod-c-note"`, `"deck"`, 1), reason: "collides across record types"},
		{name: "stable unit conflict", reviewed: strings.Replace(string(baseBytes), `"port-reply"`, `"secret-duplicate-unit"`, 1), accept: []string{"deck"}, reason: "duplicate stable unit in artifact"},
		{name: "existing output", reviewed: reviewed, output: "secret-base.json", reason: "output file already exists"},
		{name: "stage creation failure", reviewed: reviewed, output: "secret-base.json/approved.json", reason: "cannot create output stage"},
		{name: "canceled", reviewed: reviewed, canceled: true, reason: "canceled"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			basePath := filepath.Join(dir, "secret-base.json")
			reviewedPath := filepath.Join(dir, "secret-reviewed.json")
			if err := os.WriteFile(basePath, baseBytes, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(reviewedPath, []byte(tt.reviewed), 0600); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(dir, "secret-output.json")
			if tt.output != "" {
				output = filepath.Join(dir, tt.output)
			}
			accept := tt.accept
			code := ExitFailure
			if tt.usage {
				code = ExitUsage
			} else if len(accept) == 0 {
				accept = []string{"relief-captain"}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.canceled {
				cancel()
			}
			var out, diag bytes.Buffer
			got := runApproval(t, ctx, approvalArgs(basePath, output, reviewedPath, accept...), &out, &diag)
			if got != code || out.Len() != 0 || !strings.Contains(diag.String(), tt.reason) || strings.Contains(diag.String(), "secret") ||
				strings.Contains(diag.String(), "relief-captain") || strings.Contains(diag.String(), "The relief crew") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", got, out.String(), diag.String())
			}
			if !bytes.Equal(approvalBytes(t, basePath), baseBytes) || string(approvalBytes(t, reviewedPath)) != tt.reviewed {
				t.Fatal("input bytes changed")
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 2 {
				t.Fatalf("expected only input files, directory=%v err=%v", files, err)
			}
		})
	}
}

func TestContextApprovePublicationBoundary(t *testing.T) {
	dir := t.TempDir()
	inside := filepath.Join(dir, "inside")
	child := filepath.Join(inside, "child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "secret-link")
	if err := os.Symlink(child, link); err != nil {
		t.Fatal(err)
	}
	output := link + "/../approved.json"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := publishContextApproval(ctx, output, approvalBytes(t, contextApprovalBase)); err == nil || err.Error() != "canceled" {
		t.Fatalf("publish canceled approval = %v", err)
	}
	if files, err := os.ReadDir(inside); err != nil || len(files) != 1 {
		t.Fatalf("cancel left an output or stage: directory=%v err=%v", files, err)
	}
	const minimal = "../contextpack/testdata/minimal-v1.json"
	var diag bytes.Buffer
	code := runApproval(t, context.Background(), approvalArgs(minimal, output, contextApprovalReviewed, "relief-captain"), brokenWriter{}, &diag)
	if code != ExitFailure || diag.String() != "yakuori: context approve: approved pack saved, but write stdout failed\n" {
		t.Fatalf("failed stdout exit=%d stderr=%q", code, diag.String())
	}
	pack := readApprovedPack(t, filepath.Join(inside, "approved.json"))
	if len(pack.Characters) != 1 || pack.Characters[0].Provenance.Status != "confirmed" {
		t.Fatalf("published pack missing the approved character: %+v", pack.Characters)
	}
	if _, err := os.Stat(filepath.Join(dir, "approved.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output appeared at a lexically cleaned path: %v", err)
	}
}

func runApproval(t *testing.T, ctx context.Context, args []string, stdout, stderr io.Writer) int {
	t.Helper()
	return runWithPipeline(ctx, args, stdout, stderr, func() (config.Paths, error) {
		t.Fatal("context approval accessed configuration")
		return config.Paths{}, nil
	}, nil)
}

func approvalArgs(base, output, reviewed string, accept ...string) []string {
	args := []string{"context", "approve", "--base", base, "--output", output}
	for _, id := range accept {
		args = append(args, "--accept", id)
	}
	return append(args, reviewed)
}

func approvalBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readApprovedPack(t *testing.T, path string) contextpack.Pack {
	t.Helper()
	data := approvalBytes(t, path)
	doc, err := contextpack.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, doc.Canonical) {
		t.Fatal("approved output is not canonical JSON")
	}
	return doc.Pack
}
