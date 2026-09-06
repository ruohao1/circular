package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var commitPattern = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)
var errLinked = errors.New("linked worktree metadata could not be verified")

type linkedWorktree struct{ repository, branch, commit string }
type registration struct {
	path, branch, commit []byte
	prunable             bool
}

func (l *Local) inspectLinked(ctx context.Context, path string) (linkedWorktree, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || !canonical(path) {
		return linkedWorktree{}, errLinked
	}
	info, err = os.Lstat(filepath.Join(path, ".git"))
	if err != nil || !info.Mode().IsRegular() {
		return linkedWorktree{}, errLinked
	}
	commands := [][]string{
		{"rev-parse", "--path-format=absolute", "--git-common-dir"},
		{"rev-parse", "--path-format=absolute", "--show-toplevel"},
		{"symbolic-ref", "--quiet", "HEAD"},
		{"rev-parse", "--verify", "HEAD^{commit}"},
	}
	results := make([]string, 0, len(commands))
	for _, args := range commands {
		output, code, err := l.run(ctx, nil, append([]string{"-C", path}, args...)...)
		if err != nil || code != 0 {
			return linkedWorktree{}, errors.Join(errLinked, err)
		}
		results = append(results, strings.TrimRight(string(output), "\r\n"))
	}
	if results[1] != path || filepath.Base(results[0]) != ".git" || results[2] == "" || !commitPattern.MatchString(results[3]) {
		return linkedWorktree{}, errLinked
	}
	return linkedWorktree{filepath.Dir(results[0]), results[2], strings.ToLower(results[3])}, nil
}

func (l *Local) ownedLinked(ctx context.Context, path, repository, branch string) bool {
	linked, err := l.inspectLinked(ctx, path)
	return err == nil && linked.repository == repository && linked.branch == "refs/heads/"+branch
}

func (l *Local) registrations(ctx context.Context, repository string) ([]registration, error) {
	output, code, err := l.run(ctx, nil, "-C", repository, "worktree", "list", "--porcelain", "-z")
	if err != nil || code != 0 || len(output) > 0 && !bytes.HasSuffix(output, []byte{0, 0}) {
		return nil, errors.Join(errLinked, err)
	}
	var result []registration
	for _, record := range bytes.Split(output, []byte{0, 0}) {
		if len(record) == 0 {
			continue
		}
		fields := bytes.Split(record, []byte{0})
		if !bytes.HasPrefix(fields[0], []byte("worktree ")) {
			return nil, errLinked
		}
		entry := registration{path: bytes.TrimPrefix(fields[0], []byte("worktree "))}
		branches, commits := 0, 0
		for _, field := range fields {
			switch {
			case bytes.HasPrefix(field, []byte("branch ")):
				branches++
				entry.branch = bytes.TrimPrefix(field, []byte("branch "))
			case bytes.HasPrefix(field, []byte("HEAD ")):
				commits++
				entry.commit = bytes.ToLower(bytes.TrimPrefix(field, []byte("HEAD ")))
			case bytes.Equal(field, []byte("prunable")) || bytes.HasPrefix(field, []byte("prunable ")):
				entry.prunable = true
			}
		}
		if len(entry.path) == 0 || branches > 1 || commits != 1 || !commitPattern.Match(entry.commit) {
			return nil, errLinked
		}
		result = append(result, entry)
	}
	return result, nil
}

func (l *Local) branchCommit(ctx context.Context, repository, branch string) ([]byte, error) {
	output, code, err := l.run(ctx, nil, "-C", repository, "show-ref", "--verify", "--hash", "refs/heads/"+branch)
	commit := bytes.ToLower(bytes.TrimSpace(output))
	if err != nil || code != 0 || !commitPattern.Match(commit) {
		return nil, errors.Join(errLinked, err)
	}
	return commit, nil
}
