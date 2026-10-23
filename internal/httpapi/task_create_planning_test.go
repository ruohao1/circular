package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/httpapi"
)

var invalidTaskCreatePlanning = []string{
	`"status":"unknown"`,
	`"status":null`,
	`"status":false`,
	`"planned_start_date":false`,
	`"planned_start_date":""`,
	`"planned_start_date":"0000-01-01"`,
	`"planned_start_date":"2026-02-29"`,
	`"planned_start_date":"2026-1-01"`,
	`"planned_start_date":"2026-10-14T00:00:00Z"`,
	`"planned_finish_date":"infinity"`,
	`"planned_finish_date":" 2026-10-14"`,
	`"planned_start_date":"2026-10-15","planned_finish_date":"2026-10-14"`,
}

func TestTaskCreatePlanningValidationWithoutDatabase(t *testing.T) {
	p, err := pgxpool.New(t.Context(), "postgresql://test:test@127.0.0.1:1/unreachable")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	h, err := httpapi.New(p, httpapi.Config{ArtifactRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, fields := range invalidTaskCreatePlanning {
		body := fmt.Sprintf(`{"project_id":%q,"title":"Invalid",%s}`, uuid.NewString(), fields)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, httptest.NewRequest("POST", "/api/v1/tasks", strings.NewReader(body)))
		if out.Code != 422 {
			t.Errorf("body %s: %d: %s", body, out.Code, out.Body.String())
		}
	}
}

func TestTaskCreatePlanningAtomic(t *testing.T) {
	f := setup(t)
	project := f.create(t, "projects", map[string]any{"name": "Quick add"})["id"].(string)
	legacy := f.create(t, "tasks", map[string]any{"project_id": project, "title": "Legacy defaults"})
	if legacy["status"] != "open" || legacy["planned_start_date"] != nil || legacy["planned_finish_date"] != nil || legacy["revision"] != float64(1) {
		t.Fatal("changed default creation", legacy)
	}
	for _, status := range []string{"open", "ready", "in_progress", "cancelled"} {
		for i := range 2 {
			before := planningRead(t, f, project, "")
			task := f.create(t, "tasks", map[string]any{
				"project_id": project, "title": fmt.Sprintf("Quick add %s %d", status, i), "status": status,
				"planned_start_date": "2026-10-14", "planned_finish_date": "2026-10-15",
			})
			if task["status"] != status || task["planned_start_date"] != "2026-10-14" || task["planned_finish_date"] != "2026-10-15" || task["revision"] != float64(1) {
				t.Fatal("creation lost stage or dates", task)
			}
			var position int
			if err := f.pool.QueryRow(t.Context(), `SELECT board_position FROM tasks WHERE id=$1`, task["id"]).Scan(&position); err != nil {
				t.Fatal(err)
			}
			wantPosition := i
			if status == "open" {
				wantPosition++
			}
			if position != wantPosition || planningRead(t, f, project, "").BoardRevision != before.BoardRevision+1 {
				t.Fatalf("wrong append position or board revision: %d", position)
			}
		}
	}
	partial := f.create(t, "tasks", map[string]any{
		"project_id": project, "title": "Start only", "status": "ready", "planned_start_date": "0001-01-01", "planned_finish_date": nil,
	})
	if partial["planned_start_date"] != "0001-01-01" || partial["planned_finish_date"] != nil {
		t.Fatal("partial schedule changed", partial)
	}
	page := timelineRead(t, f, project, "from=2026-10-01&to=2026-10-31")
	if page.Groups[0].Total != 6 || page.Groups[1].Total != 2 {
		t.Fatal("created tasks missing from timeline", page)
	}
	before := planningRead(t, f, project, "").BoardRevision
	for _, fields := range invalidTaskCreatePlanning {
		body := fmt.Sprintf(`{"project_id":%q,"title":"Must not save",%s}`, project, fields)
		f.request(t, "POST", "/api/v1/tasks", body, 422)
	}
	var taskCount, runs int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM tasks WHERE project_id=$1`, project).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if taskCount != 10 || runs != 0 || planningRead(t, f, project, "").BoardRevision != before {
		t.Fatalf("invalid create mutated tasks, revision, or execution: tasks=%d runs=%d", taskCount, runs)
	}
	var read map[string]any
	if err := json.Unmarshal(f.request(t, "GET", "/api/v1/tasks/"+partial["id"].(string), "", 200), &read); err != nil || read["planned_start_date"] != "0001-01-01" {
		t.Fatal("created dates did not persist", read, err)
	}
}

func TestCompletionTaskCreateCannotManufactureEvidence(t *testing.T) {
	f := setup(t)
	project := f.create(t, "projects", map[string]any{"name": "Completion guard"})
	for _, stage := range []string{"in_review", "completed"} {
		raw, _ := json.Marshal(map[string]any{"project_id": project["id"], "title": "Not finished", "status": stage})
		f.request(t, "POST", "/api/v1/tasks", string(raw), 409)
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM tasks`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed creation persisted", count, err)
	}
}

func TestCompletionTaskMoveReturnsActionableConflict(t *testing.T) {
	f := setup(t)
	project := f.create(t, "projects", map[string]any{"name": "Completion move"})
	task := f.create(t, "tasks", map[string]any{"project_id": project["id"], "title": "Not finished"})
	detail := decode(t, f.request(t, "GET", "/api/v1/tasks/"+task["id"].(string)+"/planning", "", 200))
	raw, _ := json.Marshal(map[string]any{"expected_revision": 1, "expected_board_revision": detail["board_revision"], "status": "completed", "placement": "end"})
	result := decode(t, f.request(t, "POST", "/api/v1/tasks/"+task["id"].(string)+"/move", string(raw), 409))
	if result["code"] != "completion_blocked" || result["blockers"] == nil {
		t.Fatal("missing completion blocker", result)
	}
}
