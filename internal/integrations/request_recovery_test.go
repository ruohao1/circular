package integrations_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/testsupport"
)

// Both requests share the inbox consumer, but belong to separate projects.
func readyRequestsAcrossProjects(t *testing.T) (fixture, integrations.RequestRoute, string, string) {
	t.Helper()
	f, route := agentRequestFixture(t)
	if _, err := f.service.SaveRequestRoute(t.Context(), route); err != nil {
		t.Fatal(err)
	}
	first := requestEvent(t, f, testsupport.LinearAgentEvent(uuid.NewString(), "created", "First project"))
	project, repo, agent := uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, err := f.pool.Exec(t.Context(), `WITH p AS (
	 INSERT INTO projects(id,name) VALUES($1,'Healthy request project')
	), r AS (
	 INSERT INTO repositories(id,project_id,name,clone_url,default_branch,external_refs) VALUES($2,$1,'Healthy source','/fixture/healthy','main','{}')
	), a AS (
	 INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($3,$1,'Healthy engineer','fake','Preserve the requested behavior.','{}',true)
	) INSERT INTO integration_identity_bindings(project_id,provider,identity_id) VALUES($1,'linear',$4)`, project, repo, agent, route.IdentityID)
	if err != nil {
		t.Fatal(err)
	}
	other := route
	other.ProjectID, other.RepositoryID, other.AgentID = project, repo, agent
	other.ScopeType, other.ScopeID = "project", testsupport.ProviderProjectID
	if _, err := f.service.SaveRequestRoute(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	payload := testsupport.LinearAgentEvent(uuid.NewString(), "created", "Second project")
	session := payload["agentSession"].(map[string]any)
	issue := uuid.NewString()
	session["issueId"] = issue
	session["issue"].(map[string]any)["id"] = issue
	second := requestEvent(t, f, payload)
	for _, id := range []string{first, second} {
		r, err := f.service.ExternalRequest(t.Context(), id)
		if err != nil || r.Status != "ready" {
			t.Fatal("fixture did not produce automatic request", r.Status, err)
		}
	}
	return f, route, first, second
}

func TestExternalRequestUnavailableProjectDoesNotStarveHealthyProject(t *testing.T) {
	f, route, first, second := readyRequestsAcrossProjects(t)
	_, err := f.pool.Exec(t.Context(), `WITH i AS (
	 INSERT INTO provider_identities(id,provider,app_client_id,account_id,account_name,actor_id,actor_name,status) VALUES($1,'github','fixture-github','101','Fixture account','501','Circular fixture','available')
	), b AS (
	 INSERT INTO integration_identity_bindings(project_id,provider,identity_id,enabled) VALUES($2,'github',$1,false)
	) UPDATE repositories SET clone_url='https://github.com/fixture/private-source.git',external_refs='{"github":{"repository_id":"202","installation_id":"101"}}' WHERE id=$3`, uuid.NewString(), f.project, route.RepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if worked, err := f.service.ProcessExternalRequest(t.Context()); err != nil || !worked {
			t.Fatal("one project's unavailable identity blocked the queue", worked, err)
		}
	}
	blocked, err := f.service.ExternalRequest(t.Context(), first)
	if err != nil || blocked.Status != "needs_access" || blocked.RunID != "" || blocked.Reason == "" {
		t.Fatal("paused identity did not get a visible repair state", blocked, err)
	}
	healthy, err := f.service.ExternalRequest(t.Context(), second)
	if err != nil || healthy.Status != "queued" || healthy.RunID == "" {
		t.Fatal("healthy project did not progress", healthy, err)
	}
}

func TestExternalRequestTransientFailureSchedulesFairRecovery(t *testing.T) {
	f, _, first, second := readyRequestsAcrossProjects(t)
	firstRequest, err := f.service.ExternalRequest(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	var unavailable atomic.Bool
	unavailable.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var call struct {
			Query     string `json:"query"`
			Variables struct {
				ID string `json:"id"`
			} `json:"variables"`
		}
		_ = json.Unmarshal(body, &call)
		if unavailable.Load() && strings.Contains(call.Query, "CircularAgentSession") && call.Variables.ID == firstRequest.SessionID {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		f.provider.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	config := f.config
	config.LinearAPIURL = server.URL
	service, err := integrations.New(f.pool, config)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if worked, err := service.ProcessExternalRequest(t.Context()); err != nil || !worked {
			t.Fatal("temporary verification failure blocked another request", worked, err)
		}
	}
	healthy, err := service.ExternalRequest(t.Context(), second)
	if err != nil || healthy.RunID == "" || healthy.Status != "queued" {
		t.Fatal("healthy request starved behind retry", healthy, err)
	}
	var scheduled bool
	if err := f.pool.QueryRow(t.Context(), `SELECT status='ready' AND next_attempt_at>now() AND next_attempt_at<=now()+interval '5 minutes' AND reason<>'' FROM external_requests WHERE id=$1`, first).Scan(&scheduled); err != nil || !scheduled {
		t.Fatal("missing durable bounded retry", scheduled, err)
	}
	if worked, err := service.ProcessExternalRequest(t.Context()); err != nil || worked {
		t.Fatal("retry ignored its backoff", worked, err)
	}
	unavailable.Store(false)
	if _, err := f.pool.Exec(t.Context(), `UPDATE external_requests SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, first); err != nil {
		t.Fatal(err)
	}
	// A new service proves retry scheduling and launch survive consumer restart.
	restarted, err := integrations.New(f.pool, config)
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := restarted.ProcessExternalRequest(t.Context()); err != nil || !worked {
		t.Fatal("retry did not recover", worked, err)
	}
	recovered, err := restarted.ExternalRequest(t.Context(), first)
	if err != nil || recovered.RunID == "" || recovered.Status != "queued" {
		t.Fatal("request failed to recover", recovered, err)
	}
	var runs int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM runs`).Scan(&runs); err != nil || runs != 2 {
		t.Fatal("expected exactly one run per project", runs, err)
	}
}
