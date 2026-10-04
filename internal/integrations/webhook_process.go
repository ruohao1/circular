package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// ProcessIntegrationWebhook runs separately from reception. The claim is committed
// before provider calls, and its lease fences completion after crashes/restarts.
func (s *Service) ProcessIntegrationWebhook(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	claim, err := s.claimWebhook(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	status, reason, err := s.processWebhook(ctx, claim)
	if err != nil {
		reason = "Provider verification temporarily unavailable"
	}
	return true, s.finishWebhook(ctx, claim, status, reason, err != nil)
}
func (s *Service) processWebhook(ctx context.Context, c webhookClaim) (string, string, error) {
	current, err := s.resolve(ctx, c.Provider)
	if err != nil {
		return "", "", err
	}
	if current.app(c.Provider).ClientID != c.ClientID {
		return "ignored", "Unknown app identity", nil
	}
	if c.Provider == "linear" && c.Event == "AgentSessionEvent" {
		return s.processLinearSession(ctx, c)
	}
	var body struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		OrganizationID string `json:"organizationId"`
		ClientID       string `json:"oauthClientId"`
	}
	if json.Unmarshal(c.Body, &body) != nil {
		return "ignored", "Unsupported event payload", nil
	}
	var account string
	if c.Provider == "github" && (c.Event == "installation" || c.Event == "installation_repositories") {
		if body.Installation.ID <= 0 {
			return "ignored", "No installation identity", nil
		}
		account = strconv.FormatInt(body.Installation.ID, 10)
	} else if c.Provider == "linear" && (c.Event == "OAuthApp" || c.Event == "AppUserNotification") {
		if body.ClientID != "" && body.ClientID != c.ClientID {
			return "ignored", "Unknown app identity", nil
		}
		account = body.OrganizationID
	} else {
		return "ignored", "Event is not enabled", nil
	}
	var id, actor, status string
	var generation int64
	err = s.pool.QueryRow(ctx, `SELECT id,actor_id,status,generation FROM provider_identities WHERE provider=$1 AND app_client_id=$2 AND account_id=$3 AND enabled`, c.Provider, c.ClientID, account).Scan(&id, &actor, &status, &generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return "ignored", "Unknown account identity", nil
	}
	if err != nil {
		return "", "", err
	}
	// A new grant or manual repair supersedes this observation. Never re-enable a
	// revoked identity based on payload contents, even for a delayed "created" event.
	next := status
	if c.Provider == "github" {
		key, _, e := current.githubKey()
		if e != nil {
			return "", "", e
		}
		jwt, e := githubJWT(key, c.ClientID, time.Now())
		if e != nil {
			return "", "", e
		}
		var app githubAppInfo
		var installation githubInstallationInfo
		e = current.github(ctx, jwt, "/app", &app)
		if e == nil {
			e = current.github(ctx, jwt, "/app/installations/"+account, &installation)
		}
		if errors.Is(e, ErrAccess) || errors.Is(e, ErrReconnect) {
			next = "needs_access"
		} else if e != nil {
			return "", "", e
		} else if app.ClientID != c.ClientID || installation.AppID != app.ID || strconv.FormatInt(installation.ID, 10) != account || installation.SuspendedAt != nil || installation.Permissions["contents"] == "" || installation.Permissions["pull_requests"] != "write" {
			next = "needs_access"
		}
	} else if status == "available" {
		e := current.withLinearIdentity(ctx, id, func(token string) error {
			response, e := current.linearActor(ctx, token)
			if e != nil {
				return e
			}
			if response.Organization.ID != account || response.Viewer.ID != actor {
				return ErrAccess
			}
			return nil
		})
		if errors.Is(e, ErrReconnect) || errors.Is(e, ErrIdentityUnavailable) {
			next = "reconnect_required"
		} else if errors.Is(e, ErrAccess) {
			next = "needs_access"
		} else if e != nil {
			return "", "", e
		}
	}
	// Every verified access event invalidates scoped token caches in all processes.
	_, err = s.pool.Exec(ctx, `UPDATE provider_identities SET status=$3,generation=generation+1,updated_at=now() WHERE id=$1 AND generation=$2`, id, generation, next)
	if err == nil && next != "available" {
		err = s.stopUnavailableIdentity(ctx, id)
	}
	return "processed", "", err
}
