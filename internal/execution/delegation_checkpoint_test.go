package execution

import (
	"bytes"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
	git "github.com/ruohao1/circular/internal/git"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDelegationCheckpointRoundTrip(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	os.Mkdir(source, 0700)
	command := func(args ...string) {
		t.Helper()
		out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", source}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v", out, err)
		}
	}
	command("init", "--initial-branch=main")
	command("config", "user.name", "Fixture")
	command("config", "user.email", "fixture@example.invalid")
	os.WriteFile(filepath.Join(source, "deleted.txt"), []byte("delete"), 0600)
	command("add", ".")
	command("commit", "-m", "base")
	pool := testsupport.Database(t)
	snap := testsupport.SeedPRReviewSource(t, pool, uuid.Nil)
	if _, err := pool.Exec(t.Context(), `UPDATE repositories SET clone_url=$2,default_branch='main' WHERE id=$1`, snap.RepositoryID, source); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `UPDATE tasks SET completion_policy='{"mode":"direct_completion","requirements":[]}' WHERE id=$1`, snap.TaskID); err != nil {
		t.Fatal(err)
	}
	var agent uuid.UUID
	if err = tx.QueryRow(t.Context(), `SELECT agent_id FROM runs WHERE id=$1`, snap.SourceRunID).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	testsupport.ApproveTaskTx(t, tx, snap.TaskID)
	run, err := postgres.CreateRun(t.Context(), tx, postgres.RunLaunchInput{TaskID: snap.TaskID, AgentID: agent})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	id := run.RunID
	if claim, err := postgres.NewQueue(pool).Acquire(t.Context(), "checkpoint-owner"); err != nil || claim == nil || claim.RunID != id {
		t.Fatal(err)
	}
	store, err := postgres.NewResources(pool, "checkpoint-owner")
	if err != nil {
		t.Fatal(err)
	}
	retention, err := NewRetention(store, git.Config{RepositoryCacheRoot: filepath.Join(root, "cache"), WorktreeRoot: filepath.Join(root, "worktrees")}, filepath.Join(root, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := retention.git.Checkout(t.Context(), snap.RepositoryID, source)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := retention.git.Provision(t.Context(), id, repository, "main")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.WithRun(t.Context(), id, func(r *postgres.RunResources) error {
		if _, err := r.InvocationInput(); err != nil {
			return err
		}
		if _, err := r.CreatePending(workspace.Path); err != nil {
			return err
		}
		if _, err := r.RecordContainer("exited-fixture-container"); err != nil {
			return err
		}
		if _, err := r.MarkRunning(); err != nil {
			return err
		}
		return r.AppendBackendEvent("codex", "agent.message.completed", map[string]any{"content": "Parent handoff summary"}, map[string]any{})
	}); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(workspace.Path, "deleted.txt"))
	binary := bytes.Repeat([]byte{0, 1, 255, 3}, 200)
	os.WriteFile(filepath.Join(workspace.Path, "binary"), binary, 0755)
	checkpoint, err := retention.Checkpoint(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := retention.Checkpoint(t.Context(), id)
	if err != nil || checkpoint != replay {
		t.Fatal("checkpoint replay", err)
	}
	file, err := retention.content.OpenVerified(t.Context(), id, checkpoint.Source)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	restored := filepath.Join(root, "restored")
	if err = artifacts.RestoreArchive(t.Context(), file, restored); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(restored, "binary"))
	if err != nil || !bytes.Equal(got, binary) {
		t.Fatal("binary changed", err)
	}
	if _, err = os.Lstat(filepath.Join(restored, "deleted.txt")); !os.IsNotExist(err) {
		t.Fatal("deletion lost")
	}
	var count int
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM artifacts WHERE run_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal("checkpoint became final output", count, err)
	}
	os.WriteFile(filepath.Join(workspace.Path, "binary"), []byte("changed"), 0755)
	if _, err = retention.Checkpoint(t.Context(), id); err == nil {
		t.Fatal("changed retry accepted")
	}
	os.WriteFile(filepath.Join(root, "artifacts", id.String(), "delegation-source.tar"), []byte("corrupt"), 0600)
	if _, err = retention.content.OpenVerified(t.Context(), id, checkpoint.Source); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
}
