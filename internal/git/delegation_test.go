package git_test

import (
	"bytes"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
	git "github.com/ruohao1/circular/internal/git"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDelegationPatchBase(t *testing.T) {
	local, parent, root := allocated(t)
	original := gitCommand(t, parent.Path, "rev-parse", "HEAD")
	putFile(t, filepath.Join(parent.Path, "README.md"), "parent edit\n")
	putFile(t, filepath.Join(parent.Path, ".gitignore"), "ignored.bin\n")
	binary := []byte{0, 1, 255, 4}
	os.WriteFile(filepath.Join(parent.Path, "ignored.bin"), binary, 0755)
	putFile(t, filepath.Join(parent.Path, "untracked.txt"), "parent untracked\n")
	putFile(t, filepath.Join(parent.Path, ".gitattributes"), "*.txt text eol=crlf\n")
	putFile(t, filepath.Join(parent.Path, "original-crlf.txt"), "parent CRLF\r\n")
	os.Symlink("untracked.txt", filepath.Join(parent.Path, "safe-link"))
	var archive bytes.Buffer
	if err := artifacts.Archive(t.Context(), parent.Path, &archive); err != nil {
		t.Fatal(err)
	}
	repository := uuid.MustParse(filepath.Base(parent.RepositoryPath))
	commit, err := local.SnapshotDelegation(t.Context(), parent.RunID, repository, bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := local.SnapshotDelegation(t.Context(), parent.RunID, repository, bytes.NewReader(archive.Bytes()))
	if err != nil || replay != commit {
		t.Fatal("unstable snapshot", replay, err)
	}
	child, err := local.ProvisionDelegation(t.Context(), uuid.New(), parent.RepositoryPath, commit, bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(child.Path, "untracked.txt")); err != nil || string(raw) != "parent untracked\n" {
		t.Fatalf("checkout changed snapshot bytes: %q %v", raw, err)
	}
	got, err := os.ReadFile(filepath.Join(child.Path, "ignored.bin"))
	if err != nil || !bytes.Equal(got, binary) {
		t.Fatal("ignored binary lost", err)
	}
	info, err := os.Stat(filepath.Join(child.Path, "ignored.bin"))
	if err != nil || info.Mode().Perm()&0111 == 0 {
		t.Fatal("executable mode lost")
	}
	if link, err := os.Readlink(filepath.Join(child.Path, "safe-link")); err != nil || link != "untracked.txt" {
		t.Fatal(link, err)
	}
	putFile(t, filepath.Join(child.Path, "child.txt"), "specialist change\n")
	diff, err := local.Capture(t.Context(), child.Path)
	if err != nil || diff.BaseCommit != commit || !strings.Contains(string(diff.Content), "specialist change") || strings.Contains(string(diff.Content), "parent edit") || strings.Contains(string(diff.Content), "parent untracked") {
		t.Fatalf("child patch includes parent work: %s %v", diff.Content, err)
	}
	if err = local.Release(t.Context(), child, git.ReleaseOptions{DiscardChanges: true}); err != nil {
		t.Fatal(err)
	}
	parentDiff, err := local.Capture(t.Context(), parent.Path)
	if err != nil || parentDiff.BaseCommit != strings.TrimSpace(string(original)) {
		t.Fatal("parent base changed", parentDiff, err)
	}
	os.Remove(filepath.Join(parent.Path, "README.md"))
	archive.Reset()
	artifacts.Archive(t.Context(), parent.Path, &archive)
	if _, err = local.SnapshotDelegation(t.Context(), parent.RunID, repository, bytes.NewReader(archive.Bytes())); err == nil {
		t.Fatal("changed snapshot replaced checkpoint")
	}
	badChild := uuid.New()
	if _, err = local.ProvisionDelegation(t.Context(), badChild, parent.RepositoryPath, commit, bytes.NewReader(archive.Bytes())); err == nil {
		t.Fatal("wrong snapshot hydrated")
	}
	if _, err = os.Lstat(filepath.Join(root, "worktrees", badChild.String())); !os.IsNotExist(err) {
		t.Fatal("failed hydration survived", err)
	}
	if err = local.ReleaseDelegationSource(t.Context(), child.RunID, repository); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(gitCommand(t, parent.RepositoryPath, "rev-parse", "refs/circular/delegations/"+parent.RunID.String()))); got != commit {
		t.Fatal("child removed parent source", got)
	}
	if err = local.ReleaseDelegationSource(t.Context(), parent.RunID, repository); err != nil {
		t.Fatal(err)
	}
	if got := gitCommand(t, parent.RepositoryPath, "for-each-ref", "refs/circular/delegations/"+parent.RunID.String()); len(got) != 0 {
		t.Fatal("parent source pin survived", string(got))
	}
}

func TestDelegationSHA256Repository(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, source, "init", "--initial-branch=main", "--object-format=sha256")
	gitCommand(t, source, "config", "user.name", "Fixture")
	gitCommand(t, source, "config", "user.email", "fixture@example.invalid")
	putFile(t, filepath.Join(source, "source.txt"), "original\n")
	gitCommand(t, source, "add", ".")
	gitCommand(t, source, "commit", "-m", "base")
	local := localGit(t, root)
	repoID := uuid.New()
	repository, err := local.Checkout(t.Context(), repoID, source)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := local.Provision(t.Context(), uuid.New(), repository, "main")
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	if err = artifacts.Archive(t.Context(), parent.Path, &raw); err != nil {
		t.Fatal(err)
	}
	commit, err := local.SnapshotDelegation(t.Context(), parent.RunID, repoID, bytes.NewReader(raw.Bytes()))
	if err != nil || len(commit) != 64 {
		t.Fatal("SHA256 snapshot", commit, err)
	}
	if _, err = local.ProvisionDelegation(t.Context(), uuid.New(), repository, commit, bytes.NewReader(raw.Bytes())); err != nil {
		t.Fatal(err)
	}
}
