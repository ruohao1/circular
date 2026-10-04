package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type WebhookSettings struct {
	Provider         string     `json:"provider"`
	PublicOrigin     string     `json:"public_origin"`
	CallbackURL      string     `json:"callback_url"`
	Status           string     `json:"status"`
	SecretConfigured bool       `json:"secret_configured"`
	LastVerifiedAt   *time.Time `json:"last_verified_at"`
	AffectedProjects []string   `json:"affected_projects"`
	ProviderURL      string     `json:"provider_url"`
	Reason           string     `json:"reason"`
}

var receiverHostname = regexp.MustCompile(`^[a-zA-Z0-9.-]+$`)

func publicReceiverOrigin(value string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return "", ErrAppInput
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return "", ErrAppInput
	}
	if ip, e := netip.ParseAddr(host); e == nil {
		ip = ip.Unmap()
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return "", ErrAppInput
		}
	} else if !receiverHostname.MatchString(host) || !strings.Contains(host, ".") || strings.HasSuffix(host, ".") {
		return "", ErrAppInput
	}
	if port := u.Port(); port != "" && port != "443" {
		return "", ErrAppInput
	}
	return "https://" + u.Host, nil
}
func (s *Service) WebhookSettings(ctx context.Context, provider string) (WebhookSettings, error) {
	out := WebhookSettings{Provider: provider, Status: "needs_setup", AffectedProjects: []string{}}
	if provider != "github" && provider != "linear" {
		return out, ErrAppInput
	}
	current, err := s.resolve(ctx, provider)
	if err != nil {
		return out, err
	}
	client := current.app(provider).ClientID
	out.ProviderURL = current.config.LinearURL + "/settings/api/applications"
	if provider == "github" {
		out.ProviderURL = current.config.GitHubURL + "/settings/apps/" + current.config.GitHubAppSlug + "/advanced"
	}
	var state string
	err = s.pool.QueryRow(ctx, `SELECT public_origin,configuration_status,last_verified_at FROM integration_webhook_settings WHERE provider=$1 AND app_client_id=$2`, provider, client).Scan(&out.PublicOrigin, &state, &out.LastVerifiedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if err == nil {
		out.SecretConfigured = true
		out.CallbackURL = out.PublicOrigin + "/webhooks/" + provider
		out.Status = "waiting"
		if out.LastVerifiedAt != nil {
			out.Status = "receiving"
		}
		if state == "needs_access" || state == "configuration_failed" {
			out.Status = state
			out.Reason = "Review the provider webhook settings and retry configuration."
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT p.name FROM integration_identity_bindings b JOIN provider_identities i ON i.id=b.identity_id JOIN projects p ON p.id=b.project_id WHERE i.provider=$1 AND i.app_client_id=$2 ORDER BY p.name`, provider, client)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return out, err
		}
		out.AffectedProjects = append(out.AffectedProjects, name)
	}
	return out, rows.Err()
}
func (s *Service) SaveWebhookSettings(ctx context.Context, provider, origin, secret string) (WebhookSettings, error) {
	normalized, err := publicReceiverOrigin(origin)
	if err != nil {
		return WebhookSettings{}, err
	}
	if provider != "github" && provider != "linear" {
		return WebhookSettings{}, ErrAppInput
	}
	if secret != "" && (len(secret) < 16 || len(secret) > 512 || !safeToken(secret)) {
		return WebhookSettings{}, ErrAppInput
	}
	// Serialize explicit app-wide saves while committing the pending verification key
	// before the remote update. This transaction holds only an advisory lock.
	lock, err := s.pool.Begin(ctx)
	if err != nil {
		return WebhookSettings{}, err
	}
	defer rollback(ctx, lock)
	if err = lockApp(ctx, lock, provider); err != nil {
		return WebhookSettings{}, err
	}
	current, err := s.resolve(ctx, provider)
	if err != nil {
		return WebhookSettings{}, err
	}
	client := current.app(provider).ClientID
	if client == "" || s.vault == nil {
		return WebhookSettings{}, ErrConfiguration
	}
	old, oldErr := s.WebhookSigningKey(ctx, provider)
	if oldErr != nil && !errors.Is(oldErr, pgx.ErrNoRows) {
		return WebhookSettings{}, oldErr
	}
	if secret == "" {
		secret = string(old.Current)
	}
	if secret == "" && provider == "github" {
		secret = randomValue()
	}
	if secret == "" {
		return WebhookSettings{}, ErrAppInput
	}
	var jwt string
	if provider == "github" {
		key, _, e := current.githubKey()
		if e != nil {
			return WebhookSettings{}, e
		}
		jwt, e = githubJWT(key, client, time.Now())
		if e != nil {
			return WebhookSettings{}, e
		}
		var existing githubHookConfig
		if e = current.github(ctx, jwt, "/app/hook/config", &existing); e != nil {
			return WebhookSettings{}, e
		}
	}
	pending := webhookSecrets{Current: secret}
	until := time.Now().Add(10 * time.Minute)
	if len(old.Current) > 0 {
		pending = webhookSecrets{Current: string(old.Current), Previous: secret}
	}
	writeKeys := func(keys webhookSecrets, state string, success bool) error {
		plain, _ := json.Marshal(keys)
		encrypted, e := s.seal(plain, webhookKeyAAD(provider, client))
		if e != nil {
			return e
		}
		_, e = s.pool.Exec(ctx, `INSERT INTO integration_webhook_settings(provider,app_client_id,signing_keys,public_origin,previous_until,configuration_status) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(provider,app_client_id) DO UPDATE SET signing_keys=$3,public_origin=CASE WHEN $7 THEN $4 ELSE integration_webhook_settings.public_origin END,previous_until=$5,configuration_status=$6,last_verified_at=CASE WHEN $7 THEN NULL ELSE integration_webhook_settings.last_verified_at END,updated_at=now()`, provider, client, encrypted, normalized, until, state, success)
		return e
	}
	if err = writeKeys(pending, "configuration_failed", false); err != nil {
		return WebhookSettings{}, err
	}
	if provider == "github" {
		body, _ := json.Marshal(map[string]string{"url": normalized + "/webhooks/github", "content_type": "json", "secret": secret, "insecure_ssl": "0"})
		request, e := http.NewRequestWithContext(ctx, "PATCH", current.config.GitHubAPIURL+"/app/hook/config", bytes.NewReader(body))
		if e != nil {
			return WebhookSettings{}, ErrProvider
		}
		request.Header.Set("Authorization", "Bearer "+jwt)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/vnd.github+json")
		if e = current.do(request, nil); e != nil {
			return WebhookSettings{}, e
		}
	}
	ready := webhookSecrets{Current: secret, Previous: string(old.Current)}
	if ready.Previous == secret {
		ready.Previous = ""
	}
	if err = writeKeys(ready, "configured", true); err != nil {
		return WebhookSettings{}, err
	}
	return s.WebhookSettings(ctx, provider)
}

type githubHookConfig struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	InsecureSSL string `json:"insecure_ssl"`
}

func (s *Service) CheckWebhookSettings(ctx context.Context, provider string) (WebhookSettings, error) {
	settings, err := s.WebhookSettings(ctx, provider)
	if err != nil || !settings.SecretConfigured {
		return settings, err
	}
	if provider == "linear" {
		return settings, nil
	}
	current, err := s.resolve(ctx, provider)
	if err != nil {
		return settings, err
	}
	key, _, err := current.githubKey()
	if err != nil {
		return settings, err
	}
	jwt, err := githubJWT(key, current.app(provider).ClientID, time.Now())
	if err != nil {
		return settings, err
	}
	var remote githubHookConfig
	err = current.github(ctx, jwt, "/app/hook/config", &remote)
	status := "configured"
	if err != nil || remote.URL != settings.CallbackURL || remote.ContentType != "json" || remote.InsecureSSL != "0" {
		status = "needs_access"
	}
	if _, e := s.pool.Exec(ctx, `UPDATE integration_webhook_settings SET configuration_status=$3 WHERE provider=$1 AND app_client_id=$2`, provider, current.app(provider).ClientID, status); e != nil {
		return settings, e
	}
	if err != nil {
		return settings, err
	}
	return s.WebhookSettings(ctx, provider)
}
