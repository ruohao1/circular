package postgres

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/runstate"
)

func (r *RunResources) PersistDiff(worktree string, content artifacts.Content, changedFiles int, containsBinary bool) (artifacts.Record, error) {
	if changedFiles < 0 || changedFiles == 0 && (containsBinary || content.SizeBytes != 0) {
		return artifacts.Record{}, ErrResourceState
	}
	if err := r.validContent(content, "git-diff.patch"); err != nil {
		return artifacts.Record{}, err
	}
	a := artifacts.Record{ID: artifacts.DiffID(r.id), RunID: r.id, Kind: "diff", URI: content.URI, Metadata: map[string]any{"media_type": "text/x-diff", "size_bytes": content.SizeBytes, "sha256": content.SHA256, "changed_files": changedFiles, "contains_binary": containsBinary, "empty": changedFiles == 0}}
	return a, r.persistArtifact(worktree, a, "worker")
}

func (r *RunResources) PersistArchive(worktree string, content artifacts.Content) (artifacts.Record, error) {
	if err := r.validContent(content, "worktree.tar"); err != nil {
		return artifacts.Record{}, err
	}
	if content.SizeBytes < 10240 || content.SizeBytes%10240 != 0 {
		return artifacts.Record{}, ErrResourceState
	}
	a := artifacts.Record{ID: artifacts.ArchiveID(r.id), RunID: r.id, Kind: "workspace", URI: content.URI, Metadata: map[string]any{"media_type": "application/x-tar", "size_bytes": content.SizeBytes, "sha256": content.SHA256}}
	return a, r.persistArtifact(worktree, a, "worker-cleanup")
}

func (r *RunResources) validContent(content artifacts.Content, name string) error {
	if err := r.guard(); err != nil {
		return err
	}
	uri, err := artifacts.URI(r.id, name)
	digest, hexErr := hex.DecodeString(content.SHA256)
	if err != nil || uri != content.URI || content.SizeBytes < 0 || hexErr != nil || len(digest) != 32 || content.SHA256 != strings.ToLower(content.SHA256) {
		return ErrResourceState
	}
	return nil
}

func (r *RunResources) persistArtifact(worktree string, a artifacts.Record, source string) error {
	w, err := r.workspace()
	if err != nil {
		return err
	}
	if r.status != runstate.Finalizing && !r.status.Terminal() || w == nil || w.Status == "released" || w.WorktreePath != worktree {
		return ErrResourceState
	}
	metadata, err := json.Marshal(a.Metadata)
	if err != nil {
		return err
	}
	var identical bool
	err = r.tx.QueryRow(r.ctx, `SELECT run_id=$2 AND kind=$3 AND uri=$4 AND metadata::jsonb=$5::jsonb FROM artifacts WHERE id=$1`, a.ID, a.RunID, a.Kind, a.URI, metadata).Scan(&identical)
	if err == nil {
		if !identical {
			return ErrResourceConflict
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	_, err = r.tx.Exec(r.ctx, `INSERT INTO artifacts (id,run_id,kind,uri,metadata) VALUES ($1,$2,$3,$4,$5::json)`, a.ID, a.RunID, a.Kind, a.URI, metadata)
	if err != nil {
		return err
	}
	if err := r.event("artifact.created", source, map[string]any{"artifact_id": a.ID.String(), "kind": a.Kind, "uri": a.URI}); err != nil {
		return err
	}
	if a.Kind == "diff" {
		data := map[string]any{"artifact_id": a.ID.String(), "uri": a.URI}
		for key, value := range a.Metadata {
			data[key] = value
		}
		return r.event("git.diff.updated", source, data)
	}
	return nil
}

func (r *RunResources) artifacts() ([]artifacts.Record, error) {
	rows, err := r.tx.Query(r.ctx, `SELECT id,run_id,kind,uri,metadata FROM artifacts WHERE run_id=$1 ORDER BY created_at,id`, r.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []artifacts.Record{}
	for rows.Next() {
		var item artifacts.Record
		if err := rows.Scan(&item.ID, &item.RunID, &item.Kind, &item.URI, &item.Metadata); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
