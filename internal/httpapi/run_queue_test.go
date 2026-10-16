package httpapi_test

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/httpapi"
)

type queuePage struct {
	Items []struct {
		Run        map[string]any `json:"run"`
		TaskTitle  string         `json:"task_title"`
		AgentName  string         `json:"agent_name"`
		Repository *struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"repository"`
	} `json:"items"`
	NextCursor string `json:"next_cursor"`
}

func readQueue(t *testing.T, f fixture, project, query string) queuePage {
	t.Helper()
	var page queuePage
	if err := json.Unmarshal(f.request(t, "GET", "/api/v1/projects/"+project+"/run-queue?"+query, "", 200), &page); err != nil {
		t.Fatal(err)
	}
	return page
}
func queueFixture(t *testing.T, f fixture, title string, repository bool) (string, string, string) {
	t.Helper()
	project := f.create(t, "projects", map[string]any{"name": "Queue project"})["id"].(string)
	agent := f.create(t, "agents", map[string]any{"project_id": project, "name": "Queue engineer"})["id"].(string)
	input := map[string]any{"project_id": project, "title": title}
	if repository {
		input["repository_id"] = f.create(t, "repositories", map[string]any{"project_id": project, "name": "Queue source", "clone_url": "/fixture"})["id"]
	}
	task := f.create(t, "tasks", input)["id"].(string)
	return project, agent, task
}
func TestRunQueueProjectSummaries(t *testing.T) {
	f := setup(t)
	project, agent, task := queueFixture(t, f, "Implement search", true)
	first := f.create(t, "runs", map[string]any{"task_id": task, "agent_id": agent})
	second := f.create(t, "runs", map[string]any{"task_id": task, "agent_id": agent})
	f.run(t)
	page := readQueue(t, f, project, "")
	if len(page.Items) != 2 {
		t.Fatalf("items: %+v", page)
	}
	for i, item := range page.Items {
		if item.TaskTitle != "Implement search" || item.AgentName != "Queue engineer" || item.Repository == nil || item.Repository.Name != "Queue source" {
			t.Fatalf("summary: %+v", item)
		}
		want := second
		if i == 1 {
			want = first
		}
		if !reflect.DeepEqual(item.Run, want) {
			t.Fatalf("Run projection differs: got %#v want %#v", item.Run, want)
		}
	}
	p, a, task2 := queueFixture(t, f, "No source", false)
	f.create(t, "runs", map[string]any{"task_id": task2, "agent_id": a})
	if readQueue(t, f, p, "").Items[0].Repository != nil {
		t.Fatal("missing Repository must be null")
	}
	var legacy []any
	if err := json.Unmarshal(f.request(t, "GET", "/api/v1/runs?project_id="+project, "", 200), &legacy); err != nil || len(legacy) != 2 {
		t.Fatal("legacy Run array changed", err)
	}
}
func TestRunQueueGroupsAndLiteralSearch(t *testing.T) {
	f := setup(t)
	project, agent, task := queueFixture(t, f, "Fix_% Token", false)
	for _, status := range []string{"queued", "provisioning", "running", "waiting_for_approval", "waiting_for_input", "finalizing", "succeeded", "failed", "cancelled"} {
		run := f.create(t, "runs", map[string]any{"task_id": task, "agent_id": agent})
		if _, err := f.pool.Exec(t.Context(), "UPDATE runs SET status=$1 WHERE id=$2", status, run["id"]); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		query string
		want  int
	}{{"", 9}, {"group=all", 9}, {"group=active", 6}, {"group=failed", 1}, {"group=finished", 3}, {"q=%20fIX_%25%20", 9}, {"q=not_present%25", 0}, {"q=_%25missing", 0}} {
		if got := len(readQueue(t, f, project, tc.query).Items); got != tc.want {
			t.Errorf("%s: got %d want %d", tc.query, got, tc.want)
		}
	}
}
func TestRunQueuePagination(t *testing.T) {
	f := setup(t)
	project, agent, task := queueFixture(t, f, "Paging", false)
	ids := []string{}
	for range 3 {
		run := f.create(t, "runs", map[string]any{"task_id": task, "agent_id": agent})
		ids = append(ids, run["id"].(string))
	}
	if _, err := f.pool.Exec(t.Context(), "UPDATE runs SET created_at='2026-10-03T10:00:00Z' WHERE task_id=$1", task); err != nil {
		t.Fatal(err)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	cursor := ""
	for i, want := range ids {
		page := readQueue(t, f, project, "limit=1&cursor="+url.QueryEscape(cursor))
		if len(page.Items) != 1 || page.Items[0].Run["id"] != want {
			t.Fatalf("page %d: %+v", i, page)
		}
		cursor = page.NextCursor
		if (i == 2) != (cursor == "") {
			t.Fatalf("wrong cursor on page %d", i)
		}
	}
	newer := f.create(t, "runs", map[string]any{"task_id": task, "agent_id": agent})
	if _, err := f.pool.Exec(t.Context(), "UPDATE runs SET created_at='2026-10-04T10:00:00Z' WHERE id=$1", newer["id"]); err != nil {
		t.Fatal(err)
	}
	if readQueue(t, f, project, "limit=1").Items[0].Run["id"] != newer["id"] {
		t.Fatal("newest page missed inserted Run")
	}
}
func TestRunQueueCursorScopeAndMissingProject(t *testing.T) {
	f := setup(t)
	project, agent, task := queueFixture(t, f, "Scope", false)
	for range 2 {
		f.create(t, "runs", map[string]any{"task_id": task, "agent_id": agent})
	}
	cursor := url.QueryEscape(readQueue(t, f, project, "limit=1").NextCursor)
	other, _, _ := queueFixture(t, f, "Other", false)
	for _, path := range []string{other + "/run-queue?cursor=" + cursor, project + "/run-queue?group=active&cursor=" + cursor, project + "/run-queue?q=Scope&cursor=" + cursor} {
		f.request(t, "GET", "/api/v1/projects/"+path, "", 422)
	}
	empty := readQueue(t, f, other, "")
	if empty.Items == nil || len(empty.Items) != 0 || empty.NextCursor != "" {
		t.Fatal("incorrect empty page", empty)
	}
	f.request(t, "GET", "/api/v1/projects/"+uuid.NewString()+"/run-queue", "", 404)
	response, err := f.server.Client().Get(f.server.URL + "/api/v1/projects/" + project + "/run-queue")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("queue must not be cached")
	}
}
func TestRunQueueAgentFilter(t *testing.T) {
	f := setup(t)
	project, agent, task := queueFixture(t, f, "Agent history", false)
	otherAgent := f.create(t, "agents", map[string]any{"project_id": project, "name": "Other engineer"})["id"].(string)
	otherTask := f.create(t, "tasks", map[string]any{"project_id": project, "title": "Other work", "preferred_agent_id": otherAgent})["id"].(string)
	if _, err := f.pool.Exec(t.Context(), "UPDATE tasks SET preferred_agent_id=$1 WHERE id=$2", otherAgent, task); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, item := range []struct{ task, status string }{{task, "succeeded"}, {task, "failed"}, {otherTask, "queued"}} {
		run := f.create(t, "runs", map[string]any{"task_id": item.task, "agent_id": agent})
		want[run["id"].(string)] = true
		if _, err := f.pool.Exec(t.Context(), "UPDATE runs SET status=$1 WHERE id=$2", item.status, run["id"]); err != nil {
			t.Fatal(err)
		}
	}
	f.create(t, "runs", map[string]any{"task_id": task, "agent_id": otherAgent})
	if _, err := f.pool.Exec(t.Context(), "UPDATE agents SET enabled=false WHERE id=$1", agent); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		want  int
	}{{"agent_id=" + agent, 3}, {"agent_id=" + otherAgent, 1}, {"agent_id=" + agent + "&task_id=" + task, 2}, {"agent_id=" + agent + "&group=active", 1}, {"agent_id=" + agent + "&group=failed&q=history&task_id=" + task, 1}, {"agent_id=" + agent + "&q=missing", 0}, {"", 4}} {
		if got := len(readQueue(t, f, project, tc.query).Items); got != tc.want {
			t.Errorf("%s: got %d want %d", tc.query, got, tc.want)
		}
	}
	cursor := ""
	for range 3 {
		page := readQueue(t, f, project, "agent_id="+agent+"&limit=1&cursor="+url.QueryEscape(cursor))
		if len(page.Items) != 1 || page.Items[0].Run["agent_id"] != agent || !want[page.Items[0].Run["id"].(string)] {
			t.Fatalf("Agent history must page actual executions, regardless of preferred or enabled Agent: %+v", page)
		}
		delete(want, page.Items[0].Run["id"].(string))
		cursor = page.NextCursor
	}
	if cursor != "" || len(want) != 0 {
		t.Fatalf("incomplete Agent history: cursor=%q missing=%v", cursor, want)
	}
}

func TestRunQueueAgentScopeAndCursor(t *testing.T) {
	f := setup(t)
	project, agent, task := queueFixture(t, f, "Agent scope", false)
	other := f.create(t, "agents", map[string]any{"project_id": project, "name": "Empty Agent"})["id"].(string)
	_, foreign, _ := queueFixture(t, f, "Foreign", false)
	for range 2 {
		f.create(t, "runs", map[string]any{"task_id": task, "agent_id": agent})
	}
	base := "/api/v1/projects/" + project + "/run-queue?"
	for _, id := range []string{foreign, uuid.NewString()} {
		f.request(t, "GET", base+"agent_id="+id, "", 404)
	}
	empty := readQueue(t, f, project, "agent_id="+other)
	if empty.Items == nil || len(empty.Items) != 0 || empty.NextCursor != "" {
		t.Fatal("incorrect empty Agent history", empty)
	}
	filtered := url.QueryEscape(readQueue(t, f, project, "agent_id="+agent+"&limit=1").NextCursor)
	legacy := url.QueryEscape(readQueue(t, f, project, "limit=1").NextCursor)
	for _, query := range []string{"cursor=" + filtered, "agent_id=" + other + "&cursor=" + filtered, "agent_id=" + agent + "&cursor=" + legacy} {
		f.request(t, "GET", base+query, "", 422)
	}
	if len(readQueue(t, f, project, "cursor="+legacy).Items) != 1 {
		t.Fatal("unfiltered cursor compatibility changed")
	}
}

func TestRunQueueValidationWithoutDatabase(t *testing.T) {
	pool, err := pgxpool.New(t.Context(), "postgresql://test:test@127.0.0.1:1/unreachable")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	handler, err := httpapi.New(pool, httpapi.Config{ArtifactRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/projects/" + uuid.NewString() + "/run-queue?"
	paths := []string{"/api/v1/projects/bad/run-queue", base + "group=unknown", base + "limit=0", base + "limit=101", base + "limit=1.5", base + "cursor=not-a-cursor", base + "cursor=" + strings.Repeat("a", 2049), base + "q=" + url.QueryEscape(strings.Repeat("界", 201)), base + "agent_id=not-a-uuid"}
	for _, path := range paths {
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, httptest.NewRequest("GET", path, nil))
		if out.Code != 422 {
			t.Errorf("%s: %d %s", path, out.Code, out.Body.String())
		}
	}
}
