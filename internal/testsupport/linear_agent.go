package testsupport

import (
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"strings"
	"time"
)

const ProviderAppActorID = "20000000-0000-4000-8000-000000000005"
const ProviderHumanID = "20000000-0000-4000-8000-000000000006"

func LinearAgentEvent(session, action, prompt string) map[string]any {
	s := map[string]any{"id": session, "appUserId": ProviderAppActorID, "organizationId": "20000000-0000-4000-8000-000000000004", "creatorId": ProviderHumanID, "creator": map[string]string{"id": ProviderHumanID, "name": "Fixture requester"}, "issueId": ProviderIssueID, "issue": map[string]any{"id": ProviderIssueID, "identifier": "TST-1", "title": "Fixture request", "description": "Preserve the fixture.", "url": "https://linear.app/circular-fixture/issue/TST-1", "teamId": ProviderTeamID, "team": map[string]string{"id": ProviderTeamID, "name": "Engineering"}}}
	e := map[string]any{"type": "AgentSessionEvent", "action": action, "oauthClientId": "fixture-linear", "organizationId": "20000000-0000-4000-8000-000000000004", "appUserId": ProviderAppActorID, "webhookTimestamp": time.Now().UnixMilli(), "promptContext": prompt, "agentSession": s}
	if action == "prompted" {
		e["agentActivity"] = map[string]any{"id": uuid.NewString(), "agentSessionId": session, "userId": ProviderHumanID, "content": map[string]string{"type": "prompt", "body": prompt}}
	}
	return e
}
func (p *ProviderFixture) SetLinearAgentSession(s map[string]any) {
	raw, _ := json.Marshal(s)
	var copy map[string]any
	_ = json.Unmarshal(raw, &copy)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.agentSessions == nil {
		p.agentSessions = map[string]map[string]any{}
	}
	p.agentSessions[s["id"].(string)] = copy
}
func (p *ProviderFixture) serveLinearAgent(w http.ResponseWriter, access, query string, variables map[string]json.RawMessage) bool {
	if strings.Contains(query, "CircularAgentActivity(") && p.BeforeLinearActivity != nil {
		p.BeforeLinearActivity()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var id string
	_ = json.Unmarshal(variables["id"], &id)
	reply := func(data any) { fixtureJSON(w, map[string]any{"data": data}) }
	activityObject := func(a FixtureLinearActivity) any {
		return map[string]any{"id": a.ID, "agentSession": map[string]string{"id": a.SessionID}, "user": map[string]string{"id": a.ActorID}, "content": a.Content}
	}
	switch {
	case strings.Contains(query, "CircularAgentActivityReceipt"):
		nodes := []any{}
		if a, ok := p.activities[id]; ok && !p.HideActivities.Load() {
			nodes = append(nodes, activityObject(a))
		}
		reply(map[string]any{"agentActivities": map[string]any{"nodes": nodes}})
		return true
	case strings.Contains(query, "CircularAgentActivity"):
		if code := p.ActivityReject.Load(); code != 0 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(int(code))
			if code == http.StatusBadRequest {
				fixtureJSON(w, map[string]any{"errors": []any{map[string]any{"message": "Invalid scope: write required", "extensions": map[string]string{"code": "FORBIDDEN"}}}})
			}
			return true
		}
		var in struct {
			ID        string          `json:"id"`
			SessionID string          `json:"agentSessionId"`
			Content   json.RawMessage `json:"content"`
		}
		if json.Unmarshal(variables["input"], &in) != nil || p.actors[access] != "app" {
			w.WriteHeader(403)
			return true
		}
		if p.agentSessions[in.SessionID] == nil || in.ID == "" {
			w.WriteHeader(400)
			return true
		}
		if p.activities == nil {
			p.activities = map[string]FixtureLinearActivity{}
		}
		if _, exists := p.activities[in.ID]; exists {
			w.WriteHeader(409)
			return true
		}
		a := FixtureLinearActivity{ID: in.ID, SessionID: in.SessionID, ActorID: ProviderAppActorID, Content: in.Content}
		p.activities[in.ID] = a
		p.ActivityCreates.Add(1)
		if p.LoseActivityResponse.Swap(false) {
			w.WriteHeader(503)
			return true
		}
		reply(map[string]any{"agentActivityCreate": map[string]any{"success": true, "agentActivity": activityObject(a)}})
		return true
	case strings.Contains(query, "CircularSessionLinks"):
		var in struct {
			URLs []map[string]string `json:"externalUrls"`
		}
		_ = json.Unmarshal(variables["input"], &in)
		if p.agentLinks == nil {
			p.agentLinks = map[string][]map[string]string{}
		}
		p.agentLinks[id] = in.URLs
		reply(map[string]any{"agentSessionUpdate": map[string]any{"success": true}})
		return true

	case strings.Contains(query, "CircularRequestScope"):
		field := "team"
		want := ProviderTeamID
		if strings.Contains(query, "project(id:") {
			field = "project"
			want = ProviderProjectID
		}
		var value any
		if id == want {
			name := "Engineering"
			if field == "project" {
				name = "Integration work"
			}
			value = map[string]string{"id": id, "name": name}
		}
		reply(map[string]any{field: value})
		return true
	case strings.Contains(query, "CircularAgentSession"):
		s, ok := p.agentSessions[id]
		if !ok {
			reply(map[string]any{"agentSession": nil})
			return true
		}
		var issue any
		if raw, ok := s["issue"].(map[string]any); ok {
			v := map[string]any{}
			for k, x := range raw {
				v[k] = x
			}
			v["project"] = map[string]string{"id": ProviderProjectID}
			v["team"] = map[string]string{"id": ProviderTeamID}
			issue = v
		}
		reply(map[string]any{"agentSession": map[string]any{"id": id, "appUser": map[string]any{"id": s["appUserId"]}, "creator": s["creator"], "issue": issue}})
		return true
	}
	return false
}

type FixtureLinearActivity struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	ActorID   string          `json:"actor_id"`
	Content   json.RawMessage `json:"content"`
}

func (p *ProviderFixture) LinearActivities() []FixtureLinearActivity {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []FixtureLinearActivity{}
	for _, a := range p.activities {
		out = append(out, a)
	}
	return out
}
func (p *ProviderFixture) SetLinearActivity(a FixtureLinearActivity) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.activities[a.ID] = a
}
func (p *ProviderFixture) LinearSessionLinks(id string) []map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]string{}, p.agentLinks[id]...)
}
