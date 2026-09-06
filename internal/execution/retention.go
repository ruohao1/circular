// Package execution coordinates Go resource modules; it is not yet selected by
// the worker. Durable Run ownership remains PostgreSQL's responsibility.
package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
	git "github.com/ruohao1/circular/internal/git"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/runstate"
)

var ErrRetention = errors.New("Run output cannot be safely retained")

type Retention struct {
	store                        *postgres.Resources
	git                          *git.Local
	content                      *artifacts.LocalStore
	worktreeRoot, repositoryRoot string
}

// NewRetention composes the local adapters with one lease-checking persistence
// adapter and nonoverlapping roots. The Git executable/owner remain configurable
// through the existing Git seam. Serialize Finalize/Retain/Cleanup for each Run.
func NewRetention(store *postgres.Resources, config git.Config, artifactRoot string) (*Retention, error) {
	if store == nil {
		return nil, ErrRetention
	}
	roots := []string{config.WorktreeRoot, config.RepositoryCacheRoot, artifactRoot}
	for i, root := range roots {
		if !filepath.IsAbs(root) || !utf8.ValidString(root) || strings.ContainsRune(root, 0) {
			return nil, ErrRetention
		}
		resolved, err := resolve(filepath.Clean(root))
		if err != nil || resolved == string(filepath.Separator) {
			return nil, ErrRetention
		}
		roots[i] = resolved
	}
	for i, a := range roots {
		for j, b := range roots {
			if i != j && (a == b || strings.HasPrefix(a, b+string(filepath.Separator))) {
				return nil, ErrRetention
			}
		}
	}
	config.WorktreeRoot, config.RepositoryCacheRoot = roots[0], roots[1]
	local, err := git.NewLocal(config)
	if err != nil {
		return nil, err
	}
	content, err := artifacts.NewLocalStore(roots[2])
	if err != nil {
		return nil, err
	}
	return &Retention{store: store, git: local, content: content, worktreeRoot: roots[0], repositoryRoot: roots[1]}, nil
}

// Finalize captures and publishes one immutable final diff. The caller must stop
// execution first. Byte publication happens before the atomic Artifact/Events
// transaction; retries can finish an interrupted publication without overwrites.
func (r *Retention) Finalize(ctx context.Context, id uuid.UUID) (result artifacts.Record, err error) {
	state, err := r.store.Read(ctx, id)
	if err != nil {
		return result, err
	}
	target, err := r.target(state)
	if err != nil {
		return result, err
	}
	for _, a := range state.Artifacts {
		if a.ID == artifacts.DiffID(id) {
			if a.Kind != "diff" {
				return result, ErrRetention
			}
			return a, r.verify(ctx, id, a)
		}
	}
	diff, err := r.git.Capture(ctx, target)
	if err != nil {
		return result, err
	}
	content, err := r.content.Write(ctx, id, "git-diff.patch", diff.Content)
	if err != nil {
		return result, err
	}
	err = r.store.WithRun(ctx, id, func(run *postgres.RunResources) error {
		var err error
		result, err = run.PersistDiff(target, content, diff.ChangedFiles, diff.ContainsBinary)
		return err
	})
	return result, err
}

// Retain ensures both the final diff and complete worktree archive are durable.
// It does not release resources, choose a Run outcome, or release a claim.
func (r *Retention) Retain(ctx context.Context, id uuid.UUID) error {
	if _, err := r.Finalize(ctx, id); err != nil {
		return err
	}
	state, err := r.store.Read(ctx, id)
	if err != nil {
		return err
	}
	target, err := r.target(state)
	if err != nil {
		return err
	}
	for _, a := range state.Artifacts {
		if a.ID == artifacts.ArchiveID(id) {
			if a.Kind != "workspace" {
				return ErrRetention
			}
			return r.verify(ctx, id, a)
		}
	}
	content, err := r.content.WriteArchive(ctx, id, target)
	if err != nil {
		return err
	}
	return r.store.WithRun(ctx, id, func(run *postgres.RunResources) error { _, err := run.PersistArchive(target, content); return err })
}

func (r *Retention) verify(ctx context.Context, id uuid.UUID, a artifacts.Record) error {
	if a.RunID != id {
		return ErrRetention
	}
	metadata, err := json.Marshal(a.Metadata)
	if err != nil {
		return artifacts.ErrContent
	}
	var expected struct {
		Size   *int64 `json:"size_bytes"`
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(metadata, &expected); err != nil || expected.Size == nil {
		return artifacts.ErrContent
	}
	return r.content.Verify(ctx, id, artifacts.Content{URI: a.URI, SizeBytes: *expected.Size, SHA256: expected.SHA256})
}

func (r *Retention) target(state postgres.ResourceState) (string, error) {
	if state.Status != runstate.Finalizing && !state.Status.Terminal() || state.Workspace == nil || state.Workspace.Status == "released" {
		return "", ErrRetention
	}
	target := filepath.Join(r.worktreeRoot, state.RunID.String())
	if state.Workspace.RunID != state.RunID || state.Workspace.WorktreePath != target {
		return "", ErrRetention
	}
	resolved, err := resolve(target)
	if err != nil || resolved != target {
		return "", ErrRetention
	}
	return target, nil
}

func resolve(path string) (string, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return filepath.EvalSymlinks(path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err := resolve(parent)
	return filepath.Join(resolved, filepath.Base(path)), err
}
