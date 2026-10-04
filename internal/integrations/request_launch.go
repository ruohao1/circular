package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/postgres"
	"strings"
	"time"
	"unicode/utf8"
)

type requestReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func prepareExternalRequest(ctx context.Context, reader requestReader, id string) (postgres.ExternalInput, string, error) {
	var in postgres.ExternalInput
	var enabled bool
	var raw []byte
	e := reader.QueryRow(ctx, `SELECT q.id,q.source,q.title,q.prompt,r.id,r.generation,i.generation,r.project_id,r.repository_id,r.agent_id,repo.clone_url,repo.default_branch,repo.external_refs,a.name,a.backend,a.backend_config,a.instructions, r.enabled AND b.enabled AND i.enabled AND i.status='available' AND a.enabled AND a.project_id=r.project_id AND repo.project_id=r.project_id AND i.granted_scopes @> ARRAY['read','comments:create','app:mentionable','app:assignable'] FROM external_requests q JOIN linear_request_routes r ON r.id=q.route_id AND r.identity_id=q.identity_id JOIN provider_identities i ON i.id=q.identity_id JOIN integration_identity_bindings b ON b.project_id=r.project_id AND b.identity_id=i.id JOIN repositories repo ON repo.id=r.repository_id JOIN agents a ON a.id=r.agent_id WHERE q.id=$1`, id).Scan(&in.RequestID, &raw, &in.TaskTitle, &in.TaskDescription, &in.RouteID, &in.RouteGeneration, &in.IdentityGeneration, &in.ProjectID, &in.RepositoryID, &in.AgentID, &in.CloneURL, &in.BaseRef, &in.RepositoryRefs, &in.AgentName, &in.Backend, &in.BackendConfig, &in.Instructions, &enabled)
	if errors.Is(e, pgx.ErrNoRows) || e == nil && !enabled {
		return in, "", ErrRequestUnavailable
	}
	if e != nil {
		return in, "", e
	}
	in.Source = raw
	if utf8.RuneCountInString(in.TaskTitle) > 500 || strings.TrimSpace(in.TaskTitle) == "" {
		return in, "", ErrRequestInput
	}
	_, fingerprint, e := postgres.SealExternalInput(in)
	return in, fingerprint, e
}
func (s *Service) StartExternalRequest(ctx context.Context, id, fingerprint string) (ExternalRequest, error) {
	return s.startExternalRequest(ctx, id, fingerprint, false)
}
func (s *Service) startExternalRequest(ctx context.Context, id, fingerprint string, automatic bool) (ExternalRequest, error) {
	if !validUUID(id) {
		return ExternalRequest{}, ErrRequestNotFound
	}
	var existing, raw []byte
	var out ExternalRequest
	if e := s.pool.QueryRow(ctx, `SELECT to_jsonb(q),source FROM external_requests q WHERE id=$1`, id).Scan(&existing, &raw); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return out, ErrRequestNotFound
		}
		return out, e
	}
	if e := json.Unmarshal(existing, &out); e != nil {
		return out, e
	}
	if out.RunID != "" {
		return out, nil
	}
	if out.Status != "ready" && out.Status != "awaiting_approval" && out.Status != "waiting_for_active_run" && out.Status != "needs_access" {
		return out, ErrRequestUnavailable
	}
	in, hash, e := prepareExternalRequest(ctx, s.pool, id)
	if e != nil {
		return out, e
	}
	if !automatic && fingerprint != hash {
		return out, ErrRequestConflict
	}
	var source linearRequestSource
	if json.Unmarshal(raw, &source) != nil {
		return out, ErrRequestInput
	}
	verified, e := s.verifyLinearSession(ctx, out.IdentityID, source.OrganizationID, source.ActorID, out.SessionID)
	if e != nil {
		return out, e
	}
	if verified.Issue == nil || verified.Issue.ID != source.IssueID || verified.Issue.Team.ID != source.TeamID {
		return out, ErrRequestConflict
	}
	project := ""
	if verified.Issue.Project != nil {
		project = verified.Issue.Project.ID
	}
	if project != source.ProjectID {
		return out, ErrRequestConflict
	}
	if _, e = s.GitCredential(ctx, uuid.MustParse(in.RepositoryID), in.CloneURL); e != nil {
		return out, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer rollback(ctx, tx)
	// A consistent lock order serializes launch with grant/route changes and stop.
	var locked string
	if e = tx.QueryRow(ctx, `SELECT id FROM provider_identities WHERE id=$1 FOR SHARE`, out.IdentityID).Scan(&locked); e != nil {
		return out, e
	}
	if e = tx.QueryRow(ctx, `SELECT project_id FROM integration_identity_bindings WHERE identity_id=$1 AND project_id=$2 FOR SHARE`, out.IdentityID, in.ProjectID).Scan(&locked); e != nil {
		return out, e
	}
	var mode string
	if e = tx.QueryRow(ctx, `SELECT mode FROM linear_request_routes WHERE id=$1 FOR SHARE`, in.RouteID).Scan(&mode); e != nil {
		return out, e
	}
	if e = tx.QueryRow(ctx, `SELECT to_jsonb(q) FROM external_requests q WHERE id=$1 FOR NO KEY UPDATE`, id).Scan(&existing); e != nil {
		return out, e
	}
	if e = json.Unmarshal(existing, &out); e != nil {
		return out, e
	}
	if out.RunID != "" {
		return out, tx.Commit(ctx)
	}
	if out.Status == "stopped" || (automatic && (out.Status != "ready" || mode != "automatic" || source.RequesterID == "")) {
		return out, ErrRequestUnavailable
	}
	// Locks cover mutable execution settings until the immutable snapshot commits.
	if e = tx.QueryRow(ctx, `SELECT id FROM agents WHERE id=$1 FOR SHARE`, in.AgentID).Scan(&locked); e != nil {
		return out, e
	}
	if e = tx.QueryRow(ctx, `SELECT id FROM repositories WHERE id=$1 FOR SHARE`, in.RepositoryID).Scan(&locked); e != nil {
		return out, e
	}
	checked, currentHash, e := prepareExternalRequest(ctx, tx, id)
	if e != nil {
		return out, e
	}
	if currentHash != hash {
		return out, ErrRequestConflict
	}
	in = checked
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('linear-request:'||current_schema()||':'||$1||':'||$2,0))`, out.IdentityID, out.IssueID); e != nil {
		return out, e
	}
	var active string
	e = tx.QueryRow(ctx, `SELECT q.run_id FROM external_requests q JOIN runs r ON r.id=q.run_id WHERE q.identity_id=$1 AND q.issue_id=$2 AND q.id<>$3 AND r.status NOT IN ('succeeded','failed','cancelled') LIMIT 1`, out.IdentityID, out.IssueID, id).Scan(&active)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	if active != "" {
		_, e = tx.Exec(ctx, `UPDATE external_requests SET status='waiting_for_active_run',reason=$2,updated_at=now() WHERE id=$1`, id, "Another run is active for this issue: "+s.config.WebURL+"/runs/"+active+". Start this request explicitly after it finishes.")
		if e != nil {
			return out, e
		}
		out.Status = "waiting_for_active_run"
		return out, tx.Commit(ctx)
	}
	// The partial unique index is a final concurrency backstop. Reconcile terminal
	// statuses here as well, so launch remains correct while background work pauses.
	if _, e = tx.Exec(ctx, `UPDATE external_requests q SET status=CASE r.status WHEN 'cancelled' THEN 'stopped' ELSE r.status END,updated_at=now() FROM runs r WHERE q.run_id=r.id AND q.identity_id=$1 AND q.issue_id=$2 AND q.status IN ('queued','running') AND r.status IN ('succeeded','failed','cancelled')`, out.IdentityID, out.IssueID); e != nil {
		return out, e
	}
	refs, _ := json.Marshal(map[string]any{"linear": map[string]string{"issue_id": source.IssueID, "identifier": source.Identifier, "url": source.URL, "account_id": source.OrganizationID}})
	taskID := uuid.NewString()
	var repository, account string
	e = tx.QueryRow(ctx, `INSERT INTO tasks(id,project_id,repository_id,title,description,status,external_refs) VALUES($1,$2,$3,$4,$5,'open',$6) ON CONFLICT(project_id,(external_refs->'linear'->>'issue_id')) WHERE external_refs->'linear'->>'issue_id' IS NOT NULL DO UPDATE SET external_refs=tasks.external_refs RETURNING id,COALESCE(repository_id::text,''),COALESCE(external_refs->'linear'->>'account_id','')`, taskID, in.ProjectID, in.RepositoryID, in.TaskTitle, in.TaskDescription, refs).Scan(&taskID, &repository, &account)
	if e != nil {
		return out, e
	}
	if repository != in.RepositoryID || account != source.OrganizationID {
		return out, ErrRequestConflict
	}
	result, e := postgres.CreateRun(ctx, tx, postgres.RunLaunchInput{TaskID: uuid.MustParse(taskID), AgentID: uuid.MustParse(in.AgentID), RequestKey: "linear-session:" + out.SessionID, ExternalRefs: json.RawMessage(`{}`)})
	if e != nil {
		return out, e
	}
	snapshot, hash, e := postgres.SealExternalInput(in)
	if e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO external_run_inputs(run_id,request_id,snapshot,fingerprint) VALUES($1,$2,$3,$4)`, result.RunID, id, snapshot, hash); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO external_session_runs(run_id,request_id) VALUES($1,$2)`, result.RunID, id); e != nil {
		return out, e
	}
	e = tx.QueryRow(ctx, `UPDATE external_requests AS q SET run_id=$2,status='queued',reason='',prepared=$3,input_fingerprint=$4,project_id=$5,repository_id=$6,agent_id=$7,route_generation=$8,identity_generation=$9,updated_at=now() WHERE id=$1 RETURNING to_jsonb(q)`, id, result.RunID, snapshot, hash, in.ProjectID, in.RepositoryID, in.AgentID, in.RouteGeneration, in.IdentityGeneration).Scan(&existing)
	if e != nil {
		return out, e
	}
	if e = json.Unmarshal(existing, &out); e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Service) ProcessExternalRequest(ctx context.Context) (bool, error) {
	// Reserve only this attempt, with no transaction held over provider reads.
	// Its deadline is shorter than the reservation. A crashed consumer becomes
	// retryable automatically, while other projects can progress immediately.
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var id string
	var attempt int
	e := s.pool.QueryRow(ctx, `WITH candidate AS (
	 SELECT id FROM external_requests WHERE status='ready' AND next_attempt_at<=now()
	 ORDER BY next_attempt_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1
	) UPDATE external_requests q SET attempts=q.attempts+1,next_attempt_at=now()+interval '30 seconds'
	 FROM candidate c WHERE q.id=c.id RETURNING q.id,q.attempts`).Scan(&id, &attempt)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	_, e = s.startExternalRequest(ctx, id, "", true)
	if e == nil {
		return true, nil
	}
	status, reason := "ready", "Provider verification is temporarily unavailable; Circular will retry automatically."
	switch {
	case errors.Is(e, ErrRequestConflict), errors.Is(e, ErrRequestInput), errors.Is(e, postgres.ErrRunLaunchConflict), errors.Is(e, postgres.ErrRunLaunchInput), errors.Is(e, postgres.ErrResourceConflict):
		status, reason = "awaiting_approval", "Review the current route and execution inputs before starting."
	case errors.Is(e, ErrRequestUnavailable), errors.Is(e, ErrAccess), errors.Is(e, ErrReconnect), errors.Is(e, ErrIdentityUnavailable), errors.Is(e, ErrIdentityConflict), errors.Is(e, ErrConfiguration), errors.Is(e, ErrAppIdentity):
		status, reason = "needs_access", "Restore the project's provider identity and repository access, then review this request before starting."
	}
	// A newer attempt, a manual start or a stop supersedes this completion.
	_, err := s.pool.Exec(ctx, `UPDATE external_requests SET status=$2,reason=$3,next_attempt_at=now()+$4::interval,updated_at=now() WHERE id=$1 AND status='ready' AND attempts=$5`, id, status, reason, incomingRetryDelay(attempt).String(), attempt)
	return true, err
}
