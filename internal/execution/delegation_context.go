package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/delegations"
	"github.com/ruohao1/circular/internal/postgres"
)

func (s *Supervisor) prepareDelegationContext(ctx context.Context, id uuid.UUID) (digest string, result error) {
	var input postgres.ProvisioningContext
	var checkpoint delegations.Checkpoint
	var outcome delegations.Result
	// Persist cleanup identity before publishing files, even if allocation fails.
	result = s.store.WithRun(ctx, id, func(r *postgres.RunResources) error {
		var err error
		input, checkpoint, outcome, err = r.ContinuationContext()
		return err
	})
	if result != nil {
		return "", result
	}
	result = s.store.WithRun(ctx, id, func(r *postgres.RunResources) error {
		state, err := r.State()
		if err != nil {
			return err
		}
		path := filepath.Join(s.retention.worktreeRoot, id.String())
		if state.Workspace == nil || state.Workspace.WorktreePath != path || state.Workspace.Status != "suspended" || state.Workspace.ContainerID != nil {
			return postgres.ErrResourceState
		}
		commit, ref, err := s.retention.git.Base(ctx, input.RepositoryID, id)
		if err != nil || commit != checkpoint.BaseCommit || ref != checkpoint.BaseRef {
			return ErrRetention
		}
		if err = verifyDelegationWorkspace(ctx, path, checkpoint.Source); err != nil {
			return err
		}
		if err = s.retention.content.Verify(ctx, id, checkpoint.Source); err != nil {
			return err
		}
		files := map[string]io.Reader{}
		for _, entry := range []struct {
			name    string
			owner   uuid.UUID
			content *artifacts.Content
		}{
			{"progress.json", id, &checkpoint.Progress}, {"report.txt", outcome.ChildRunID, outcome.Report}, {"diff.patch", outcome.ChildRunID, outcome.Patch},
		} {
			if entry.content == nil {
				continue
			}
			file, err := s.retention.content.OpenVerified(ctx, entry.owner, *entry.content)
			if err != nil {
				return err
			}
			defer file.Close()
			files[entry.name] = file
		}
		value := delegations.Context{RunID: id, Checkpoint: checkpoint, Result: outcome}
		if err = delegations.WriteContext(filepath.Join(s.retention.worktreeRoot, ".delegations", id.String()), value, files); err != nil {
			return err
		}
		digest = input.DelegationContextSHA256
		return r.RenewLease()
	})
	return digest, result
}

func verifyDelegationWorkspace(ctx context.Context, path string, source artifacts.Content) error {
	h := sha256.New()
	if err := artifacts.Archive(ctx, path, h); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != source.SHA256 {
		return ErrRetention
	}
	return nil
}

func (r *Retention) removeDelegationContext(state postgres.ResourceState) error {
	path := filepath.Join(r.worktreeRoot, ".delegations", state.RunID.String())
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	value, err := delegations.ReadContext(path)
	if err != nil {
		return err
	}
	digest, err := delegations.ContextFingerprint(value)
	if err != nil || value.RunID != state.RunID || digest != state.DelegationContextSHA256 {
		return ErrRetention
	}
	return os.RemoveAll(path)
}

func (r *Retention) retainDelegationReport(ctx context.Context, id uuid.UUID, path string) error {
	return r.store.WithRun(ctx, id, func(run *postgres.RunResources) error {
		report, err := run.DelegationReport()
		if err != nil || report == "" {
			return err
		}
		content, err := r.content.Write(ctx, id, "delegation-report.txt", []byte(report))
		if err != nil {
			return err
		}
		if err = r.content.Verify(ctx, id, content); err != nil {
			return err
		}
		return run.PersistDelegationReport(path, content)
	})
}
