package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

var ErrDiff = errors.New("Git diff capture failed")

type Diff struct {
	Content        []byte
	ChangedFiles   int
	ContainsBinary bool
}

func (d Diff) Empty() bool { return d.ChangedFiles == 0 }

// Capture takes a binary-capable snapshot of all non-ignored changes through a
// private index. It never changes the live index or trusts agent-written .git
// contents to select metadata outside this Run's managed Repository cache.
func (l *Local) Capture(ctx context.Context, worktree string) (diff Diff, result error) {
	runID, err := uuid.Parse(filepath.Base(worktree))
	if err != nil || filepath.Base(worktree) != runID.String() || worktree != filepath.Join(l.config.WorktreeRoot, runID.String()) || !canonical(worktree) {
		return Diff{}, ErrDiff
	}
	directory, err := l.trustedGitDirectory(worktree, runID)
	if err != nil {
		return Diff{}, ErrDiff
	}
	file, err := os.CreateTemp(filepath.Dir(worktree), "."+runID.String()+".diff-index-")
	if err != nil {
		return Diff{}, ErrDiff
	}
	index := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(index)
		return Diff{}, ErrDiff
	}
	if err := os.Remove(index); err != nil {
		return Diff{}, ErrDiff
	}
	defer func() {
		for _, path := range []string{index, index + ".lock"} {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, ErrDiff)
			}
		}
	}()
	environment := map[string]string{"GIT_DIR": directory, "GIT_COMMON_DIR": filepath.Dir(filepath.Dir(directory)), "GIT_WORK_TREE": worktree, "GIT_INDEX_FILE": index}
	commands := [][]string{
		{"read-tree", "HEAD"},
		{"add", "--all", "--", "."},
		{"diff", "--cached", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--no-color", "HEAD", "--"},
		{"diff", "--cached", "--name-only", "-z", "HEAD", "--"},
		{"diff", "--cached", "--numstat", "-z", "HEAD", "--"},
	}
	outputs := make([][]byte, 0, len(commands))
	for _, args := range commands {
		output, code, err := l.run(ctx, environment, append([]string{"--literal-pathspecs", "-C", worktree}, args...)...)
		if err != nil || code != 0 {
			return Diff{}, failure(ErrDiff, runID, worktree, code, err)
		}
		outputs = append(outputs, output)
	}
	diff.Content = outputs[2]
	for _, name := range bytes.Split(outputs[3], []byte{0}) {
		if len(name) != 0 {
			diff.ChangedFiles++
		}
	}
	for _, record := range bytes.Split(outputs[4], []byte{0}) {
		if bytes.HasPrefix(record, []byte("-\t-\t")) {
			diff.ContainsBinary = true
		}
	}
	return diff, nil
}

func (l *Local) trustedGitDirectory(worktree string, runID uuid.UUID) (string, error) {
	directory, err := readBackpointer(worktree)
	if err != nil || !canonical(directory) {
		return "", ErrDiff
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() || filepath.Base(filepath.Dir(directory)) != "worktrees" || filepath.Base(filepath.Dir(filepath.Dir(directory))) != ".git" {
		return "", ErrDiff
	}
	repository := filepath.Dir(filepath.Dir(filepath.Dir(directory)))
	if _, err := l.repositoryID(repository); err != nil {
		return "", ErrDiff
	}
	backpointer, err := os.ReadFile(filepath.Join(directory, "gitdir"))
	if err != nil || strings.TrimSpace(string(backpointer)) != filepath.Join(worktree, ".git") {
		return "", ErrDiff
	}
	head, err := os.ReadFile(filepath.Join(directory, "HEAD"))
	if err != nil || strings.TrimSpace(string(head)) != "ref: refs/heads/circular/run/"+runID.String() {
		return "", ErrDiff
	}
	return directory, nil
}
