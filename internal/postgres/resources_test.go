package postgres_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestWorkspaceHandoffCommitsItsIdentityAndEventsTogether(t *testing.T) {
	pool := database(t)
	id := seed(t, pool, 1)[0]
	acquire(t, postgres.NewQueue(pool), "resource-owner")
	store, err := postgres.NewResources(pool, "resource-owner")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), id.String())
	if err := store.WithRun(t.Context(), id, func(run *postgres.RunResources) error {
		first, err := run.CreatePending(path)
		if err != nil {
			return err
		}
		retry, err := run.CreatePending(path)
		if err != nil || first != retry {
			return errors.New("identical pending handoff was not reusable")
		}
		if _, err := run.RecordContainer("immutable-container-id"); err != nil {
			return err
		}
		if _, err := run.RecordContainer("immutable-container-id"); err != nil {
			return err
		}
		_, err = run.MarkRunning()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	state, err := store.Read(t.Context(), id)
	if err != nil || state.Status != "running" || state.Workspace == nil || state.Workspace.Status != "ready" || state.Workspace.ContainerID == nil || *state.Workspace.ContainerID != "immutable-container-id" {
		t.Fatalf("handoff did not persist: %+v %v", state, err)
	}
	s := testsupport.Observe(t, pool, id)
	if s.Workspace == nil || s.Workspace.ID != postgres.WorkspaceID(id) || s.Workspace.Status != "ready" || s.Workspace.ContainerID == nil || *s.Workspace.ContainerID != "immutable-container-id" {
		t.Fatal(s.Workspace)
	}
	s.AssertTypes(t, "workspace.provisioning", "workspace.provisioning", "workspace.ready", "run.started")
	if s.Events[1].Data["stage"] != "container_started" {
		t.Fatal(s.Events[1])
	}
	testsupport.AssertJSON(t, s.Events[3].Data, map[string]any{"backend": "fake"})
}

func TestFinalDiffAndEventsCommitAtomicallyAndIdenticalRetriesDoNotDuplicate(t *testing.T) {
	pool := database(t)
	id := seed(t, pool, 1)[0]
	queue := postgres.NewQueue(pool)
	acquire(t, queue, "resource-owner")
	store, err := postgres.NewResources(pool, "resource-owner")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), id.String())
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error { _, err := r.CreatePending(path); return err }); err != nil {
		t.Fatal(err)
	}
	if err := queue.ReconcileExit(t.Context(), id, "resource-owner"); err != nil {
		t.Fatal(err)
	}
	contentStore, err := artifacts.NewLocalStore(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	content, err := contentStore.Write(t.Context(), id, "git-diff.patch", []byte("patch"))
	if err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("caller rejects transaction")
	if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error {
		if _, err := r.PersistDiff(path, content, 1, false); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("rollback failed: %v", err)
	}
	state, err := store.Read(t.Context(), id)
	if err != nil || len(state.Artifacts) != 0 {
		t.Fatal("rolled back diff became visible")
	}
	for range 2 {
		if err := store.WithRun(t.Context(), id, func(r *postgres.RunResources) error { _, err := r.PersistDiff(path, content, 1, false); return err }); err != nil {
			t.Fatal(err)
		}
	}
	state, err = store.Read(t.Context(), id)
	if err != nil || len(state.Artifacts) != 1 || state.Artifacts[0].Kind != "diff" {
		t.Fatalf("diff was not retained exactly once: %+v %v", state, err)
	}
	s := testsupport.Observe(t, pool, id)
	s.AssertTypes(t, "workspace.provisioning", "run.failed", "artifact.created", "git.diff.updated")
	if len(s.Artifacts) != 1 || s.Artifacts[0].ID != artifacts.DiffID(id) {
		t.Fatal(s.Artifacts)
	}
	a := s.Artifacts[0]
	if a.Metadata["changed_files"].(json.Number).String() != "1" || a.Metadata["empty"] != false || a.Metadata["size_bytes"].(json.Number).String() != "5" {
		t.Fatal(a.Metadata)
	}
	data := map[string]any{"artifact_id": a.ID.String(), "uri": a.URI}
	for k, v := range a.Metadata {
		data[k] = v
	}
	testsupport.AssertJSON(t, s.Events[3].Data, data)
}
