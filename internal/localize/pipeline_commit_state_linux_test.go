//go:build linux && pipelinefaults

package localize

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/publication"
	"golang.org/x/sys/unix"
)

type pipelineCommitStateCase struct {
	name      string
	state     publication.State
	phase     string
	operation string
	inPlace   bool
}

// These final phases are authored independently of the state judge and its
// historical scenario table. A failed published update keeps the prior record.
func pipelineCommitStateCases() []pipelineCommitStateCase {
	return []pipelineCommitStateCase{
		{"prepared", publication.NotPublished, "prepared", "none", false},
		{"restored", publication.Restored, "restored", "restore-rename", true},
		{"source-backed-up", publication.SourceBackedUp, "source-backed-up", "backup-rename", true},
		{"external-source", publication.SourceBackedUp, "source-backed-up", "backup-rename", true},
		{"published", publication.Published, "published", "publish-rename", false},
		{"published-stale", publication.Published, "prepared", "none", false},
	}
}

func TestPipelineCommitPublicationState(t *testing.T) {
	if os.Getenv(pipelineCommitStateChildRoot) != "" {
		pipelineCommitStateChild(t)
		return
	}
	t.Run("valid", func(t *testing.T) {
		for _, row := range pipelineCommitStateCases() {
			for _, kind := range []string{"text", "w3"} {
				for _, mode := range pipelineFixtureModes() {
					if row.inPlace && mode.mode != publication.InPlace {
						continue // Backup and restore do not exist in the other modes.
					}
					t.Run(row.name+"/"+kind+"/"+mode.name, func(t *testing.T) {
						// Given/When: the real Core, SQLite and publication run.
						want, got := pipelineCommitStateRun(t, row, pipelineFixtureSpec(kind, mode))
						// Then: the independent judge accepts its physical state.
						if problems := pipelineHarnessJudge(want, got); len(problems) != 0 {
							t.Fatalf("real %s state rejected: %v", row.name, problems)
						}
					})
				}
			}
		}
		t.Run("postwait", testPipelineCommitStatePostWait)
	})
	t.Run("reject", testPipelineCommitStateReject)
}

func pipelineCommitStateRun(t *testing.T, row pipelineCommitStateCase, spec pipelineHarnessCase) (pipelineHarnessExpectation, pipelineHarnessSnapshot) {
	t.Helper()
	h := newPipelineHarness(t, spec, true)
	if root := os.Getenv(pipelineCommitStateChildRoot); root != "" {
		pipelineCommitStateRehome(t, h, root)
	}
	want := h.commitStateExpectation(row)
	var external *pipelineHarnessFile
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, runErr := h.executeWithHooks(ctx, row.name, "record-or-publish", func() publication.PipelineFaultHooks {
		hooks := h.ops.hooks()
		syncRecord, writeRecord, rename := hooks.RecordSync, hooks.RecordWrite, hooks.Rename
		hooks.RecordSync = func(f *os.File) error {
			err := syncRecord(f)
			info, statErr := f.Stat()
			if err == nil && statErr == nil && info.IsDir() && row.name == "prepared" {
				h.injected = append(h.injected, "prepared-cancel")
				cancel()
			}
			return errors.Join(err, statErr)
		}
		hooks.RecordWrite = func(f *os.File, raw []byte) (int, error) {
			if row.name == "published-stale" && f.Name() == ".record-published" {
				h.trace.mark(pipelineBoundaryRecordWriteBefore)
				h.injected = append(h.injected, "published-write-eio")
				return 0, unix.EIO
			}
			return writeRecord(f, raw)
		}
		hooks.Rename = func(fromFD int, from string, toFD int, to string, flags uint) error {
			if from == "stage" && row.inPlace {
				if _, err := h.ops.stageFD.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("publish fault before mandatory stage Close: %v", err)
				}
				h.trace.mark(pipelineBoundaryStageCloseAfter)
				h.trace.mark(pipelineBoundaryPublishRenameBefore)
				h.injected = append(h.injected, "publish-rename-eio")
				if row.name == "external-source" {
					if err := os.WriteFile(h.sourcePath, h.fixture.input.target, 0600); err != nil {
						t.Fatal(err)
					}
					file, err := pipelineHarnessReadFile(h.sourcePath)
					if err != nil {
						t.Fatal(err)
					}
					external = &file
				}
				return unix.EIO
			}
			if strings.HasPrefix(from, ".yakuori-backup-") && row.name == "source-backed-up" {
				h.trace.mark(pipelineBoundaryBackupRenameBefore)
				h.injected = append(h.injected, "restore-rename-eio")
				return unix.EIO
			}
			return rename(fromFD, from, toFD, to, flags)
		}
		return hooks
	})
	switch row.name {
	case "published":
		if runErr != nil {
			t.Fatal(runErr)
		}
	case "prepared":
		if !errors.Is(runErr, context.Canceled) {
			t.Fatalf("prepared cancellation not reached: %v", runErr)
		}
	default:
		if !errors.Is(runErr, unix.EIO) {
			t.Fatalf("real publication fault not reached: %v", runErr)
		}
	}
	if h.result.Publication.State != row.state || !h.result.TMCommitted {
		t.Fatalf("result=%+v, want committed/%s", h.result, row.state)
	}
	var injected []string
	switch row.name {
	case "prepared":
		injected = []string{"prepared-cancel"}
	case "restored", "external-source":
		injected = []string{"publish-rename-eio"}
	case "source-backed-up":
		injected = []string{"publish-rename-eio", "restore-rename-eio"}
	case "published-stale":
		injected = []string{"published-write-eio"}
	}
	if !slices.Equal(h.injected, injected) {
		t.Fatalf("fault injections=%v, want exactly %v", h.injected, injected)
	}
	got, err := h.snapshot(want)
	if err != nil {
		t.Fatal(err)
	}
	if row.name == "published-stale" {
		last := got.RecordEvents[len(got.RecordEvents)-1]
		if last.Phase != "published" || last.Operation != "close" {
			t.Fatalf("failed published record Close attributed to the wrong phase: %+v", last)
		}
	}
	if external != nil {
		if problems := pipelineHarnessCheckCaptured("external-output", got.Files["output"], external, want.OutputBytes); len(problems) != 0 {
			t.Fatalf("recovery changed the external file: %v", problems)
		}
		pipelineHarnessSaveJSON(t, t.Name()+"/external-before-recovery.json", external)
	}
	pipelineFixtureSaveJSON(t, "commit-expected.json", want)
	pipelineHarnessSaveJSON(t, t.Name()+"/commit-state.json", got)
	pipelineHarnessSaveJSON(t, t.Name()+"/commit-rows.json", got.Rows)
	for label, file := range got.Files {
		if file.Exists {
			pipelineHarnessSave(t, t.Name()+"/commit-"+label+".bin", file.Bytes)
		}
	}
	var sequence int
	var database, path string
	if err := h.fixture.db.QueryRow("PRAGMA database_list").Scan(&sequence, &database, &path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pipelineHarnessSave(t, t.Name()+"/commit-tm.db", raw)
	if !pipelineLockAvailable(t, filepath.Dir(h.options.Source)) {
		t.Fatal("publication lock was not released")
	}
	return want, got
}
