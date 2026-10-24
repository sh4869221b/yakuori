//go:build linux && pipelinefaults

package tm

import (
	"context"
	"database/sql"
)

// PipelineCommitHooks observes one eligible Commit on this Store. Before runs
// after all DML, before the context check and SQL COMMIT. Its connection is
// borrowed synchronously and must not be closed or retained. After runs only
// after successful SQL COMMIT; cancellation there does not turn success into
// a COMMIT error.
type PipelineCommitHooks struct {
	Before func(context.Context, *sql.Conn) error
	After  func()
}

// SetPipelineCommitHooks replaces the pending checkpoints. Commit consumes them
// after binding/manifest checks, even if obtaining a connection or DML fails.
// The next fresh Run and other Stores retain their ordinary nil-hook behavior.
func (s *Store) SetPipelineCommitHooks(h PipelineCommitHooks) {
	s.commitHooksMu.Lock()
	defer s.commitHooksMu.Unlock()
	s.beforeCommit, s.afterCommit = h.Before, h.After
}
