package controlmcp_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/controlmcp"
	"github.com/ruohao1/circular/internal/httpapi"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestTaskScheduleMCPPresenceAndConditionalRevision(t *testing.T) {
	p := testsupport.Database(t)
	h, err := httpapi.New(p, httpapi.Config{ArtifactRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(h)
	defer s.Close()
	c := connect(t, controlmcp.Config{APIURL: s.URL})
	project := call(t, c, "create_project", object{"name": "Schedule"})["project"].(object)["id"]
	task := createApprovedTask(t, p, c, object{"project_id": project, "title": "Schedule"})
	detail := call(t, c, "get_task_planning", object{"task_id": task["id"]})
	out := call(t, c, "update_task", object{"task_id": task["id"], "expected_revision": 1, "expected_board_revision": detail["board_revision"], "planned_start_date": "2026-10-04", "planned_finish_date": "2026-10-06"})
	if out["task"].(object)["planned_finish_date"] != "2026-10-06" {
		t.Fatal(out)
	}
	out = approveTaskEdit(t, p, c, object{"task_id": task["id"], "expected_revision": 2, "title": "Renamed"})
	if out["task"].(object)["planned_start_date"] != "2026-10-04" {
		t.Fatal("omission cleared date", out)
	}
	rejects(t, c, "update_task", object{"task_id": task["id"], "expected_revision": 3, "planned_start_date": nil}, "expected_board_revision")
	out = call(t, c, "update_task", object{"task_id": task["id"], "expected_revision": 3, "expected_board_revision": out["board_revision"], "planned_start_date": nil})
	if out["task"].(object)["planned_start_date"] != nil || out["task"].(object)["planned_finish_date"] != "2026-10-06" {
		t.Fatal("null or omission lost", out)
	}
}

func TestTaskScheduleMCPOlderAPIAndConflictMessages(t *testing.T) {
	for _, status := range []int{422, 409, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var writes atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writes.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				if status == 422 {
					_, _ = w.Write([]byte(`{"detail":[{"type":"json_invalid","msg":"One JSON object with unique, known fields is required"}]}`))
				} else if status == 409 {
					_, _ = w.Write([]byte(`{"detail":"Task changed; reload before trying again"}`))
				} else {
					_, _ = w.Write([]byte(`{"detail":"Task not found"}`))
				}
			}))
			defer s.Close()
			c := connect(t, controlmcp.Config{APIURL: s.URL})
			result, err := c.CallTool(t.Context(), &mcp.CallToolParams{Name: "update_task", Arguments: object{"task_id": uuid.NewString(), "expected_revision": 1, "expected_board_revision": 2, "planned_start_date": "2026-10-04"}})
			if err != nil || !result.IsError {
				t.Fatal(result, err)
			}
			var message string
			for _, content := range result.Content {
				if text, ok := content.(*mcp.TextContent); ok {
					message += text.Text
				}
			}
			if status == 422 && !strings.Contains(message, "upgrade") {
				t.Fatal("unsupported dates need upgrade guidance", message)
			}
			if status == 409 && (!strings.Contains(message, "reload") || strings.Contains(message, "upgrade")) {
				t.Fatal("conflict should request reload, not upgrade", message)
			}
			if status == 404 && (!strings.Contains(message, "not found") || strings.Contains(message, "upgrade")) {
				t.Fatal("missing Task is not an unsupported API", message)
			}
			if writes.Load() != 1 {
				t.Fatal("must not retry a reduced body", writes.Load())
			}
		})
	}
}

func TestTaskTimelineMCPPagination(t *testing.T) {
	var requests atomic.Int32
	project := uuid.NewString()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Path != "/api/v1/projects/"+project+"/task-timeline" || q.Get("from") != "2026-10-01" || q.Get("to") != "2026-10-31" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		if q.Get("q") == "large" {
			_, _ = w.Write([]byte(`{"large":"` + strings.Repeat("x", 513<<10) + `"}`))
			return
		}
		want := "20"
		if q.Get("section") != "" {
			want = "100"
		}
		if q.Get("limit") != want {
			t.Errorf("limit = %q, want %s", q.Get("limit"), want)
		}
		if q.Get("section") != "" && (q.Get("section") != "scheduled" || q.Get("cursor") != "opaque" || q.Get("status") != "ready") {
			t.Errorf("lost scope: %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"board_revision":3,"from":"2026-10-01","to":"2026-10-31","outside_window_total":1,"groups":[{"section":"scheduled","total":21,"items":[],"next_cursor":"opaque"}]}`))
	}))
	defer s.Close()
	c := connect(t, controlmcp.Config{APIURL: s.URL, ReadOnly: true})
	in := object{"project_id": project, "from": "2026-10-01", "to": "2026-10-31"}
	out := call(t, c, "list_task_timeline", in)
	if out["board_revision"] != float64(3) || out["groups"].([]any)[0].(object)["next_cursor"] != "opaque" {
		t.Fatal(out)
	}
	in["limit"], in["section"], in["cursor"], in["status"] = 100, "scheduled", "opaque", "ready"
	call(t, c, "list_task_timeline", in)
	rejects(t, c, "list_task_timeline", object{"project_id": project, "from": "2026-10-01"}, "to")
	in["limit"] = 101
	rejects(t, c, "list_task_timeline", in, "limit")
	if requests.Load() != 2 {
		t.Fatal("invalid input reached API", requests.Load())
	}
	in["limit"], in["q"] = 100, "large"
	rejects(t, c, "list_task_timeline", in, "smaller page limit")
}
