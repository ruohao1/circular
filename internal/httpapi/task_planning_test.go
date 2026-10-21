package httpapi_test

import (
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/httpapi"
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type planningPage struct {
	BoardRevision int64 `json:"board_revision"`
	Groups        []struct {
		Status     string           `json:"status"`
		Total      int              `json:"total"`
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"next_cursor"`
	} `json:"groups"`
}

func planningRead(t *testing.T, f fixture, project, query string) planningPage {
	t.Helper()
	var page planningPage
	if e := json.Unmarshal(f.request(t, "GET", "/api/v1/projects/"+project+"/task-planning?"+query, "", 200), &page); e != nil {
		t.Fatal(e)
	}
	return page
}
func TestTaskPlanningGroupedRead(t *testing.T) {
	f := setup(t)
	project, agent, task := queueFixture(t, f, "Fix_% Token", true)
	for i := range 54 {
		f.create(t, "tasks", map[string]any{"project_id": project, "title": fmt.Sprint("Task ", i)})
	}
	f.historyRun(t, task, agent, "queued")
	finished := f.historyRun(t, task, agent, "queued")
	if _, e := f.pool.Exec(t.Context(), `UPDATE runs SET status='succeeded' WHERE id=$1`, finished["id"]); e != nil {
		t.Fatal(e)
	}
	queueFixture(t, f, "Foreign", false)
	page := planningRead(t, f, project, "")
	if page.BoardRevision != 56 || len(page.Groups) != 5 || page.Groups[0].Status != "open" || page.Groups[0].Total != 55 || len(page.Groups[0].Items) != 50 || page.Groups[0].NextCursor == "" {
		t.Fatal(page)
	}
	first := page.Groups[0].Items[0]
	if first["task"].(map[string]any)["id"] != task || first["run_count"] != float64(2) || first["active_run_count"] != float64(1) || first["latest_run"].(map[string]any)["id"] != finished["id"] || first["repository"] == nil || first["preferred_agent"] != nil {
		t.Fatal(first)
	}
	if _, ok := first["task"].(map[string]any)["description"]; ok {
		t.Fatal("collection repeats full instructions")
	}
	for _, group := range page.Groups[1:] {
		if group.Total != 0 || group.Items == nil || len(group.Items) != 0 {
			t.Fatal(group)
		}
	}
	response, e := f.server.Client().Get(f.server.URL + "/api/v1/projects/" + project + "/task-planning")
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable planning")
	}
}

func TestTaskPlanningSnapshotStaysCoherentDuringCreation(t *testing.T) {
	f := setup(t)
	project, _, _ := queueFixture(t, f, "Original", false)
	locker, e := f.pool.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer locker.Rollback(t.Context())
	if _, e = locker.Exec(t.Context(), `LOCK TABLE tasks IN ACCESS EXCLUSIVE MODE`); e != nil {
		t.Fatal(e)
	}
	type response struct {
		page planningPage
		err  error
	}
	result := make(chan response, 1)
	go func() {
		r, e := f.server.Client().Get(f.server.URL + "/api/v1/projects/" + project + "/task-planning")
		if e != nil {
			result <- response{err: e}
			return
		}
		defer r.Body.Close()
		data, e := io.ReadAll(r.Body)
		var p planningPage
		if e == nil {
			e = json.Unmarshal(data, &p)
		}
		result <- response{p, e}
	}()
	// The request reads the Project revision before waiting on the Tasks table.
	deadline := time.Now().Add(5 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		if e = f.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT count(*) FROM tasks t WHERE%')`).Scan(&blocked); e != nil {
			t.Fatal(e)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("planning request did not reach the controlled snapshot boundary")
	}
	if _, e = locker.Exec(t.Context(), `INSERT INTO tasks(id,project_id,title,description,status,external_refs) VALUES($1,$2,'Concurrent','','open','{}')`, uuid.New(), project); e != nil {
		t.Fatal(e)
	}
	if e = locker.Commit(t.Context()); e != nil {
		t.Fatal(e)
	}
	got := <-result
	if got.err != nil || got.page.BoardRevision != 2 || len(got.page.Groups) != 5 || got.page.Groups[0].Total != 1 || len(got.page.Groups[0].Items) != 1 {
		t.Fatal("mixed snapshots", got)
	}
	fresh := planningRead(t, f, project, "")
	if fresh.BoardRevision != 3 || fresh.Groups[0].Total != 2 {
		t.Fatal(fresh)
	}
}
func TestTaskPlanningFiltersAndCursor(t *testing.T) {
	f := setup(t)
	project, agent, task := queueFixture(t, f, "Fix_% Token", false)
	for _, title := range []string{"FIX_% next", "other", "cancelled"} {
		created := f.create(t, "tasks", map[string]any{"project_id": project, "title": title, "priority": "high", "preferred_agent_id": agent})
		if title == "cancelled" {
			if _, e := f.pool.Exec(t.Context(), `UPDATE tasks SET status='cancelled' WHERE id=$1`, created["id"]); e != nil {
				t.Fatal(e)
			}
		}
	}
	page := planningRead(t, f, project, "q=fIX_%25&status=open&limit=1")
	if page.Groups[0].Total != 2 || page.Groups[0].Items[0]["task"].(map[string]any)["id"] != task {
		t.Fatal(page)
	}
	cursor := url.QueryEscape(page.Groups[0].NextCursor)
	next := planningRead(t, f, project, "q=fIX_%25&status=open&limit=1&cursor="+cursor)
	if len(next.Groups[0].Items) != 1 || next.Groups[0].NextCursor != "" {
		t.Fatal(next)
	}
	for _, query := range []string{"q=other&status=open", "q=fIX_%25&status=ready", "q=fIX_%25&status=open&priority=high"} {
		f.request(t, "GET", "/api/v1/projects/"+project+"/task-planning?"+query+"&cursor="+cursor, "", 422)
	}
	if got := planningRead(t, f, project, "priority=high&preferred_agent_id="+agent).Groups[0].Total; got != 2 {
		t.Fatal(got)
	}
	cancelled := planningRead(t, f, project, "status=cancelled")
	if len(cancelled.Groups) != 1 || cancelled.Groups[0].Total != 1 {
		t.Fatal(cancelled)
	}
	other, _, _ := queueFixture(t, f, "Foreign", false)
	f.request(t, "GET", "/api/v1/projects/"+other+"/task-planning?q=fIX_%25&status=open&cursor="+cursor, "", 422)
	f.create(t, "tasks", map[string]any{"project_id": project, "title": "new"})
	f.request(t, "GET", "/api/v1/projects/"+project+"/task-planning?q=fIX_%25&status=open&limit=1&cursor="+cursor, "", 409)
	f.request(t, "GET", "/api/v1/projects/"+uuid.NewString()+"/task-planning", "", 404)
	f.request(t, "GET", "/api/v1/projects/"+other+"/task-planning?preferred_agent_id="+agent, "", 404)
}
func TestTaskPlanningValidationWithoutDatabase(t *testing.T) {
	pool, e := pgxpool.New(t.Context(), "postgresql://test:test@127.0.0.1:1/unreachable")
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	h, e := httpapi.New(pool, httpapi.Config{ArtifactRoot: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	project, task := uuid.NewString(), uuid.NewString()
	check := func(method, path, body string, want int) {
		t.Helper()
		out := httptest.NewRecorder()
		h.ServeHTTP(out, httptest.NewRequest(method, path, strings.NewReader(body)))
		if out.Code != want {
			t.Errorf("%s %s: %d want %d: %s", method, path, out.Code, want, out.Body.String())
		}
	}
	for _, query := range []string{"status=bad", "priority=bad", "limit=0", "limit=101", "limit=1.5", "cursor=x", "status=open&cursor=" + strings.Repeat("x", 2049), "repository_id=bad", "preferred_agent_id=bad", "q=" + url.QueryEscape(strings.Repeat("界", 201))} {
		check("GET", "/api/v1/projects/"+project+"/task-planning?"+query, "", 422)
	}
	for _, body := range []string{`{}`, `null`, `[]`, `{"expected_revision":0}`, `{"expected_revision":1.5}`, `{"expected_revision":9007199254740992}`, `{"expected_revision":1,"title":null}`, `{"expected_revision":1,"title":" "}`, `{"expected_revision":1,"priority":"bad"}`, `{"expected_revision":1,"title":"a","title":"b"}`, `{"expected_revision":1,"status":"open"}`, `{"expected_revision":1,"repository_id":"bad"}`, `{"expected_revision":1,"title":"` + strings.Repeat("界", 501) + `"}`, `{"expected_revision":1,"description":"` + strings.Repeat("界", 20001) + `"}`} {
		check("PATCH", "/api/v1/tasks/"+task, body, 422)
	}
	check("PATCH", "/api/v1/tasks/"+task, `{"expected_revision":1,"description":"`+strings.Repeat("x", 128<<10)+`"}`, 413)
	for _, body := range []string{`{"expected_revision":1,"expected_board_revision":1,"status":"ready","placement":"before"}`, `{"expected_revision":1,"expected_board_revision":1,"status":"ready","placement":"end","anchor_task_id":null}`, `{"expected_revision":1,"expected_board_revision":1,"status":"bad","placement":"end"}`} {
		check("POST", "/api/v1/tasks/"+task+"/move", body, 422)
	}
	check("POST", "/api/v1/tasks", `{"project_id":"`+project+`","title":"x","priority":"bad"}`, 422)
}
func TestTaskPlanningUpdateAndMoveHTTP(t *testing.T) {
	f := setup(t)
	project, agent, task := queueFixture(t, f, "A", true)
	path := "/api/v1/tasks/" + task
	detail := decode(t, f.request(t, "GET", path+"/planning", "", 200))
	if detail["content_editable"] != true || detail["repository_editable"] != true || detail["content_lock_reason"] != nil {
		t.Fatal(detail)
	}
	updated := decode(t, f.request(t, "PATCH", path, `{"expected_revision":1,"title":"  New title  ","priority":"urgent","preferred_agent_id":"`+agent+`"}`, 200))
	saved := updated["task"].(map[string]any)
	if saved["title"] != "New title" || saved["priority"] != "urgent" || saved["revision"] != float64(2) || saved["repository_id"] == nil {
		t.Fatal(updated)
	}
	f.request(t, "PATCH", path, `{"expected_revision":1,"title":"stale"}`, 409)
	cleared := decode(t, f.request(t, "PATCH", path, `{"expected_revision":2,"repository_id":null,"preferred_agent_id":null}`, 200))
	saved = cleared["task"].(map[string]any)
	if saved["repository_id"] != nil || saved["preferred_agent_id"] != nil || saved["priority"] != "urgent" {
		t.Fatal(saved)
	}
	moved := decode(t, f.request(t, "POST", path+"/move", fmt.Sprintf(`{"expected_revision":3,"expected_board_revision":%v,"status":"in_progress","placement":"end"}`, cleared["board_revision"]), 200))
	if moved["task"].(map[string]any)["status"] != "in_progress" {
		t.Fatal(moved)
	}
	var legacy []any
	if e := json.Unmarshal(f.request(t, "GET", "/api/v1/tasks?project_id="+project, "", 200), &legacy); e != nil || len(legacy) != 1 {
		t.Fatal(legacy, e)
	}
	restored := decode(t, f.request(t, "PATCH", path, fmt.Sprintf(`{"expected_revision":4,"repository_id":%q}`, detail["task"].(map[string]any)["repository_id"]), 200))
	f.request(t, "POST", path+"/move", fmt.Sprintf(`{"expected_revision":5,"expected_board_revision":%v,"status":"ready","placement":"end"}`, restored["board_revision"]), 200)
	f.create(t, "runs", map[string]any{"task_id": task, "agent_id": agent})
	f.request(t, "PATCH", path, `{"expected_revision":7,"description":"changed while active"}`, 409)
	detail = decode(t, f.request(t, "GET", path+"/planning", "", 200))
	if detail["content_editable"] != false || detail["content_lock_reason"] != "active_work" || detail["repository_lock_reason"] != "has_runs" {
		t.Fatal(detail)
	}
	f.request(t, "GET", "/api/v1/tasks/"+uuid.NewString()+"/planning", "", 404)
}
func TestRunQueueTaskFilterCompatibility(t *testing.T) {
	f := setup(t)
	project, agent, task := queueFixture(t, f, "First", false)
	other := f.create(t, "tasks", map[string]any{"project_id": project, "title": "Other"})["id"].(string)
	for range 3 {
		f.historyRun(t, task, agent, "queued")
	}
	f.historyRun(t, other, agent, "queued")
	page := readQueue(t, f, project, "task_id="+task+"&limit=2")
	if len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatal(page)
	}
	for _, item := range page.Items {
		if item.Run["task_id"] != task {
			t.Fatal(item)
		}
	}
	next := readQueue(t, f, project, "task_id="+task+"&limit=2&cursor="+url.QueryEscape(page.NextCursor))
	if len(next.Items) != 1 || next.NextCursor != "" {
		t.Fatal(next)
	}
	f.request(t, "GET", "/api/v1/projects/"+project+"/run-queue?cursor="+url.QueryEscape(page.NextCursor), "", 422)
	legacy := readQueue(t, f, project, "limit=2")
	if len(readQueue(t, f, project, "cursor="+url.QueryEscape(legacy.NextCursor)).Items) != 2 {
		t.Fatal("legacy cursor changed")
	}
	f.request(t, "GET", "/api/v1/projects/"+project+"/run-queue?task_id="+uuid.NewString(), "", 404)
}
