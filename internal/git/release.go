package git

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
)

type ReleaseOptions struct {
	// DiscardChanges is only appropriate after output retention or for an
	// allocation that was never handed off. Normal release protects dirty data.
	DiscardChanges bool
}

// Release removes only the verified Run worktree and its registration, retaining
// the Run branch. The caller must stop the container and hold the durable Run
// lease before releasing resources; the local file locks are not claim authority.
func (l *Local) Release(ctx context.Context, w Worktree, options ReleaseOptions) (result error) {
	target := filepath.Join(l.config.WorktreeRoot, w.RunID.String())
	if w.Path != target || w.Branch != "circular/run/"+w.RunID.String() || !canonical(target) {
		return failure(ErrRelease, w.RunID, target, -1, nil)
	}
	repositoryID, err := l.repositoryID(w.RepositoryPath)
	if err != nil {
		return failure(ErrRelease, w.RunID, target, -1, nil)
	}
	if err := ctx.Err(); err != nil {
		return failure(ErrRelease, w.RunID, target, -1, err)
	}
	// A pending Workspace may precede the first allocation, or an interrupted
	// cleanup may already have removed the empty root. Recreate only the
	// validated managed root so cross-process lock/recovery remains idempotent.
	if err := ensureRoot(l.config.WorktreeRoot); err != nil {
		return failure(ErrRelease, w.RunID, target, -1, nil)
	}
	unlock, err := l.lock(ctx, w.RunID, target)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, unlock()) }()
	unlockRepository, err := l.lock(ctx, w.RunID, w.RepositoryPath)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, unlockRepository()) }()
	if err := l.validateRepository(ctx, w); err != nil {
		return failure(ErrRelease, w.RunID, target, -1, err)
	}
	registrations, err := l.releaseStaging(ctx, w, repositoryID)
	if err != nil {
		return failure(ErrRelease, w.RunID, target, -1, err)
	}
	marker, err := hasMarker(target, w.RunID, repositoryID)
	if err != nil {
		return failure(ErrRelease, w.RunID, target, -1, nil)
	}
	if !exists(target) {
		if err := l.staleRegistration(ctx, w, target, marker, registrations); err != nil {
			return failure(ErrRelease, w.RunID, target, -1, err)
		}
		if marker {
			if err := removeMarker(target, w.RunID, repositoryID); err != nil {
				return failure(ErrRelease, w.RunID, target, -1, nil)
			}
		}
		return nil
	}
	linked, err := l.inspectLinked(ctx, target)
	if err != nil {
		if ctx.Err() != nil {
			return failure(ErrRelease, w.RunID, target, -1, ctx.Err())
		}
		if err := l.staleDirectory(ctx, w, repositoryID, marker); err != nil {
			return failure(ErrRelease, w.RunID, target, -1, err)
		}
		return nil
	}
	if linked.repository != w.RepositoryPath || linked.branch != "refs/heads/"+w.Branch {
		return failure(ErrRelease, w.RunID, target, -1, err)
	}
	if !marker {
		if err := createMarker(target, w.RunID, repositoryID); err != nil {
			return failure(ErrRelease, w.RunID, target, -1, nil)
		}
	}
	status, code, err := l.run(ctx, nil, "-C", target, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching", "--ignore-submodules=none")
	if err != nil || code != 0 || len(status) != 0 && !options.DiscardChanges {
		return failure(ErrRelease, w.RunID, target, code, err)
	}
	if err := l.removeLinked(ctx, w.RepositoryPath, target, options.DiscardChanges); err != nil {
		return failure(ErrRelease, w.RunID, target, -1, err)
	}
	if exists(target) {
		return failure(ErrRelease, w.RunID, target, -1, nil)
	}
	if err := removeMarker(target, w.RunID, repositoryID); err != nil {
		return failure(ErrRelease, w.RunID, target, -1, nil)
	}
	return nil
}

func (l *Local) releaseStaging(ctx context.Context, w Worktree, repositoryID uuid.UUID) ([]registration, error) {
	registrations, err := l.registrations(ctx, w.RepositoryPath)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Dir(w.Path))
	if err != nil {
		return nil, err
	}
	prefix := "." + w.RunID.String() + ".worktree-"
	candidates := map[string]bool{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			candidates[filepath.Join(filepath.Dir(w.Path), strings.TrimSuffix(entry.Name(), ".owner"))] = true
		}
	}
	for _, entry := range registrations {
		path := string(entry.path)
		if filepath.Dir(path) == filepath.Dir(w.Path) && strings.HasPrefix(filepath.Base(path), prefix) {
			candidates[path] = true
		}
	}
	paths := make([]string, 0, len(candidates))
	for path := range candidates {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, staging := range paths {
		marker, err := hasMarker(staging, w.RunID, repositoryID)
		if err != nil {
			return nil, err
		}
		var matches []registration
		for _, entry := range registrations {
			if bytes.Equal(entry.path, []byte(staging)) {
				matches = append(matches, entry)
			}
		}
		if len(matches) != 0 && (len(matches) != 1 || string(matches[0].branch) != "refs/heads/"+w.Branch) {
			return nil, ErrRelease
		}
		if exists(staging) {
			if !marker && !(len(matches) != 0 && l.ownedLinked(ctx, staging, w.RepositoryPath, w.Branch)) {
				return nil, ErrRelease
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			err := l.removePrivate(cleanup, w.RepositoryPath, w.Path, staging, w.Branch)
			cancel()
			if err != nil || ctx.Err() != nil {
				return nil, errors.Join(err, ctx.Err())
			}
		} else {
			current, err := l.registrations(ctx, w.RepositoryPath)
			if err != nil {
				return nil, err
			}
			if err := l.staleRegistration(ctx, w, staging, false, current); err != nil {
				return nil, err
			}
		}
		if marker {
			if err := removeMarker(staging, w.RunID, repositoryID); err != nil {
				return nil, err
			}
		}
	}
	if len(paths) != 0 {
		root, err := receiptRoot(w.Path)
		if err != nil {
			return nil, err
		}
		err = syncRoot(root)
		closeErr := root.Close()
		if err != nil || closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
	}
	return registrations, nil
}

func (l *Local) staleRegistration(ctx context.Context, w Worktree, target string, marker bool, registrations []registration) error {
	var matches []registration
	for _, entry := range registrations {
		if bytes.Equal(entry.path, []byte(target)) {
			matches = append(matches, entry)
		}
	}
	if len(matches) == 0 && !marker {
		return nil
	}
	commit, err := l.branchCommit(ctx, w.RepositoryPath, w.Branch)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return nil
	}
	if len(matches) != 1 || string(matches[0].branch) != "refs/heads/"+w.Branch || !bytes.Equal(matches[0].commit, commit) || !matches[0].prunable {
		return ErrRelease
	}
	return l.removeLinked(ctx, w.RepositoryPath, target, false)
}

func (l *Local) staleDirectory(ctx context.Context, w Worktree, repositoryID uuid.UUID, marker bool) error {
	registrations, err := l.registrations(ctx, w.RepositoryPath)
	if err != nil {
		return err
	}
	for _, entry := range registrations {
		if bytes.Equal(entry.path, []byte(w.Path)) {
			return ErrRelease
		}
	}
	if exists(filepath.Join(w.Path, ".git")) {
		if !staleBackpointer(w.Path, w.RepositoryPath) {
			return ErrRelease
		}
		if !marker {
			if err := createMarker(w.Path, w.RunID, repositoryID); err != nil {
				return errOwnership
			}
		}
	} else if !marker {
		return ErrRelease
	}
	if _, err := l.branchCommit(ctx, w.RepositoryPath, w.Branch); err != nil {
		return err
	}
	if marker, err := hasMarker(w.Path, w.RunID, repositoryID); err != nil || !marker {
		return errOwnership
	}
	// Once verified deletion begins, cancellation waits for bounded cleanup so
	// another worker cannot acquire the Repository lock while files are removed.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err = removeOwned(cleanup, w.Path)
	if err == nil {
		err = removeMarker(w.Path, w.RunID, repositoryID)
	}
	return errors.Join(err, ctx.Err())
}

func readBackpointer(target string) (string, error) {
	root, err := receiptRoot(filepath.Join(target, ".git"))
	if err != nil {
		return "", errLinked
	}
	defer root.Close()
	file, err := root.OpenFile(".git", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", errLinked
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return "", errLinked
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 || !bytes.HasPrefix(data, []byte("gitdir: ")) || bytes.ContainsRune(data, 0) {
		return "", errLinked
	}
	path := strings.TrimSuffix(strings.TrimPrefix(string(data), "gitdir: "), "\n")
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errLinked
	}
	return path, nil
}

func staleBackpointer(target, repository string) bool {
	path, err := readBackpointer(target)
	if err != nil || strings.ContainsAny(path, "\r\n") {
		return false
	}
	admin := filepath.Join(repository, ".git", "worktrees")
	info, err := os.Lstat(admin)
	return err == nil && info.IsDir() && canonical(admin) && filepath.Dir(path) == admin && canonical(path) && !exists(path)
}
