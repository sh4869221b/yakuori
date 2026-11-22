//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"path/filepath"
	"testing"

	"github.com/sh4869221b/yakuori/internal/publication"
	"golang.org/x/sys/unix"
)

func TestNativeContextTMRecovery(t *testing.T) {
	f := newNativeContextFixture(t)
	h := nativeContextEngine(t, false, "猫", nil)
	pack := f.pack(t, f.scope, nil)
	options := publication.Options{Source: f.source, Output: f.source, Mode: publication.InPlace}
	injected := errors.New("fixture publish rename failure")
	var paths publication.PipelineFaultPaths
	var stageRenameFailed bool
	observation := &AcceptanceObservation{SourceSHA256: f.session.ArtifactDigest()}
	result, err := h.core.WithAcceptanceObservation(observation).fileWithTM(t.Context(), options, f.adapter, h.profile, nil,
		&tmExecution{store: f.store, scope: f.scope, contextPack: pack}, func(ctx context.Context, options publication.Options) (publicationRun, error) {
			run, err := publication.PrepareWithLimits(ctx, options, h.core.limits)
			if err != nil {
				return nil, err
			}
			paths = run.SetPipelineFaultHooks(publication.PipelineFaultHooks{Rename: func(oldFD int, oldName string, newFD int, newName string, flags uint) error {
				if oldName == filepath.Base(paths.Stage) && newName == filepath.Base(f.source) {
					w3SQLiteAssertNoOutput(t, f.source)
					if !bytes.Equal(fileBytes(t, paths.Backup), f.raw) {
						t.Fatal("original was not backed up before the stage rename")
					}
					stageRenameFailed = true
					return injected
				}
				return unix.Renameat2(oldFD, oldName, newFD, newName, flags)
			}})
			return run, nil
		})
	var failure *Error
	if !errors.Is(err, injected) || !errors.As(err, &failure) || failure.Phase != "publish" || !stageRenameFailed ||
		!result.TMCommitted || result.Publication.State != publication.Restored || !observation.FinalValidated || len(h.requests) != 2 {
		t.Fatalf("post-commit failure: result=%+v error=%v Generate=%d validated=%v injected=%v",
			result, err, len(h.requests), observation.FinalValidated, stageRenameFailed)
	}
	if !bytes.Equal(fileBytes(t, f.source), f.raw) {
		t.Fatal("recovery did not restore original bytes")
	}
	rows := w3SQLiteRows(t, f.db)
	if len(rows) != len(nativeContextTexts) {
		t.Fatalf("verified rows did not survive publish failure: %v", rows)
	}
	for _, text := range nativeContextTexts {
		f.row(t, text)
	}
	oldStage := fileBytes(t, paths.Stage)
	h.requests = nil
	retry, err := h.core.FileWithTMContext(t.Context(), options, f.adapter, h.profile, f.store, f.scope, pack)
	if err != nil || !retry.TMCommitted || retry.Publication.State != publication.Published ||
		retry.Publication.RunID == result.Publication.RunID || len(h.requests) != 0 {
		t.Fatalf("confirmed-pack rerun: result=%+v error=%v Generate=%d", retry, err, len(h.requests))
	}
	if got := w3SQLiteObserve(t, f.adapter, f.source); !maps.Equal(got, nativeContextTexts) {
		t.Fatalf("restored then published W3 output=%v", got)
	}
	if !bytes.Equal(fileBytes(t, retry.Publication.BackupPath), f.raw) || !bytes.Equal(fileBytes(t, paths.Stage), oldStage) {
		t.Fatal("rerun did not preserve the original backup and the unpublished old stage")
	}
	w3SQLiteAssertRows(t, f.db, rows)
}
