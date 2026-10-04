package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrRequestConflict = errors.New("request settings changed or conflict; refresh and review the current selection")
var ErrRequestInput = errors.New("choose an enabled agent and repository in this project and an accessible Linear scope")
var ErrRequestUnavailable = errors.New("request is unavailable; restore app access and review its route")
var ErrRequestNotFound = errors.New("external request not found")

type RequestRoute struct {
	ID             string `json:"id"`
	IdentityID     string `json:"identity_id"`
	ScopeType      string `json:"scope_type"`
	ScopeID        string `json:"scope_id"`
	ProjectID      string `json:"project_id"`
	RepositoryID   string `json:"repository_id"`
	AgentID        string `json:"agent_id"`
	Mode           string `json:"mode"`
	Enabled        bool   `json:"enabled"`
	Generation     int64  `json:"generation"`
	ScopeName      string `json:"scope_name"`
	AgentName      string `json:"agent_name"`
	RepositoryName string `json:"repository_name"`
}

func validUUID(v string) bool { id, e := uuid.Parse(v); return e == nil && id != uuid.Nil }
func (s *Service) SaveRequestRoute(ctx context.Context, r RequestRoute) (RequestRoute, error) {
	for _, id := range []string{r.IdentityID, r.ScopeID, r.ProjectID, r.RepositoryID, r.AgentID} {
		if !validUUID(id) {
			return r, ErrRequestInput
		}
	}
	if (r.ScopeType != "team" && r.ScopeType != "project") || (r.Mode != "automatic" && r.Mode != "approval") || (r.ID != "" && !validUUID(r.ID)) {
		return r, ErrRequestInput
	}
	// Pausing an unchanged route is always local, including during an outage or
	// after an agent was disabled. Provider health must not prevent opting out.
	if !r.Enabled && r.ID != "" {
		var raw []byte
		e := s.pool.QueryRow(ctx, `UPDATE linear_request_routes t SET enabled=false,generation=generation+1,updated_at=now() WHERE id=$1 AND project_id=$2 AND identity_id=$3 AND scope_type=$4 AND scope_id=$5 AND repository_id=$6 AND agent_id=$7 AND mode=$8 AND generation=$9 RETURNING to_jsonb(t)`, r.ID, r.ProjectID, r.IdentityID, r.ScopeType, r.ScopeID, r.RepositoryID, r.AgentID, r.Mode, r.Generation).Scan(&raw)
		if errors.Is(e, pgx.ErrNoRows) {
			return r, ErrRequestConflict
		}
		if e != nil {
			return r, e
		}
		e = json.Unmarshal(raw, &r)
		return r, e
	}

	var allowed bool
	var generation int64
	err := s.pool.QueryRow(ctx, `SELECT i.generation,b.enabled AND i.enabled AND i.status='available' AND a.enabled AND a.project_id=b.project_id AND repo.project_id=b.project_id AND (NOT $5 OR (i.granted_scopes @> ARRAY['read','comments:create','app:mentionable','app:assignable'] AND EXISTS(SELECT 1 FROM integration_webhook_settings w WHERE w.provider='linear' AND w.app_client_id=i.app_client_id AND w.last_verified_at IS NOT NULL AND w.configuration_status='configured'))) FROM integration_identity_bindings b JOIN provider_identities i ON i.id=b.identity_id CROSS JOIN agents a CROSS JOIN repositories repo WHERE b.project_id=$1 AND b.provider='linear' AND i.id=$2 AND a.id=$3 AND repo.id=$4`, r.ProjectID, r.IdentityID, r.AgentID, r.RepositoryID, r.Enabled).Scan(&generation, &allowed)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !allowed {
		return r, ErrRequestInput
	}
	if err != nil {
		return r, err
	}
	err = s.withLinearIdentity(ctx, r.IdentityID, func(token string) error {
		var data map[string]*struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		query := fmt.Sprintf(`query CircularRequestScope($id: String!) { %s(id: $id) { id name } }`, r.ScopeType)
		if e := s.linear(ctx, token, query, map[string]any{"id": r.ScopeID}, &data); e != nil {
			return e
		}
		if data[r.ScopeType] == nil || data[r.ScopeType].ID != r.ScopeID {
			return ErrRequestInput
		}
		r.ScopeName = data[r.ScopeType].Name
		if len(r.ScopeName) > 2000 {
			r.ScopeName = ""
		}
		return nil
	})
	if err != nil {
		return r, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return r, err
	}
	defer rollback(ctx, tx)
	// Binding and identity locks serialize explicit detach/revoke with enablement.
	var still bool
	err = tx.QueryRow(ctx, `SELECT i.generation=$3 AND i.status='available' AND i.enabled AND b.enabled FROM provider_identities i JOIN integration_identity_bindings b ON b.identity_id=i.id WHERE i.id=$1 AND b.project_id=$2 FOR SHARE OF i,b`, r.IdentityID, r.ProjectID, generation).Scan(&still)
	if err != nil || !still {
		return r, ErrRequestConflict
	}
	var raw []byte
	if r.ID == "" {
		r.ID = uuid.NewString()
		err = tx.QueryRow(ctx, `INSERT INTO linear_request_routes AS t(id,identity_id,scope_type,scope_id,project_id,repository_id,agent_id,mode,enabled,scope_name) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING to_jsonb(t)`, r.ID, r.IdentityID, r.ScopeType, r.ScopeID, r.ProjectID, r.RepositoryID, r.AgentID, r.Mode, r.Enabled, r.ScopeName).Scan(&raw)
	} else {
		err = tx.QueryRow(ctx, `UPDATE linear_request_routes AS t SET repository_id=$6,agent_id=$7,mode=$8,enabled=$9,scope_name=$11,generation=generation+1,updated_at=now() WHERE id=$1 AND identity_id=$2 AND scope_type=$3 AND scope_id=$4 AND project_id=$5 AND generation=$10 RETURNING to_jsonb(t)`, r.ID, r.IdentityID, r.ScopeType, r.ScopeID, r.ProjectID, r.RepositoryID, r.AgentID, r.Mode, r.Enabled, r.Generation, r.ScopeName).Scan(&raw)
	}
	var pgerr *pgconn.PgError
	if errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgerr) && pgerr.Code == "23505") {
		return r, ErrRequestConflict
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(raw, &r); err != nil {
		return r, err
	}
	return r, tx.Commit(ctx)
}
func (s *Service) RequestRoutes(ctx context.Context, project string) ([]RequestRoute, error) {
	rows, e := s.pool.Query(ctx, `SELECT to_jsonb(r)||jsonb_build_object('agent_name',a.name,'repository_name',repo.name) FROM linear_request_routes r JOIN agents a ON a.id=r.agent_id JOIN repositories repo ON repo.id=r.repository_id WHERE r.project_id=$1 ORDER BY scope_type,scope_id`, project)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []RequestRoute{}
	for rows.Next() {
		var raw []byte
		var r RequestRoute
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &r); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Service) selectRequestRoute(ctx context.Context, identity, project, team string) (RequestRoute, error) {
	var r RequestRoute
	var raw []byte
	e := s.pool.QueryRow(ctx, `SELECT to_jsonb(r) FROM linear_request_routes r WHERE identity_id=$1 AND ((scope_type='project' AND scope_id::text=$2) OR (scope_type='team' AND scope_id::text=$3)) ORDER BY CASE scope_type WHEN 'project' THEN 0 ELSE 1 END LIMIT 1`, identity, project, team).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return r, nil
	}
	if e != nil {
		return r, e
	}
	e = json.Unmarshal(raw, &r)
	return r, e
}

// RouteExternalRequest changes a still-unstarted request into an approval. It
// never grants authority to text in the request or starts execution.
func (s *Service) RouteExternalRequest(ctx context.Context, id, routeID, fingerprint string) (ExternalRequest, error) {
	var out ExternalRequest
	if !validUUID(id) || !validUUID(routeID) {
		return out, ErrRequestInput
	}
	detail, e := s.ExternalRequest(ctx, id)
	if e != nil {
		return out, e
	}
	if detail.RunID != "" || detail.Status == "stopped" || detail.Status == "unsupported" || detail.Status == "rejected" {
		return out, ErrRequestUnavailable
	}
	if fingerprint != detail.InputFingerprint {
		return out, ErrRequestConflict
	}
	var raw []byte
	var source linearRequestSource
	if e = s.pool.QueryRow(ctx, `SELECT source FROM external_requests WHERE id=$1`, id).Scan(&raw); e != nil {
		return out, e
	}
	if e = json.Unmarshal(raw, &source); e != nil || !validUUID(source.IssueID) {
		return out, ErrRequestInput
	}
	verified, e := s.verifyLinearSession(ctx, source.IdentityID, source.OrganizationID, source.ActorID, source.SessionID)
	if e != nil {
		return out, e
	}
	project := ""
	if verified.Issue != nil && verified.Issue.Project != nil {
		project = verified.Issue.Project.ID
	}
	if verified.Issue == nil || verified.Issue.ID != source.IssueID || verified.Issue.Team.ID != source.TeamID || project != source.ProjectID {
		return out, ErrRequestConflict
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer rollback(ctx, tx)
	var locked string
	if e = tx.QueryRow(ctx, `SELECT id FROM provider_identities WHERE id=$1 FOR SHARE`, source.IdentityID).Scan(&locked); e != nil {
		return out, e
	}
	var route RequestRoute
	if e = tx.QueryRow(ctx, `SELECT to_jsonb(r) FROM linear_request_routes r WHERE id=$1 AND identity_id=$2 FOR SHARE`, routeID, source.IdentityID).Scan(&raw); e != nil {
		return out, ErrRequestInput
	}
	if e = json.Unmarshal(raw, &route); e != nil {
		return out, e
	}
	if !route.Enabled || (route.ScopeType == "project" && route.ScopeID != source.ProjectID) || (route.ScopeType == "team" && route.ScopeID != source.TeamID) {
		return out, ErrRequestInput
	}
	if e = tx.QueryRow(ctx, `SELECT to_jsonb(q) FROM external_requests q WHERE id=$1 FOR NO KEY UPDATE`, id).Scan(&raw); e != nil {
		return out, e
	}
	if e = json.Unmarshal(raw, &out); e != nil {
		return out, e
	}
	if out.RunID != "" || out.Status == "stopped" {
		return out, ErrRequestUnavailable
	}
	currentHash := out.InputFingerprint
	if _, hash, err := prepareExternalRequest(ctx, tx, id); err == nil {
		currentHash = hash
	}
	if currentHash != fingerprint {
		return out, ErrRequestConflict
	}
	if _, e = tx.Exec(ctx, `UPDATE external_requests SET route_id=$2,project_id=$3,repository_id=$4,agent_id=$5,status='awaiting_approval',reason='Review this route and its current execution inputs before starting.',updated_at=now() WHERE id=$1`, id, route.ID, route.ProjectID, route.RepositoryID, route.AgentID); e != nil {
		return out, e
	}
	_, hash, e := prepareExternalRequest(ctx, tx, id)
	if e != nil {
		return out, e
	}
	if e = tx.QueryRow(ctx, `UPDATE external_requests q SET input_fingerprint=$2 WHERE id=$1 RETURNING to_jsonb(q)`, id, hash).Scan(&raw); e != nil {
		return out, e
	}
	if e = json.Unmarshal(raw, &out); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
