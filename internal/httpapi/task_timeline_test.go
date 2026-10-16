package httpapi_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type timelinePage struct {
	BoardRevision      int64  `json:"board_revision"`
	From               string `json:"from"`
	To                 string `json:"to"`
	OutsideWindowTotal int    `json:"outside_window_total"`
	Groups             []struct {
		Section    string           `json:"section"`
		Total      int              `json:"total"`
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"next_cursor"`
	} `json:"groups"`
}

func timelineRead(t *testing.T, f fixture, project, query string) timelinePage {
	t.Helper()
	var page timelinePage
	if err := json.Unmarshal(f.request(t, "GET", "/api/v1/projects/"+project+"/task-timeline?"+query, "", 200), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestTaskTimelineWindowAndUnscheduled(t *testing.T) {
	f := setup(t)
	project, agent, task := queueFixture(t, f, "界_% Long", true)
	if _, err := f.pool.Exec(t.Context(), `UPDATE tasks SET planned_start_date='0001-01-01',planned_finish_date='9999-12-31' WHERE id=$1`, task); err != nil {
		t.Fatal(err)
	}
	f.create(t, "runs", map[string]any{"task_id": task, "agent_id": agent})
	dates := [][3]string{{"Start boundary", "2026-10-01", "2026-10-01"}, {"Finish boundary", "2026-10-31", "2026-10-31"}, {"Before", "2026-09-01", "2026-09-30"}, {"After", "2026-11-01", "2026-11-02"}, {"Start only", "2027-01-01", ""}, {"Finish only", "", "2025-01-01"}, {"Neither", "", ""}, {"Cancelled", "2026-10-02", "2026-10-03"}}
	for _, row := range dates {
		id := f.create(t, "tasks", map[string]any{"project_id": project, "title": row[0]})["id"]
		status := "open"
		if row[0] == "Cancelled" {
			status = "cancelled"
		}
		if _, err := f.pool.Exec(t.Context(), `UPDATE tasks SET planned_start_date=NULLIF($2,'')::date,planned_finish_date=NULLIF($3,'')::date,status=$4 WHERE id=$1`, id, row[1], row[2], status); err != nil {
			t.Fatal(err)
		}
	}
	q := "from=2026-10-01&to=2026-10-31"
	p := timelineRead(t, f, project, q)
	if p.BoardRevision != 10 || p.From != "2026-10-01" || p.To != "2026-10-31" || p.OutsideWindowTotal != 2 || len(p.Groups) != 2 || p.Groups[0].Section != "scheduled" || p.Groups[0].Total != 3 || len(p.Groups[0].Items) != 3 || p.Groups[1].Section != "unscheduled" || p.Groups[1].Total != 3 {
		t.Fatalf("wrong page: %+v", p)
	}
	first := p.Groups[0].Items[0]
	if first["task"].(map[string]any)["id"] != task || first["run_count"] != float64(1) || first["active_run_count"] != float64(1) || first["repository"] == nil || first["latest_run"] == nil {
		t.Fatal(first)
	}
	if _, exists := first["task"].(map[string]any)["description"]; exists {
		t.Fatal("repeats full instructions")
	}
	literal := timelineRead(t, f, project, q+"&q="+url.QueryEscape(" 界_% "))
	if literal.Groups[0].Total != 1 || literal.Groups[1].Total != 0 || literal.OutsideWindowTotal != 0 {
		t.Fatal(literal)
	}
	cancelled := timelineRead(t, f, project, q+"&status=cancelled")
	if cancelled.Groups[0].Total != 1 {
		t.Fatal(cancelled)
	}
	for _, invalid := range []string{"", "from=2026-10-01", "from=2026-10-01&to=2026-09-30", "from=2026-01-01&to=2027-01-02", "from=0000-01-01&to=0001-01-01", "from=2026-02-29&to=2026-03-01", q + "&limit=101", q + "&section=other", q + "&cursor=x", q + "&q=" + strings.Repeat("a", 201)} {
		f.request(t, "GET", "/api/v1/projects/"+project+"/task-timeline?"+invalid, "", 422)
	}
	timelineRead(t, f, project, "from=2024-01-01&to=2024-12-31")
	for _, filter := range []string{"status=completed", "priority=urgent", "preferred_agent_id=" + agent} {
		got := timelineRead(t, f, project, q+"&"+filter)
		if got.Groups[0].Total != 0 || got.Groups[1].Total != 0 || got.OutsideWindowTotal != 0 {
			t.Fatal(got)
		}
	}
	foreign, foreignAgent, _ := queueFixture(t, f, "Foreign", true)
	_ = foreign
	f.request(t, "GET", "/api/v1/projects/"+project+"/task-timeline?"+q+"&preferred_agent_id="+foreignAgent, "", 404)
	response, err := f.server.Client().Get(f.server.URL + "/api/v1/projects/" + project + "/task-timeline?" + q)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable timeline")
	}
}

func TestTaskTimelineCursorScopeAndRevision(t *testing.T) {
	f := setup(t)
	project := f.create(t, "projects", map[string]any{"name": "Timeline"})["id"].(string)
	query := strings.Repeat("界", 200)
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO tasks(id,project_id,title,description,status,external_refs,planned_start_date,planned_finish_date,created_at) SELECT gen_random_uuid(),$1,$2,'','open','{}',CASE WHEN section=1 THEN DATE '2026-10-01' END,CASE WHEN section=1 THEN DATE '2026-10-01' END,'2026-10-01'::timestamptz FROM generate_series(1,115) n CROSS JOIN generate_series(1,2) section`, project, query); err != nil {
		t.Fatal(err)
	}
	base := "from=2026-10-01&to=2026-10-31&q=" + url.QueryEscape(query)
	for _, section := range []string{"scheduled", "unscheduled"} {
		seen := map[string]bool{}
		cursor := ""
		firstCursor := ""
		for {
			p := timelineRead(t, f, project, base+"&section="+section+"&limit=50&cursor="+url.QueryEscape(cursor))
			if len(p.Groups) != 1 || p.Groups[0].Total != 115 || len(p.Groups[0].Items) > 50 {
				t.Fatal(p)
			}
			for _, item := range p.Groups[0].Items {
				id := item["task"].(map[string]any)["id"].(string)
				if seen[id] {
					t.Fatal("duplicate", id)
				}
				seen[id] = true
			}
			cursor = p.Groups[0].NextCursor
			if cursor == "" {
				break
			}
			if firstCursor == "" {
				firstCursor = cursor
			}
			if len(cursor) > 4096 {
				t.Fatal("Unicode query cursor too large")
			}
		}
		if len(seen) != 115 {
			t.Fatal("missing tasks", len(seen))
		}
		q := url.Values{"from": {"2026-10-01"}, "to": {"2026-10-31"}, "q": {query}, "section": {section}, "limit": {"50"}, "cursor": {firstCursor}}
		for name, value := range map[string]string{"from": "2026-09-30", "to": "2026-10-30", "q": "other", "section": map[string]string{"scheduled": "unscheduled", "unscheduled": "scheduled"}[section], "limit": "20", "status": "open", "priority": "normal"} {
			changed, _ := url.ParseQuery(q.Encode())
			changed.Set(name, value)
			f.request(t, "GET", "/api/v1/projects/"+project+"/task-timeline?"+changed.Encode(), "", 422)
		}
		for _, bad := range []string{"!", strings.Repeat("a", 4097)} {
			changed, _ := url.ParseQuery(q.Encode())
			changed.Set("cursor", bad)
			f.request(t, "GET", "/api/v1/projects/"+project+"/task-timeline?"+changed.Encode(), "", 422)
		}
		foreign := f.create(t, "projects", map[string]any{"name": "Other"})["id"].(string)
		f.request(t, "GET", "/api/v1/projects/"+foreign+"/task-timeline?"+q.Encode(), "", 422)
		if section == "unscheduled" {
			f.create(t, "tasks", map[string]any{"project_id": project, "title": "revision change"})
			f.request(t, "GET", "/api/v1/projects/"+project+"/task-timeline?"+q.Encode(), "", 409)
		}
	}
}

func TestTaskTimelineSnapshotDuringMutation(t *testing.T) {
	f := setup(t)
	project, _, _ := queueFixture(t, f, "Original", false)
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `LOCK TABLE tasks IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		page timelinePage
		err  error
	}
	done := make(chan result, 1)
	go func() {
		response, err := f.server.Client().Get(f.server.URL + "/api/v1/projects/" + project + "/task-timeline?from=2026-10-01&to=2026-10-31")
		var p timelinePage
		if err == nil {
			defer response.Body.Close()
			var raw []byte
			raw, err = io.ReadAll(response.Body)
			if err == nil {
				err = json.Unmarshal(raw, &p)
			}
		}
		done <- result{p, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		if err = f.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%/* task timeline totals */%')`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("timeline did not reach snapshot boundary")
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO tasks(id,project_id,title,description,status,external_refs,planned_start_date,planned_finish_date) VALUES($1,$2,'Concurrent','','open','{}','2026-10-01','2026-10-01')`, uuid.New(), project); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.page.BoardRevision != 2 || len(got.page.Groups) != 2 || got.page.Groups[0].Total != 0 || got.page.Groups[1].Total != 1 {
		t.Fatal(fmt.Sprint(got))
	}
	fresh := timelineRead(t, f, project, "from=2026-10-01&to=2026-10-31")
	if fresh.BoardRevision != 3 || fresh.Groups[0].Total != 1 {
		t.Fatal(fresh)
	}
}
