package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/httpapi"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/testsupport"
)

func bundledMCP(t *testing.T, pool *pgxpool.Pool) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	address := "http://" + server.Listener.Addr().String()
	handler, err := httpapi.New(pool, httpapi.Config{ArtifactRoot: t.TempDir(), CORSOrigins: []string{"http://localhost:5173"}, Integrations: integrations.Config{APIURL: address}})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(server.Close)
	return server
}

func mcpSession(t *testing.T, endpoint, name string) *mcp.ClientSession {
	t.Helper()
	client, err := mcp.NewClient(&mcp.Implementation{Name: name, Version: "test"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: endpoint, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func mcpCall(t *testing.T, client *mcp.ClientSession, name string, input map[string]any) map[string]any {
	t.Helper()
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: input})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("%s failed: %+v", name, result.Content)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	return decode(t, data)
}

func mcpStatus(t *testing.T, server *httptest.Server) map[string]any {
	t.Helper()
	r, err := server.Client().Get(server.URL + "/api/v1/mcp/connection")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatal(r.Status)
	}
	var result map[string]any
	if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestBundledMCPConnectionAndModelCatalogWithoutDatabase(t *testing.T) {
	pool, err := pgxpool.New(t.Context(), "postgresql://test:test@127.0.0.1:1/unreachable")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	server := bundledMCP(t, pool)
	status := mcpStatus(t, server)
	if status["available"] != true || status["last_activity"] != nil || status["url"] != server.URL+"/mcp" || status["read_only_url"] != server.URL+"/mcp/read-only" {
		t.Fatal(status)
	}
	for _, access := range []string{"control", "read-only"} {
		path := "/mcp"
		if access == "read-only" {
			path += "/read-only"
		}
		client := mcpSession(t, server.URL+path, "Console test")
		result := mcpCall(t, client, "list_models", map[string]any{})
		if result["catalog"] == nil {
			t.Fatal(result)
		}
		activity := mcpStatus(t, server)["last_activity"].(map[string]any)
		if activity["client_name"] != "Console test" || activity["access"] != access || activity["at"] == "" {
			t.Fatal(activity)
		}
	}
	// Rejected origins cannot dispatch tools, even when wrapped in API CORS.
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	request.Header.Set("Origin", "https://untrusted.example")
	r, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Fatal(r.Status)
	}
}

func TestBundledMCPMutationsShareAPIStateAndReadOnlyCannotWrite(t *testing.T) {
	server := bundledMCP(t, testsupport.Database(t))
	full := mcpSession(t, server.URL+"/mcp", "HTTP workflow test")
	project := mcpCall(t, full, "create_project", map[string]any{"name": "HTTP MCP project"})["project"].(map[string]any)
	agent := mcpCall(t, full, "create_agent", map[string]any{"project_id": project["id"], "name": "Simulated engineer", "instructions": "Test the workflow", "backend": "fake"})["agent"].(map[string]any)
	task := mcpCall(t, full, "create_task", map[string]any{"project_id": project["id"], "title": "Test HTTP launch"})["task"].(map[string]any)
	launch := map[string]any{"task_id": task["id"], "agent_id": agent["id"], "request_key": "http-connection-test"}
	run := mcpCall(t, full, "start_run", launch)["run"].(map[string]any)
	retry := mcpCall(t, full, "start_run", launch)["run"].(map[string]any)
	if run["id"] != retry["id"] {
		t.Fatal("HTTP launch retry created a second attempt")
	}
	readOnly := mcpSession(t, server.URL+"/mcp/read-only", "HTTP inspection test")
	projects := mcpCall(t, readOnly, "list_projects", map[string]any{})
	if projects["total"] != float64(1) {
		t.Fatal(projects)
	}
	result, err := readOnly.CallTool(t.Context(), &mcp.CallToolParams{Name: "create_project", Arguments: map[string]any{"name": "Must not exist"}})
	if err == nil && !result.IsError {
		t.Fatal("read-only connection permitted mutation")
	}
	var runs []map[string]any
	if err := json.Unmarshal(fixture{server: server}.request(t, "GET", "/api/v1/runs", "", 200), &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0]["id"] != run["id"] {
		t.Fatal("MCP and console API do not share the same run", runs)
	}
	projects = mcpCall(t, full, "list_projects", map[string]any{})
	if projects["total"] != float64(1) {
		t.Fatal("read-only request changed projects", projects)
	}
	mcpCall(t, full, "cancel_run", map[string]any{"run_id": run["id"]})
}
