package postgres

import (
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/runstate"
)

func WorkspaceID(id uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("io.circular.workspace:"+id.String()))
}

func (r *RunResources) CreatePending(path string) (Workspace, error) {
	if err := r.guard(); err != nil {
		return Workspace{}, err
	}
	if r.status != runstate.Provisioning || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) || !utf8.ValidString(path) || strings.ContainsRune(path, 0) {
		return Workspace{}, ErrResourceState
	}
	w, err := r.workspace()
	if err != nil {
		return Workspace{}, err
	}
	id := WorkspaceID(r.id)
	if w != nil {
		if w.ID != id || w.WorktreePath != path || w.Status != "pending" || w.ContainerID != nil {
			return Workspace{}, ErrResourceConflict
		}
		return *w, nil
	}
	_, err = r.tx.Exec(r.ctx, `INSERT INTO workspaces (id,run_id,worktree_path,status) VALUES ($1,$2,$3,'pending')`, id, r.id, path)
	if err != nil {
		return Workspace{}, err
	}
	if err := r.event("workspace.provisioning", "worker", map[string]any{"status": "pending", "workspace_id": id.String()}); err != nil {
		return Workspace{}, err
	}
	return Workspace{ID: id, RunID: r.id, WorktreePath: path, Status: "pending"}, nil
}

func (r *RunResources) RecordContainer(containerID string) (Workspace, error) {
	if !utf8.ValidString(containerID) || strings.ContainsRune(containerID, 0) || containerID == "" || utf8.RuneCountInString(containerID) > 200 {
		return Workspace{}, ErrResourceState
	}
	w, err := r.workspace()
	if err != nil {
		return Workspace{}, err
	}
	if w == nil {
		return Workspace{}, ErrResourceState
	}
	if w.ContainerID != nil {
		if *w.ContainerID != containerID {
			return Workspace{}, ErrResourceConflict
		}
		return *w, nil
	}
	if w.Status != "pending" {
		return Workspace{}, ErrResourceState
	}
	_, err = r.tx.Exec(r.ctx, `UPDATE workspaces SET container_id=$2,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, w.ID, containerID)
	if err != nil {
		return Workspace{}, err
	}
	if err := r.event("workspace.provisioning", "worker", map[string]any{"status": "pending", "stage": "container_started", "workspace_id": w.ID.String(), "container_id": containerID}); err != nil {
		return Workspace{}, err
	}
	w.ContainerID = &containerID
	return *w, nil
}

func (r *RunResources) MarkRunning() (Workspace, error) {
	w, err := r.workspace()
	if err != nil {
		return Workspace{}, err
	}
	if r.status != runstate.Provisioning || w == nil || w.Status != "pending" || w.ContainerID == nil {
		return Workspace{}, ErrResourceState
	}
	if err := r.workspaceStatus(w, "ready", "worker"); err != nil {
		return Workspace{}, err
	}
	_, err = r.tx.Exec(r.ctx, `UPDATE runs SET status='running',started_at=COALESCE(started_at,clock_timestamp()),error=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, r.id)
	if err != nil {
		return Workspace{}, err
	}
	r.status = runstate.Running
	if err := r.event("run.started", "worker", map[string]any{"backend": r.backend}); err != nil {
		return Workspace{}, err
	}
	return *w, nil
}

func (r *RunResources) workspaceStatus(w *Workspace, target, source string) error {
	allowed := (w.Status == "pending" && (target == "ready" || target == "failed")) || (w.Status == "ready" && (target == "failed" || target == "released")) || (w.Status == "failed" && target == "released")
	if !allowed {
		return ErrResourceState
	}
	_, err := r.tx.Exec(r.ctx, `UPDATE workspaces SET status=$2,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, w.ID, target)
	if err != nil {
		return err
	}
	data := map[string]any{"status": target, "workspace_id": w.ID.String()}
	if w.ContainerID != nil {
		data["container_id"] = *w.ContainerID
	}
	if err := r.event("workspace."+target, source, data); err != nil {
		return err
	}
	w.Status = target
	return nil
}

// ReleaseWorkspace records completed resource cleanup, not a Run outcome. Its
// caller must retain output and release the exact resources under WithRun first.
func (r *RunResources) ReleaseWorkspace() error {
	w, err := r.workspace()
	if err != nil {
		return err
	}
	if !r.status.Terminal() {
		return ErrResourceState
	}
	if w == nil || w.Status == "released" {
		return nil
	}
	if w.Status == "pending" {
		if err := r.workspaceStatus(w, "failed", "worker-cleanup"); err != nil {
			return err
		}
	}
	return r.workspaceStatus(w, "released", "worker-cleanup")
}

// RecordCleanupFailure preserves the Run's primary terminal outcome and error.
func (r *RunResources) RecordCleanupFailure(message string) error {
	w, err := r.workspace()
	if err != nil {
		return err
	}
	if !r.status.Terminal() {
		return ErrResourceState
	}
	if w == nil || w.Status == "released" {
		return nil
	}
	if w.Status == "pending" || w.Status == "ready" {
		if err := r.workspaceStatus(w, "failed", "worker-cleanup"); err != nil {
			return err
		}
	}
	message = strings.ReplaceAll(strings.ToValidUTF8(message, "�"), "\x00", "�")
	runes := []rune(message)
	if len(runes) > 4000 {
		message = string(runes[:4000])
	}
	return r.event("workspace.failed", "worker-cleanup", map[string]any{"stage": "cleanup", "workspace_id": w.ID.String(), "error": message})
}
