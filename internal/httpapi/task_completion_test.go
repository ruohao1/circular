package httpapi_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/controlmcp"
	"github.com/ruohao1/circular/internal/httpapi"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestCompletionHTTPPolicyFreezeAndMoveParity(t *testing.T) {
	f := setup(t)
	project := f.create(t, "projects", map[string]any{"name": "Completion"})["id"].(string)
	repo := f.create(t, "repositories", map[string]any{"project_id": project, "name": "Docs", "clone_url": "/fixture"})["id"]
	task := f.create(t, "tasks", map[string]any{"project_id": project, "title": "Document decision", "repository_id": repo, "description": "Record the decision and reasons", "status": "ready"})["id"].(string)
	path := "/api/v1/tasks/" + task
	if _, err := f.pool.Exec(t.Context(), `UPDATE tasks SET completion_policy=NULL WHERE id=$1`, task); err != nil {
		t.Fatal(err)
	}
	var v completion.View
	if err := json.Unmarshal(f.request(t, "GET", path+"/completion", "", 200), &v); err != nil || v.Policy != nil {
		t.Fatal(v, err)
	}
	f.approveMutation(t, "PUT", path+"/completion-policy", `{"expected_revision":1,"policy":{"mode":"direct_completion","requirements":[{"key":"result","description":"Decision with reasons"}]}}`)
	for _, body := range []string{
		`{"expected_revision":2,"policy":{"mode":"direct_completion","requirements":[],"force":true}}`,
		`{"expected_revision":2,"policy":{"mode":"direct_completion","mode":"review_required"}}`,
		`{"expected_revision":2,"policy":{"mode":"invalid"}}`,
		`{"expected_revision":2,"policy":{"mode":"direct_completion"},"actor":"agent"}`,
	} {
		f.request(t, "PUT", path+"/completion-policy", body, 422)
	}
	f.request(t, "PUT", path+"/completion-policy", strings.Repeat(" ", 128<<10+1), 413)
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	secret := bytes.Repeat([]byte{72}, 32)
	session := uuid.New()
	_, err = postgres.ClaimExternalWork(t.Context(), tx, postgres.ExternalWorkClaim{TaskID: uuid.MustParse(task), SessionID: session, ExpectedRevision: 2, Secret: secret, ClientName: "Fixture coder"}, time.Now().UTC())
	if err != nil {
		tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var revision, board int64
	if err = f.pool.QueryRow(t.Context(), `SELECT t.revision,p.task_board_revision FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.id=$1`, task).Scan(&revision, &board); err != nil {
		t.Fatal(err)
	}
	move := fmt.Sprintf(`{"expected_revision":%d,"expected_board_revision":%d,"status":"completed","placement":"end"}`, revision, board)
	blocked := decode(t, f.request(t, "POST", path+"/move", move, 409))
	if blocked["code"] != "completion_blocked" || blocked["blockers"].([]any)[0].(map[string]any)["code"] != "owner_active" {
		t.Fatal(blocked)
	}
	server, err := controlmcp.New(controlmcp.Config{APIURL: f.server.URL})
	if err != nil {
		t.Fatal(err)
	}
	left, right := mcp.NewInMemoryTransports()
	connection, err := server.Connect(t.Context(), left, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var args map[string]any
	if err = json.Unmarshal([]byte(move), &args); err != nil {
		t.Fatal(err)
	}
	args["task_id"] = task
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "move_task", Arguments: args})
	raw, _ := json.Marshal(result)
	if err != nil || !result.IsError || !strings.Contains(string(raw), "owner_active") {
		t.Fatal(string(raw), err)
	}
	f.request(t, "PUT", path+"/completion-policy", fmt.Sprintf(`{"expected_revision":%d,"policy":{"mode":"review_required","requirements":[]}}`, revision), 409)
	tx, err = f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = postgres.FinishExternalWork(t.Context(), tx, postgres.WorkFinishInput{Credential: postgres.WorkCredential{SessionID: session, Secret: secret}, OperationID: uuid.New(), Outcome: "succeeded", Report: &completion.Report{Result: "Decision recorded", Evidence: []completion.Evidence{{Key: "result", Detail: "Reasons documented"}}}}, time.Now().UTC())
	if err != nil {
		tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	data := f.request(t, "GET", path+"/completion", "", 200)
	if err = json.Unmarshal(data, &v); err != nil || !v.Verdict.CanComplete || v.Stage != "completed" || v.Contract == nil {
		t.Fatal(v, err)
	}
	for _, secretField := range []string{"secret", "credential", "access_token", "client_name"} {
		if strings.Contains(string(data), secretField) {
			t.Fatal("private session field exposed", secretField)
		}
	}
	read := decode(t, f.request(t, "GET", path, "", 200))
	if read["completion_policy"] == nil || read["completion_contract"] == nil {
		t.Fatal("planning read omitted frozen policy", read)
	}
}

func TestCompletionHTTPDoneRefreshAllowsNewClaimAndRejectsOldMove(t *testing.T) {
	pool := testsupport.Database(t)
	source := testsupport.SeedExternalPRReviewSource(t, pool, uuid.Nil)
	provider := testsupport.NewProviderFixture()
	provider.SetReviewPR(source.PR, "open")
	provider.CompletionChecksEnabled.Store(true)
	var changed atomic.Bool
	nextSession := uuid.New()
	failures := make(chan error, 1)
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pulls/1") && changed.CompareAndSwap(false, true) {
			err := func() error {
				ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
				defer cancel()
				tx, err := pool.Begin(ctx)
				if err != nil {
					return err
				}
				defer tx.Rollback(ctx)
				var revision, board int64
				if err = tx.QueryRow(ctx, `SELECT t.revision,p.task_board_revision FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.id=$1`, source.TaskID).Scan(&revision, &board); err != nil {
					return err
				}
				moved, err := postgres.MoveTask(ctx, tx, source.TaskID, postgres.TaskMoveInput{ExpectedRevision: revision, ExpectedBoardRevision: board, Status: "ready", Placement: "end"})
				if err != nil {
					return err
				}
				if _, err = postgres.ClaimExternalWork(ctx, tx, postgres.ExternalWorkClaim{TaskID: source.TaskID, SessionID: nextSession, ExpectedRevision: moved.Revision, Secret: bytes.Repeat([]byte{88}, 32), ClientName: "New coder"}, time.Now().UTC()); err != nil {
					return err
				}
				return tx.Commit(ctx)
			}()
			failures <- err
		}
		provider.ServeHTTP(w, r)
	}))
	defer external.Close()
	root := t.TempDir()
	key := filepath.Join(root, "github.pem")
	if err := os.WriteFile(key, provider.GitHubPrivateKey(), 0600); err != nil {
		t.Fatal(err)
	}
	config := integrations.Config{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), GitHub: integrations.OAuthApp{ClientID: "fixture-github", ClientSecret: "fixture-github-secret"}, GitHubPrivateKeyFile: key, GitHubURL: external.URL, GitHubAPIURL: external.URL, ArtifactRoot: root}
	service, err := integrations.New(pool, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.SaveGitHubIdentity(t.Context(), source.ProjectID.String(), nil); err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.New(pool, httpapi.Config{ArtifactRoot: root, Integrations: config})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	f := fixture{server, pool, root}
	var revision, board int64
	if err = pool.QueryRow(t.Context(), `SELECT t.revision,p.task_board_revision FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.id=$1`, source.TaskID).Scan(&revision, &board); err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", "/api/v1/tasks/"+source.TaskID.String()+"/move", fmt.Sprintf(`{"expected_revision":%d,"expected_board_revision":%d,"status":"completed","placement":"end"}`, revision, board), 409)
	select {
	case err := <-failures:
		if err != nil {
			t.Fatal("provider I/O held planning locks", err)
		}
	default:
		t.Fatal("Done did not query provider")
	}
	var current uuid.UUID
	var stage string
	if err = pool.QueryRow(t.Context(), `SELECT completion_session_id,status FROM tasks WHERE id=$1`, source.TaskID).Scan(&current, &stage); err != nil || current != nextSession || stage != "in_progress" {
		t.Fatal(current, stage, err)
	}
}

func TestCompletionHTTPBoundariesAndDoneRefresh(t *testing.T) {
	f := setup(t)
	snap := testsupport.SeedExternalPRReviewSource(t, f.pool, uuid.Nil)
	path := "/api/v1/tasks/" + snap.TaskID.String()
	f.request(t, "GET", path+"/pr-reviews", "", 200)
	f.request(t, "GET", path+"/pr-reviews/prepare", "", 200)
	for _, body := range []string{`{"number":1,"url":"https://evil.invalid","session_id":"` + snap.Source.WorkSessionID.String() + `","expected_revision":1}`, `{"number":-1}`, `{"number":1,"project_id":"` + uuid.NewString() + `"}`, `{"number":1,"actor_id":"` + uuid.NewString() + `"}`} {
		f.request(t, "PUT", path+"/pull-request", body, 422)
	}
	f.request(t, "POST", path+"/completion/refresh", `{"report":{"result":"green"}}`, 422)
	f.request(t, "POST", path+"/pr-reviews", `{"request_key":"`+uuid.NewString()+`","mode":"normal","expected_input_fingerprint":"`+strings.Repeat("a", 64)+`","source_run_id":"`+uuid.NewString()+`"}`, 422)
	repoPath := "/api/v1/repositories/" + snap.RepositoryID.String() + "/completion-checks"
	f.request(t, "GET", repoPath, "", 200)
	f.request(t, "PUT", repoPath, `{"expected_revision":1,"checks":[],"app_id":"999"}`, 422)
	if _, err := f.pool.Exec(t.Context(), `UPDATE task_completion_evidence SET observation='{"error_code":"sentinel"}' WHERE session_id=$1`, snap.Source.WorkSessionID); err != nil {
		t.Fatal(err)
	}
	var revision, board int64
	if err := f.pool.QueryRow(t.Context(), `SELECT t.revision,p.task_board_revision FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.id=$1`, snap.TaskID).Scan(&revision, &board); err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", path+"/move", fmt.Sprintf(`{"expected_revision":%d,"expected_board_revision":%d,"status":"completed","placement":"end"}`, revision, board), 409)
	v := decode(t, f.request(t, "GET", path+"/completion", "", 200))
	if v["observation"].(map[string]any)["error_code"] != "provider_unavailable" {
		t.Fatal("Done did not force provider refresh", v)
	}
}
