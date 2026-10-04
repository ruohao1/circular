package controlmcp_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/controlmcp"
)

type apiTransport func(*http.Request) (*http.Response, error)

func (transport apiTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return transport(r)
}

func mockAPIClient(t *testing.T, calls *atomic.Int32) *http.Client {
	t.Helper()
	return &http.Client{Transport: apiTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Host != "circular-api.local" || r.URL.Path != "/api/v1/projects" {
			t.Errorf("unexpected API request: %s %s", r.Method, r.URL)
		}
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			fmt.Fprint(recorder, `[{"id":"c4305d03-5c7c-43ec-b9b5-ea1151a6feea","name":"Example"}]`)
		} else if r.Method == http.MethodPost {
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input["name"] != "New project" {
				t.Errorf("unexpected project input: %v %v", input, err)
			}
			recorder.WriteHeader(http.StatusCreated)
			fmt.Fprint(recorder, `{"id":"c4305d03-5c7c-43ec-b9b5-ea1151a6feea","name":"New project"}`)
		} else {
			t.Errorf("unexpected API method: %s", r.Method)
			recorder.WriteHeader(http.StatusMethodNotAllowed)
		}
		return recorder.Result(), nil
	})}
}

func TestHTTPTransportUsesPublicAPIWithSeparateAccessModes(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		for _, protocol := range []string{"", "2025-11-25"} {
			t.Run(fmt.Sprintf("read_only=%v/protocol=%s", readOnly, protocol), func(t *testing.T) {
				var apiCalls atomic.Int32
				var activityMu sync.Mutex
				var activityNames []string
				var handler http.Handler
				endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					handler.ServeHTTP(w, r)
				}))
				t.Cleanup(endpoint.Close)
				address, _ := url.Parse(endpoint.URL)
				var err error
				handler, err = controlmcp.NewHTTP(controlmcp.Config{
					APIURL: "http://circular-api.local", ReadOnly: readOnly, HTTPClient: mockAPIClient(t, &apiCalls),
				}, controlmcp.HTTPOptions{
					AllowedHosts: []string{address.Host}, AllowedOrigins: []string{"http://localhost:5173"},
					OnActivity: func(name, version string) {
						activityMu.Lock()
						defer activityMu.Unlock()
						activityNames = append(activityNames, name+" "+version)
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				client, err := mcp.NewClient(&mcp.Implementation{Name: "HTTP test client", Version: "1"}, nil).Connect(t.Context(),
					&mcp.StreamableClientTransport{Endpoint: endpoint.URL, MaxRetries: -1}, &mcp.ClientSessionOptions{ProtocolVersion: protocol})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = client.Close() })
				listed, err := client.ListTools(t.Context(), nil)
				wantTools := 39
				if readOnly {
					wantTools = 20
				}
				if err != nil || len(listed.Tools) != wantTools {
					t.Fatalf("missing HTTP tool surface: %v %+v", err, listed)
				}
				if projects := call(t, client, "list_projects", object{}); projects["total"] != float64(1) {
					t.Fatal(projects)
				}
				if readOnly {
					rejects(t, client, "create_project", object{"name": "New project"}, "")
					if apiCalls.Load() != 1 {
						t.Fatal("read-only mutation reached API")
					}
				} else {
					if project := call(t, client, "create_project", object{"name": "New project"}); project["project"].(object)["name"] != "New project" {
						t.Fatal(project)
					}
					if apiCalls.Load() != 2 {
						t.Fatal("unexpected API call count", apiCalls.Load())
					}
				}
				activityMu.Lock()
				defer activityMu.Unlock()
				if len(activityNames) == 0 {
					t.Fatal("successful MCP connection did not record client activity")
				}
				for _, name := range activityNames {
					if name != "HTTP test client 1" {
						t.Fatalf("incorrect client identity: %q", name)
					}
				}
			})
		}
	}
}

func TestHTTPOriginAndHostChecksPrecedeToolDispatch(t *testing.T) {
	var apiCalls atomic.Int32
	handler, err := controlmcp.NewHTTP(controlmcp.Config{APIURL: "http://circular-api.local", HTTPClient: mockAPIClient(t, &apiCalls)}, controlmcp.HTTPOptions{
		AllowedHosts:   []string{"localhost:8000", "127.0.0.1:8000", "[::1]:8000"},
		AllowedOrigins: []string{"http://localhost:5173", "http://localhost:8000"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, host, method string
		origins            []string
		allowed            bool
	}{
		{name: "native client", host: "localhost:8000", allowed: true},
		{name: "IPv4", host: "127.0.0.1:8000", allowed: true},
		{name: "IPv6", host: "[::1]:8000", allowed: true},
		{name: "console", host: "localhost:8000", origins: []string{"http://localhost:5173"}, allowed: true},
		{name: "API origin", host: "localhost:8000", origins: []string{"http://localhost:8000"}, allowed: true},
		{name: "foreign origin", host: "localhost:8000", origins: []string{"https://evil.example"}},
		{name: "opaque origin", host: "localhost:8000", origins: []string{"null"}},
		{name: "empty origin", host: "localhost:8000", origins: []string{""}},
		{name: "repeated origin", host: "localhost:8000", origins: []string{"http://localhost:5173", "https://evil.example"}},
		{name: "path in origin", host: "localhost:8000", origins: []string{"http://localhost:5173/"}},
		{name: "empty fragment in origin", host: "localhost:8000", origins: []string{"http://localhost:5173#"}},
		{name: "credentials in origin", host: "localhost:8000", origins: []string{"http://evil@localhost:5173"}},
		{name: "foreign preflight", host: "localhost:8000", method: http.MethodOptions, origins: []string{"https://evil.example"}},
		{name: "rebinding", host: "evil.example:8000", origins: []string{"http://localhost:5173"}},
		{name: "foreign native host", host: "evil.example:8000"},
		{name: "wrong port", host: "localhost:8001"},
		{name: "malformed host", host: "localhost:8000#"},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := apiCalls.Load()
			method := test.method
			if method == "" {
				method = http.MethodPost
			}
			request := httptest.NewRequest(method, "http://localhost:8000/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_project","arguments":{"name":"New project"}}}`))
			request.Host = test.host
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			request.Header.Set("Mcp-Protocol-Version", "2025-11-25")
			// Forwarded headers must not turn a rejected request into a trusted one.
			request.Header.Set("X-Forwarded-Host", "localhost:8000")
			request.Header.Set("Forwarded", "host=localhost:8000;proto=http")
			if test.origins != nil {
				request.Header["Origin"] = test.origins
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if test.allowed {
				if response.Code != http.StatusOK || apiCalls.Load() != before+1 {
					t.Fatalf("trusted request failed: %d %s", response.Code, response.Body.String())
				}
			} else if response.Code != http.StatusForbidden || apiCalls.Load() != before {
				t.Fatalf("untrusted request reached MCP: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestHTTPRejectsOversizedRequestsAndUnsafeConfiguration(t *testing.T) {
	for _, options := range []controlmcp.HTTPOptions{
		{},
		{AllowedHosts: []string{"*"}},
		{AllowedHosts: []string{"http://localhost:8000"}},
		{AllowedHosts: []string{"localhost:8000"}, AllowedOrigins: []string{"*"}},
		{AllowedHosts: []string{"localhost:8000"}, AllowedOrigins: []string{"http://localhost:5173/path"}},
	} {
		if _, err := controlmcp.NewHTTP(controlmcp.Config{}, options); err == nil {
			t.Fatalf("accepted unsafe trust configuration: %+v", options)
		}
	}
	var apiCalls atomic.Int32
	handler, err := controlmcp.NewHTTP(controlmcp.Config{APIURL: "http://circular-api.local", HTTPClient: mockAPIClient(t, &apiCalls)}, controlmcp.HTTPOptions{AllowedHosts: []string{"localhost:8000"}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://localhost:8000/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_project","arguments":{"name":"`+strings.Repeat("x", 256<<10)+`"}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge || apiCalls.Load() != 0 {
		t.Fatalf("oversized request reached MCP: %d %s", response.Code, response.Body.String())
	}
}
