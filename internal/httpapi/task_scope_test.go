package httpapi_test

import (
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/httpapi"
)

func taskScopeFixture(t *testing.T, f fixture) (string, string) {
	t.Helper()
	project := f.create(t, "projects", map[string]any{"name": "Scopes"})["id"].(string)
	agent := f.create(t, "agents", map[string]any{"project_id": project, "name": "Preferred"})["id"].(string)
	repository := f.create(t, "repositories", map[string]any{"project_id": project, "name": "Source", "clone_url": "/fixture/source"})["id"].(string)
	// Each stage has two in-window Tasks, one unscheduled Task, and one outside the window.
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO tasks(id,project_id,title,description,status,external_refs,priority,preferred_agent_id,repository_id,planned_start_date,planned_finish_date)
 SELECT gen_random_uuid(),$1,'Scope '||status,'',status,'{}','high',$2,$3,
 CASE WHEN n<3 THEN DATE '2026-10-01' WHEN n=4 THEN DATE '2026-11-01' END,
 CASE WHEN n<3 THEN DATE '2026-10-02' WHEN n=4 THEN DATE '2026-11-02' END
 FROM unnest(ARRAY['open','ready','in_progress','in_review','completed','cancelled']) status CROSS JOIN generate_series(1,4) n`, project, agent, repository); err != nil {
		t.Fatal(err)
	}
	for _, override := range []map[string]any{{"priority": "low"}, {"title": "Unrelated"}, {"preferred_agent_id": nil}, {"repository_id": nil}} {
		body := map[string]any{"project_id": project, "title": "Scope distraction", "priority": "high", "preferred_agent_id": agent, "repository_id": repository}
		for key, value := range override {
			body[key] = value
		}
		id := f.create(t, "tasks", body)["id"]
		if _, err := f.pool.Exec(t.Context(), `UPDATE tasks SET status='ready',planned_start_date='2026-10-01',planned_finish_date='2026-10-02' WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
	foreign := f.create(t, "projects", map[string]any{"name": "Other"})["id"]
	f.create(t, "tasks", map[string]any{"project_id": foreign, "title": "Scope foreign", "priority": "high"})
	return project, "q=Scope&priority=high&preferred_agent_id=" + agent + "&repository_id=" + repository
}

func TestTaskPlanningPresetScopes(t *testing.T) {
	f := setup(t)
	project, filters := taskScopeFixture(t, f)
	for _, test := range []struct {
		query string
		want  []string
	}{
		{"", []string{"open", "ready", "in_progress", "in_review", "completed"}},
		{"scope=all", []string{"open", "ready", "in_progress", "in_review", "completed", "cancelled"}},
		{"scope=active", []string{"ready", "in_progress", "in_review"}},
		{"scope=backlog", []string{"open"}},
		{"scope=active&status=ready", []string{"ready"}},
		{"scope=all&status=cancelled", []string{"cancelled"}},
		{"status=cancelled", []string{"cancelled"}},
		{"scope=active&status=cancelled", []string{}},
		{"scope=backlog&status=ready", []string{}},
	} {
		t.Run(test.query, func(t *testing.T) {
			page := planningRead(t, f, project, filters+"&limit=1&"+test.query)
			statuses := []string{}
			for _, group := range page.Groups {
				statuses = append(statuses, group.Status)
				if group.Total != 4 || len(group.Items) != 1 || group.NextCursor == "" || group.Items[0]["task"].(map[string]any)["status"] != group.Status {
					t.Fatalf("incorrect filtered stage: %+v", group)
				}
			}
			if !slices.Equal(statuses, test.want) {
				t.Fatalf("stages %v, want %v", statuses, test.want)
			}
		})
	}
	base := filters + "&scope=active&status=ready&limit=1"
	first := planningRead(t, f, project, base)
	cursor := url.QueryEscape(first.Groups[0].NextCursor)
	next := planningRead(t, f, project, base+"&cursor="+cursor)
	if len(next.Groups) != 1 || next.Groups[0].Total != 4 || len(next.Groups[0].Items) != 1 || next.Groups[0].Items[0]["task"].(map[string]any)["id"] == first.Groups[0].Items[0]["task"].(map[string]any)["id"] {
		t.Fatal("incorrect scoped page", next)
	}
	for _, query := range []string{strings.Replace(base, "scope=active", "scope=all", 1), strings.Replace(base, "&scope=active", "", 1), strings.Replace(base, "priority=high", "priority=low", 1)} {
		f.request(t, "GET", "/api/v1/projects/"+project+"/task-planning?"+query+"&cursor="+cursor, "", 422)
	}
	f.create(t, "tasks", map[string]any{"project_id": project, "title": "Revision change"})
	f.request(t, "GET", "/api/v1/projects/"+project+"/task-planning?"+base+"&cursor="+cursor, "", 409)
}

func TestTaskTimelinePresetScopes(t *testing.T) {
	f := setup(t)
	project, filters := taskScopeFixture(t, f)
	base := filters + "&from=2026-10-01&to=2026-10-31"
	for _, test := range []struct {
		query                           string
		statuses                        []string
		scheduled, unscheduled, outside int
	}{
		{"", []string{"open", "ready", "in_progress", "in_review", "completed"}, 10, 5, 5},
		{"scope=all", []string{"open", "ready", "in_progress", "in_review", "completed", "cancelled"}, 12, 6, 6},
		{"scope=active", []string{"ready", "in_progress", "in_review"}, 6, 3, 3},
		{"scope=backlog", []string{"open"}, 2, 1, 1},
		{"scope=active&status=ready", []string{"ready"}, 2, 1, 1},
		{"scope=all&status=cancelled", []string{"cancelled"}, 2, 1, 1},
		{"status=cancelled", []string{"cancelled"}, 2, 1, 1},
		{"scope=active&status=cancelled", []string{}, 0, 0, 0},
		{"scope=backlog&status=ready", []string{}, 0, 0, 0},
	} {
		t.Run(test.query, func(t *testing.T) {
			page := timelineRead(t, f, project, base+"&limit=1&"+test.query)
			if len(page.Groups) != 2 || page.Groups[0].Total != test.scheduled || page.Groups[1].Total != test.unscheduled || page.OutsideWindowTotal != test.outside {
				t.Fatalf("incorrect scoped totals: %+v", page)
			}
			for _, group := range page.Groups {
				if len(group.Items) != min(group.Total, 1) || (group.NextCursor != "") != (group.Total > 1) {
					t.Fatalf("incorrect scoped page: %+v", group)
				}
				for _, item := range group.Items {
					if !slices.Contains(test.statuses, item["task"].(map[string]any)["status"].(string)) {
						t.Fatal("Task outside preset", item)
					}
				}
			}
		})
	}
	for _, section := range []string{"scheduled", "unscheduled"} {
		query := base + "&scope=active&section=" + section + "&limit=1"
		first := timelineRead(t, f, project, query)
		cursor := url.QueryEscape(first.Groups[0].NextCursor)
		next := timelineRead(t, f, project, query+"&cursor="+cursor)
		if len(next.Groups) != 1 || len(next.Groups[0].Items) != 1 || next.Groups[0].Items[0]["task"].(map[string]any)["id"] == first.Groups[0].Items[0]["task"].(map[string]any)["id"] {
			t.Fatal("incorrect scoped page", next)
		}
		for _, changed := range []string{strings.Replace(query, "scope=active", "scope=all", 1), strings.Replace(query, "&scope=active", "", 1), query + "&status=ready"} {
			f.request(t, "GET", "/api/v1/projects/"+project+"/task-timeline?"+changed+"&cursor="+cursor, "", 422)
		}
		f.create(t, "tasks", map[string]any{"project_id": project, "title": "Revision change"})
		f.request(t, "GET", "/api/v1/projects/"+project+"/task-timeline?"+query+"&cursor="+cursor, "", 409)
	}
}

func TestTaskPresetScopeValidationWithoutDatabase(t *testing.T) {
	pool, err := pgxpool.New(t.Context(), "postgresql://test:test@127.0.0.1:1/unreachable")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h, err := httpapi.New(pool, httpapi.Config{ArtifactRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"task-planning", "task-timeline"} {
		for _, scope := range []string{"unknown", "Active"} {
			path := "/api/v1/projects/" + uuid.NewString() + "/" + endpoint + "?scope=" + scope + "&from=2026-10-01&to=2026-10-31"
			response := httptest.NewRecorder()
			h.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
			if response.Code != 422 {
				t.Errorf("%s: %d, want 422: %s", path, response.Code, response.Body.String())
			}
		}
	}
}
