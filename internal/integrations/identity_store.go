package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// withIdentityToken serializes refresh at the shared workspace grant, not at a
// project. Persist rotations before provider operations that can fail.
func (s *Service) withIdentityToken(ctx context.Context, project, provider string, use func(string) error) error {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT identity_id FROM integration_identity_bindings WHERE project_id=$1 AND provider=$2 AND enabled`, project, provider).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrIdentityUnavailable
	}
	if err != nil {
		return err
	}
	return s.withSharedIdentityToken(ctx, id, provider, use)
}

func (s *Service) withSharedIdentityToken(ctx context.Context, identityID, provider string, use func(string) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	var id, clientID string
	var encrypted []byte
	err = tx.QueryRow(ctx, `SELECT id,app_client_id,credentials FROM provider_identities WHERE id=$1 AND provider=$2 AND enabled AND status='available' FOR UPDATE`, identityID, provider).Scan(&id, &clientID, &encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrIdentityUnavailable
	}
	if err != nil {
		return err
	}
	current, err := s.resolveWith(ctx, provider, tx)
	if err != nil {
		return err
	}
	if clientID != current.app(provider).ClientID {
		return ErrIdentityConflict
	}
	unavailable := func(cause error) error {
		if _, e := tx.Exec(ctx, `UPDATE provider_identities SET status='reconnect_required',generation=generation+1,updated_at=now() WHERE id=$1`, id); e != nil {
			return e
		}
		if e := tx.Commit(ctx); e != nil {
			return e
		}
		if e := s.stopUnavailableIdentity(ctx, id); e != nil {
			return e
		}
		return cause
	}
	plain, err := s.open(encrypted, "provider-identity:"+id)
	if err != nil {
		return unavailable(ErrReconnect)
	}
	var token tokenSet
	if json.Unmarshal(plain, &token) != nil || !safeToken(token.AccessToken) {
		return unavailable(ErrReconnect)
	}
	if !token.ExpiresAt.IsZero() && time.Until(token.ExpiresAt) < 90*time.Second {
		if token.RefreshToken == "" {
			return unavailable(ErrReconnect)
		}
		refreshed, err := current.token(ctx, provider, "", "", token.RefreshToken)
		if errors.Is(err, ErrReconnect) {
			return unavailable(ErrReconnect)
		}
		if err != nil {
			return err
		}
		if refreshed.Scopes == nil {
			refreshed.Scopes = token.Scopes
		}
		if refreshed.RefreshToken == "" {
			refreshed.RefreshToken = token.RefreshToken
		}
		plain, _ = json.Marshal(refreshed)
		encrypted, err = s.seal(plain, "provider-identity:"+id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE provider_identities SET credentials=$2,granted_scopes=$3,updated_at=now() WHERE id=$1`, id, encrypted, nonNilScopes(refreshed.Scopes)); err != nil {
			return err
		}

		wanted, _ := linearIdentityScopes("agent")
		if provider == "linear" && containsScopes(token.Scopes, wanted) && !containsScopes(refreshed.Scopes, wanted) {
			return unavailable(ErrIdentityUnavailable)
		}
		token = refreshed
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	err = use(token.AccessToken)
	if errors.Is(err, ErrReconnect) {
		_, e := s.pool.Exec(ctx, `UPDATE provider_identities SET status='reconnect_required',generation=generation+1,updated_at=now() WHERE id=$1 AND credentials=$2`, id, encrypted)
		if e != nil {
			return e
		}
		if e = s.stopUnavailableIdentity(ctx, id); e != nil {
			return e
		}
	}
	return err
}

// The existence of a binding is the mode selector, including disabled bindings.
func (s *Service) hasIdentityBinding(ctx context.Context, project, provider string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM integration_identity_bindings WHERE project_id=$1 AND provider=$2)`, project, provider).Scan(&exists)
	return exists, err
}
