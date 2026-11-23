//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sh4869221b/yakuori/internal/publication"
	"golang.org/x/sys/unix"
)

func TestGenericJSONContextTMRecovery(t *testing.T) {
	f := newGenericJSONContextFixture(t)
	options := publication.Options{Source: f.source, Output: f.source, Mode: publication.InPlace}
	rows := []genericJSONTMRow{f.row(t, 0, genericJSONLines[0]), f.row(t, 1, genericJSONLines[1])}
	f.assertRows(t)
	injected := errors.New("fixture stage rename failure")
	var paths publication.PipelineFaultPaths
	obs := &AcceptanceObservation{SourceSHA256: f.session.ArtifactDigest()}
	result, err := f.h.core.WithAcceptanceObservation(obs).fileWithTM(t.Context(), options, newGenericJSONAdapter(t), f.h.profile, nil,
		&tmExecution{store: f.store, scope: genericJSONScope, contextPack: readGenericJSONPack(t, f.packPath)},
		func(ctx context.Context, options publication.Options) (publicationRun, error) {
			run, err := publication.PrepareWithLimits(ctx, options, f.h.core.limits)
			if err != nil {
				return nil, err
			}
			paths = run.SetPipelineFaultHooks(publication.PipelineFaultHooks{Rename: func(oldFD int, oldName string, newFD int, newName string, flags uint) error {
				if oldName == filepath.Base(paths.Stage) && newName == filepath.Base(f.source) {
					f.assertRows(t, rows...)
					if _, err := os.Lstat(f.source); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("original path was not vacated for publication: %v", err)
					}
					if !bytes.Equal(fileBytes(t, paths.Backup), f.raw) {
						t.Fatal("backup did not contain original JSON")
					}
					return injected
				}
				return unix.Renameat2(oldFD, oldName, newFD, newName, flags)
			}})
			return run, nil
		})
	var failure *Error
	if !errors.Is(err, injected) || !errors.As(err, &failure) || failure.Phase != "publish" || !result.TMCommitted ||
		result.Publication.State != publication.Restored || result.Publication.RunID == "" ||
		len(f.h.requests) != 2 || !obs.FinalValidated || obs.Validation != (ValidationCounts{Attempted: 2, Passed: 2}) {
		t.Fatalf("result=%+v error=%v Generate=%d observation=%+v", result, err, len(f.h.requests), obs)
	}
	if !bytes.Equal(fileBytes(t, f.source), f.raw) {
		t.Fatal("in-place recovery did not restore original JSON")
	}
	f.assertRows(t, rows...)
	oldStage := f.assertOutput(t, paths.Stage, genericJSONLines)

	f.h.requests = nil
	rerunObs := &AcceptanceObservation{SourceSHA256: f.session.ArtifactDigest()}
	rerun, err := f.h.core.WithAcceptanceObservation(rerunObs).FileWithTMContext(t.Context(), options, newGenericJSONAdapter(t),
		f.h.profile, f.store, genericJSONScope, readGenericJSONPack(t, f.packPath))
	f.assertSuccess(t, rerun, err, rerunObs, genericJSONLines, [2]string{"tm", "tm"})
	if rerun.Publication.RunID == "" || rerun.Publication.RunID == result.Publication.RunID ||
		rerunObs.TMValidation != (ValidationCounts{Attempted: 2, Passed: 2}) {
		t.Fatalf("rerun did not use a fresh publication run and revalidate both hits: result=%+v validation=%+v",
			rerun, rerunObs.TMValidation)
	}
	f.assertOutput(t, f.source, genericJSONLines)
	if !bytes.Equal(fileBytes(t, rerun.Publication.BackupPath), f.raw) || !bytes.Equal(fileBytes(t, paths.Stage), oldStage) {
		t.Fatal("rerun did not preserve the original backup and the old unpublished stage")
	}
	f.assertRows(t, rows...)
}
