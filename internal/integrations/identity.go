package integrations

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrIdentityConflict    = errors.New("this identity belongs to a different provider, app or workspace")
	ErrIdentityUnavailable = errors.New("Circular identity is disabled or needs attention in Integrations")
)

// IdentityStatus is the only public view of a shared provider identity. Secrets
// and rotating grants deliberately live in a separate persistence type.
type IdentityStatus struct {
	Provider           string   `json:"provider"`
	Mode               string   `json:"mode"`
	IdentityID         string   `json:"identity_id"`
	AccountID          string   `json:"account_id"`
	AccountName        string   `json:"account_name"`
	ActorID            string   `json:"actor_id"`
	ActorName          string   `json:"actor_name"`
	ActorLogin         string   `json:"actor_login"`
	AvatarURL          string   `json:"avatar_url"`
	Status             string   `json:"status"`
	Capabilities       []string `json:"capabilities"`
	AffectedProjects   []string `json:"affected_projects"`
	EnvironmentManaged bool     `json:"environment_managed"`
	Reason             string   `json:"reason"`
}

func (s *Service) Identity(ctx context.Context, project, provider string) (IdentityStatus, error) {
	result := IdentityStatus{Provider: provider, Mode: "user", Status: "needs_setup", Capabilities: []string{}, AffectedProjects: []string{}, EnvironmentManaged: s.environmentApp(provider)}
	if provider != "github" && provider != "linear" {
		return result, ErrAppInput
	}
	var selected, enabled bool
	var clientID string
	err := s.pool.QueryRow(ctx, `SELECT i.id,i.account_id,i.account_name,i.actor_id,i.actor_name,i.actor_login,i.avatar_url,i.status,i.enabled,b.enabled,i.app_client_id,i.granted_scopes FROM integration_identity_bindings b JOIN provider_identities i ON i.id=b.identity_id AND i.provider=b.provider WHERE b.project_id=$1 AND b.provider=$2`, project, provider).Scan(&result.IdentityID, &result.AccountID, &result.AccountName, &result.ActorID, &result.ActorName, &result.ActorLogin, &result.AvatarURL, &result.Status, &enabled, &selected, &clientID, &result.Capabilities)
	if errors.Is(err, pgx.ErrNoRows) {
		if provider == "github" {
			current, err := s.resolve(ctx, provider)
			if err != nil {
				return result, err
			}
			if _, _, err := current.githubKey(); err == nil {
				result.Status = "available"
			}
		}
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.Mode = "app"
	current, err := s.resolve(ctx, provider)
	if err != nil {
		return result, err
	}
	if !selected {
		result.Status = "disabled"
		result.Reason = "Circular identity is paused for this project. Enable it to resume."
	} else if !enabled || clientID != current.app(provider).ClientID {
		result.Status = "reconnect_required"
	} else if result.Status == "available" {
		result.Status = "enabled"
	}
	rows, err := s.pool.Query(ctx, `SELECT p.name FROM integration_identity_bindings b JOIN projects p ON p.id=b.project_id WHERE b.identity_id=$1 AND b.enabled ORDER BY p.name,p.id`, result.IdentityID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return result, err
		}
		result.AffectedProjects = append(result.AffectedProjects, name)
	}
	return result, rows.Err()
}

func (s *Service) BindIdentity(ctx context.Context, project, provider, identityID string) (IdentityStatus, error) {
	if _, err := uuid.Parse(identityID); err != nil {
		return IdentityStatus{}, ErrIdentityConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return IdentityStatus{}, err
	}
	defer rollback(ctx, tx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('identity-binding:'||current_schema()||':'||$1||':'||$2,0))`, project, provider); err != nil {
		return IdentityStatus{}, err
	}
	current, err := s.resolveWith(ctx, provider, tx)
	if err != nil {
		return IdentityStatus{}, err
	}
	var actualProvider, clientID, status, accountID string
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT provider,app_client_id,status,enabled,account_id FROM provider_identities WHERE id=$1 FOR UPDATE`, identityID).Scan(&actualProvider, &clientID, &status, &enabled, &accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return IdentityStatus{}, ErrIdentityConflict
	}
	if err != nil {
		return IdentityStatus{}, err
	}
	if actualProvider != provider || clientID != current.app(provider).ClientID {
		return IdentityStatus{}, ErrIdentityConflict
	}
	if !enabled || status != "available" {
		return IdentityStatus{}, ErrIdentityUnavailable
	}
	if provider == "linear" {
		var expected string
		if err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT config->>'account_id' FROM integrations WHERE project_id=$1 AND provider='linear' AND enabled),'')`, project).Scan(&expected); err != nil {
			return IdentityStatus{}, err
		}
		if expected != "" && expected != accountID {
			return IdentityStatus{}, ErrIdentityConflict
		}
	}
	// Reconnecting cannot silently move a project to a different account.
	var previous string
	err = tx.QueryRow(ctx, `SELECT identity_id FROM integration_identity_bindings WHERE project_id=$1 AND provider=$2 FOR UPDATE`, project, provider).Scan(&previous)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return IdentityStatus{}, err
	}
	if previous != "" && previous != identityID {
		return IdentityStatus{}, ErrIdentityConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO integration_identity_bindings(project_id,provider,identity_id) VALUES($1,$2,$3) ON CONFLICT(project_id,provider) DO UPDATE SET enabled=true,updated_at=now() WHERE integration_identity_bindings.identity_id=EXCLUDED.identity_id`, project, provider, identityID)
	if err != nil {
		return IdentityStatus{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IdentityStatus{}, err
	}
	return s.Identity(ctx, project, provider)
}

// Detach preserves the explicit selection and the shared grant. A detached
// project must never silently resume publishing as its human connection.
func (s *Service) DetachIdentity(ctx context.Context, project, provider string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	if err := lockApp(ctx, tx, provider); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE integration_identity_bindings SET enabled=false,updated_at=now() WHERE project_id=$1 AND provider=$2`, project, provider); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM identity_oauth_states WHERE project_id=$1 AND provider=$2`, project, provider); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
