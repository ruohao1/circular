package execution

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
	git "github.com/ruohao1/circular/internal/git"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestPrepareReviewContextRejectsCorruptOriginalEvidence(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "verified context", true: "corrupt evidence"}[corrupt], func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			command := func(args ...string) string {
				t.Helper()
				c := exec.CommandContext(t.Context(), "git", append([]string{"-C", source}, args...)...)
				output, err := c.CombinedOutput()
				if err != nil {
					t.Fatalf("fixture Git failed: %s %v", output, err)
				}
				return strings.TrimSpace(string(output))
			}
			command("init", "--initial-branch=main")
			command("config", "user.name", "Fixture")
			command("config", "user.email", "fixture@example.invalid")
			if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("before\n"), 0600); err != nil {
				t.Fatal(err)
			}
			command("add", ".")
			command("commit", "-m", "base")
			base := command("rev-parse", "HEAD")
			if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("after\n"), 0600); err != nil {
				t.Fatal(err)
			}
			command("commit", "-am", "head")
			head := command("rev-parse", "HEAD")
			pool := testsupport.Database(t)
			snap := testsupport.SeedPRReviewSource(t, pool, uuid.Nil)
			if _, err := pool.Exec(t.Context(), `UPDATE repositories SET clone_url=$2 WHERE id=$1`, snap.RepositoryID, source); err != nil {
				t.Fatal(err)
			}
			snap.CloneURL = source
			snap.PR.BaseSHA = base
			snap.PR.HeadSHA = head
			contents, err := artifacts.NewLocalStore(filepath.Join(root, "artifacts"))
			if err != nil {
				t.Fatal(err)
			}
			if corrupt {
				content, err := contents.Write(t.Context(), snap.SourceRunID, "git-diff.patch", []byte("original evidence"))
				if err != nil {
					t.Fatal(err)
				}
				id := uuid.New()
				checksum := strings.Repeat("a", 64)
				metadata, _ := json.Marshal(map[string]any{"size_bytes": content.SizeBytes, "sha256": checksum})
				if _, err := pool.Exec(t.Context(), `INSERT INTO artifacts(id,run_id,kind,uri,metadata) VALUES($1,$2,'diff',$3,$4)`, id, snap.SourceRunID, content.URI, metadata); err != nil {
					t.Fatal(err)
				}
				snap.Evidence = []prreviews.Evidence{{SourceRunID: snap.SourceRunID, ArtifactID: id, Kind: "diff", SHA256: checksum}}
			}
			snap, err = prreviews.SealSnapshot(snap)
			if err != nil {
				t.Fatal(err)
			}
			review, err := postgres.NewPRReviewStore(pool).Launch(t.Context(), postgres.ReviewLaunch{Snapshot: snap, Request: prreviews.LaunchRequest{RequestKey: uuid.New(), ExpectedInputFingerprint: snap.InputFingerprint, Mode: "normal"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := postgres.NewQueue(pool).Acquire(t.Context(), "context-worker"); err != nil {
				t.Fatal(err)
			}
			resources, err := postgres.NewResources(pool, "context-worker")
			if err != nil {
				t.Fatal(err)
			}
			retention, err := NewRetention(resources, git.Config{RepositoryCacheRoot: filepath.Join(root, "cache"), WorktreeRoot: filepath.Join(root, "worktrees")}, filepath.Join(root, "artifacts"))
			if err != nil {
				t.Fatal(err)
			}
			s := &Supervisor{store: resources, retention: retention, config: Config{ReviewContextRoot: filepath.Join(root, "review-contexts")}}
			var inputs postgres.ProvisioningContext
			if err := resources.WithRun(t.Context(), review.RunID, func(r *postgres.RunResources) error { var err error; inputs, err = r.ProvisioningContext(); return err }); err != nil {
				t.Fatal(err)
			}
			if err := resources.WithRun(t.Context(), review.RunID, func(r *postgres.RunResources) error {
				_, err := r.CreatePending(filepath.Join(retention.worktreeRoot, review.RunID.String()))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			_, value, err := s.preparePRReviewContext(t.Context(), inputs)
			if corrupt {
				if err == nil {
					t.Fatal("corrupt prior artifact was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			saved, err := prreviews.ReadContext(filepath.Join(s.config.ReviewContextRoot, review.RunID.String()))
			if err != nil || saved.RunID != value.RunID || saved.MergeBaseSHA != base {
				t.Fatal(saved, err)
			}
			metadata := filepath.Join(s.config.ReviewContextRoot, review.RunID.String(), "git")
			probe := exec.CommandContext(t.Context(), "git", "--git-dir="+metadata, "rev-parse", "HEAD")
			output, err := probe.CombinedOutput()
			if err != nil || strings.TrimSpace(string(output)) != head {
				t.Fatalf("review Git metadata cannot identify captured HEAD: %v %s", err, output)
			}
		})
	}
}
