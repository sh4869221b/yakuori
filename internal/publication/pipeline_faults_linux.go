//go:build linux && pipelinefaults

package publication

import (
	"errors"
	"os"
	"path/filepath"
)

// PipelineFaultHooks changes only one prepared Run, after its capability probe.
// Sync, Write and Rename hooks replace the operation and must delegate when
// success is intended. A successful rename must return nil, including when the
// hook cancels or pauses afterward. Close hooks run before the mandatory real
// Close; they inject an error or cancellation without owning descriptor teardown.
type PipelineFaultHooks struct {
	SyncStage   func(*os.File) error
	CloseStage  func(*os.File) error
	Rename      func(int, string, int, string, uint) error
	RecordWrite func(*os.File, []byte) (int, error)
	RecordSync  func(*os.File) error
	RecordClose func(*os.File) error
}

// PipelineFaultPaths contains only evidence paths owned by the prepared Run.
type PipelineFaultPaths struct {
	Stage  string
	Record string
	Backup string
}

// SetPipelineFaultHooks is called sequentially on an already-prepared Run.
// Nil fields preserve existing operations, including previously installed hooks.
func (r *Run) SetPipelineFaultHooks(h PipelineFaultHooks) PipelineFaultPaths {
	if h.SyncStage != nil {
		r.syncStage = h.SyncStage
	}
	if h.CloseStage != nil {
		r.closeStage = func(f *os.File) error {
			injected := h.CloseStage(f)
			return errors.Join(injected, f.Close())
		}
	}
	if h.Rename != nil {
		r.rename = h.Rename
	}
	if h.RecordWrite != nil {
		r.recordWrite = h.RecordWrite
	}
	if h.RecordSync != nil {
		r.recordSync = h.RecordSync
	}
	if h.RecordClose != nil {
		r.recordClose = func(f *os.File) error {
			injected := h.RecordClose(f)
			return errors.Join(injected, f.Close())
		}
	}
	paths := PipelineFaultPaths{
		Stage:  filepath.Join(r.runDir.path, "stage"),
		Record: filepath.Join(r.runDir.path, "record.json"),
	}
	if r.mode == InPlace {
		paths.Backup = r.backupPath()
	}
	return paths
}
