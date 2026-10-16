package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/postgres"
)

type taskTimelineScope struct {
	taskPlanningFilter
	From    string `json:"from"`
	To      string `json:"to"`
	Section string `json:"section"`
	Limit   int64  `json:"limit"`
}
type taskTimelineCursor struct {
	taskTimelineScope
	Version   int       `json:"v"`
	Revision  int64     `json:"revision"`
	Date      string    `json:"date,omitempty"`
	CreatedAt string    `json:"created_at,omitempty"`
	ID        uuid.UUID `json:"id"`
}
type taskTimelineGroup struct {
	Section    string            `json:"section"`
	Total      int64             `json:"total"`
	Items      []json.RawMessage `json:"items"`
	NextCursor string            `json:"next_cursor"`
}
type taskTimelinePage struct {
	BoardRevision      int64               `json:"board_revision"`
	From               string              `json:"from"`
	To                 string              `json:"to"`
	OutsideWindowTotal int64               `json:"outside_window_total"`
	Groups             []taskTimelineGroup `json:"groups"`
}

const timelineWhere = `t.project_id=$1 AND (($2='' AND t.status<>'cancelled') OR t.status=$2)
 AND ($3='' OR strpos(lower(t.title),lower($3))>0) AND ($4='' OR t.priority=$4)
 AND ($5::uuid IS NULL OR t.repository_id=$5) AND ($6::uuid IS NULL OR t.preferred_agent_id=$6)
 AND $7::date <= $8::date`
const timelineScheduled = `t.planned_start_date IS NOT NULL AND t.planned_finish_date IS NOT NULL`
const timelineOverlap = `t.planned_start_date<=$8::date AND t.planned_finish_date>=$7::date`

func (a *api) taskTimeline(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	project, ok := identifier(w, r.PathValue("project_id"), "path", "project_id")
	if !ok {
		return
	}
	q := r.URL.Query()
	scope := taskTimelineScope{taskPlanningFilter: taskPlanningFilter{Project: project.String(), Query: strings.TrimSpace(q.Get("q")), Status: q.Get("status"), Priority: q.Get("priority")}, From: q.Get("from"), To: q.Get("to"), Section: q.Get("section")}
	if scope.From == "" || scope.To == "" || postgres.ValidateTaskSchedule(&scope.From, &scope.To) != nil {
		invalid(w, "query", "from", "value_error", "from and to must be real YYYY-MM-DD dates with from on or before to")
		return
	}
	from, _ := time.Parse("2006-01-02", scope.From)
	to, _ := time.Parse("2006-01-02", scope.To)
	if to.Sub(from) > 365*24*time.Hour {
		invalid(w, "query", "to", "value_error", "Timeline window must contain at most 366 calendar days")
		return
	}
	if !utf8.ValidString(scope.Query) || utf8.RuneCountInString(scope.Query) > 200 || scope.Status != "" && !postgres.ValidTaskStatus(scope.Status) || scope.Priority != "" && !postgres.ValidTaskPriority(scope.Priority) || scope.Section != "" && scope.Section != "scheduled" && scope.Section != "unscheduled" {
		invalid(w, "query", "", "value_error", "Invalid Timeline filter or section")
		return
	}
	scope.Limit, ok = integerQuery(w, r, "limit", 50, 1, 100)
	if !ok {
		return
	}
	var repository, agent *uuid.UUID
	for _, resource := range []struct {
		name string
		id   **uuid.UUID
		text *string
	}{{"repository_id", &repository, &scope.Repository}, {"preferred_agent_id", &agent, &scope.Agent}} {
		if raw := q.Get(resource.name); raw != "" {
			id, valid := identifier(w, raw, "query", resource.name)
			if !valid {
				return
			}
			*resource.id = &id
			*resource.text = id.String()
		}
	}
	var after *taskTimelineCursor
	if encoded := q.Get("cursor"); encoded != "" {
		bad := func() {
			invalid(w, "query", "cursor", "value_error", "Cursor does not match this Project, window, filters, section and limit")
		}
		if len(encoded) > 4096 || scope.Section == "" {
			bad()
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		var cursor taskTimelineCursor
		if err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Version != 1 || cursor.taskTimelineScope != scope || cursor.Revision < 1 || cursor.Revision > 9007199254740991 || cursor.ID == uuid.Nil {
			bad()
			return
		}
		if scope.Section == "scheduled" {
			if cursor.Date == "" || cursor.CreatedAt != "" || postgres.ValidateTaskSchedule(&cursor.Date, nil) != nil {
				bad()
				return
			}
		} else if _, err := time.Parse(time.RFC3339Nano, cursor.CreatedAt); err != nil || cursor.Date != "" {
			bad()
			return
		}
		after = &cursor
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if dbError(w, err, "Task Timeline") {
		return
	}
	defer rollback(r.Context(), tx)
	page := taskTimelinePage{From: scope.From, To: scope.To, Groups: []taskTimelineGroup{}}
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
		problem(w, 409, "Task Timeline changed; reload both sections before continuing")
		return
	}
	args := []any{project, scope.Status, scope.Query, scope.Priority, repository, agent, scope.From, scope.To}
	var scheduled, unscheduled int64
	err = tx.QueryRow(r.Context(), `SELECT /* task timeline totals */
 count(*) FILTER(WHERE `+timelineScheduled+` AND `+timelineOverlap+`),
 count(*) FILTER(WHERE NOT (`+timelineScheduled+`)),
 count(*) FILTER(WHERE `+timelineScheduled+` AND NOT (`+timelineOverlap+`))
 FROM tasks t WHERE `+timelineWhere, args...).Scan(&scheduled, &unscheduled, &page.OutsideWindowTotal)
	if dbError(w, err, "Task Timeline") {
		return
	}
	sections := []string{"scheduled", "unscheduled"}
	if scope.Section != "" {
		sections = []string{scope.Section}
	}
	for _, section := range sections {
		group := taskTimelineGroup{Section: section, Total: scheduled, Items: []json.RawMessage{}}
		where := timelineScheduled + ` AND ` + timelineOverlap
		order := "t.planned_start_date,t.id"
		seek := `($9::date IS NULL OR (t.planned_start_date,t.id)>($9::date,$10::uuid))`
		var key any
		var lastID *uuid.UUID
		if section == "unscheduled" {
			group.Total = unscheduled
			where = `NOT (` + timelineScheduled + `)`
			order = "t.created_at DESC,t.id DESC"
			seek = `($9::timestamptz IS NULL OR (t.created_at,t.id)<($9::timestamptz,$10::uuid))`
		}
		if after != nil {
			lastID = &after.ID
			if section == "scheduled" {
				key = after.Date
			} else {
				key = after.CreatedAt
			}
		}
		rows, err := tx.Query(r.Context(), `SELECT `+planningItemJSON()+`,to_char(t.planned_start_date,'YYYY-MM-DD'),t.created_at,t.id
 FROM (SELECT * FROM tasks t WHERE `+timelineWhere+` AND `+where+` AND `+seek+` ORDER BY `+order+` LIMIT $11) t
 `+planningResourceJoins+planningRunJoins+` ORDER BY `+order, append(args, key, lastID, scope.Limit+1)...)
		if dbError(w, err, "Task Timeline") {
			return
		}
		cursor := taskTimelineCursor{taskTimelineScope: scope, Version: 1, Revision: page.BoardRevision}
		cursor.Section = section
		for rows.Next() {
			var item json.RawMessage
			var date *string
			var created time.Time
			var id uuid.UUID
			if err = rows.Scan(&item, &date, &created, &id); err != nil {
				rows.Close()
				dbError(w, err, "Task Timeline")
				return
			}
			group.Items = append(group.Items, item)
			if int64(len(group.Items)) == scope.Limit {
				cursor.ID = id
				if section == "scheduled" {
					cursor.Date = *date
				} else {
					cursor.CreatedAt = created.UTC().Format(time.RFC3339Nano)
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if dbError(w, err, "Task Timeline") {
			return
		}
		if int64(len(group.Items)) > scope.Limit {
			group.Items = group.Items[:scope.Limit]
			raw, _ := json.Marshal(cursor)
			group.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
		}
		page.Groups = append(page.Groups, group)
	}
	if dbError(w, tx.Commit(r.Context()), "Task Timeline") {
		return
	}
	respond(w, 200, page)
}
