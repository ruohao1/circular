package execution

import (
	"context"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/delegations"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/runstate"
	"io"
	"os"
	"path/filepath"
)

// Checkpoint is called only after the supervisor has verified a clean CLI exit.
// Keep ownership fenced while capturing immutable internal evidence.
func (r *Retention) Checkpoint(ctx context.Context, id uuid.UUID) (checkpoint delegations.Checkpoint, result error) {
	result = r.store.WithRun(ctx, id, func(run *postgres.RunResources) error {
		state, err := run.State()
		if err != nil {
			return err
		}
		target := filepath.Join(r.worktreeRoot, id.String())
		if state.Status != runstate.Running || state.Kind != runstate.Coding || state.RepositoryID == nil || state.Workspace == nil || state.Workspace.Status != "ready" || state.Workspace.WorktreePath != target {
			return ErrRetention
		}
		progress, digest, err := run.DelegationProgress()
		if err != nil {
			return err
		}
		baseCommit, baseRef, err := r.git.Base(ctx, *state.RepositoryID, id)
		if err != nil {
			return err
		}
		file, err := os.CreateTemp("", "circular-delegation-*.tar")
		if err != nil {
			return err
		}
		defer file.Close()
		if err = os.Remove(file.Name()); err != nil {
			return err
		}
		if err = artifacts.Archive(ctx, target, file); err != nil {
			return err
		}
		if _, err = file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		checkpoint.Source, err = r.content.WriteStream(ctx, id, "delegation-source.tar", file)
		if err != nil {
			return err
		}
		checkpoint.Progress, err = r.content.Write(ctx, id, "delegation-progress.json", progress)
		if err != nil {
			return err
		}
		if err = r.content.Verify(ctx, id, checkpoint.Progress); err != nil {
			return err
		}
		source, err := r.content.OpenVerified(ctx, id, checkpoint.Source)
		if err != nil {
			return err
		}
		defer source.Close()
		checkpoint.SourceCommit, err = r.git.SnapshotDelegation(ctx, id, *state.RepositoryID, source)
		if err != nil {
			return err
		}
		checkpoint.BaseCommit, checkpoint.BaseRef, checkpoint.InputSHA256 = baseCommit, baseRef, digest
		return run.RenewLease()
	})
	return checkpoint, result
}
