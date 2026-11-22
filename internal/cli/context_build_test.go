package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/contextpack"
)

const contextBuildInput = "../contextpack/testdata/build-dialogue-v1.json"

func contextBuildArgs(input, dir string) []string {
	return []string{"context", "build", "--source-language", "en", "--target-language", "ja",
		"--game", "sample-game", "--artifact", "dialogue", "--output-dir", dir, input}
}

func TestContextBuildWritesSeparateValidatedCandidates(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "context-candidate-reviewed.json")
	edited, err := os.ReadFile("../contextpack/testdata/dialogue-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, edited, 0600); err != nil {
		t.Fatal(err)
	}
	resolve := func() (config.Paths, error) {
		t.Fatal("context command accessed configuration")
		return config.Paths{}, nil
	}
	build := func() (string, []byte) {
		t.Helper()
		var out, diag bytes.Buffer
		args := contextBuildArgs(contextBuildInput, dir)
		if code := run(args, &out, &diag, resolve); code != ExitOK || diag.Len() != 0 {
			t.Fatalf("build exit=%d stdout=%q stderr=%q", code, out.String(), diag.String())
		}
		pathLine, fingerprint, _ := strings.Cut(out.String(), "\n")
		path, ok := strings.CutPrefix(pathLine, "candidate: ")
		if !ok || filepath.Dir(path) != dir || path == existing || !strings.HasPrefix(filepath.Base(path), "context-candidate-") ||
			filepath.Ext(path) != ".json" || !strings.HasPrefix(fingerprint, "fingerprint: ") {
			t.Fatalf("wrong build result: %q", out.String())
		}
		out.Reset()
		if code := run([]string{"context", "validate", path}, &out, &diag, resolve); code != ExitOK || out.String() != fingerprint || diag.Len() != 0 {
			t.Fatalf("validate exit=%d fingerprint=%q want=%q stderr=%q", code, out.String(), fingerprint, diag.String())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return path, data
	}
	first, firstData := build()
	second, secondData := build()
	if first == second || !bytes.Equal(firstData, secondData) {
		t.Fatal("rerun did not save a distinct, byte-identical candidate")
	}
	got, err := os.ReadFile(existing)
	if err != nil || !bytes.Equal(got, edited) {
		t.Fatalf("existing edited pack changed: %v", err)
	}
	doc, err := contextpack.Read(bytes.NewReader(firstData))
	if err != nil || !bytes.Equal(doc.Canonical, firstData) {
		t.Fatalf("candidate file is not canonical: %v", err)
	}
	units := doc.Pack.Units
	if len(units) != 4 || units[0].RawSourceDigest != units[1].RawSourceDigest || *units[0].SpeakerID == *units[1].SpeakerID ||
		*units[0].SceneID == *units[1].SceneID || *units[1].SceneID != *units[2].SceneID || units[2].SpeakerID != nil ||
		units[3].SpeakerID != nil || units[3].SceneID != nil || len(doc.Pack.World) != 0 {
		t.Fatalf("Generic JSON context was not retained: %+v", units)
	}
	for _, u := range units {
		if u.PreviousUnitID != nil || u.Provenance.Status != "candidate" || u.Provenance.UserEdited == nil || *u.Provenance.UserEdited {
			t.Fatalf("unexpected order link or confirmation: %+v", u)
		}
	}
	if files, err := os.ReadDir(dir); err != nil || len(files) != 3 {
		t.Fatalf("unexpected files after build: %v, %v", files, err)
	}
}

func TestContextBuildRejectsInvalidInput(t *testing.T) {
	data, err := os.ReadFile(contextBuildInput)
	if err != nil {
		t.Fatal(err)
	}
	inputDir, output := t.TempDir(), t.TempDir()
	for _, tt := range []struct {
		name           string
		before, after  string
		wantDiagnostic string
	}{
		{"missing ID", `"id": "inn-1", `, "", "missing required generic JSON field"},
		{"duplicate ID", `"id": "gate-1"`, `"id": "inn-1"`, "duplicate unit ID"},
		{"blank ID", `"id": "inn-1"`, `"id": "   "`, "invalid stable unit identity"},
		{"duplicate metadata", `"speaker": "keeper"`, `"speaker": "keeper", "speaker": "secret"`, "duplicate generic JSON field"},
		{"invalid metadata", `"speaker": "keeper"`, `"speaker": "   "`, "invalid origin metadata for speaker"},
		{"inconsistent scope", `"artifact_namespace": "dialogue"`, `"artifact_namespace": "secret"`, "generic JSON language or scope mismatch"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(inputDir, "secret.json")
			changed := bytes.Replace(data, []byte(tt.before), []byte(tt.after), 1)
			if bytes.Equal(changed, data) {
				t.Fatal("invalid-input fixture was not changed")
			}
			if err := os.WriteFile(path, changed, 0600); err != nil {
				t.Fatal(err)
			}
			var out, diag bytes.Buffer
			if code := Run(contextBuildArgs(path, output), &out, &diag); code != ExitFailure || out.Len() != 0 ||
				!strings.Contains(diag.String(), tt.wantDiagnostic) || strings.Contains(diag.String(), "secret") {
				t.Fatalf("build exit=%d stdout=%q stderr=%q", code, out.String(), diag.String())
			}
		})
	}
	if files, err := os.ReadDir(output); err != nil || len(files) != 0 {
		t.Fatalf("invalid input produced files: %v, %v", files, err)
	}
}

type contextBeforeCommit struct {
	context.Context
	before func()
}

func (ctx contextBeforeCommit) Err() error {
	ctx.before()
	return ctx.Context.Err()
}

func TestPublishContextCandidatePreservesExistingFiles(t *testing.T) {
	for _, tt := range []struct{ name, wantDiagnostic string }{
		{"canceled", "canceled"},
		{"collision", "cannot publish candidate"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			oldPath := filepath.Join(dir, "reviewed.json")
			old := []byte("edited pack")
			data := []byte("validated candidate")
			if err := os.WriteFile(oldPath, old, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var blocker string
			beforeCommit := contextBeforeCommit{Context: ctx, before: func() {
				paths, err := filepath.Glob(filepath.Join(dir, ".context-stage-*"))
				if err != nil || len(paths) != 1 {
					t.Fatalf("expected one private stage before commit: %v, %v", paths, err)
				}
				contents, err := os.ReadFile(paths[0])
				if err != nil || !bytes.Equal(contents, data) {
					t.Fatalf("stage is incomplete: %v", err)
				}
				info, err := os.Stat(paths[0])
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("stage is not private: %v", err)
				}
				if tt.name == "canceled" {
					cancel()
					return
				}
				suffix := strings.TrimPrefix(filepath.Base(paths[0]), ".context-stage-")
				blocker = filepath.Join(dir, "context-candidate-"+suffix+".json")
				if err := os.WriteFile(blocker, old, 0600); err != nil {
					t.Fatal(err)
				}
			}}
			if path, err := publishContextCandidate(beforeCommit, dir, data); path != "" || err == nil || err.Error() != tt.wantDiagnostic {
				t.Fatalf("unexpected publish result: path=%q err=%v", path, err)
			}
			wantFiles := []string{oldPath}
			if blocker != "" {
				wantFiles = append(wantFiles, blocker)
			}
			if files, err := os.ReadDir(dir); err != nil || len(files) != len(wantFiles) {
				t.Fatalf("failed publication left files: %v, %v", files, err)
			}
			for _, path := range wantFiles {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, old) {
					t.Fatalf("existing pack changed: %v", err)
				}
			}
		})
	}
}

func TestPublishContextCandidateFollowsOutputDirectory(t *testing.T) {
	root := t.TempDir()
	lexical := filepath.Join(root, "lexical")
	resolved := filepath.Join(root, "resolved")
	child := filepath.Join(resolved, "child")
	for _, dir := range []string{lexical, resolved, child} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(lexical, "link")
	if err := os.Symlink(child, link); err != nil {
		t.Fatal(err)
	}
	data := []byte("candidate")
	candidate, err := publishContextCandidate(t.Context(), link+"/..", data)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(resolved, filepath.Base(candidate)), candidate} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("candidate absent from output path: path=%q err=%v", path, err)
		}
	}
	if files, err := os.ReadDir(lexical); err != nil || len(files) != 1 || files[0].Name() != "link" {
		t.Fatalf("unexpected files in lexical directory: %v, %v", files, err)
	}
}
