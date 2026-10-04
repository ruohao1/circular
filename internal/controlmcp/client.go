// Package controlmcp gives external coding agents a bounded interface to the
// Circular control plane. It uses the public HTTP contract, never the database,
// provider credentials, Docker socket, or a Run's private workspace.
package controlmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const DefaultAPIURL = "http://localhost:8000"
const DefaultWebURL = "http://localhost:5173"
const maxResponseBytes = 8 << 20

type Config struct {
	APIURL   string
	WebURL   string
	ReadOnly bool
	// HTTPClient can route requests through the public API handler in process.
	// The client is copied; Circular still bounds timeouts and rejects redirects.
	HTTPClient *http.Client
}

type client struct {
	apiURL, origin, webURL string
	readOnly               bool
	http                   *http.Client
}

func baseURL(value, fallback string, api bool) (string, error) {
	if value == "" {
		value = fallback
	}
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("use an absolute HTTP(S) URL without credentials, query parameters, or fragments")
	}
	path := strings.TrimRight(u.Path, "/")
	if path != "" && (!api || path != "/api/v1") {
		return "", errors.New("use the Circular origin, optionally ending in /api/v1 for the API URL")
	}
	u.Path, u.RawPath = "", ""
	return strings.TrimRight(u.String(), "/"), nil
}

func newClient(config Config) (*client, error) {
	origin, err := baseURL(config.APIURL, DefaultAPIURL, true)
	if err != nil {
		return nil, fmt.Errorf("invalid API URL: %w", err)
	}
	web, err := baseURL(config.WebURL, DefaultWebURL, false)
	if err != nil {
		return nil, fmt.Errorf("invalid console URL: %w", err)
	}
	httpClient := &http.Client{}
	if config.HTTPClient != nil {
		*httpClient = *config.HTTPClient
	}
	if httpClient.Timeout <= 0 || httpClient.Timeout > 30*time.Second {
		httpClient.Timeout = 30 * time.Second
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client{
		apiURL: origin + "/api/v1", origin: origin, webURL: web, readOnly: config.ReadOnly,
		http: httpClient,
	}, nil
}

func (c *client) request(ctx context.Context, method, address string, body any) ([]byte, error) {
	if c.readOnly && method != http.MethodGet && !c.readOnlyReviewRefresh(method, address) {
		return nil, errors.New("this Circular MCP connection is read-only")
	}
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("cannot construct Circular request")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if method != http.MethodGet {
			return nil, errors.New("Circular request did not complete; the action may have been saved. Inspect current state before retrying. For start_run or create_github_repository, reuse the same request_key and parameters")
		}
		return nil, errors.New("cannot reach Circular; check that the API is running and --api-url is reachable from the MCP process")
	}
	defer res.Body.Close()
	data, err = io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return nil, errors.New("Circular response was interrupted; for a launch or repository creation retry, reuse the same request_key and parameters")
	}
	if len(data) > maxResponseBytes {
		return nil, errors.New("Circular response exceeds 8 MiB; narrow the project/task filter or download the artifact through the console")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var problem struct {
			Detail json.RawMessage `json:"detail"`
		}
		message := http.StatusText(res.StatusCode)
		if json.Unmarshal(data, &problem) == nil && len(problem.Detail) > 0 {
			if json.Unmarshal(problem.Detail, &message) != nil {
				message = string(problem.Detail)
			}
		}
		if len(message) > 2000 {
			message = message[:2000] + "…"
		}
		return nil, fmt.Errorf("Circular HTTP %d: %s", res.StatusCode, message)
	}
	return data, nil
}

func (c *client) json(ctx context.Context, method, path string, body, result any) error {
	data, err := c.request(ctx, method, c.apiURL+path, body)
	if err != nil {
		return err
	}
	if json.Unmarshal(data, result) != nil {
		return errors.New("Circular returned an invalid JSON response")
	}
	return nil
}

// Older APIs ignore unknown body fields. Verify support before sending a keyed
// launch so connecting this binary to an older Circular cannot duplicate work.
func (c *client) requireKeyedLaunch(ctx context.Context) error {
	data, err := c.request(ctx, http.MethodGet, c.origin+"/openapi.json", nil)
	if err != nil {
		return err
	}
	var contract struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if json.Unmarshal(data, &contract) != nil || len(contract.Components.Schemas["RunCreate"].Properties["request_key"]) == 0 {
		return errors.New("upgrade Circular's API and run its migrations before launching through MCP; this API does not advertise retry-safe launches")
	}
	return nil
}

// Check verifies the configured API, including launch compatibility. It does not
// create records, start a model, or require provider credentials.
func Check(ctx context.Context, config Config) error {
	c, err := newClient(config)
	if err != nil {
		return err
	}
	var health map[string]any
	if err := c.json(ctx, http.MethodGet, "/health", nil, &health); err != nil {
		return err
	}
	if health["status"] != "ok" {
		return errors.New("Circular API is not healthy")
	}
	return c.requireKeyedLaunch(ctx)
}

// The sole POST permitted in a read-only connection is this provider-read action.
func (c *client) readOnlyReviewRefresh(method, address string) bool {
	prefix := c.apiURL + "/pr-reviews/"
	if method != http.MethodPost || !strings.HasPrefix(address, prefix) || !strings.HasSuffix(address, "/refresh") {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(address, prefix), "/refresh")
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil && id == parsed.String()
}
