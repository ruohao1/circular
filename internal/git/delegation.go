package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
)

func delegationRef(id uuid.UUID) string { return "refs/circular/delegations/" + id.String() }

func (l *Local) captureDelegation(ctx context.Context, worktree, repository string, base runBase) (Diff, error) {
	tree, err := l.delegationTree(ctx, base.RunID, repository, worktree)
	if err != nil {
		return Diff{}, err
	}
	diff := Diff{BaseCommit: base.Commit, BaseRef: base.Ref}
	outputs := make([][]byte, 0, 3)
	for _, flags := range [][]string{{"--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--no-color"}, {"--name-only", "-z"}, {"--numstat", "-z"}} {
		args := append([]string{"-C", repository, "diff"}, flags...)
		args = append(args, base.Commit, tree, "--")
		out, code, err := l.run(ctx, delegationEnvironment(), args...)
		if err != nil || code != 0 {
			return Diff{}, ErrDiff
		}
		outputs = append(outputs, out)
	}
	diff.Content = outputs[0]
	for _, name := range bytes.Split(outputs[1], []byte{0}) {
		if len(name) > 0 {
			diff.ChangedFiles++
		}
	}
	for _, record := range bytes.Split(outputs[2], []byte{0}) {
		if bytes.HasPrefix(record, []byte("-\t-\t")) {
			diff.ContainsBinary = true
		}
	}
	return diff, nil
}

// SnapshotDelegation hashes source bytes without filters or repository config.
// Its private index and synthetic commit never change the parent's base or index.
func (l *Local) SnapshotDelegation(ctx context.Context, parentRunID, repositoryID uuid.UUID, archive io.Reader) (commit string, result error) {
	repository := filepath.Join(l.config.RepositoryCacheRoot, repositoryID.String())
	if parentRunID == uuid.Nil || repositoryID == uuid.Nil {
		return "", ErrBase
	}
	if _, err := l.repositoryID(repository); err != nil {
		return "", err
	}
	unlock, err := l.lock(ctx, parentRunID, repository)
	if err != nil {
		return "", err
	}
	defer func() { result = errors.Join(result, unlock()) }()
	base, err := l.readBase(ctx, repository, repositoryID, parentRunID)
	if err != nil {
		return "", err
	}
	directory, err := os.MkdirTemp(l.config.RepositoryCacheRoot, ".delegation-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(directory)
	source := filepath.Join(directory, "source")
	if err = artifacts.RestoreArchive(ctx, archive, source); err != nil {
		return "", err
	}
	tree, err := l.delegationTree(ctx, parentRunID, repository, source)
	if err != nil {
		return "", err
	}
	environment := delegationEnvironment()
	value, code, err := l.run(ctx, environment, "-C", repository, "commit-tree", tree, "-p", base.Commit, "-m", "Circular delegation checkpoint")
	if err != nil || code != 0 {
		return "", ErrBase
	}
	commit = strings.TrimSpace(string(value))
	if !commitPattern.MatchString(commit) {
		return "", ErrBase
	}
	previous, code, err := l.run(ctx, nil, "-C", repository, "show-ref", "--verify", "--hash", delegationRef(parentRunID))
	if err != nil {
		return "", err
	}
	if code == 0 {
		if strings.TrimSpace(string(previous)) != commit {
			return "", ErrBase
		}
		return commit, nil
	}
	if code != 1 && code != 128 {
		return "", ErrBase
	}
	if _, code, err = l.run(ctx, nil, "-C", repository, "update-ref", delegationRef(parentRunID), commit, strings.Repeat("0", len(commit))); err != nil || code != 0 {
		return "", ErrBase
	}
	return commit, nil
}

func (l *Local) ReleaseDelegationSource(ctx context.Context, parentRunID, repositoryID uuid.UUID) error {
	repository := filepath.Join(l.config.RepositoryCacheRoot, repositoryID.String())
	if _, err := l.repositoryID(repository); err != nil {
		return err
	}
	unlock, err := l.lock(ctx, parentRunID, repository)
	if err != nil {
		return err
	}
	defer unlock()
	_, code, err := l.run(ctx, nil, "-C", repository, "update-ref", "-d", delegationRef(parentRunID))
	if err != nil || code != 0 {
		return ErrBase
	}
	return nil
}

func delegationEnvironment() map[string]string {
	return map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_COUNT": "0", "GIT_ATTR_NOSYSTEM": "1", "GIT_CONFIG_PARAMETERS": "", "GIT_AUTHOR_NAME": "Circular", "GIT_AUTHOR_EMAIL": "circular@example.invalid", "GIT_COMMITTER_NAME": "Circular", "GIT_COMMITTER_EMAIL": "circular@example.invalid", "GIT_AUTHOR_DATE": "2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE": "2000-01-01T00:00:00Z"}
}

func (l *Local) delegationTree(ctx context.Context, runID uuid.UUID, repository, source string) (string, error) {
	format, code, err := l.run(ctx, delegationEnvironment(), "-C", repository, "rev-parse", "--show-object-format")
	objectFormat := strings.TrimSpace(string(format))
	if err != nil || code != 0 || (objectFormat != "sha1" && objectFormat != "sha256") {
		return "", ErrBase
	}
	directory, err := os.MkdirTemp(l.config.RepositoryCacheRoot, ".delegation-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(directory)
	metadata := filepath.Join(directory, "git")
	environment := map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_COUNT": "0", "GIT_ATTR_NOSYSTEM": "1", "GIT_CONFIG_PARAMETERS": "", "GIT_DIR": metadata, "GIT_COMMON_DIR": metadata, "GIT_WORK_TREE": source, "GIT_INDEX_FILE": filepath.Join(metadata, "index"), "GIT_OBJECT_DIRECTORY": filepath.Join(repository, ".git", "objects"), "GIT_AUTHOR_NAME": "Circular", "GIT_AUTHOR_EMAIL": "circular@example.invalid", "GIT_COMMITTER_NAME": "Circular", "GIT_COMMITTER_EMAIL": "circular@example.invalid", "GIT_AUTHOR_DATE": "2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE": "2000-01-01T00:00:00Z"}
	run := func(input io.Reader, args ...string) ([]byte, error) {
		out, code, err := l.runWithInput(ctx, environment, 1024*1024, input, args...)
		if err != nil || code != 0 {
			return nil, fmt.Errorf("checkpoint %s: %w", args[0], failure(ErrBase, runID, repository, code, err))
		}
		return bytes.TrimSpace(out), nil
	}
	delete(environment, "GIT_WORK_TREE")
	if _, err = run(nil, "init", "--bare", "--template=", "--object-format="+objectFormat, metadata); err != nil {
		return "", err
	}
	environment["GIT_WORK_TREE"] = source
	if _, err = run(nil, "read-tree", "--empty"); err != nil {
		return "", err
	}
	type entry struct{ name, path, mode string }
	entries := []entry{}
	err = filepath.WalkDir(source, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if item.Name() == ".git" {
			if item.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if item.IsDir() {
			return nil
		}
		name, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		mode := "100644"
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			path = filepath.Join(directory, fmt.Sprintf("link-%d", len(entries)))
			if err = os.WriteFile(path, []byte(target), 0600); err != nil {
				return err
			}
			mode = "120000"
		} else if !info.Mode().IsRegular() {
			return ErrBase
		} else if info.Mode().Perm()&0111 != 0 {
			mode = "100755"
		}
		entries = append(entries, entry{name, path, mode})
		return nil
	})
	if err != nil {
		return "", err
	}
	var index bytes.Buffer
	for start := 0; start < len(entries); start += 100 {
		batch := entries[start:min(start+100, len(entries))]
		args := []string{"hash-object", "-w", "--no-filters", "--"}
		for _, entry := range batch {
			args = append(args, entry.path)
		}
		hashes, err := run(nil, args...)
		if err != nil {
			return "", err
		}
		lines := strings.Split(string(hashes), "\n")
		if len(lines) != len(batch) {
			return "", ErrBase
		}
		for i, entry := range batch {
			if !commitPattern.MatchString(lines[i]) {
				return "", ErrBase
			}
			fmt.Fprintf(&index, "%s %s\t%s\x00", entry.mode, lines[i], entry.name)
		}
	}
	if _, err = run(&index, "update-index", "-z", "--index-info"); err != nil {
		return "", err
	}
	tree, err := run(nil, "write-tree")
	if err != nil {
		return "", err
	}
	return string(tree), nil
}
