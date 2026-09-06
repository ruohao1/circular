package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Checkout publishes or refreshes the exact UUID-owned Repository cache. The
// base ref is refreshed without checking files out into the shared cache.
func (l *Local) Checkout(ctx context.Context, id uuid.UUID, cloneURL string) (path string, result error) {
	target := filepath.Join(l.config.RepositoryCacheRoot, id.String())
	if !canonical(target) {
		return "", failure(ErrInvalidCache, id, target, -1, nil)
	}
	if err := ensureRoot(l.config.RepositoryCacheRoot); err != nil {
		return "", failure(ErrLock, id, target, -1, nil)
	}
	unlock, err := l.lock(ctx, id, target)
	if err != nil {
		return "", err
	}
	defer func() { result = errors.Join(result, unlock()) }()
	if !canonical(target) {
		return "", failure(ErrInvalidCache, id, target, -1, nil)
	}
	if exists(target) {
		if err := l.validateCache(ctx, id, target); err != nil {
			return "", err
		}
		previous, err := l.refresh(ctx, id, target, "remote", "get-url", "--", "origin")
		if err != nil {
			return "", err
		}
		if _, err := l.refresh(ctx, id, target, "remote", "set-url", "--", "origin", cloneURL); err != nil {
			return "", err
		}
		if _, err := l.refresh(ctx, id, target, "fetch", "--prune", "--", "origin"); err != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_, _ = l.refresh(cleanup, id, target, "remote", "set-url", "--", "origin", strings.TrimRight(string(previous), "\r\n"))
			return "", err
		}
		ref, err := l.refresh(ctx, id, target, "symbolic-ref", "--quiet", "HEAD")
		if err != nil {
			return "", err
		}
		localRef := strings.TrimSpace(string(ref))
		if !strings.HasPrefix(localRef, "refs/heads/") {
			return "", failure(ErrFetch, id, target, -1, nil)
		}
		remoteRef := "refs/remotes/origin/" + strings.TrimPrefix(localRef, "refs/heads/")
		if _, err := l.refresh(ctx, id, target, "update-ref", localRef, remoteRef); err != nil {
			return "", err
		}
		return target, nil
	}
	staging, err := os.MkdirTemp(l.config.RepositoryCacheRoot, "."+id.String()+".clone-")
	if err != nil {
		return "", failure(ErrClone, id, target, -1, nil)
	}
	published := false
	defer func() {
		if !published {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if err := removeOwned(cleanup, staging); err != nil {
				result = errors.Join(result, failure(ErrCleanup, id, target, -1, err))
			}
		}
	}()
	_, code, err := l.run(ctx, nil, "clone", "--no-checkout", "--", cloneURL, staging)
	if err != nil || code != 0 {
		return "", failure(ErrClone, id, target, code, err)
	}
	if err := l.validateCache(ctx, id, staging); err != nil {
		return "", failure(ErrInvalidCache, id, target, -1, err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if exists(target) {
		return "", failure(ErrInvalidCache, id, target, -1, nil)
	}
	if err := os.Rename(staging, target); err != nil {
		return "", failure(ErrClone, id, target, -1, nil)
	}
	published = true
	return target, nil
}

func (l *Local) refresh(ctx context.Context, id uuid.UUID, target string, args ...string) ([]byte, error) {
	output, code, err := l.run(ctx, nil, append([]string{"-C", target}, args...)...)
	if err != nil || code != 0 {
		return nil, failure(ErrFetch, id, target, code, err)
	}
	return output, nil
}

func (l *Local) validateCache(ctx context.Context, id uuid.UUID, path string) error {
	if !canonical(path) {
		return failure(ErrInvalidCache, id, path, -1, nil)
	}
	info, err := os.Lstat(filepath.Join(path, ".git"))
	if err != nil || !info.IsDir() || !canonical(filepath.Join(path, ".git")) {
		return failure(ErrInvalidCache, id, path, -1, nil)
	}
	inside, code, err := l.run(ctx, nil, "-C", path, "rev-parse", "--is-inside-work-tree")
	if err != nil || code != 0 || !bytes.Equal(bytes.TrimSpace(inside), []byte("true")) {
		return failure(ErrInvalidCache, id, path, code, err)
	}
	head, code, err := l.run(ctx, nil, "-C", path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || code != 0 || len(bytes.TrimSpace(head)) == 0 {
		return failure(ErrInvalidCache, id, path, code, err)
	}
	return nil
}
