package git

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrPath       = errors.New("invalid managed Run worktree path")
	ErrRepository = errors.New("invalid worktree Repository")
	ErrConflict   = errors.New("Run worktree path or branch is already allocated")
	ErrRef        = errors.New("Run base ref resolution failed")
	ErrProvision  = errors.New("Run worktree provisioning failed")
	ErrRelease    = errors.New("Run worktree release failed")
)

type Worktree struct {
	RunID          uuid.UUID
	RepositoryPath string
	Path           string
	Branch         string
}

func (l *Local) repositoryID(path string) (uuid.UUID, error) {
	id, err := uuid.Parse(filepath.Base(path))
	if err != nil || filepath.Base(path) != id.String() || path != filepath.Join(l.config.RepositoryCacheRoot, id.String()) || !canonical(path) {
		return uuid.Nil, ErrRepository
	}
	return id, nil
}

func (l *Local) validateRepository(ctx context.Context, worktree Worktree) error {
	if err := l.validateCache(ctx, worktree.RunID, worktree.RepositoryPath); err != nil {
		return failure(ErrRepository, worktree.RunID, worktree.Path, -1, err)
	}
	common, code, err := l.run(ctx, nil, "-C", worktree.RepositoryPath, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || code != 0 || strings.TrimRight(string(common), "\r\n") != filepath.Join(worktree.RepositoryPath, ".git") {
		return failure(ErrRepository, worktree.RunID, worktree.Path, code, err)
	}
	return nil
}

// Provision allocates one linked worktree and branch for a Run. It never reuses
// a live path or existing branch. Run locks precede Repository locks; both use
// Python-compatible flock files and remain held until rollback has settled.
func (l *Local) Provision(ctx context.Context, runID uuid.UUID, repository, baseRef string) (worktree Worktree, result error) {
	target := filepath.Join(l.config.WorktreeRoot, runID.String())
	worktree = Worktree{RunID: runID, RepositoryPath: repository, Path: target, Branch: "circular/run/" + runID.String()}
	if !canonical(target) {
		return Worktree{}, failure(ErrConflict, runID, target, -1, nil)
	}
	if err := ensureRoot(l.config.WorktreeRoot); err != nil {
		return Worktree{}, failure(ErrPath, runID, target, -1, nil)
	}
	repositoryID, err := l.repositoryID(repository)
	if err != nil {
		return Worktree{}, failure(ErrRepository, runID, target, -1, nil)
	}
	marker, _ := markerName(target, runID)
	conflict := func() bool { return exists(target) || exists(filepath.Join(l.config.WorktreeRoot, marker)) }
	if conflict() {
		return Worktree{}, failure(ErrConflict, runID, target, -1, nil)
	}
	unlock, err := l.lock(ctx, runID, target)
	if err != nil {
		return Worktree{}, err
	}
	defer func() { result = errors.Join(result, unlock()) }()
	if conflict() {
		return Worktree{}, failure(ErrConflict, runID, target, -1, nil)
	}
	unlockRepository, err := l.lock(ctx, runID, repository)
	if err != nil {
		return Worktree{}, err
	}
	defer func() { result = errors.Join(result, unlockRepository()) }()
	if err := l.validateRepository(ctx, worktree); err != nil {
		return Worktree{}, err
	}
	if conflict() {
		return Worktree{}, failure(ErrConflict, runID, target, -1, nil)
	}
	_, code, err := l.run(ctx, nil, "-C", repository, "show-ref", "--verify", "--quiet", "refs/heads/"+worktree.Branch)
	if code == 0 && err == nil {
		return Worktree{}, failure(ErrConflict, runID, target, -1, nil)
	}
	if err != nil || code != 1 {
		return Worktree{}, failure(ErrProvision, runID, target, code, err)
	}
	if baseRef == "" || !safeText(baseRef) || strings.ContainsAny(baseRef, "\r\n") {
		return Worktree{}, failure(ErrRef, runID, target, -1, nil)
	}
	output, code, err := l.run(ctx, nil, "-C", repository, "rev-parse", "--verify", "--end-of-options", baseRef+"^{commit}")
	commit := strings.ToLower(strings.TrimSpace(string(output)))
	if err != nil || code != 0 || !commitPattern.MatchString(commit) {
		return Worktree{}, failure(ErrRef, runID, target, code, err)
	}
	staging, err := os.MkdirTemp(l.config.WorktreeRoot, "."+runID.String()+".worktree-")
	if err != nil {
		return Worktree{}, failure(ErrProvision, runID, target, -1, nil)
	}
	published, branchMayBeOwned := false, false
	allocation := worktree
	defer func() {
		if !published {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := l.rollback(cleanup, allocation, repositoryID, staging, commit, branchMayBeOwned); err != nil {
				cleanupError := failure(ErrCleanup, runID, target, -1, err)
				var primary *Error
				if errors.As(result, &primary) {
					primary.CleanupError = cleanupError
				} else {
					result = errors.Join(result, cleanupError)
				}
			}
		}
	}()
	if err := createMarker(staging, runID, repositoryID); err != nil {
		return Worktree{}, failure(ErrProvision, runID, target, -1, nil)
	}
	branchMayBeOwned = true
	for _, args := range [][]string{
		{"-C", repository, "worktree", "add", "-b", worktree.Branch, staging, commit},
		{"-C", repository, "worktree", "move", staging, target},
	} {
		_, code, err := l.run(ctx, nil, args...)
		if err != nil || code != 0 {
			return Worktree{}, failure(ErrProvision, runID, target, code, err)
		}
	}
	linked, err := l.inspectLinked(ctx, target)
	if err != nil || linked.repository != repository || linked.branch != "refs/heads/"+worktree.Branch || linked.commit != commit {
		return Worktree{}, failure(ErrProvision, runID, target, -1, err)
	}
	if err := createMarker(target, runID, repositoryID); err != nil {
		kind := ErrProvision
		if errors.Is(err, os.ErrExist) {
			kind = ErrConflict
		}
		return Worktree{}, failure(kind, runID, target, -1, nil)
	}
	if err := removeMarker(staging, runID, repositoryID); err != nil {
		return Worktree{}, failure(ErrProvision, runID, target, -1, nil)
	}
	if l.config.Owner != nil {
		if err := applyOwner(ctx, target, *l.config.Owner); err != nil {
			return Worktree{}, failure(ErrProvision, runID, target, -1, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return Worktree{}, err
	}
	published = true
	return worktree, nil
}

func applyOwner(ctx context.Context, target string, owner FileOwner) error {
	parent, err := receiptRoot(target)
	if err != nil {
		return err
	}
	defer parent.Close()
	root, err := openDirectory(parent, filepath.Base(target))
	if err != nil {
		return err
	}
	defer root.Close()
	return fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return root.Lchown(path, owner.UID, owner.GID)
	})
}

func (l *Local) rollback(ctx context.Context, w Worktree, repositoryID uuid.UUID, staging, commit string, branchMayBeOwned bool) error {
	if l.ownedLinked(ctx, w.Path, w.RepositoryPath, w.Branch) {
		if err := l.removeLinked(ctx, w.RepositoryPath, w.Path, true); err != nil {
			return err
		}
	}
	if err := l.removePrivate(ctx, w.RepositoryPath, w.Path, staging, w.Branch); err != nil {
		return err
	}
	if err := removeMarker(staging, w.RunID, repositoryID); err != nil {
		return err
	}
	if branchMayBeOwned {
		_, code, err := l.run(ctx, nil, "-C", w.RepositoryPath, "update-ref", "-d", "refs/heads/"+w.Branch, commit)
		if err != nil || code != 0 {
			return errors.Join(ErrCleanup, err)
		}
	}
	return removeMarker(w.Path, w.RunID, repositoryID)
}

func (l *Local) removeLinked(ctx context.Context, repository, path string, force bool) error {
	args := []string{"-C", repository, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	_, code, err := l.run(ctx, nil, append(args, path)...)
	if err != nil || code != 0 {
		return errors.Join(ErrRelease, err)
	}
	return nil
}

func (l *Local) removePrivate(ctx context.Context, repository, target, staging, branch string) error {
	if filepath.Dir(staging) != filepath.Dir(target) || !strings.HasPrefix(filepath.Base(staging), "."+filepath.Base(target)+".worktree-") {
		return ErrCleanup
	}
	if exists(staging) {
		_, _, _ = l.run(ctx, nil, "-C", repository, "worktree", "remove", "--force", staging)
		if exists(staging) {
			if err := removeOwned(ctx, staging); err != nil {
				return err
			}
		}
	}
	registrations, err := l.registrations(ctx, repository)
	if err != nil {
		return err
	}
	var matches []registration
	for _, entry := range registrations {
		if bytes.Equal(entry.path, []byte(staging)) {
			matches = append(matches, entry)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	if len(matches) != 1 || string(matches[0].branch) != "refs/heads/"+branch || !matches[0].prunable {
		return ErrCleanup
	}
	return l.removeLinked(ctx, repository, staging, true)
}
