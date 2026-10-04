package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type tokenSet struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scopes       []string  `json:"scopes,omitempty"`
}

type account struct {
	ID     string `json:"account_id"`
	Name   string `json:"account_name"`
	URL    string `json:"account_url"`
	Status string `json:"status"`
}

func (s *Service) authorizeURL(provider, state, challenge string) string {
	values := url.Values{"client_id": {s.app(provider).ClientID}, "redirect_uri": {s.CallbackURL(provider)}, "state": {state}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	if provider == "github" {
		return s.config.GitHubURL + "/login/oauth/authorize?" + values.Encode()
	}
	values.Set("response_type", "code")
	values.Set("scope", "read,comments:create")
	values.Set("prompt", "consent")
	return s.config.LinearURL + "/oauth/authorize?" + values.Encode()
}

func (s *Service) token(ctx context.Context, provider, code, verifier, refresh string) (tokenSet, error) {
	app := s.app(provider)
	values := url.Values{"client_id": {app.ClientID}}
	if app.ClientSecret != "" {
		values.Set("client_secret", app.ClientSecret)
	}
	if refresh != "" {
		values.Set("grant_type", "refresh_token")
		values.Set("refresh_token", refresh)
	} else {
		values.Set("grant_type", "authorization_code")
		values.Set("code", code)
		values.Set("redirect_uri", s.CallbackURL(provider))
		values.Set("code_verifier", verifier)
	}
	endpoint := s.config.LinearAPIURL + "/oauth/token"
	if provider == "github" {
		endpoint = s.config.GitHubURL + "/login/oauth/access_token"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return tokenSet{}, ErrProvider
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var response struct {
		AccessToken  string          `json:"access_token"`
		RefreshToken string          `json:"refresh_token"`
		ExpiresIn    int64           `json:"expires_in"`
		TokenType    string          `json:"token_type"`
		Error        string          `json:"error"`
		Scope        json.RawMessage `json:"scope"`
	}
	if err := s.do(req, &response); err != nil {
		return tokenSet{}, err
	}
	if response.Error != "" {
		return tokenSet{}, ErrReconnect
	}
	if !safeToken(response.AccessToken) || !strings.EqualFold(response.TokenType, "bearer") || response.ExpiresIn < 0 || response.ExpiresIn > 366*24*3600 {
		return tokenSet{}, ErrProvider
	}
	if response.RefreshToken != "" && !safeToken(response.RefreshToken) {
		return tokenSet{}, ErrProvider
	}
	scopes, err := parseScopes(response.Scope)
	if err != nil {
		return tokenSet{}, err
	}
	result := tokenSet{AccessToken: response.AccessToken, RefreshToken: response.RefreshToken, Scopes: scopes}
	if response.ExpiresIn > 0 {
		result.ExpiresAt = time.Now().Add(time.Duration(response.ExpiresIn) * time.Second)
	}
	return result, nil
}

// Linear returns a space-delimited string today and an array for older apps.
// Missing scope means unknown, never an inferred comment grant.
func parseScopes(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var values []string
	var text string
	if json.Unmarshal(raw, &text) == nil {
		values = strings.Fields(strings.ReplaceAll(text, ",", " "))
	} else if json.Unmarshal(raw, &values) != nil {
		return nil, ErrProvider
	}
	if len(values) > 50 {
		return nil, ErrProvider
	}
	for _, v := range values {
		if !safeToken(v) || len(v) > 100 {
			return nil, ErrProvider
		}
	}
	if values == nil {
		values = []string{}
	}
	return values, nil
}
func commentGrant(scopes []string) bool {
	for _, scope := range scopes {
		if scope == "comments:create" || scope == "write" || scope == "admin" {
			return true
		}
	}
	return false
}

func safeToken(value string) bool {
	if len(value) == 0 || len(value) > 8192 {
		return false
	}
	for _, r := range value {
		if r <= 32 || r >= 127 {
			return false
		}
	}
	return true
}

func (s *Service) do(req *http.Request, output any) error {
	req.Header.Set("User-Agent", "Circular")
	response, err := s.client.Do(req)
	if err != nil {
		return ErrProvider
	}
	defer response.Body.Close()
	if limited := providerRetryError(response.StatusCode, response.Header); limited != nil {
		return limited
	}
	if response.StatusCode == http.StatusUnauthorized {
		return ErrReconnect
	}
	if response.StatusCode == http.StatusForbidden && response.Header.Get("X-RateLimit-Remaining") != "0" {
		return ErrAccess
	}
	if response.StatusCode == http.StatusNotFound {
		return ErrAccess
	}
	if response.StatusCode == http.StatusBadRequest {
		data, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
		if err != nil || len(data) > 64*1024 {
			return ErrProvider
		}
		if req.Method == http.MethodPost && req.URL.String() == s.config.LinearAPIURL+"/graphql" {
			var detail linearResponse
			if json.Unmarshal(data, &detail) != nil {
				return ErrProvider
			}
			return detail.rejection()
		}
		var detail struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		if json.Unmarshal(data, &detail) != nil {
			return ErrProvider
		}
		if detail.Error == "invalid_grant" || detail.Error == "bad_refresh_token" || detail.Error == "bad_verification_code" {
			return ErrReconnect
		}
		// Linear reports revoked refresh tokens as invalid_request. Reconnect
		// the existing identity instead of retrying a permanently revoked grant.
		if req.Method == http.MethodPost && req.URL.String() == s.config.LinearAPIURL+"/oauth/token" && detail.Error == "invalid_request" && detail.ErrorDescription == "Refresh token revoked" {
			return ErrReconnect
		}
		return ErrProvider
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ErrProvider
	}
	if output == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil || len(data) > 4*1024*1024 || json.Unmarshal(data, output) != nil {
		return ErrProvider
	}
	return nil
}

func (s *Service) github(ctx context.Context, token, path string, output any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", s.config.GitHubAPIURL+path, nil)
	if err != nil {
		return ErrProvider
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	return s.do(req, output)
}

type linearResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Path       json.RawMessage `json:"path"`
		Extensions struct {
			Code string `json:"code"`
		} `json:"extensions"`
	} `json:"errors"`
}

func (r linearResponse) rejection() error {
	// Partial data or a field path means execution may have started. Keep
	// these writes fenced for receipt recovery, even when one error denies access.
	if len(r.Errors) == 0 || (len(r.Data) != 0 && string(r.Data) != "null") {
		return ErrProvider
	}
	result := ErrAccess
	for _, e := range r.Errors {
		if len(e.Path) != 0 && string(e.Path) != "null" {
			return ErrProvider
		}
		switch e.Extensions.Code {
		case "AUTHENTICATION_ERROR", "UNAUTHENTICATED":
			result = ErrReconnect
		case "FORBIDDEN":
		default:
			return ErrProvider
		}
	}
	return result
}

func (s *Service) linear(ctx context.Context, token, query string, variables map[string]any, output any) error {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": variables})
	req, err := http.NewRequestWithContext(ctx, "POST", s.config.LinearAPIURL+"/graphql", bytes.NewReader(body))
	if err != nil {
		return ErrProvider
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	var response linearResponse
	if err := s.do(req, &response); err != nil {
		return err
	}
	if len(response.Errors) != 0 {
		return response.rejection()
	}
	if len(response.Data) == 0 || string(response.Data) == "null" || json.Unmarshal(response.Data, output) != nil {
		return ErrProvider
	}
	return nil
}

func (s *Service) revoke(ctx context.Context, provider string, token tokenSet) error {
	var req *http.Request
	var err error
	if provider == "github" {
		body, _ := json.Marshal(map[string]string{"access_token": token.AccessToken})
		req, err = http.NewRequestWithContext(ctx, "DELETE", s.config.GitHubAPIURL+"/applications/"+url.PathEscape(s.config.GitHub.ClientID)+"/token", bytes.NewReader(body))
		if err == nil {
			req.SetBasicAuth(s.config.GitHub.ClientID, s.config.GitHub.ClientSecret)
			req.Header.Set("Content-Type", "application/json")
		}
	} else {
		value, hint := token.RefreshToken, "refresh_token"
		if value == "" {
			value, hint = token.AccessToken, "access_token"
		}
		body := url.Values{"token": {value}, "token_type_hint": {hint}}
		req, err = http.NewRequestWithContext(ctx, "POST", s.config.LinearAPIURL+"/oauth/revoke", strings.NewReader(body.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	if err != nil {
		return ErrProvider
	}
	return s.do(req, nil)
}
