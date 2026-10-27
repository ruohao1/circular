package httpapi_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/postgres"
)

func workJSON(t *testing.T, f fixture, method, path string, input any, status int) map[string]any {
	t.Helper()
	var body string
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		body = string(raw)
	}
	if method == "PUT" && strings.HasSuffix(path, "/completion-policy") && status == 200 {
		f.approveMutation(t, method, path, body)
		return decode(t, f.request(t, "GET", strings.TrimSuffix(path, "-policy"), "", 200))
	}
	return decode(t, f.request(t, method, path, body, status))
}
func workMove(t *testing.T, f fixture, task, stage string) {
	t.Helper()
	state := decode(t, f.request(t, "GET", "/api/v1/tasks/"+task+"/planning", "", 200))
	workJSON(t, f, "POST", "/api/v1/tasks/"+task+"/move", map[string]any{"expected_revision": state["task"].(map[string]any)["revision"], "expected_board_revision": state["board_revision"], "status": stage, "placement": "end"}, 200)
}
func workFixture(t *testing.T, mode string) (fixture, string, string, string, map[string]any) {
	t.Helper()
	f := setup(t)
	project, agent, task := queueFixture(t, f, "External work", true)
	if _, err := f.pool.Exec(t.Context(), `UPDATE projects SET description='Follow the documented roadmap' WHERE id=$1`, project); err != nil {
		t.Fatal(err)
	}
	if mode == "review_required" {
		if _, err := f.pool.Exec(t.Context(), `UPDATE repositories SET completion_check_config='{"revision":1,"checks":[{"workflow_id":"123","app_id":"15368","workflow_path":".github/workflows/ci.yml","name":"test"}]}' WHERE project_id=$1`, project); err != nil {
			t.Fatal(err)
		}
	}
	state := decode(t, f.request(t, "GET", "/api/v1/tasks/"+task, "", 200))
	workJSON(t, f, "PUT", "/api/v1/tasks/"+task+"/completion-policy", map[string]any{"expected_revision": state["revision"], "policy": map[string]any{"mode": mode, "requirements": []any{map[string]any{"key": "tests", "description": "Record validation"}}}}, 200)
	workMove(t, f, task, "ready")
	state = decode(t, f.request(t, "GET", "/api/v1/tasks/"+task, "", 200))
	return f, project, agent, task, map[string]any{"session_id": uuid.NewString(), "session_secret": strings.Repeat("ab", 32), "expected_revision": state["revision"], "client_name": "Codex", "client_version": "1"}
}
func TestTaskWorkLifecycle(t *testing.T) {
	for _, mode := range []string{"direct_completion", "review_required"} {
		t.Run(mode, func(t *testing.T) {
			f, project, agent, task, claim := workFixture(t, mode)
			path := "/api/v1/tasks/" + task + "/work"
			before := workJSON(t, f, "GET", path, nil, 200)
			if before["project"].(map[string]any)["id"] != project || before["project"].(map[string]any)["description"] != "Follow the documented roadmap" || before["owner"] != nil || before["repository"] == nil || before["task"].(map[string]any)["description"] != "Prepared requirements" || len(before["prerequisites"].([]any)) != 0 {
				t.Fatal(before)
			}
			accepted := workJSON(t, f, "POST", path+"/claim", claim, 201)
			credential := accepted["credential"].(map[string]any)
			if accepted["created"] != true || credential["session_id"] != claim["session_id"] || credential["session_secret"] != claim["session_secret"] || accepted["task_revision"] != claim["expected_revision"].(float64)+1 {
				t.Fatal(accepted)
			}
			replay := workJSON(t, f, "POST", path+"/claim", claim, 200)
			if replay["created"] != false || !reflect.DeepEqual(replay["session"], accepted["session"]) || replay["task_revision"] != accepted["task_revision"] {
				t.Fatal(replay)
			}
			owned := workJSON(t, f, "GET", path, nil, 200)
			if owned["owner"].(map[string]any)["id"] != claim["session_id"] || owned["task"].(map[string]any)["status"] != "in_progress" {
				t.Fatal(owned)
			}
			other := map[string]any{"session_id": uuid.NewString(), "session_secret": strings.Repeat("cd", 32), "expected_revision": claim["expected_revision"], "client_name": "Claude"}
			conflict := workJSON(t, f, "POST", path+"/claim", other, 409)
			if conflict["code"] != "task_owned" || conflict["owner"].(map[string]any)["id"] != claim["session_id"] || conflict["reload_path"] != path {
				t.Fatal(conflict)
			}
			workJSON(t, f, "POST", "/api/v1/runs", map[string]any{"task_id": task, "agent_id": agent, "request_key": "competing-native"}, 409)
			workJSON(t, f, "POST", "/api/v1/tasks/"+task+"/move", map[string]any{"expected_revision": accepted["task_revision"], "expected_board_revision": accepted["board_revision"], "status": "completed", "placement": "end"}, 409)
			sessionPath := "/api/v1/work-sessions/" + claim["session_id"].(string)
			beat := map[string]any{"session_secret": claim["session_secret"], "sequence": 2}
			contact := workJSON(t, f, "POST", sessionPath+"/heartbeat", beat, 200)["session"]
			beat["sequence"] = 1
			if got := workJSON(t, f, "POST", sessionPath+"/heartbeat", beat, 200)["session"]; !reflect.DeepEqual(contact, got) {
				t.Fatal("old heartbeat renewed contact", got)
			}
			finish := map[string]any{"session_secret": claim["session_secret"], "operation_id": uuid.NewString(), "outcome": "succeeded", "summary": "Validated implementation"}
			if missing := workJSON(t, f, "POST", sessionPath+"/finish", finish, 409); missing["code"] != "completion_blocked" {
				t.Fatal(missing)
			}
			finish["report"] = map[string]any{"result": "Validated implementation", "evidence": []any{}}
			if missing := workJSON(t, f, "POST", sessionPath+"/finish", finish, 422); missing["code"] != "invalid_completion_report" {
				t.Fatal(missing)
			}
			if got := workJSON(t, f, "GET", path, nil, 200); got["owner"] == nil || got["task"].(map[string]any)["status"] != "in_progress" {
				t.Fatal("invalid finish changed ownership", got)
			}
			finish["report"] = map[string]any{"result": "Validated implementation", "evidence": []any{map[string]any{"key": "tests", "detail": "Go tests passed", "links": []string{"https://example.com/checks/1"}}}}
			done := workJSON(t, f, "POST", sessionPath+"/finish", finish, 200)
			again := workJSON(t, f, "POST", sessionPath+"/finish", finish, 200)
			if !reflect.DeepEqual(done["receipt"], again["receipt"]) || done["task_revision"] != again["task_revision"] {
				t.Fatal("finish was not replayed", done, again)
			}
			finished := workJSON(t, f, "GET", path, nil, 200)
			wantStage := "completed"
			if mode == "review_required" {
				wantStage = "in_review"
			}
			if finished["owner"] != nil || finished["task"].(map[string]any)["status"] != wantStage || finished["completion"].(map[string]any)["report"] == nil {
				t.Fatal(finished)
			}
			workJSON(t, f, "POST", path+"/claim", claim, 200)
			if lost := workJSON(t, f, "POST", sessionPath+"/heartbeat", beat, 409); lost["code"] != "work_claim_lost" {
				t.Fatal(lost)
			}
			history := workJSON(t, f, "GET", sessionPath, nil, 200)
			if len(history["receipts"].([]any)) != 2 || history["session"].(map[string]any)["outcome"] != "succeeded" {
				t.Fatal(history)
			}
			for _, value := range []any{owned, conflict, contact, done, finished, history} {
				raw, _ := json.Marshal(value)
				for _, forbidden := range []string{claim["session_secret"].(string), "secret_digest", "admission_digest", "session_secret"} {
					if strings.Contains(string(raw), forbidden) {
						t.Fatal("credential leaked", forbidden)
					}
				}
			}
			var runs, sessions int
			if err := f.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM runs),(SELECT count(*) FROM work_sessions)`).Scan(&runs, &sessions); err != nil || runs != 0 || sessions != 1 {
				t.Fatal(runs, sessions, err)
			}
		})
	}
}
func TestTaskWorkReleaseFencesOldOwner(t *testing.T) {
	f, _, _, task, claim := workFixture(t, "direct_completion")
	path := "/api/v1/tasks/" + task + "/work"
	workJSON(t, f, "POST", path+"/claim", claim, 201)
	release := map[string]any{"expected_session_id": claim["session_id"], "operation_id": uuid.NewString(), "reason": "User confirmed the old client stopped"}
	old := workJSON(t, f, "POST", path+"/release", release, 200)
	workMove(t, f, task, "ready")
	revision := decode(t, f.request(t, "GET", "/api/v1/tasks/"+task, "", 200))["revision"]
	replacement := map[string]any{"session_id": uuid.NewString(), "session_secret": strings.Repeat("cd", 32), "expected_revision": revision, "client_name": "Claude"}
	workJSON(t, f, "POST", path+"/claim", replacement, 201)
	sessionPath := "/api/v1/work-sessions/" + claim["session_id"].(string)
	workJSON(t, f, "POST", sessionPath+"/heartbeat", map[string]any{"session_secret": claim["session_secret"], "sequence": 1}, 409)
	workJSON(t, f, "POST", sessionPath+"/finish", map[string]any{"session_secret": claim["session_secret"], "operation_id": uuid.NewString(), "outcome": "failed", "summary": "Old work"}, 409)
	replay := workJSON(t, f, "POST", path+"/release", release, 200)
	if !reflect.DeepEqual(old["receipt"], replay["receipt"]) {
		t.Fatal(replay)
	}
	workJSON(t, f, "POST", path+"/claim", claim, 200)
	release["operation_id"] = uuid.NewString()
	if lost := workJSON(t, f, "POST", path+"/release", release, 409); lost["code"] != "work_claim_lost" {
		t.Fatal(lost)
	}
	if current := workJSON(t, f, "GET", path, nil, 200)["owner"].(map[string]any); current["id"] != replacement["session_id"] {
		t.Fatal(current)
	}
}
func TestTaskWorkAdmissionAndCredentialErrors(t *testing.T) {
	f, project, _, task, claim := workFixture(t, "direct_completion")
	path := "/api/v1/tasks/" + task + "/work"
	prerequisite := f.create(t, "tasks", map[string]any{"project_id": project, "title": "Unfinished prerequisite"})["id"].(string)
	state := decode(t, f.request(t, "GET", "/api/v1/tasks/"+task, "", 200))
	updated := decode(t, f.approvePlanning(t, "/api/v1/tasks/"+task, fmt.Sprintf(`{"expected_revision":%v,"prerequisite_task_ids":[%q]}`, state["revision"], prerequisite)))
	claim["expected_revision"] = updated["task"].(map[string]any)["revision"]
	view := workJSON(t, f, "GET", path, nil, 200)
	dependency := view["prerequisites"].([]any)[0].(map[string]any)
	if dependency["id"] != prerequisite || dependency["status"] != "open" {
		t.Fatal(dependency)
	}
	blocked := workJSON(t, f, "POST", path+"/claim", claim, 422)
	if blocked["code"] != "task_not_ready" || blocked["blocking_task_ids"].([]any)[0] != prerequisite {
		t.Fatal(blocked)
	}
	// Historical completed prerequisite; creation through today's API forbids an unproven Done.
	if _, err := f.pool.Exec(t.Context(), `UPDATE tasks SET status='completed' WHERE id=$1`, prerequisite); err != nil {
		t.Fatal(err)
	}
	claim["expected_revision"] = float64(1)
	if stale := workJSON(t, f, "POST", path+"/claim", claim, 409); stale["code"] != "task_revision_conflict" {
		t.Fatal(stale)
	}
	claim["expected_revision"] = updated["task"].(map[string]any)["revision"]
	workJSON(t, f, "POST", path+"/claim", claim, 201)
	claim["client_name"] = "Changed identity"
	if changed := workJSON(t, f, "POST", path+"/claim", claim, 409); changed["code"] != "work_replay_conflict" {
		t.Fatal(changed)
	}
	claim["session_secret"] = strings.Repeat("cd", 32)
	if wrong := workJSON(t, f, "POST", path+"/claim", claim, 403); wrong["code"] != "work_credential_invalid" || strings.Contains(fmt.Sprint(wrong), claim["session_secret"].(string)) {
		t.Fatal(wrong)
	}
	sessionPath := "/api/v1/work-sessions/" + claim["session_id"].(string)
	workJSON(t, f, "POST", sessionPath+"/heartbeat", map[string]any{"session_secret": claim["session_secret"], "sequence": 1}, 403)
	workJSON(t, f, "POST", sessionPath+"/finish", map[string]any{"session_secret": claim["session_secret"], "operation_id": uuid.NewString(), "outcome": "failed", "summary": "Denied"}, 403)
}
func TestTaskWorkStrictBoundary(t *testing.T) {
	f, _, _, task, claim := workFixture(t, "direct_completion")
	path := "/api/v1/tasks/" + task + "/work/claim"
	raw, _ := json.Marshal(claim)
	for _, body := range []string{`null`, `[]`, `{} {}`, `{"session_id":null}`, strings.TrimSuffix(string(raw), "}") + `,"client_name":"duplicate"}`, strings.TrimSuffix(string(raw), "}") + `,"task_id":"` + uuid.NewString() + `"}`} {
		f.request(t, "POST", path, body, 422)
	}
	for _, secret := range []string{"", "ab", strings.Repeat("AB", 32), strings.Repeat("z", 64)} {
		claim["session_secret"] = secret
		workJSON(t, f, "POST", path, claim, 422)
	}
	for _, tc := range []struct {
		origin, content string
		status          int
	}{{"https://evil.invalid", "application/json", 403}, {"", "text/plain", 415}} {
		req, _ := http.NewRequestWithContext(t.Context(), "POST", f.server.URL+path, strings.NewReader(string(raw)))
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Content-Type", tc.content)
		res, err := f.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.status || res.Header.Get("Cache-Control") != "no-store" {
			t.Fatal(res.StatusCode, res.Header)
		}
	}
	f.request(t, "POST", path, strings.Repeat(" ", 128*1024+1), 413)
	res, err := f.server.Client().Get(f.server.URL + "/api/v1/tasks/" + task + "/work")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.Header.Get("Cache-Control") != "no-store" || decode(t, data)["owner"] != nil {
		t.Fatal(res.Header, string(data))
	}
}

func TestTaskWorkNonSuccessAndFinishReplayAfterReopen(t *testing.T) {
	for _, outcome := range []string{"succeeded", "failed", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			f, _, _, task, claim := workFixture(t, "direct_completion")
			path := "/api/v1/tasks/" + task + "/work"
			workJSON(t, f, "POST", path+"/claim", claim, 201)
			finish := map[string]any{"session_secret": claim["session_secret"], "operation_id": uuid.NewString(), "outcome": outcome, "summary": "Attempt finished"}
			if outcome == "succeeded" {
				finish["report"] = map[string]any{"result": "Attempt finished", "evidence": []any{map[string]any{"key": "tests", "detail": "Tests passed"}}}
			}
			sessionPath := "/api/v1/work-sessions/" + claim["session_id"].(string)
			result := workJSON(t, f, "POST", sessionPath+"/finish", finish, 200)
			state := workJSON(t, f, "GET", path, nil, 200)
			stage := "in_progress"
			if outcome == "succeeded" {
				stage = "completed"
			}
			if state["owner"] != nil || state["task"].(map[string]any)["status"] != stage {
				t.Fatal(state)
			}
			workMove(t, f, task, "ready")
			replay := workJSON(t, f, "POST", sessionPath+"/finish", finish, 200)
			if !reflect.DeepEqual(result["receipt"], replay["receipt"]) {
				t.Fatal(replay)
			}
			if state := workJSON(t, f, "GET", path, nil, 200); state["task"].(map[string]any)["status"] != "ready" || state["owner"] != nil {
				t.Fatal("replay changed reopened task", state)
			}
			finish["summary"] = "Changed report"
			if got := workJSON(t, f, "POST", sessionPath+"/finish", finish, 409); got["code"] != "work_replay_conflict" {
				t.Fatal(got)
			}
		})
	}
}
func TestTaskWorkRequiresCompletionPreparation(t *testing.T) {
	for _, mode := range []string{"direct_completion", "review_required"} {
		t.Run(mode, func(t *testing.T) {
			f, _, _, task, claim := workFixture(t, mode)
			if mode == "direct_completion" {
				if _, err := f.pool.Exec(t.Context(), `UPDATE tasks SET completion_policy=NULL WHERE id=$1`, task); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.pool.Exec(t.Context(), `UPDATE repositories SET completion_check_config=NULL WHERE id=(SELECT repository_id FROM tasks WHERE id=$1)`, task); err != nil {
					t.Fatal(err)
				}
			}
			path := "/api/v1/tasks/" + task + "/work"
			if got := workJSON(t, f, "POST", path+"/claim", claim, 422); got["code"] != "task_not_ready" {
				t.Fatal(got)
			}
			if got := workJSON(t, f, "GET", path, nil, 200); got["owner"] != nil || got["task"].(map[string]any)["status"] != "ready" {
				t.Fatal(got)
			}
		})
	}
}
func TestTaskWorkNativeReleaseWaitsForCleanup(t *testing.T) {
	f, _, agent, task, claim := workFixture(t, "direct_completion")
	run := f.create(t, "runs", map[string]any{"task_id": task, "agent_id": agent, "request_key": "native"})
	acquired, err := postgres.NewQueue(f.pool).Acquire(t.Context(), "native-work-test")
	if err != nil || acquired == nil || acquired.RunID.String() != run["id"] {
		t.Fatal(acquired, err)
	}
	path := "/api/v1/tasks/" + task + "/work"
	owner := workJSON(t, f, "GET", path, nil, 200)["owner"].(map[string]any)
	release := map[string]any{"expected_session_id": owner["id"], "operation_id": uuid.NewString(), "reason": "User requested cancellation"}
	result := workJSON(t, f, "POST", path+"/release", release, 200)
	if result["pending_cleanup"] != true || result["session"].(map[string]any)["released_at"] != nil {
		t.Fatal(result)
	}
	workJSON(t, f, "POST", path+"/claim", claim, 409)
	replay := workJSON(t, f, "POST", path+"/release", release, 200)
	if !reflect.DeepEqual(result["receipt"], replay["receipt"]) {
		t.Fatal(replay)
	}
}
