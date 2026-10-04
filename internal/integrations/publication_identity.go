package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

var errPublicationUncertain = errors.New("the original publication could not be confirmed; retry checks its receipt without posting again")

func (p PublisherIdentity) valid(provider string) bool {
	if p.Provider != provider || p.AccountID == "" || p.ActorID == "" || (p.Mode != "user" && p.Mode != "app") {
		return false
	}
	if p.Mode == "app" {
		if _, err := uuid.Parse(p.IdentityID); err != nil || p.AppClientID == "" {
			return false
		}
	}
	if provider == "github" {
		return positiveNumber(p.AccountID) && positiveNumber(p.ActorID)
	}
	_, a := uuid.Parse(p.AccountID)
	_, b := uuid.Parse(p.ActorID)
	return a == nil && b == nil
}

func (s *Service) PublicationIdentity(ctx context.Context, project, provider, accountID string) (PublisherIdentity, error) {
	identity, err := s.Identity(ctx, project, provider)
	if err != nil {
		return PublisherIdentity{}, err
	}
	if identity.Mode == "app" {
		if identity.Status != "enabled" {
			return PublisherIdentity{}, ErrIdentityUnavailable
		}
		if identity.AccountID != accountID {
			return PublisherIdentity{}, ErrIdentityConflict
		}
		current, err := s.resolve(ctx, provider)
		if err != nil {
			return PublisherIdentity{}, err
		}
		return PublisherIdentity{Mode: "app", IdentityID: identity.IdentityID, Provider: provider, AccountID: accountID, ActorID: identity.ActorID, AppClientID: current.app(provider).ClientID}, nil
	}
	result := PublisherIdentity{Mode: "user", Provider: provider, AccountID: accountID}
	err = s.withUserToken(ctx, project, provider, func(token string) error {
		if provider == "github" {
			var actor struct {
				ID json.Number `json:"id"`
			}
			if err := s.github(ctx, token, "/user", &actor); err != nil {
				return err
			}
			result.ActorID = string(actor.ID)
		} else {
			actor, err := s.linearActor(ctx, token)
			if err != nil {
				return err
			}
			if actor.Organization.ID != accountID {
				return ErrIdentityConflict
			}
			result.ActorID = actor.Viewer.ID
		}
		if !result.valid(provider) {
			return ErrProvider
		}
		return nil
	})
	return result, err
}

func (s *Service) withLinearPublisher(ctx context.Context, project string, p PublisherIdentity, readOnly bool, use func(string) error) error {
	if !p.valid("linear") {
		return ErrIdentityConflict
	}
	operation := func(token string) error {
		actor, err := s.linearActor(ctx, token)
		if err != nil {
			return err
		}
		if actor.Organization.ID != p.AccountID || (!readOnly && actor.Viewer.ID != p.ActorID) {
			return ErrIdentityConflict
		}
		if !readOnly {
			settings, err := s.LinearRunUpdates(ctx, project)
			if err != nil {
				return err
			}
			if !settings.Enabled || !settings.Authorized {
				return ErrIdentityUnavailable
			}
		}
		return use(token)
	}
	if readOnly {
		return s.withToken(ctx, project, "linear", operation)
	}
	if p.Mode == "user" {
		return s.withUserToken(ctx, project, "linear", operation)
	}
	identity, err := s.Identity(ctx, project, "linear")
	if err != nil {
		return err
	}
	if identity.IdentityID != p.IdentityID || identity.Status != "enabled" {
		return ErrIdentityUnavailable
	}
	return s.withIdentityToken(ctx, project, "linear", operation)
}

func (s *Service) withGitHubPublisher(ctx context.Context, project, repo string, purpose GitHubPurpose, p PublisherIdentity, readOnly bool, use func(string, PublisherIdentity) error) error {
	if !p.valid("github") {
		return ErrIdentityConflict
	}
	if readOnly {
		return s.withGitHubRepository(ctx, project, repo, GitHubReviewRead, func(token string, actor PublisherIdentity) error { return use(token, actor) })
	}
	if p.Mode == "user" {
		return s.withUserToken(ctx, project, "github", func(token string) error {
			var actor struct {
				ID json.Number `json:"id"`
			}
			if err := s.github(ctx, token, "/user", &actor); err != nil {
				return err
			}
			if string(actor.ID) != p.ActorID {
				return ErrIdentityConflict
			}
			return use(token, p)
		})
	}
	return s.withGitHubRepository(ctx, project, repo, purpose, func(token string, actor PublisherIdentity) error {
		if actor != p {
			return ErrIdentityConflict
		}
		return use(token, p)
	})
}

func (s *Service) githubProjectPermission(ctx context.Context, project string) error {
	bound, err := s.hasIdentityBinding(ctx, project, "github")
	if err != nil {
		return err
	}
	if !bound {
		return s.withUserToken(ctx, project, "github", func(token string) error { return s.deliveryPermission(ctx, token, "") })
	}
	identity, err := s.Identity(ctx, project, "github")
	if err != nil {
		return err
	}
	if identity.Status != "enabled" {
		return ErrIdentityUnavailable
	}
	current, err := s.resolve(ctx, "github")
	if err != nil {
		return err
	}
	key, _, err := current.githubKey()
	if err != nil {
		return err
	}
	jwt, err := githubJWT(key, current.app("github").ClientID, time.Now())
	if err != nil {
		return err
	}
	var installation githubInstallationInfo
	if err := current.github(ctx, jwt, "/app/installations/"+identity.AccountID, &installation); err != nil {
		return err
	}
	if installation.SuspendedAt != nil || installation.Permissions["contents"] != "write" || installation.Permissions["pull_requests"] != "write" {
		return ErrDeliveryPermission
	}
	return nil
}
