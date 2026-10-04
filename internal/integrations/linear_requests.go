package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/backends"
	"github.com/ruohao1/circular/internal/postgres"
	"net/url"
	"time"
)

const maxRequestPrompt = 128 << 10

type ExternalRequest struct {
	ID               string    `json:"id"`
	Title            string    `json:"title"`
	IdentityID       string    `json:"identity_id"`
	SessionID        string    `json:"session_id"`
	IssueID          string    `json:"issue_id"`
	SourceURL        string    `json:"source_url"`
	RequesterID      string    `json:"requester_id"`
	RequesterName    string    `json:"requester_name"`
	ProjectID        string    `json:"project_id"`
	RepositoryID     string    `json:"repository_id"`
	AgentID          string    `json:"agent_id"`
	RunID            string    `json:"run_id"`
	Status           string    `json:"status"`
	Reason           string    `json:"reason"`
	InputFingerprint string    `json:"input_fingerprint"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}
type RequestMessage struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}
type ExternalRequestDetail struct {
	ExternalRequest
	Prompt          string           `json:"prompt"`
	AgentName       string           `json:"agent_name"`
	RepositoryName  string           `json:"repository_name"`
	DeliveryReason  string           `json:"delivery_reason"`
	Model           string           `json:"model"`
	ReasoningEffort string           `json:"reasoning_effort"`
	RunURL          string           `json:"run_url"`
	PullRequestURL  string           `json:"pull_request_url"`
	DeliveryStatus  string           `json:"delivery_status"`
	Messages        []RequestMessage `json:"messages"`
}
type RequestListQuery struct {
	ProjectID string
	Unrouted  bool
	Cursor    string
	Limit     int
}
type RequestPage struct {
	Items      []ExternalRequest `json:"items"`
	NextCursor string            `json:"next_cursor"`
}
type linearRequestSource struct {
	IdentityID     string `json:"identity_id"`
	OrganizationID string `json:"organization_id"`
	ActorID        string `json:"actor_id"`
	SessionID      string `json:"session_id"`
	IssueID        string `json:"issue_id"`
	TeamID         string `json:"team_id"`
	ProjectID      string `json:"linear_project_id"`
	Identifier     string `json:"identifier"`
	URL            string `json:"url"`
	RequesterID    string `json:"requester_id"`
	RequesterName  string `json:"requester_name"`
	Title          string `json:"title"`
	Prompt         string `json:"prompt"`
}
type linearSessionEvent struct {
	Type           string `json:"type"`
	Action         string `json:"action"`
	ClientID       string `json:"oauthClientId"`
	OrganizationID string `json:"organizationId"`
	ActorID        string `json:"appUserId"`
	Prompt         string `json:"promptContext"`
	Session        struct {
		ID             string `json:"id"`
		ActorID        string `json:"appUserId"`
		OrganizationID string `json:"organizationId"`
		IssueID        string `json:"issueId"`
		CreatorID      string `json:"creatorId"`
		Creator        *struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"creator"`
		Issue *struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"issue"`
	} `json:"agentSession"`
	Activity *struct {
		ID        string `json:"id"`
		SessionID string `json:"agentSessionId"`
		Signal    string `json:"signal"`
		UserID    string `json:"userId"`
		Content   struct {
			Type string `json:"type"`
			Body string `json:"body"`
		} `json:"content"`
	} `json:"agentActivity"`
}
type verifiedLinearSession struct {
	ID      string `json:"id"`
	AppUser struct {
		ID string `json:"id"`
	} `json:"appUser"`
	Creator *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"creator"`
	Issue *struct {
		ID          string `json:"id"`
		Identifier  string `json:"identifier"`
		Title       string `json:"title"`
		Description string `json:"description"`
		URL         string `json:"url"`
		Team        struct {
			ID string `json:"id"`
		} `json:"team"`
		Project *struct {
			ID string `json:"id"`
		} `json:"project"`
	} `json:"issue"`
}

func (s *Service) verifyLinearSession(ctx context.Context, identity, account, actor, session string) (verifiedLinearSession, error) {
	var out verifiedLinearSession
	e := s.withLinearIdentity(ctx, identity, func(token string) error {
		who, e := s.linearActor(ctx, token)
		if e != nil {
			return e
		}
		if who.Organization.ID != account || who.Viewer.ID != actor {
			return ErrAccess
		}
		var data struct {
			Session *verifiedLinearSession `json:"agentSession"`
		}
		if e = s.linear(ctx, token, `query CircularAgentSession($id: String!) { agentSession(id: $id) { id appUser { id } creator { id name } issue { id identifier title description url team { id } project { id } } } }`, map[string]any{"id": session}, &data); e != nil {
			return e
		}
		if data.Session == nil || data.Session.ID != session || data.Session.AppUser.ID != actor {
			return ErrAccess
		}
		out = *data.Session
		return nil
	})
	return out, e
}
func (s *Service) processLinearSession(ctx context.Context, c webhookClaim) (string, string, error) {
	var event linearSessionEvent
	if json.Unmarshal(c.Body, &event) != nil || event.Type != "AgentSessionEvent" || event.ClientID != c.ClientID || !validUUID(event.Session.ID) || !validUUID(event.OrganizationID) || event.ActorID == "" || event.Session.ActorID != event.ActorID || event.Session.OrganizationID != event.OrganizationID {
		return "ignored", "Unrecognized session identity", nil
	}
	var identity, identityStatus string
	var enabled bool
	var generation int64
	var scopes []string
	err := s.pool.QueryRow(ctx, `SELECT id,generation,granted_scopes,status,enabled FROM provider_identities WHERE provider='linear' AND app_client_id=$1 AND account_id=$2 AND actor_id=$3`, c.ClientID, event.OrganizationID, event.ActorID).Scan(&identity, &generation, &scopes, &identityStatus, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return "ignored", "Unknown or unavailable app identity", nil
	}
	if err != nil {
		return "", "", err
	}
	if event.Action == "prompted" && event.Activity != nil && event.Activity.Signal == "stop" {
		return s.acceptLinearPrompt(ctx, event, identity)
	}
	if !enabled || identityStatus != "available" {
		return "ignored", "App identity is unavailable", nil
	}
	wanted, _ := linearIdentityScopes("agent")
	if !containsScopes(scopes, wanted) {
		return "ignored", "Enable Linear agent capabilities first", nil
	}
	if event.Action == "prompted" {
		return s.acceptLinearPrompt(ctx, event, identity)
	}
	if event.Action != "created" {
		return "ignored", "Unsupported session action", nil
	}
	verified, err := s.verifyLinearSession(ctx, identity, event.OrganizationID, event.ActorID, event.Session.ID)
	if errors.Is(err, ErrAccess) || errors.Is(err, ErrReconnect) {
		return "ignored", "Session access could not be verified", nil
	}
	if err != nil {
		return "", "", err
	}
	source := linearRequestSource{IdentityID: identity, OrganizationID: event.OrganizationID, ActorID: event.ActorID, SessionID: event.Session.ID, Prompt: event.Prompt}
	status, reason := "needs_routing", "Choose a route in Circular for this Linear issue."
	if verified.Issue == nil {
		status, reason = "unsupported", "Mention or delegate Circular on an issue to start coding work."
	} else {
		issue := verified.Issue
		if issue.ID != event.Session.IssueID || !validUUID(issue.ID) || !validUUID(issue.Team.ID) {
			return "ignored", "Session issue identity changed", nil
		}
		u, e := url.Parse(issue.URL)
		if e != nil || u.Scheme != "https" || u.Host != "linear.app" || u.User != nil {
			return "ignored", "Invalid issue link", nil
		}
		source.IssueID, source.TeamID, source.Identifier, source.Title, source.URL = issue.ID, issue.Team.ID, issue.Identifier, issue.Title, issue.URL
		if issue.Project != nil {
			source.ProjectID = issue.Project.ID
		}
		if source.Prompt == "" {
			source.Prompt = issue.Title + "\n\n" + issue.Description
		}
	}
	if verified.Creator != nil && event.Session.Creator != nil && verified.Creator.ID == event.Session.Creator.ID && event.Session.CreatorID == verified.Creator.ID && validUUID(verified.Creator.ID) {
		source.RequesterID, source.RequesterName = verified.Creator.ID, verified.Creator.Name
	}
	if len(source.Prompt) > maxRequestPrompt {
		source.Prompt = ""
		status, reason = "rejected", "The request exceeds the 128 KiB context limit. Shorten it and start a new Linear session."
	}
	if len(source.Title) > 2000 || len(source.RequesterName) > 1000 {
		status, reason = "rejected", "The request metadata is too large."
		source.Title = ""
		source.RequesterName = ""
	}
	route := RequestRoute{}
	if status == "needs_routing" {
		route, err = s.selectRequestRoute(ctx, identity, source.ProjectID, source.TeamID)
		if err != nil {
			return "", "", err
		}
		if route.ID != "" && route.Enabled {
			var usable bool
			err = s.pool.QueryRow(ctx, `SELECT a.enabled AND a.project_id=$2 AND repo.project_id=$2 AND b.enabled AND NOT EXISTS(SELECT 1 FROM tasks WHERE project_id=$2 AND external_refs->'linear'->>'issue_id'=$4 AND (repository_id IS DISTINCT FROM $3::uuid OR external_refs->'linear'->>'account_id' IS DISTINCT FROM $6)) FROM agents a CROSS JOIN repositories repo JOIN integration_identity_bindings b ON b.project_id=repo.project_id AND b.identity_id=$5 WHERE a.id=$1 AND repo.id=$3`, route.AgentID, route.ProjectID, route.RepositoryID, source.IssueID, identity, source.OrganizationID).Scan(&usable)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return "", "", err
			}
			if usable {
				status, reason = "ready", ""
				if route.Mode == "approval" || source.RequesterID == "" {
					status, reason = "awaiting_approval", "Review the request in Circular before starting."
				}
			} else {
				reason = "The selected agent, repository or imported task needs review."
			}
		}
	}
	raw, _ := json.Marshal(source)
	fingerprint := digest(string(raw))
	id := uuid.NewString()
	_, err = s.pool.Exec(ctx, `INSERT INTO external_requests AS q(id,identity_id,session_id,issue_id,source_url,requester_id,requester_name,project_id,repository_id,agent_id,route_id,route_generation,identity_generation,status,reason,title,prompt,source,input_fingerprint) VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7,NULLIF($8,'')::uuid,NULLIF($9,'')::uuid,NULLIF($10,'')::uuid,NULLIF($11,'')::uuid,$12,$13,$14,$15,$16,$17,$18,$19) ON CONFLICT(identity_id,session_id) DO UPDATE SET issue_id=EXCLUDED.issue_id,source_url=EXCLUDED.source_url,requester_id=EXCLUDED.requester_id,requester_name=EXCLUDED.requester_name,project_id=EXCLUDED.project_id,repository_id=EXCLUDED.repository_id,agent_id=EXCLUDED.agent_id,route_id=EXCLUDED.route_id,route_generation=EXCLUDED.route_generation,identity_generation=EXCLUDED.identity_generation,status=EXCLUDED.status,reason=EXCLUDED.reason,title=EXCLUDED.title,prompt=EXCLUDED.prompt,source=EXCLUDED.source,input_fingerprint=EXCLUDED.input_fingerprint,updated_at=now() WHERE q.source='{}'::jsonb AND q.stopped_at IS NULL AND q.run_id IS NULL`, id, identity, source.SessionID, source.IssueID, source.URL, source.RequesterID, source.RequesterName, route.ProjectID, route.RepositoryID, route.AgentID, route.ID, route.Generation, generation, status, reason, source.Title, source.Prompt, raw, fingerprint)
	return "processed", "", err
}
func (s *Service) ExternalRequest(ctx context.Context, id string) (ExternalRequestDetail, error) {
	out := ExternalRequestDetail{Messages: []RequestMessage{}}
	if !validUUID(id) {
		return out, ErrRequestNotFound
	}
	var raw []byte
	e := s.pool.QueryRow(ctx, `SELECT to_jsonb(r) FROM external_requests r WHERE id=$1`, id).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, ErrRequestNotFound
	}
	if e != nil {
		return out, e
	}
	if e = json.Unmarshal(raw, &out); e != nil {
		return out, e
	}
	var prepared postgres.ExternalInput
	var snapshot []byte
	if out.RunID != "" {
		if e := s.pool.QueryRow(ctx, `SELECT snapshot FROM external_run_inputs WHERE request_id=$1`, id).Scan(&snapshot); e == nil {
			_ = json.Unmarshal(snapshot, &prepared)
		}
	} else if in, hash, e := prepareExternalRequest(ctx, s.pool, id); e == nil {
		prepared = in
		out.InputFingerprint = hash
	}
	if prepared.AgentID != "" {
		out.AgentID, out.RepositoryID, out.ProjectID = prepared.AgentID, prepared.RepositoryID, prepared.ProjectID
		out.AgentName = prepared.AgentName
		out.Model = prepared.Backend
		if prepared.Backend == "codex" {
			if settings, e := backends.ValidateCodexConfig(prepared.BackendConfig); e == nil {
				out.Model = settings.Model
				out.ReasoningEffort = settings.ReasoningEffort
			}
		}
	}
	if out.RunID != "" {
		out.RunURL = s.config.WebURL + "/runs/" + out.RunID
		_ = s.pool.QueryRow(ctx, `SELECT COALESCE(pull_request_url,'') FROM github_run_deliveries WHERE run_id=$1`, out.RunID).Scan(&out.PullRequestURL)
	}
	_ = s.pool.QueryRow(ctx, `SELECT status,last_error FROM linear_agent_activities WHERE request_id=$1 AND status<>'cancelled' ORDER BY CASE status WHEN 'uncertain' THEN 0 WHEN 'pending' THEN 1 WHEN 'failed' THEN 2 ELSE 3 END,updated_at DESC,id DESC LIMIT 1`, id).Scan(&out.DeliveryStatus, &out.DeliveryReason)
	if out.RepositoryID != "" {
		_ = s.pool.QueryRow(ctx, `SELECT name FROM repositories WHERE id=$1`, out.RepositoryID).Scan(&out.RepositoryName)
	}
	rows, e := s.pool.Query(ctx, `SELECT id,body,created_at FROM external_request_messages WHERE request_id=$1 ORDER BY created_at,id`, id)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var m RequestMessage
		if e = rows.Scan(&m.ID, &m.Body, &m.CreatedAt); e != nil {
			return out, e
		}
		out.Messages = append(out.Messages, m)
	}
	return out, rows.Err()
}
func (s *Service) ExternalRequests(ctx context.Context, q RequestListQuery) (RequestPage, error) {
	out := RequestPage{Items: []ExternalRequest{}}
	if q.Limit <= 0 || q.Limit > 100 {
		q.Limit = 20
	}
	if (q.ProjectID == "") == !q.Unrouted || q.ProjectID != "" && !validUUID(q.ProjectID) || (q.Cursor != "" && !validUUID(q.Cursor)) {
		return out, ErrRequestInput
	}
	rows, e := s.pool.Query(ctx, `SELECT to_jsonb(r) FROM external_requests r WHERE (($1::text='' AND project_id IS NULL) OR project_id=NULLIF($1,'')::uuid) AND ($2::text='' OR (created_at,id)<(SELECT created_at,id FROM external_requests WHERE id=NULLIF($2,'')::uuid)) ORDER BY created_at DESC,id DESC LIMIT $3`, q.ProjectID, q.Cursor, q.Limit+1)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var r ExternalRequest
		if e = rows.Scan(&raw); e != nil {
			return out, e
		}
		if e = json.Unmarshal(raw, &r); e != nil {
			return out, e
		}
		out.Items = append(out.Items, r)
	}
	if len(out.Items) > q.Limit {
		out.Items = out.Items[:q.Limit]
		out.NextCursor = out.Items[len(out.Items)-1].ID
	}
	return out, rows.Err()
}
