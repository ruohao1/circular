package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type runQueueCursor struct {
	Version   int       `json:"v"`
	Project   string    `json:"project"`
	Group     string    `json:"group"`
	Query     string    `json:"q"`
	CreatedAt time.Time `json:"created_at"`
	ID        uuid.UUID `json:"id"`
}

type runQueuePage struct {
	Items      []json.RawMessage `json:"items"`
	NextCursor string            `json:"next_cursor"`
}

// runQueue is a console read model. RunRead remains a direct table projection
// for the existing API and MCP callers; the joined names belong only here.
func (a *api) runQueue(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	project, ok := identifier(w, r.PathValue("project_id"), "path", "project_id")
	if !ok {
		return
	}
	q := r.URL.Query()
	group := q.Get("group")
	if group == "" {
		group = "all"
	}
	switch group {
	case "all", "active", "failed", "finished":
	default:
		invalid(w, "query", "group", "enum", "Choose all, active, failed, or finished")
		return
	}
	search := strings.TrimSpace(q.Get("q"))
	if !utf8.ValidString(search) || utf8.RuneCountInString(search) > 200 {
		invalid(w, "query", "q", "string_too_long", "Search must contain at most 200 characters")
		return
	}
	limit, ok := integerQuery(w, r, "limit", 50, 1, 100)
	if !ok {
		return
	}
	cursor := runQueueCursor{Version: 1, Project: project.String(), Group: group, Query: search}
	var afterTime *time.Time
	var afterID *uuid.UUID
	if encoded := q.Get("cursor"); encoded != "" {
		var decoded runQueueCursor
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		if len(encoded) > 2048 || err != nil || json.Unmarshal(raw, &decoded) != nil || decoded.Version != 1 || decoded.Project != cursor.Project || decoded.Group != group || decoded.Query != search || decoded.CreatedAt.IsZero() || decoded.ID == uuid.Nil {
			invalid(w, "query", "cursor", "value_error", "Cursor does not match this Project and filter")
			return
		}
		afterTime, afterID = &decoded.CreatedAt, &decoded.ID
	}
	var exists bool
	if dbError(w, a.pool.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM projects WHERE id=$1)", project).Scan(&exists), "Project") {
		return
	}
	if !exists {
		problem(w, 404, "Project not found")
		return
	}
	rows, err := a.pool.Query(r.Context(), `
 SELECT json_build_object('run',`+projection("RunRead", "r")+`,
   'task_title',t.title,'agent_name',a.name,
   'repository',CASE WHEN repo.id IS NULL THEN NULL ELSE json_build_object('id',repo.id,'name',repo.name) END),
   r.created_at,r.id
 FROM runs r JOIN tasks t ON t.id=r.task_id JOIN agents a ON a.id=r.agent_id
 LEFT JOIN repositories repo ON repo.id=t.repository_id
 WHERE t.project_id=$1
   AND ($2='all' OR ($2='active' AND r.status IN ('queued','provisioning','running','waiting_for_approval','waiting_for_input','finalizing'))
     OR ($2='failed' AND r.status='failed') OR ($2='finished' AND r.status IN ('succeeded','failed','cancelled')))
   AND ($3='' OR strpos(lower(t.title),lower($3))>0)
   AND ($4::timestamptz IS NULL OR (r.created_at,r.id)<($4,$5::uuid))
 ORDER BY r.created_at DESC,r.id DESC LIMIT $6`, project, group, search, afterTime, afterID, limit+1)
	if dbError(w, err, "Run queue") {
		return
	}
	defer rows.Close()
	page := runQueuePage{Items: []json.RawMessage{}}
	for rows.Next() {
		var item json.RawMessage
		var created time.Time
		var id uuid.UUID
		if dbError(w, rows.Scan(&item, &created, &id), "Run queue") {
			return
		}
		page.Items = append(page.Items, item)
		if int64(len(page.Items)) == limit {
			cursor.CreatedAt = created
			cursor.ID = id
		}
	}
	if dbError(w, rows.Err(), "Run queue") {
		return
	}
	if int64(len(page.Items)) > limit {
		page.Items = page.Items[:limit]
		data, _ := json.Marshal(cursor)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	respond(w, 200, page)
}
