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

func TestTaskScheduleStrictValidationWithoutDatabase(t *testing.T) {
	p, err := pgxpool.New(t.Context(), "postgresql://test:test@127.0.0.1:1/unreachable")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	h, err := httpapi.New(p, httpapi.Config{ArtifactRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"expected_revision":1,"planned_start_date":null}`,
		`{"expected_revision":1,"planned_finish_date":"2026-10-04"}`,
		`{"expected_revision":1,"expected_board_revision":null}`,
		`{"expected_revision":1,"expected_board_revision":1.5}`,
		`{"expected_revision":1,"expected_board_revision":9007199254740992}`,
		`{"expected_revision":1,"expected_board_revision":0}`,
		`{"expected_revision":1,"expected_board_revision":1,"planned_start_date":false}`,
		`{"expected_revision":1,"expected_board_revision":1,"planned_start_date":"2026-02-29"}`,
		`{"expected_revision":1,"expected_board_revision":1,"planned_start_date":"0000-01-01"}`,
		`{"expected_revision":1,"expected_board_revision":1,"planned_start_date":"2026-1-01"}`,
		`{"expected_revision":1,"expected_board_revision":1,"planned_start_date":"2026-10-04T00:00:00Z"}`,
		`{"expected_revision":1,"expected_board_revision":1,"planned_start_date":" 2026-10-04"}`,
		`{"expected_revision":1,"expected_board_revision":1,"planned_start_date":"infinity"}`,
		`{"expected_revision":1,"expected_board_revision":1,"planned_start_date":"2026-10-05","planned_finish_date":"2026-10-04"}`,
		`{"expected_revision":1,"expected_board_revision":1,"planned_start_date":null,"planned_start_date":null}`,
		`{"expected_revision":1,"expected_board_revision":1,"surprise":null}`,
		`{"expected_revision":1,"expected_board_revision":1} {}`,
		"{\"expected_revision\":1,\"expected_board_revision\":1,\"planned_start_date\":\"\xff\"}",
	} {
		out := httptest.NewRecorder()
		h.ServeHTTP(out, httptest.NewRequest("PATCH", "/api/v1/tasks/"+uuid.NewString(), strings.NewReader(body)))
		if out.Code != 422 {
			t.Errorf("body %q: %d: %s", body, out.Code, out.Body.String())
		}
	}
}

func TestTaskScheduleAtomicPatch(t *testing.T) {
	f := setup(t)
	project, _, id := queueFixture(t, f, "Keep title", false)
	page := planningRead(t, f, project, "")
	patch := func(body string, want int) map[string]any {
		t.Helper()
		raw := f.request(t, "PATCH", "/api/v1/tasks/"+id, body, want)
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := patch(fmt.Sprintf(`{"expected_revision":1,"expected_board_revision":%d,"title":"Scheduled","planned_start_date":"0099-12-30","planned_finish_date":"0099-12-31"}`, page.BoardRevision), 200)
	task := out["task"].(map[string]any)
	if task["planned_start_date"] != "0099-12-30" || task["planned_finish_date"] != "0099-12-31" || task["revision"] != float64(2) || out["board_revision"] != float64(page.BoardRevision+1) {
		t.Fatal(out)
	}
	board := page.BoardRevision + 1
	patch(fmt.Sprintf(`{"expected_revision":2,"expected_board_revision":%d,"title":"Must roll back","planned_start_date":"0100-01-01"}`, board), 422)
	f.create(t, "tasks", map[string]any{"project_id": project, "title": "Concurrent creation"})
	patch(fmt.Sprintf(`{"expected_revision":2,"expected_board_revision":%d,"title":"Stale","planned_start_date":null}`, board), 409)
	var detail map[string]any
	if err := json.Unmarshal(f.request(t, "GET", "/api/v1/tasks/"+id+"/planning", "", 200), &detail); err != nil {
		t.Fatal(err)
	}
	stored := detail["task"].(map[string]any)
	if stored["title"] != "Scheduled" || stored["planned_start_date"] != "0099-12-30" || stored["revision"] != float64(2) {
		t.Fatal(detail)
	}
	for _, item := range planningRead(t, f, project, "q=Scheduled").Groups[0].Items {
		if item["task"].(map[string]any)["planned_finish_date"] != "0099-12-31" {
			t.Fatal(item)
		}
	}
	out = patch(`{"expected_revision":2,"priority":"high"}`, 200)
	if out["task"].(map[string]any)["planned_start_date"] != "0099-12-30" {
		t.Fatal("old-field edit lost schedule", out)
	}
	out = patch(fmt.Sprintf(`{"expected_revision":3,"expected_board_revision":%v,"planned_start_date":null}`, out["board_revision"]), 200)
	if out["task"].(map[string]any)["planned_start_date"] != nil || out["task"].(map[string]any)["planned_finish_date"] != "0099-12-31" {
		t.Fatal("partial clear", out)
	}
	var runs int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs WHERE task_id=$1`, id).Scan(&runs); err != nil || runs != 0 {
		t.Fatal(runs, err)
	}
}
