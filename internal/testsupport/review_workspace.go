package testsupport

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	git "github.com/ruohao1/circular/internal/git"
	"github.com/ruohao1/circular/internal/prreviews"
)

// ReviewWorkspace uses real Git with diverged base/head commits and an unrelated
// private branch. It makes copying the whole shared cache observable in tests.
type ReviewWorkspace struct {
	Git          *git.Local
	Worktree     git.Worktree
	Context      prreviews.Context
	Directory    string
	UnrelatedSHA string
}

func PrepareReviewWorkspace(t *testing.T) ReviewWorkspace {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0755); err != nil {
		t.Fatal(err)
	}
	command := func(args ...string) string {
		t.Helper()
		output, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", source}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture Git: %v %s", err, output)
		}
		return strings.TrimSpace(string(output))
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	command("init", "--initial-branch=main")
	command("config", "user.name", "Review fixture")
	command("config", "user.email", "review@example.invalid")
	write("changed.txt", "before\n")
	write("unchanged.txt", "preserved\n")
	command("add", ".")
	command("commit", "-m", "merge base")
	command("checkout", "-b", "feature")
	write("changed.txt", "after\n")
	command("commit", "-am", "head")
	head := command("rev-parse", "HEAD")
	command("checkout", "main")
	write("upstream.txt", "base advanced\n")
	command("add", ".")
	command("commit", "-m", "base advanced")
	base := command("rev-parse", "HEAD")
	command("checkout", "-b", "unrelated")
	write("private.txt", "unrelated cache content\n")
	command("add", ".")
	command("commit", "-m", "unrelated")
	unrelated := command("rev-parse", "HEAD")
	command("checkout", "main")
	local, err := git.NewLocal(git.Config{RepositoryCacheRoot: filepath.Join(root, "cache"), WorktreeRoot: filepath.Join(root, "worktrees")})
	if err != nil {
		t.Fatal(err)
	}
	run, repository := uuid.New(), uuid.New()
	snapshot, err := prreviews.SealSnapshot(prreviews.LaunchSnapshot{
		SourceRunID: uuid.New(), TaskID: uuid.New(), ProjectID: uuid.New(), RepositoryID: repository, CloneURL: source,
		Reviewer: prreviews.ReviewerSnapshot{AgentID: uuid.New(), Backend: "codex", BackendConfig: json.RawMessage(`{}`)},
		PR:       prreviews.PRIdentity{Number: 1, BaseSHA: base, HeadSHA: head},
	})
	if err != nil {
		t.Fatal(err)
	}
	captured, err := local.PreparePRReview(t.Context(), run, repository, source, snapshot.PR)
	if err != nil {
		t.Fatal(err)
	}
	value := prreviews.Context{ReviewID: uuid.New(), RunID: run, Snapshot: snapshot, MergeBaseSHA: captured.MergeBaseSHA, DiffSHA256: captured.DiffSHA256, Files: captured.Files}
	directory := filepath.Join(root, "review-contexts", run.String())
	if err := prreviews.WriteContext(directory, value, captured.Diff); err != nil {
		t.Fatal(err)
	}
	w, err := local.Provision(t.Context(), run, captured.RepositoryPath, head)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(w.Path, 0755); err != nil {
		t.Fatal(err)
	}
	return ReviewWorkspace{Git: local, Worktree: w, Context: value, Directory: directory, UnrelatedSHA: unrelated}
}
