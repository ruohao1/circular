package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/postgres"
)

type taskPlanningFilter struct {
	Project    string `json:"project"`
	Query      string `json:"q"`
	Scope      string `json:"scope,omitempty"`
	Status     string `json:"status"`
	Priority   string `json:"priority"`
	Repository string `json:"repository"`
	Agent      string `json:"agent"`
}
type taskPlanningCursor struct {
	taskPlanningFilter
	Version  int       `json:"v"`
	Revision int64     `json:"revision"`
	Position int64     `json:"position"`
	ID       uuid.UUID `json:"id"`
}
type taskPlanningGroup struct {
	Status     string            `json:"status"`
	Total      int64             `json:"total"`
	Items      []json.RawMessage `json:"items"`
	NextCursor string            `json:"next_cursor"`
}
type taskPlanningPage struct {
	BoardRevision int64               `json:"board_revision"`
	Groups        []taskPlanningGroup `json:"groups"`
}

const planningRepositoryJSON = `CASE WHEN repo.id IS NULL THEN NULL ELSE json_build_object('id',repo.id,'name',repo.name) END`
const planningAgentJSON = `CASE WHEN a.id IS NULL THEN NULL ELSE json_build_object('id',a.id,'name',a.name,'backend',a.backend,'enabled',a.enabled) END`
const planningResourceJoins = ` LEFT JOIN repositories repo ON repo.id=t.repository_id LEFT JOIN agents a ON a.id=t.preferred_agent_id `
const planningWhere = `t.project_id=$1 AND t.status=$2 AND ($3='' OR strpos(lower(t.title),lower($3))>0)
 AND ($4='' OR t.priority=$4) AND ($5::uuid IS NULL OR t.repository_id=$5) AND ($6::uuid IS NULL OR t.preferred_agent_id=$6)`

func planningItemJSON() string {
	return `json_build_object('task',` + projection("TaskPlanningSummary", "t") + `,
   'repository',` + planningRepositoryJSON + `,'preferred_agent',` + planningAgentJSON + `,
   'latest_run',latest.item,'run_count',counts.total,'active_run_count',counts.active)`
}

const planningRunJoins = `
   LEFT JOIN LATERAL (SELECT json_build_object('id',r.id,'status',r.status,'attempt',r.attempt,'agent_id',r.agent_id,'agent_name',ra.name,'kind',r.kind,'created_at',r.created_at,'started_at',r.started_at,'finished_at',r.finished_at) item
    FROM runs r JOIN agents ra ON ra.id=r.agent_id WHERE r.task_id=t.id ORDER BY r.attempt DESC LIMIT 1) latest ON true
   CROSS JOIN LATERAL (SELECT count(*) total,count(*) FILTER(WHERE r.status IN ('queued','provisioning','running','waiting_for_approval','waiting_for_input','finalizing')) active FROM runs r WHERE r.task_id=t.id) counts`

func taskPlanningStatuses(scope, status string) ([]string, bool) {
	statuses := []string{"open", "ready", "in_progress", "in_review", "completed"}
	switch scope {
	case "":
		if status != "" {
			return []string{status}, true
		}
	case "active":
		statuses = []string{"ready", "in_progress", "in_review"}
	case "backlog":
		statuses = []string{"open"}
	case "all":
		statuses = append(statuses, "cancelled")
	default:
		return nil, false
	}
	if status == "" {
		return statuses, true
	}
	for _, candidate := range statuses {
		if candidate == status {
			return []string{status}, true
		}
	}
	return []string{}, true
}

func (a *api) taskPlanning(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	project, ok := identifier(w, r.PathValue("project_id"), "path", "project_id")
	if !ok {
		return
	}
	q := r.URL.Query()
	filter := taskPlanningFilter{Project: project.String(), Query: strings.TrimSpace(q.Get("q")), Scope: q.Get("scope"), Status: q.Get("status"), Priority: q.Get("priority")}
	statuses, validScope := taskPlanningStatuses(filter.Scope, filter.Status)
	if !validScope {
		invalid(w, "query", "scope", "value_error", "Scope must be active, backlog, or all")
		return
	}
	if !utf8.ValidString(filter.Query) || utf8.RuneCountInString(filter.Query) > 200 || filter.Status != "" && !postgres.ValidTaskStatus(filter.Status) || filter.Priority != "" && !postgres.ValidTaskPriority(filter.Priority) {
		invalid(w, "query", "", "value_error", "Invalid search, status, or priority")
		return
	}
	var repository, agent *uuid.UUID
	for _, resource := range []struct {
		name string
		id   **uuid.UUID
		text *string
	}{{"repository_id", &repository, &filter.Repository}, {"preferred_agent_id", &agent, &filter.Agent}} {
		if raw := q.Get(resource.name); raw != "" {
			id, ok := identifier(w, raw, "query", resource.name)
			if !ok {
				return
			}
			*resource.id = &id
			*resource.text = id.String()
		}
	}
	limit, ok := integerQuery(w, r, "limit", 50, 1, 100)
	if !ok {
		return
	}
	var after *taskPlanningCursor
	if encoded := q.Get("cursor"); encoded != "" {
		var cursor taskPlanningCursor
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		if len(encoded) > 2048 || filter.Status == "" || err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Version != 1 || cursor.taskPlanningFilter != filter || cursor.Revision < 1 || cursor.Revision > 9007199254740991 || cursor.Position < 0 || cursor.ID == uuid.Nil {
			invalid(w, "query", "cursor", "value_error", "Cursor does not match this Project and filter")
			return
		}
		after = &cursor
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if dbError(w, err, "Task planning") {
		return
	}
	defer rollback(r.Context(), tx)
	page := taskPlanningPage{Groups: []taskPlanningGroup{}}
	if dbError(w, tx.QueryRow(r.Context(), `SELECT task_board_revision FROM projects WHERE id=$1`, project).Scan(&page.BoardRevision), "Project") {
		return
	}
	for _, resource := range []struct {
		table string
		id    *uuid.UUID
	}{{"repositories", repository}, {"agents", agent}} {
		if resource.id != nil {
			var id uuid.UUID
			if dbError(w, tx.QueryRow(r.Context(), `SELECT id FROM `+resource.table+` WHERE id=$1 AND project_id=$2`, *resource.id, project).Scan(&id), "Filter resource") {
				return
			}
		}
	}
	if after != nil && after.Revision != page.BoardRevision {
		problem(w, 409, "Task list changed; reload all groups before loading more")
		return
	}
	for _, status := range statuses {
		group := taskPlanningGroup{Status: status, Items: []json.RawMessage{}}
		args := []any{project, status, filter.Query, filter.Priority, repository, agent}
		if dbError(w, tx.QueryRow(r.Context(), `SELECT count(*) FROM tasks t WHERE `+planningWhere, args...).Scan(&group.Total), "Task planning") {
			return
		}
		var position *int64
		var id *uuid.UUID
		if after != nil {
			position = &after.Position
			id = &after.ID
		}
		args = append(args, position, id, limit+1)
		rows, err := tx.Query(r.Context(), `SELECT `+planningItemJSON()+`,t.board_position,t.id
   FROM (SELECT * FROM tasks t WHERE `+planningWhere+` AND ($7::bigint IS NULL OR (t.board_position,t.id)>($7,$8::uuid)) ORDER BY t.board_position,t.id LIMIT $9) t
   `+planningResourceJoins+`
 `+planningRunJoins+`
   ORDER BY t.board_position,t.id`, args...)
		if dbError(w, err, "Task planning") {
			return
		}
		cursor := taskPlanningCursor{taskPlanningFilter: filter, Version: 1, Revision: page.BoardRevision}
		cursor.Status = status
		for rows.Next() {
			var item json.RawMessage
			var pos int64
			var task uuid.UUID
			if err = rows.Scan(&item, &pos, &task); err != nil {
				rows.Close()
				dbError(w, err, "Task planning")
				return
			}
			group.Items = append(group.Items, item)
			if int64(len(group.Items)) == limit {
				cursor.Position = pos
				cursor.ID = task
			}
		}
		err = rows.Err()
		rows.Close()
		if dbError(w, err, "Task planning") {
			return
		}
		if int64(len(group.Items)) > limit {
			group.Items = group.Items[:limit]
			raw, _ := json.Marshal(cursor)
			group.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
		}
		page.Groups = append(page.Groups, group)
	}
	if dbError(w, tx.Commit(r.Context()), "Task planning") {
		return
	}
	respond(w, 200, page)
}

func (a *api) taskPlanningDetail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := identifier(w, r.PathValue("task_id"), "path", "task_id")
	if !ok {
		return
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if dbError(w, err, "Task") {
		return
	}
	defer rollback(r.Context(), tx)
	var task, repository, agent json.RawMessage
	var revision int64
	err = tx.QueryRow(r.Context(), `SELECT `+projection("TaskRead", "t")+`,`+planningRepositoryJSON+`,`+planningAgentJSON+`,p.task_board_revision FROM tasks t JOIN projects p ON p.id=t.project_id `+planningResourceJoins+` WHERE t.id=$1`, id).Scan(&task, &repository, &agent, &revision)
	if dbError(w, err, "Task") {
		return
	}
	caps, err := postgres.TaskPlanningCapabilities(r.Context(), tx, id)
	if dbError(w, err, "Task") {
		return
	}
	prerequisites, err := postgres.TaskPrerequisites(r.Context(), tx, id)
	if dbError(w, err, "Task prerequisites") {
		return
	}
	if dbError(w, tx.Commit(r.Context()), "Task") {
		return
	}
	respond(w, 200, map[string]any{"task": task, "repository": repository, "preferred_agent": agent, "board_revision": revision, "content_editable": caps.ContentEditable, "repository_editable": caps.RepositoryEditable, "content_lock_reason": caps.ContentLockReason, "repository_lock_reason": caps.RepositoryLockReason, "prerequisite_task_ids": prerequisites, "dependencies_editable": caps.DependenciesEditable, "dependencies_lock_reason": caps.DependenciesLockReason})
}
