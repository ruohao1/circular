package codexworkload

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	git "github.com/ruohao1/circular/internal/git"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/runstate"
	"github.com/ruohao1/circular/internal/runtimes"
	"github.com/ruohao1/circular/internal/testsupport"
)

// This is the only substituted boundary: the external model CLI. Execute real
// Git using the shell environment that the production launcher gives Codex.
func reviewGitProbe() int {
	var environment []string
	for _, arg := range os.Args {
		table, ok := strings.CutPrefix(arg, "shell_environment_policy.set={")
		if !ok {
			continue
		}
		for _, assignment := range strings.Split(strings.TrimSuffix(table, "}"), ",") {
			name, quoted, ok := strings.Cut(assignment, "=")
			value, err := strconv.Unquote(quoted)
			if !ok || err != nil {
				return 91
			}
			environment = append(environment, name+"="+value)
		}
	}
	value, err := prreviews.ReadContext("/review-context")
	if err != nil {
		return 92
	}
	probe := exec.Command("/bin/sh", "-eu", "-c", `
test "$(id -u)" != 0
test "$(git rev-parse HEAD)" = "$1"
test "$(git rev-parse refs/review/base)" = "$2"
test "$(git merge-base refs/review/base HEAD)" = "$3"
test -z "$(git status --porcelain)"
test "$(git diff --name-only refs/review/merge-base HEAD --)" = changed.txt
test "$(git show refs/review/merge-base:changed.txt)" = before
test "$(git show HEAD:changed.txt)" = after
test "$(git show HEAD:unchanged.txt)" = preserved
test -z "$(git remote)"
test "$(git for-each-ref --format='%(refname)')" = "refs/review/base
refs/review/head
refs/review/merge-base"
test ! -e "$(sed 's/^gitdir: //' /workspace/.git)"
if touch /workspace/must-not-write 2>/dev/null; then exit 93; fi
if git update-ref refs/review/must-not-write HEAD 2>/dev/null; then exit 94; fi
if touch /review-context/git/must-not-write 2>/dev/null; then exit 95; fi
touch "$TMPDIR/scratch-ok"
`, "review-probe", value.Snapshot.PR.HeadSHA, value.Snapshot.PR.BaseSHA, value.MergeBaseSHA)
	probe.Env = environment
	if output, err := probe.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "review Git probe failed: %v %s\n", err, output)
		return 96
	}
	fmt.Fprintln(os.Stdout, `{"type":"fixture.review_git_verified"}`)
	return 0
}

func TestRealDockerReviewCanInspectPinnedGitWithoutSharedCache(t *testing.T) {
	if os.Getenv("CIRCULAR_RUN_DOCKER_TESTS") != "1" {
		t.Skip("CIRCULAR_RUN_DOCKER_TESTS=1 enables the real review Git regression")
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	build, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	const image = "circular-review-git:test"
	if output, err := exec.CommandContext(build, "docker", "build", "-f", filepath.Join(root, "infra/review-git-test.Dockerfile"), "-t", image, root).CombinedOutput(); err != nil {
		t.Fatalf("build review fixture: %v %s", err, output)
	}
	f := testsupport.PrepareReviewWorkspace(t)
	if err := f.Git.PreparePRReviewGit(t.Context(), f.Directory); err != nil {
		t.Fatal(err)
	}
	digest, err := prreviews.Fingerprint(f.Context)
	if err != nil {
		t.Fatal(err)
	}
	config := runtimes.DockerConfig{WorktreeRoot: filepath.Dir(f.Worktree.Path), ReviewContextRoot: filepath.Dir(f.Directory), ContainerUser: "65532:65532"}
	d, err := runtimes.NewDocker(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := d.ReleaseReview(cleanup, f.Context.RunID, "", digest); err != nil {
			t.Error("release test review", err)
		}
	})
	input, _ := json.Marshal(map[string]any{"protocol_version": 1, "prompt": "review-git-probe", "purpose": "pr_review", "review_context_sha256": digest, "api_key": "fixture-api-key"})
	spec := runtimes.Spec{RunID: f.Context.RunID, Kind: runstate.PRReview, Review: &runtimes.ReviewMount{ContextSHA256: digest}, Worktree: f.Worktree.Path, Image: image, Stdin: input, CPULimit: 1, MemoryLimitMB: 256, TemporaryStorageMB: 64}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	handle, err := d.Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	result, waitErr := d.Wait(ctx, handle)
	output, err := d.Output(ctx, handle)
	if err != nil {
		t.Fatal(err)
	}
	var transcript strings.Builder
	for chunk, err := range output {
		if err != nil {
			t.Fatal(err)
		}
		transcript.Write(chunk.Data)
	}
	if waitErr != nil || result.ExitCode == nil || *result.ExitCode != 0 || !strings.Contains(transcript.String(), `"type":"fixture.review_git_verified"`) {
		t.Fatalf("container review Git failed: %+v %v\n%s", result, waitErr, transcript.String())
	}
	// Recovery still adopts exactly these existing read-only mounts.
	recovered, err := runtimes.NewDocker(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.ReleaseReview(ctx, f.Context.RunID, handle.ResourceID, digest); err != nil {
		t.Fatal(err)
	}
	if err := f.Git.Release(ctx, f.Worktree, git.ReleaseOptions{}); err != nil {
		t.Fatal("worktree cleanup", err)
	}
}
