package integrations_test

import (
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/testsupport"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func agentRequestFixture(t *testing.T) (fixture, integrations.RequestRoute) {
	t.Helper()
	f := setup(t)
	f.provider.SetLinearScopes("read comments:create app:mentionable app:assignable")
	auth, e := f.service.BeginLinearAppAuthorization(t.Context(), f.project, "agent")
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(auth.URL)
	if u.Query().Get("actor") != "app" || u.Query().Get("scope") != "read,comments:create,app:mentionable,app:assignable" {
		t.Fatal("wrong capabilities")
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, e := client.Get(auth.URL)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	callback, _ := url.Parse(res.Header.Get("Location"))
	if _, e = f.service.Complete(t.Context(), "linear", auth.State, auth.Browser, callback.Query().Get("code")); e != nil {
		t.Fatal(e)
	}
	identity, e := f.service.Identity(t.Context(), f.project, "linear")
	if e != nil {
		t.Fatal(e)
	}
	repo, agent := uuid.NewString(), uuid.NewString()
	_, e = f.pool.Exec(t.Context(), `WITH r AS (INSERT INTO repositories(id,project_id,name,clone_url,default_branch,external_refs) VALUES($1,$3,'Requests','/fixture/source','main','{}')) INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($2,$3,'Request engineer','fake','Preserve the requested behavior.','{}',true)`, repo, agent, f.project)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.SaveWebhookSettings(t.Context(), "linear", "https://receiver.example", "fixture-agent-webhook-secret"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE integration_webhook_settings SET last_verified_at=now()`); e != nil {
		t.Fatal(e)
	}
	return f, integrations.RequestRoute{IdentityID: identity.IdentityID, ScopeType: "team", ScopeID: testsupport.ProviderTeamID, ProjectID: f.project, RepositoryID: repo, AgentID: agent, Mode: "automatic", Enabled: true}
}
func requestEvent(t *testing.T, f fixture, payload map[string]any) string {
	t.Helper()
	session := payload["agentSession"].(map[string]any)
	f.provider.SetLinearAgentSession(session)
	acceptFixtureWebhook(t, f, "linear", "fixture-linear", "AgentSessionEvent", payload)
	if worked, e := f.service.ProcessIntegrationWebhook(t.Context()); e != nil || !worked {
		t.Fatal(worked, e)
	}
	var id string
	e := f.pool.QueryRow(t.Context(), `SELECT id FROM external_requests WHERE session_id=$1`, session["id"]).Scan(&id)
	if e != nil {
		t.Fatal(e)
	}
	return id
}
func TestLinearSessionRoutesAndDeduplicatesImmutableInput(t *testing.T) {
	f, route := agentRequestFixture(t)
	fallback, e := f.service.SaveRequestRoute(t.Context(), route)
	if e != nil {
		t.Fatal(e)
	}
	route.ScopeType = "project"
	route.ScopeID = testsupport.ProviderProjectID
	route.Mode = "approval"
	exact, e := f.service.SaveRequestRoute(t.Context(), route)
	if e != nil {
		t.Fatal(e)
	}
	payload := testsupport.LinearAgentEvent(uuid.NewString(), "created", "Please fix the fixture.")
	id := requestEvent(t, f, payload)
	request, e := f.service.ExternalRequest(t.Context(), id)
	if e != nil || request.Status != "awaiting_approval" || request.Prompt != "Please fix the fixture." {
		t.Fatal(request, e)
	}
	var used string
	if e = f.pool.QueryRow(t.Context(), `SELECT route_id FROM external_requests WHERE id=$1`, id).Scan(&used); e != nil || used != exact.ID || used == fallback.ID {
		t.Fatal("wrong precedence", used, e)
	}
	payload["promptContext"] = "changed repeated create"
	if duplicate := requestEvent(t, f, payload); duplicate != id {
		t.Fatal("duplicate request")
	}
	request, e = f.service.ExternalRequest(t.Context(), id)
	if e != nil || request.Prompt != "Please fix the fixture." {
		t.Fatal("rewrote signed input")
	}
}
func TestLinearSessionRejectsForeignIdentityAndBoundsContext(t *testing.T) {
	f, route := agentRequestFixture(t)
	if _, e := f.service.SaveRequestRoute(t.Context(), route); e != nil {
		t.Fatal(e)
	}
	for _, field := range []string{"oauthClientId", "organizationId", "appUserId"} {
		p := testsupport.LinearAgentEvent(uuid.NewString(), "created", "Fix this")
		p[field] = uuid.NewString()
		acceptFixtureWebhook(t, f, "linear", "fixture-linear", "AgentSessionEvent", p)
		if _, e := f.service.ProcessIntegrationWebhook(t.Context()); e != nil {
			t.Fatal(e)
		}
	}
	var count int
	if e := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM external_requests`).Scan(&count); e != nil || count != 0 {
		t.Fatal("foreign authority accepted", count, e)
	}
	for _, tc := range []struct {
		name, want string
		mutate     func(map[string]any)
	}{{"no issue", "unsupported", func(p map[string]any) {
		s := p["agentSession"].(map[string]any)
		delete(s, "issue")
		delete(s, "issueId")
	}}, {"oversize", "rejected", func(p map[string]any) { p["promptContext"] = strings.Repeat("x", 128*1024+1) }}, {"no human", "awaiting_approval", func(p map[string]any) {
		s := p["agentSession"].(map[string]any)
		delete(s, "creator")
		delete(s, "creatorId")
	}}} {
		t.Run(tc.name, func(t *testing.T) {
			p := testsupport.LinearAgentEvent(uuid.NewString(), "created", "Fix this")
			tc.mutate(p)
			id := requestEvent(t, f, p)
			r, e := f.service.ExternalRequest(t.Context(), id)
			if e != nil || r.Status != tc.want {
				t.Fatal(r.Status, e)
			}
			if len(r.Prompt) > 128*1024 {
				t.Fatal("oversized prompt retained")
			}
		})
	}
}
func TestRequestRouteValidatesSelectionsGenerationAndRepositoryConflict(t *testing.T) {
	f, route := agentRequestFixture(t)
	saved, e := f.service.SaveRequestRoute(t.Context(), route)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.SaveRequestRoute(t.Context(), route); !errors.Is(e, integrations.ErrRequestConflict) {
		t.Fatal("duplicate route", e)
	}
	bad := route
	bad.ScopeType = "project"
	bad.ScopeID = testsupport.ProviderProjectID
	bad.AgentID = uuid.NewString()
	if _, e = f.service.SaveRequestRoute(t.Context(), bad); e == nil {
		t.Fatal("unknown agent")
	}
	saved.Generation--
	if _, e = f.service.SaveRequestRoute(t.Context(), saved); !errors.Is(e, integrations.ErrRequestConflict) {
		t.Fatal("stale edit", e)
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE agents SET enabled=false WHERE id=$1`, route.AgentID); e != nil {
		t.Fatal(e)
	}
	bad = route
	bad.ScopeType = "project"
	bad.ScopeID = testsupport.ProviderProjectID
	if _, e = f.service.SaveRequestRoute(t.Context(), bad); e == nil {
		t.Fatal("disabled agent accepted")
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE agents SET enabled=true WHERE id=$1`, route.AgentID); e != nil {
		t.Fatal(e)
	}
	other := uuid.NewString()
	refs, _ := json.Marshal(map[string]any{"linear": map[string]string{"issue_id": testsupport.ProviderIssueID, "account_id": "20000000-0000-4000-8000-000000000004"}})
	_, e = f.pool.Exec(t.Context(), `WITH r AS(INSERT INTO repositories(id,project_id,name,clone_url,default_branch,external_refs) VALUES($1,$2,'Other','/other','main','{}')) INSERT INTO tasks(id,project_id,repository_id,title,description,status,external_refs) VALUES($3,$2,$1,'Imported','Prior text','open',$4)`, other, f.project, uuid.NewString(), refs)
	if e != nil {
		t.Fatal(e)
	}
	id := requestEvent(t, f, testsupport.LinearAgentEvent(uuid.NewString(), "created", "Fix this"))
	r, e := f.service.ExternalRequest(t.Context(), id)
	if e != nil || r.Status != "needs_routing" {
		t.Fatal("reassigned existing imported task", r, e)
	}
}

func TestRouteUnmappedRequestRequiresCurrentPreviewAndNeverLaunches(t *testing.T) {
	f, route := agentRequestFixture(t)
	id := requestEvent(t, f, testsupport.LinearAgentEvent(uuid.NewString(), "created", "Route this request"))
	q, e := f.service.ExternalRequest(t.Context(), id)
	if e != nil {
		t.Fatal(e)
	}
	route, e = f.service.SaveRequestRoute(t.Context(), route)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.RouteExternalRequest(t.Context(), id, route.ID, strings.Repeat("a", 64)); !errors.Is(e, integrations.ErrRequestConflict) {
		t.Fatal("stale route preview", e)
	}
	got, e := f.service.RouteExternalRequest(t.Context(), id, route.ID, q.InputFingerprint)
	if e != nil || got.Status != "awaiting_approval" || got.RunID != "" || got.InputFingerprint == q.InputFingerprint {
		t.Fatal(got, e)
	}
	page, e := f.service.ExternalRequests(t.Context(), integrations.RequestListQuery{ProjectID: f.project})
	if e != nil || len(page.Items) != 1 {
		t.Fatal(page, e)
	}
	if _, e = f.service.StopExternalRequest(t.Context(), id); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.RouteExternalRequest(t.Context(), id, route.ID, got.InputFingerprint); !errors.Is(e, integrations.ErrRequestUnavailable) {
		t.Fatal("routed stopped request", e)
	}
}

func TestRequestRouteCanBePausedDuringProviderOutage(t *testing.T) {
	f, r := agentRequestFixture(t)
	r, e := f.service.SaveRequestRoute(t.Context(), r)
	if e != nil {
		t.Fatal(e)
	}
	f.provider.Unavailable.Store(true)
	r.Enabled = false
	got, e := f.service.SaveRequestRoute(t.Context(), r)
	if e != nil || got.Enabled {
		t.Fatal("cannot turn off unavailable route", got, e)
	}
}
