package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/prreviews"
)

type ReviewSource struct {
	RepositoryPath, MergeBaseSHA, DiffSHA256 string
	Diff                                     []byte
	Files                                    []prreviews.ChangedFile
	Limitations                              []string
}

func reviewRef(run uuid.UUID, side string) string {
	return "refs/circular/reviews/" + run.String() + "/" + side
}

func (l *Local) PreparePRReview(ctx context.Context, run, repository uuid.UUID, cloneURL string, identity prreviews.PRIdentity) (v ReviewSource, result error) {
	if run == uuid.Nil || repository == uuid.Nil || identity.Number <= 0 || !prreviews.CommitPattern.MatchString(identity.BaseSHA) || !prreviews.CommitPattern.MatchString(identity.HeadSHA) {
		return v, prreviews.ErrSourceInvalid
	}
	target := filepath.Join(l.config.RepositoryCacheRoot, repository.String())
	if !canonical(target) || ensureRoot(l.config.RepositoryCacheRoot) != nil {
		return v, prreviews.ErrSourceInvalid
	}
	unlock, err := l.lock(ctx, repository, target)
	if err != nil {
		return v, err
	}
	defer func() { result = errors.Join(result, unlock()) }()
	environment, err := l.credentials(ctx, repository, cloneURL)
	if err != nil {
		return v, err
	}
	if !exists(target) {
		if _, err := l.initializeCache(ctx, repository, target, cloneURL, environment); err != nil {
			return v, errors.Join(prreviews.ErrSourceInvalid, err)
		}
	}
	if err := l.validateCacheStructure(ctx, repository, target); err != nil {
		return v, errors.Join(prreviews.ErrSourceInvalid, err)
	}
	origin, code, err := l.runBounded(ctx, nil, 8192, "-C", target, "remote", "get-url", "--", "origin")
	if err != nil || code != 0 || strings.TrimRight(string(origin), "\r\n") != cloneURL {
		return v, prreviews.ErrSourceInvalid
	}
	// Replacements and ambient attributes must not reinterpret trusted object IDs.
	if environment == nil {
		environment = map[string]string{}
	}
	environment["GIT_NO_REPLACE_OBJECTS"] = "1"
	environment["GIT_ATTR_NOSYSTEM"] = "1"
	exact := func(sha string) bool {
		output, code, err := l.runBounded(ctx, environment, 512, "-C", target, "rev-parse", "--verify", "--end-of-options", sha+"^{commit}")
		return err == nil && code == 0 && string(bytes.TrimSpace(output)) == sha
	}
	for _, sha := range []string{identity.BaseSHA, identity.HeadSHA} {
		if !exact(sha) {
			_, _, _ = l.runBounded(ctx, environment, 4096, "-C", target, "fetch", "--no-tags", "--", "origin", sha)
		}
		if !exact(sha) {
			_, _, _ = l.runBounded(ctx, environment, 4096, "-C", target, "fetch", "--no-tags", "--", "origin", "refs/pull/"+strconv.Itoa(identity.Number)+"/head")
		}
		if !exact(sha) {
			return v, prreviews.ErrSourceInvalid
		}
	}
	bounded := func(limit int, args ...string) ([]byte, error) {
		out, code, err := l.runBounded(ctx, environment, limit, append([]string{"-C", target}, args...)...)
		if err != nil || code != 0 {
			return nil, errors.Join(prreviews.ErrSourceInvalid, err)
		}
		return out, nil
	}
	merge, err := bounded(512, "merge-base", identity.BaseSHA, identity.HeadSHA)
	if err != nil {
		return v, err
	}
	v.MergeBaseSHA = strings.TrimSpace(string(merge))
	if !prreviews.CommitPattern.MatchString(v.MergeBaseSHA) || !exact(v.MergeBaseSHA) {
		return v, prreviews.ErrSourceInvalid
	}
	for _, pin := range []struct{ side, sha string }{{"base", identity.BaseSHA}, {"head", identity.HeadSHA}, {"merge-base", v.MergeBaseSHA}} {
		ref := reviewRef(run, pin.side)
		current, code, err := l.runBounded(ctx, environment, 512, "-C", target, "show-ref", "--verify", "--hash", ref)
		if err != nil {
			return v, prreviews.ErrSourceInvalid
		}
		if code == 0 {
			if strings.TrimSpace(string(current)) != pin.sha {
				return v, prreviews.ErrSourceInvalid
			}
			continue
		}
		if _, err := bounded(512, "update-ref", ref, pin.sha, strings.Repeat("0", len(pin.sha))); err != nil {
			return v, err
		}
	}
	// A clone with a deleted remote default can have an unborn HEAD. Give the
	// managed cache an exact object so the existing worktree validator can operate.
	if _, code, err := l.runBounded(ctx, environment, 512, "-C", target, "rev-parse", "--verify", "HEAD^{commit}"); err != nil || code != 0 {
		if _, err := bounded(512, "update-ref", "HEAD", identity.HeadSHA); err != nil {
			return v, err
		}
	}
	names, err := bounded(prreviews.MaxContextBytes, "diff", "--no-ext-diff", "--no-textconv", "--find-renames", "--name-status", "-z", v.MergeBaseSHA, identity.HeadSHA, "--")
	if err != nil {
		return v, err
	}
	v.Files, err = reviewFiles(names)
	if err != nil {
		return v, err
	}
	v.Diff, err = bounded(prreviews.MaxDiffBytes, "diff", "--no-ext-diff", "--no-textconv", "--find-renames", "--binary", "--full-index", "--unified=0", v.MergeBaseSHA, identity.HeadSHA, "--")
	if err != nil {
		return v, err
	}
	v.Limitations, err = l.reviewManifest(ctx, target, environment, v.MergeBaseSHA, identity.HeadSHA, v.Files)
	if err != nil {
		return v, err
	}
	checksum := sha256.Sum256(v.Diff)
	v.DiffSHA256 = hex.EncodeToString(checksum[:])
	v.RepositoryPath = target
	return v, nil
}
