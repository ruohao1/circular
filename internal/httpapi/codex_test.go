package httpapi_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/ruohao1/circular/internal/testsupport"
)

func TestCodexAgentRegistrationAndRunPreserveModelConfiguration(t *testing.T) {
	f := setup(t)
	project := f.create(t, "projects", map[string]any{"name": "Codex fixture"})
	for index, config := range []map[string]any{{"model": "gpt-fixture-codex"}, {"model": "gpt-6-astra", "reasoning_effort": "ultra"}, {}} {
		agent := f.create(t, "agents", map[string]any{"project_id": project["id"], "name": "Codex engineer " + strconv.Itoa(index), "backend": "codex", "backend_config": config})
		if agent["backend"] != "codex" || agent["enabled"] != true {
			t.Fatal(agent)
		}
		testsupport.AssertJSON(t, agent["backend_config"], config)
		task := f.create(t, "tasks", map[string]any{"project_id": project["id"], "title": "Use configured Codex agent"})
		run := f.create(t, "runs", map[string]any{"task_id": task["id"], "agent_id": agent["id"]})
		if run["backend"] != "codex" || run["status"] != "queued" {
			t.Fatal("Run did not preserve the Codex backend", run)
		}
		var storedBackend string
		var storedConfig []byte
		if err := f.pool.QueryRow(t.Context(), `SELECT backend,backend_config FROM agents WHERE id=$1`, agent["id"]).Scan(&storedBackend, &storedConfig); err != nil {
			t.Fatal(err)
		}
		if storedBackend != "codex" {
			t.Fatal("Codex backend was not persisted")
		}
		testsupport.AssertJSON(t, decode(t, storedConfig), config)
	}
}

func TestCodexAgentRegistrationRejectsUnsupportedAndSecretConfiguration(t *testing.T) {
	f := setup(t)
	project := f.create(t, "projects", map[string]any{"name": "Codex validation fixture"})
	const secret = "private-key-never-reflect"
	for _, test := range []struct {
		name, backend string
		config        map[string]any
	}{
		{"unknown_backend", secret, map[string]any{}},
		{"api_key", "codex", map[string]any{"api_key": secret}},
		{"environment", "codex", map[string]any{"env": map[string]any{"CODEX_API_KEY": secret}}},
		{"command", "codex", map[string]any{"command": []string{secret}}},
		{"secret_field_name", "codex", map[string]any{secret: true}},
		{"invalid_model", "codex", map[string]any{"model": secret + " invalid"}},
		{"flag_model", "codex", map[string]any{"model": "--" + secret}},
		{"model_type", "codex", map[string]any{"model": map[string]any{"secret": secret}}},
		{"model_null", "codex", map[string]any{"model": nil}},
		{"model_empty", "codex", map[string]any{"model": ""}},
		{"effort_invalid", "codex", map[string]any{"reasoning_effort": secret}},
		{"effort_null", "codex", map[string]any{"reasoning_effort": nil}},
		{"effort_type", "codex", map[string]any{"reasoning_effort": map[string]any{"secret": secret}}},
		{"effort_unsupported", "codex", map[string]any{"model": "gpt-5.6-luna", "reasoning_effort": "ultra"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"project_id": project["id"], "name": "Invalid agent", "backend": test.backend, "backend_config": test.config})
			if err != nil {
				t.Fatal(err)
			}
			response := f.request(t, "POST", "/api/v1/agents", string(body), 422)
			if strings.Contains(string(response), secret) {
				t.Fatal("Agent validation reflected a private field name or value")
			}
			var count int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agents WHERE project_id=$1 AND preset IS NULL`, project["id"]).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("rejected Agent configuration was persisted")
			}
		})
	}
}

func TestCodexModelCatalogAndAgentSettings(t *testing.T) {
	f := setup(t)
	catalog := decode(t, f.request(t, "GET", "/api/v1/backends/codex/models", "", 200))
	if catalog["default_model"] != "gpt-6-astra" {
		t.Fatal(catalog)
	}
	models := catalog["models"].([]any)
	if len(models) == 0 || models[0].(map[string]any)["id"] != "gpt-6-astra" {
		t.Fatal(catalog)
	}
	project := f.create(t, "projects", map[string]any{"name": "Model selection fixture"})
	var agents []map[string]any
	if err := json.Unmarshal(f.request(t, "GET", "/api/v1/agents?project_id="+project["id"].(string), "", 200), &agents); err != nil {
		t.Fatal(err)
	}
	agent := agents[0]
	path := "/api/v1/agents/" + agent["id"].(string)
	updated := decode(t, f.request(t, "PATCH", path, `{"backend_config":{"model":"gpt-5.6-sol","reasoning_effort":"high"}}`, 200))
	testsupport.AssertJSON(t, updated["backend_config"], map[string]any{"model": "gpt-5.6-sol", "reasoning_effort": "high"})
	for _, name := range []string{"id", "project_id", "name", "instructions", "enabled", "preset", "created_at", "backend"} {
		testsupport.AssertJSON(t, updated[name], agent[name])
	}
	for _, body := range []string{`{}`, `{"backend_config":null}`, `{"backend_config":{"model":"gpt-5.6-luna","reasoning_effort":"ultra"}}`, `{"backend_config":{"reasoning_effort":"private-secret"}}`, `{"backend_config":{"api_key":"private-secret"}}`} {
		response := f.request(t, "PATCH", path, body, 422)
		if strings.Contains(string(response), "private-secret") {
			t.Fatal("validation reflected private settings")
		}
	}
	var config []byte
	if err := f.pool.QueryRow(t.Context(), "SELECT backend_config FROM agents WHERE id=$1", agent["id"]).Scan(&config); err != nil {
		t.Fatal(err)
	}
	testsupport.AssertJSON(t, decode(t, config), updated["backend_config"])
	reset := decode(t, f.request(t, "PATCH", path, `{"backend_config":{}}`, 200))
	testsupport.AssertJSON(t, reset["backend_config"], map[string]any{"model": "gpt-6-astra", "reasoning_effort": "low"})
	fake := f.create(t, "agents", map[string]any{"name": "Fake", "project_id": project["id"]})
	f.request(t, "PATCH", "/api/v1/agents/"+fake["id"].(string), `{"backend_config":{}}`, 422)
	f.request(t, "PATCH", "/api/v1/agents/00000000-0000-0000-0000-000000000000", `{"backend_config":{}}`, 404)
	var runs int
	if err := f.pool.QueryRow(t.Context(), "SELECT count(*) FROM runs WHERE task_id IN (SELECT id FROM tasks WHERE project_id=$1)", project["id"]).Scan(&runs); err != nil || runs != 0 {
		t.Fatal("model settings must not start a run", err, runs)
	}
}
