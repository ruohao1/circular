package httpapi_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/controlmcp"
	"github.com/ruohao1/circular/internal/httpapi"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestPRReviewHTTPDefaultsAndCannotForgePurpose(t *testing.T) {
	f := setup(t)
	project := f.create(t, "projects", map[string]any{"name": "PR review API"})
	path := "/api/v1/projects/" + project["id"].(string) + "/integrations/github/pr-reviews"
	settings := decode(t, f.request(t, "GET", path, "", 200))
	if settings["automatic"] != false {
		t.Fatal("automatic by default")
	}
	agent := f.create(t, "agents", map[string]any{"project_id": project["id"], "name": "Coder"})
	task := f.create(t, "tasks", map[string]any{"project_id": project["id"], "title": "Source task"})
	run := f.create(t, "runs", map[string]any{"task_id": task["id"], "agent_id": agent["id"], "kind": "pr_review"})
	if run["kind"] != "coding" {
		t.Fatal("forged review purpose")
	}
	source := "/api/v1/runs/" + run["id"].(string) + "/pr-reviews"
	f.request(t, "POST", source, `{}`, 422)
	f.request(t, "GET", source+"?limit=101", "", 422)
	f.request(t, "GET", "/api/v1/pr-reviews/"+uuid.NewString(), "", 404)
	for _, payload := range []string{`{"automatic":false,"kind":"coding"}`, `{"automatic":false,"automatic":true}`, `{"automatic":false,"reviewer_id":"00000000-0000-0000-0000-000000000000"}`, `{"automatic":null}`} {
		f.request(t, "POST", path, payload, 422)
	}
	f.request(t, "POST", path, `{"automatic":false}`, 200)
	f.request(t, "GET", source+"/prepare?reviewer_id=bad", "", 422)
	payload := map[string]any{"request_key": uuid.NewString(), "expected_input_fingerprint": "invalid", "mode": "normal"}
	raw, _ := json.Marshal(payload)
	f.request(t, "POST", source, string(raw), 422)
	for _, action := range []string{source, "/api/v1/pr-reviews/" + uuid.NewString() + "/refresh"} {
		request := httptest.NewRequest(http.MethodPost, action, nil)
		request.Header.Set("Origin", "https://foreign.invalid")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		f.server.Config.Handler.ServeHTTP(response, request)
		if response.Code != 403 {
			t.Fatal("origin not checked", response.Code)
		}
	}
	execution := decode(t, f.request(t, "GET", "/api/v1/runs/"+run["id"].(string)+"/execution", "", 200))
	if _, exists := execution["pr_review_id"]; !exists || execution["pr_review_id"] != nil {
		t.Fatal("execution missing review identity")
	}
}

func TestPRReviewHTTPLaunchReplayHistoryAndMCPReadAgree(t *testing.T) {
	pool := testsupport.Database(t)
	snapshot := testsupport.SeedPRReviewSource(t, pool, uuid.Nil)
	provider := testsupport.NewProviderFixture()
	provider.SetReviewPR(snapshot.PR, "open")
	external := httptest.NewServer(provider)
	defer external.Close()
	config := integrations.Config{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), GitHub: integrations.OAuthApp{ClientID: "fixture-github", ClientSecret: "fixture-github-secret"}, GitHubURL: external.URL, GitHubAPIURL: external.URL}
	service, err := integrations.New(pool, config)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := service.Begin(t.Context(), snapshot.ProjectID.String(), "github")
	if err != nil {
		t.Fatal(err)
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(auth.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	callback, _ := url.Parse(response.Header.Get("Location"))
	if _, err = service.Complete(t.Context(), "github", auth.State, auth.Browser, callback.Query().Get("code")); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	handler, err := httpapi.New(pool, httpapi.Config{ArtifactRoot: root, Integrations: config})
	if err != nil {
		t.Fatal(err)
	}
	apiServer := httptest.NewServer(handler)
	defer apiServer.Close()
	f := fixture{apiServer, pool, root}
	path := "/api/v1/runs/" + snapshot.SourceRunID.String() + "/pr-reviews"
	var prepared prreviews.Preparation
	if err = json.Unmarshal(f.request(t, "GET", path+"/prepare", "", 200), &prepared); err != nil || !prepared.Ready {
		t.Fatal(err, prepared.Reason)
	}
	input := map[string]any{"request_key": uuid.NewString(), "reviewer_id": snapshot.Reviewer.AgentID.String(), "expected_input_fingerprint": prepared.Snapshot.InputFingerprint, "mode": "normal"}
	raw, _ := json.Marshal(input)
	var first prreviews.Review
	if err = json.Unmarshal(f.request(t, "POST", path, string(raw), 202), &first); err != nil {
		t.Fatal(err)
	}
	provider.Unavailable.Store(true)
	var replay prreviews.Review
	if err = json.Unmarshal(f.request(t, "POST", path, string(raw), 202), &replay); err != nil || replay.ID != first.ID {
		t.Fatal("replay used provider or changed run", err)
	}
	provider.Unavailable.Store(false)
	input["automatic"] = true
	bad, _ := json.Marshal(input)
	f.request(t, "POST", path, string(bad), 422)
	delete(input, "automatic")
	input["request_key"] = uuid.NewString()
	input["mode"] = "again"
	input["previous_review_id"] = first.ID.String()
	raw, _ = json.Marshal(input)
	var second prreviews.Review
	if err = json.Unmarshal(f.request(t, "POST", path, string(raw), 202), &second); err != nil || second.PreviousReviewID == nil || *second.PreviousReviewID != first.ID || second.RunID == first.RunID {
		t.Fatal("again not explicit new work", err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE pr_reviews SET created_at='2026-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	seen := map[uuid.UUID]bool{}
	cursor := ""
	for range 2 {
		var page prreviews.Page
		if err = json.Unmarshal(f.request(t, "GET", path+"?limit=1&cursor="+url.QueryEscape(cursor), "", 200), &page); err != nil || len(page.Items) != 1 || seen[page.Items[0].ID] {
			t.Fatal("history duplicated or omitted", err)
		}
		seen[page.Items[0].ID] = true
		cursor = page.NextCursor
	}
	if cursor != "" || !seen[first.ID] || !seen[second.ID] {
		t.Fatal("unstable history")
	}
	execution := decode(t, f.request(t, "GET", "/api/v1/runs/"+first.RunID.String()+"/execution", "", 200))
	if execution["pr_review_id"] != first.ID.String() || execution["run"].(map[string]any)["kind"] != "pr_review" {
		t.Fatal("review run projection disagrees")
	}
	server, err := controlmcp.New(controlmcp.Config{APIURL: apiServer.URL, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	left, right := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), left, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "review-parity", Version: "test"}, nil).Connect(t.Context(), right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_pr_review", Arguments: map[string]any{"review_id": first.ID.String()}})
	if err != nil || result.IsError {
		t.Fatal("MCP read failed", err, result)
	}
	encoded, _ := json.Marshal(result.StructuredContent)
	var data struct {
		Review prreviews.Review `json:"review"`
	}
	if json.Unmarshal(encoded, &data) != nil || data.Review.ID != first.ID || data.Review.RunID != first.RunID || data.Review.Snapshot.InputFingerprint != first.Snapshot.InputFingerprint {
		t.Fatal("MCP and HTTP disagree")
	}
}
