package git

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ruohao1/circular/internal/prreviews"
)

var reviewObjectPath = regexp.MustCompile(`^objects/([0-9a-f]{2}/[0-9a-f]{38,62}|pack/pack-[0-9a-f]{40,64}\.(pack|idx|rev))$`)

// PreparePRReviewGit builds a self-contained Git directory beside the immutable
// review inputs. It fetches only the captured commits and their ancestry, never
// copies the shared cache's configuration, hooks, credentials, or other refs,
// and leaves the host worktree registration intact for normal cleanup.
// The caller must hold the Run allocation lock until context persistence.
func (l *Local) PreparePRReviewGit(ctx context.Context, directory string) (result error) {
	value, err := prreviews.ReadContext(directory)
	if err != nil || filepath.Base(directory) != value.RunID.String() {
		return prreviews.ErrSourceInvalid
	}
	repository := filepath.Join(l.config.RepositoryCacheRoot, value.Snapshot.RepositoryID.String())
	unlock, err := l.lock(ctx, value.RunID, repository)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, unlock()) }()
	if err := l.validateCache(ctx, value.RunID, repository); err != nil {
		return prreviews.ErrSourceInvalid
	}
	environment := map[string]string{
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_COUNT": "0",
		"GIT_NO_REPLACE_OBJECTS": "1", "GIT_ATTR_NOSYSTEM": "1",
	}
	target := filepath.Join(directory, "git")
	if exists(target) {
		return l.verifyPRReviewGit(ctx, target, value, environment)
	}
	staging, err := os.MkdirTemp(directory, ".git-")
	if err != nil {
		return prreviews.ErrSourceInvalid
	}
	defer os.RemoveAll(staging)
	format := "sha1"
	if len(value.Snapshot.PR.HeadSHA) == 64 {
		format = "sha256"
	}
	for _, args := range [][]string{
		{"init", "--bare", "--template=", "--object-format=" + format, staging},
		{"-C", staging, "fetch", "--no-tags", "--no-write-fetch-head", "--no-auto-maintenance", "--", repository,
			value.Snapshot.PR.BaseSHA + ":refs/review/base", value.Snapshot.PR.HeadSHA + ":refs/review/head", value.MergeBaseSHA + ":refs/review/merge-base"},
		{"-C", staging, "update-ref", "--no-deref", "HEAD", value.Snapshot.PR.HeadSHA},
		{"-C", staging, "read-tree", value.Snapshot.PR.HeadSHA},
	} {
		if _, code, err := l.runBounded(ctx, environment, 4096, args...); err != nil || code != 0 {
			return errors.Join(prreviews.ErrSourceInvalid, err)
		}
	}
	if err := os.WriteFile(filepath.Join(staging, "config"), []byte(reviewGitConfig(format)), 0644); err != nil {
		return prreviews.ErrSourceInvalid
	}
	if err := os.Chmod(staging, 0755); err != nil {
		return prreviews.ErrSourceInvalid
	}
	if err := l.verifyPRReviewGit(ctx, staging, value, environment); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(staging, target)
}

func reviewGitConfig(format string) string {
	config := "[core]\n\trepositoryformatversion = 0\n\tbare = false\n\thooksPath = /dev/null\n"
	if format == "sha256" {
		config = strings.Replace(config, "repositoryformatversion = 0", "repositoryformatversion = 1", 1) + "[extensions]\n\tobjectFormat = sha256\n"
	}
	return config
}

func (l *Local) verifyPRReviewGit(ctx context.Context, directory string, value prreviews.Context, environment map[string]string) error {
	if resolved, err := filepath.EvalSymlinks(directory); err != nil || resolved != directory {
		return prreviews.ErrSourceInvalid
	}
	// Reused inputs must remain ordinary, self-contained files. In particular,
	// Git alternates, commondir pointers, hooks, and config includes must never
	// reintroduce the shared cache or other host paths on recovery.
	if err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return prreviews.ErrSourceInvalid
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return prreviews.ErrSourceInvalid
		}
		name, err := filepath.Rel(directory, path)
		if err != nil {
			return prreviews.ErrSourceInvalid
		}
		switch name {
		case "HEAD", "config", "index", "packed-refs", "refs/review/base", "refs/review/head", "refs/review/merge-base":
			return nil
		}
		if !reviewObjectPath.MatchString(filepath.ToSlash(name)) {
			return prreviews.ErrSourceInvalid
		}
		return nil
	}); err != nil {
		return err
	}
	format := "sha1"
	if len(value.Snapshot.PR.HeadSHA) == 64 {
		format = "sha256"
	}
	config, err := os.ReadFile(filepath.Join(directory, "config"))
	if err != nil || string(config) != reviewGitConfig(format) {
		return prreviews.ErrSourceInvalid
	}
	for _, check := range []struct {
		args []string
		want string
	}{
		{[]string{"rev-parse", "HEAD"}, value.Snapshot.PR.HeadSHA},
		{[]string{"for-each-ref", "--format=%(objectname) %(refname)"}, value.Snapshot.PR.BaseSHA + " refs/review/base\n" + value.Snapshot.PR.HeadSHA + " refs/review/head\n" + value.MergeBaseSHA + " refs/review/merge-base"},
		{[]string{"diff", "--cached", "--exit-code", "--no-ext-diff", "--no-textconv", "HEAD", "--"}, ""},
	} {
		output, code, err := l.runBounded(ctx, environment, prreviews.MaxDiffBytes, append([]string{"--git-dir=" + directory}, check.args...)...)
		if err != nil || code != 0 || strings.TrimSpace(string(output)) != check.want {
			return prreviews.ErrSourceInvalid
		}
	}
	if _, code, err := l.runBounded(ctx, environment, 4096, "--git-dir="+directory, "fsck", "--full", "--no-dangling", "--no-reflogs"); err != nil || code != 0 {
		return prreviews.ErrSourceInvalid
	}
	return nil
}
