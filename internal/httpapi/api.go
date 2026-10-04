// Package httpapi exposes Circular's contract-first resource and replay interface.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/contracts"
	"github.com/ruohao1/circular/internal/agents"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/backends"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/postgres"
)

type Config struct {
	ArtifactRoot        string
	RepositoryCacheRoot string
	CORSOrigins         []string
	SSEPollInterval     time.Duration
	Integrations        integrations.Config
}

type api struct {
	pool         *pgxpool.Pool
	content      *artifacts.LocalStore
	config       Config
	integrations *integrations.Service
}

// New constructs the public HTTP handler without connecting or allocating files.
func New(pool *pgxpool.Pool, config Config) (http.Handler, error) {
	if pool == nil {
		return nil, errors.New("database pool is required")
	}
	content, err := artifacts.NewLocalStore(config.ArtifactRoot)
	if err != nil {
		return nil, err
	}
	if config.SSEPollInterval == 0 {
		config.SSEPollInterval = 500 * time.Millisecond
	}
	if config.SSEPollInterval < 0 {
		return nil, errors.New("SSE poll interval must be positive")
	}
	config.CORSOrigins = append([]string(nil), config.CORSOrigins...)
	connections, err := integrations.New(pool, integrationConfig(config))
	if err != nil {
		return nil, err
	}
	a := &api{pool: pool, content: content, config: config, integrations: connections}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(contracts.OpenAPI)
	})
	mux.HandleFunc("GET /docs", docs)
	mux.HandleFunc("GET /redoc", docs)
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]string{"status": "ok"}) })
	for _, resource := range []struct{ table, schema string }{{"projects", "Project"}, {"repositories", "Repository"}, {"agents", "Agent"}, {"tasks", "Task"}} {
		mux.HandleFunc("POST /api/v1/"+resource.table, func(w http.ResponseWriter, r *http.Request) { a.create(w, r, resource.table, resource.schema) })
		mux.HandleFunc("GET /api/v1/"+resource.table, func(w http.ResponseWriter, r *http.Request) { a.list(w, r, resource.table, resource.schema+"Read") })
	}
	mux.HandleFunc("POST /api/v1/runs", a.createRun)
	mux.HandleFunc("GET /api/v1/backends/codex/models", codexModels)
	mux.HandleFunc("PATCH /api/v1/agents/{agent_id}", a.updateAgent)
	mux.HandleFunc("POST /api/v1/projects/{project_id}/discovery", a.createDiscoveryTask)
	mux.HandleFunc("GET /api/v1/tasks/{task_id}", a.task)
	a.integrationRoutes(mux)
	mux.HandleFunc("GET /api/v1/runs", func(w http.ResponseWriter, r *http.Request) { a.list(w, r, "runs", "RunRead") })
	mux.HandleFunc("GET /api/v1/runs/{run_id}", a.run)
	mux.HandleFunc("POST /api/v1/runs/{run_id}/cancel", a.cancel)
	mux.HandleFunc("GET /api/v1/runs/{run_id}/execution", a.execution)
	mux.HandleFunc("GET /api/v1/runs/{run_id}/agent-proposals", a.agentProposals)
	mux.HandleFunc("POST /api/v1/runs/{run_id}/agent-proposals", a.saveAgentProposal)
	mux.HandleFunc("POST /api/v1/runs/{run_id}/agent-proposals/{proposal_id}/create", a.createProposedAgent)
	mux.HandleFunc("POST /api/v1/runs/{run_id}/agent-proposals/{proposal_id}/dismiss", a.dismissAgentProposal)
	mux.HandleFunc("GET /api/v1/runs/{run_id}/events", a.events)
	mux.HandleFunc("GET /api/v1/runs/{run_id}/events/stream", a.stream)
	mux.HandleFunc("GET /api/v1/runs/{run_id}/artifacts/{artifact_id}/content", a.artifact)
	if err := a.mcpRoutes(mux); err != nil {
		return nil, fmt.Errorf("initialize MCP: %w", err)
	}
	return a.cors(mux), nil
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func problem(w http.ResponseWriter, status int, detail string) {
	respond(w, status, map[string]string{"detail": detail})
}

func dbError(w http.ResponseWriter, err error, name string) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, pgx.ErrNoRows) {
		problem(w, 404, name+" not found")
	} else {
		problem(w, 500, "Internal Server Error")
	}
	return true
}

type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func projection(schema, alias string) string {
	parts := []string{}
	for _, name := range sortedFields(contractSchemas[schema].Properties) {
		parts = append(parts, "'"+name+"',"+alias+"."+pgx.Identifier{name}.Sanitize())
	}
	return "json_build_object(" + strings.Join(parts, ",") + ")"
}

func record(ctx context.Context, q queryer, table, schema, where string, args ...any) (json.RawMessage, error) {
	var data json.RawMessage
	err := q.QueryRow(ctx, "SELECT "+projection(schema, "t")+" FROM "+table+" t "+where, args...).Scan(&data)
	return data, err
}

func records(ctx context.Context, q queryer, table, schema, where string, args ...any) ([]json.RawMessage, error) {
	rows, err := q.Query(ctx, "SELECT "+projection(schema, "t")+" FROM "+table+" t "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	data := []json.RawMessage{}
	for rows.Next() {
		var value json.RawMessage
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		data = append(data, value)
	}
	return data, rows.Err()
}

func rollback(ctx context.Context, tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func insert(ctx context.Context, tx pgx.Tx, table, schema string, values map[string]any) (json.RawMessage, error) {
	values["id"] = uuid.NewString()
	columns, placeholders, args := []string{}, []string{}, []any{}
	for _, name := range sortedFields(values) {
		columns = append(columns, pgx.Identifier{name}.Sanitize())
		args = append(args, values[name])
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
	}
	var result json.RawMessage
	err := tx.QueryRow(ctx, "INSERT INTO "+table+" AS t ("+strings.Join(columns, ",")+") VALUES ("+strings.Join(placeholders, ",")+") RETURNING "+projection(schema, "t"), args...).Scan(&result)
	return result, err
}

func (a *api) create(w http.ResponseWriter, r *http.Request, table, schema string) {
	values, ok := body(w, r, schema+"Create")
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := a.pool.Begin(ctx)
	if dbError(w, err, schema) {
		return
	}
	defer rollback(ctx, tx)
	if table != "projects" {
		var id uuid.UUID
		err = tx.QueryRow(ctx, "SELECT id FROM projects WHERE id=$1", values["project_id"]).Scan(&id)
		if dbError(w, err, "project") {
			return
		}
	}
	if table == "agents" {
		if values["backend"] != "fake" && values["backend"] != "codex" {
			problem(w, 422, "supported backends are fake and codex")
			return
		}
		if values["backend"] == "codex" {
			config, _ := json.Marshal(values["backend_config"])
			if _, err := backends.ValidateCodexConfig(config); err != nil {
				problem(w, 422, err.Error())
				return
			}
		}
		values["enabled"] = true
	}
	if table == "tasks" {
		if values["repository_id"] != nil {
			var project uuid.UUID
			err = tx.QueryRow(ctx, "SELECT project_id FROM repositories WHERE id=$1", values["repository_id"]).Scan(&project)
			if dbError(w, err, "repository") {
				return
			}
			if project.String() != values["project_id"] {
				problem(w, 422, "repository belongs to another project")
				return
			}
		}
		values["status"] = "open"
	}
	result, err := insert(ctx, tx, table, schema+"Read", values)
	if dbError(w, err, schema) {
		return
	}
	if table == "projects" {
		if _, err := agents.EnsureDiscovery(ctx, tx, values["id"].(string)); dbError(w, err, "discovery agent") {
			return
		}
		reviewer, err := agents.EnsureReviewer(ctx, tx, values["id"].(string))
		if dbError(w, err, "PR reviewer") {
			return
		}
		if _, err = tx.Exec(ctx, `INSERT INTO pr_review_settings(project_id,reviewer_id) VALUES($1,$2) ON CONFLICT(project_id) DO NOTHING`, values["id"], reviewer); dbError(w, err, "PR review settings") {
			return
		}
	}
	if dbError(w, tx.Commit(ctx), schema) {
		return
	}
	respond(w, 201, result)
}

func (a *api) createRun(w http.ResponseWriter, r *http.Request) {
	values, ok := body(w, r, "RunCreate")
	if !ok {
		return
	}
	if key, supplied := values["request_key"].(string); supplied {
		key = strings.TrimSpace(key)
		if key == "" {
			problem(w, 422, "request_key must not be blank")
			return
		}
		values["request_key"] = key
	}
	ctx := r.Context()
	tx, err := a.pool.Begin(ctx)
	if dbError(w, err, "run") {
		return
	}
	defer rollback(ctx, tx)
	refs, _ := json.Marshal(values["external_refs"])
	key, _ := values["request_key"].(string)
	launch, err := postgres.CreateRun(ctx, tx, postgres.RunLaunchInput{TaskID: uuid.MustParse(values["task_id"].(string)), AgentID: uuid.MustParse(values["agent_id"].(string)), RequestKey: key, ExternalRefs: refs})
	if errors.Is(err, postgres.ErrRunLaunchConflict) {
		problem(w, 409, err.Error())
		return
	}
	if errors.Is(err, postgres.ErrRunLaunchInput) {
		problem(w, 422, strings.TrimPrefix(err.Error(), postgres.ErrRunLaunchInput.Error()+": "))
		return
	}
	if dbError(w, err, "run") {
		return
	}
	result, err := record(ctx, tx, "runs", "RunRead", "WHERE t.id=$1", launch.RunID)
	if dbError(w, err, "run") || dbError(w, tx.Commit(ctx), "run") {
		return
	}
	status := 200
	if launch.Created {
		status = 201
	}
	respond(w, status, result)
}

func (a *api) list(w http.ResponseWriter, r *http.Request, table, schema string) {
	where, args := []string{}, []any{}
	for _, name := range []string{"project_id", "task_id"} {
		value, exists := r.URL.Query()[name]
		if !exists || name == "task_id" && table != "runs" || table == "projects" {
			continue
		}
		id, ok := identifier(w, value[0], "query", name)
		if !ok {
			return
		}
		args = append(args, id)
		if name == "project_id" && table == "runs" {
			where = append(where, fmt.Sprintf("t.task_id IN (SELECT id FROM tasks WHERE project_id=$%d)", len(args)))
		} else {
			where = append(where, fmt.Sprintf("t.%s=$%d", name, len(args)))
		}
	}
	condition := ""
	if len(where) > 0 {
		condition = "WHERE " + strings.Join(where, " AND ") + " "
	}
	data, err := records(r.Context(), a.pool, table, schema, condition+"ORDER BY t.created_at DESC", args...)
	if !dbError(w, err, table) {
		respond(w, 200, data)
	}
}

func (a *api) run(w http.ResponseWriter, r *http.Request) {
	id, ok := identifier(w, r.PathValue("run_id"), "path", "run_id")
	if !ok {
		return
	}
	data, err := record(r.Context(), a.pool, "runs", "RunRead", "WHERE t.id=$1", id)
	if !dbError(w, err, "run") {
		respond(w, 200, data)
	}
}

func (a *api) cancel(w http.ResponseWriter, r *http.Request) {
	id, ok := identifier(w, r.PathValue("run_id"), "path", "run_id")
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := a.pool.Begin(ctx)
	if dbError(w, err, "run") {
		return
	}
	defer rollback(ctx, tx)
	err = postgres.CancelRun(ctx, tx, id, "api")
	if errors.Is(err, postgres.ErrRunCancelConflict) {
		problem(w, 409, err.Error())
		return
	}
	if dbError(w, err, "run") {
		return
	}

	data, err := record(ctx, tx, "runs", "RunRead", "WHERE t.id=$1", id)
	if dbError(w, err, "run") {
		return
	}
	if dbError(w, tx.Commit(ctx), "run") {
		return
	}
	respond(w, 200, data)
}

func (a *api) execution(w http.ResponseWriter, r *http.Request) {
	id, ok := identifier(w, r.PathValue("run_id"), "path", "run_id")
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := a.pool.Begin(ctx)
	if dbError(w, err, "run") {
		return
	}
	defer rollback(ctx, tx)
	run, err := record(ctx, tx, "runs", "RunRead", "WHERE t.id=$1 FOR SHARE", id)
	if dbError(w, err, "run") {
		return
	}
	task, err := record(ctx, tx, "tasks", "TaskRead", "WHERE t.id=(SELECT task_id FROM runs WHERE id=$1)", id)
	if dbError(w, err, "task") {
		return
	}
	agent, err := record(ctx, tx, "agents", "AgentRead", "WHERE t.id=(SELECT agent_id FROM runs WHERE id=$1)", id)
	if dbError(w, err, "agent") {
		return
	}
	workspace, err := record(ctx, tx, "workspaces", "WorkspaceRead", "WHERE t.run_id=$1", id)
	if !errors.Is(err, pgx.ErrNoRows) && dbError(w, err, "workspace") {
		return
	}
	retained, err := records(ctx, tx, "artifacts", "ArtifactRead", "WHERE t.run_id=$1 ORDER BY t.created_at,t.id", id)
	if dbError(w, err, "artifact") {
		return
	}
	var last int64
	err = tx.QueryRow(ctx, "SELECT COALESCE(MAX(sequence),0) FROM events WHERE run_id=$1", id).Scan(&last)
	if dbError(w, err, "event") {
		return
	}
	usage := json.RawMessage(`{"input_tokens":0,"output_tokens":0}`)
	var latest json.RawMessage
	err = tx.QueryRow(ctx, "SELECT data FROM events WHERE run_id=$1 AND type='usage.updated' ORDER BY sequence DESC LIMIT 1", id).Scan(&latest)
	if err == nil {
		usage = latest
	} else if !errors.Is(err, pgx.ErrNoRows) {
		dbError(w, err, "usage")
		return
	}
	var reviewID *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT (SELECT id FROM pr_reviews WHERE run_id=$1)`, id).Scan(&reviewID); dbError(w, err, "PR review") {
		return
	}
	if dbError(w, tx.Commit(ctx), "run") {
		return
	}
	respond(w, 200, map[string]any{"pr_review_id": reviewID, "run": run, "task": task, "agent": agent, "workspace": workspace, "artifacts": retained, "usage": usage, "last_event_sequence": last})
}

func integerQuery(w http.ResponseWriter, r *http.Request, name string, fallback, min, max int64) (int64, bool) {
	values, exists := r.URL.Query()[name]
	if !exists {
		return fallback, true
	}
	value, err := strconv.ParseInt(strings.TrimSpace(values[0]), 10, 64)
	if err != nil {
		invalid(w, "query", name, "int_parsing", "Input should be a valid integer")
		return 0, false
	}
	if value < min {
		invalid(w, "query", name, "greater_than_equal", fmt.Sprintf("Input should be greater than or equal to %d", min))
		return 0, false
	}
	if max > 0 && value > max {
		invalid(w, "query", name, "less_than_equal", fmt.Sprintf("Input should be less than or equal to %d", max))
		return 0, false
	}
	return value, true
}

func (a *api) exists(w http.ResponseWriter, r *http.Request, id uuid.UUID) bool {
	var found uuid.UUID
	return !dbError(w, a.pool.QueryRow(r.Context(), "SELECT id FROM runs WHERE id=$1", id).Scan(&found), "run")
}

func (a *api) events(w http.ResponseWriter, r *http.Request) {
	id, ok := identifier(w, r.PathValue("run_id"), "path", "run_id")
	if !ok {
		return
	}
	after, ok := integerQuery(w, r, "after", 0, 0, 0)
	if !ok {
		return
	}
	limit, ok := integerQuery(w, r, "limit", 200, 1, 1000)
	if !ok {
		return
	}
	if !a.exists(w, r, id) {
		return
	}
	data, err := records(r.Context(), a.pool, "events", "EventRead", "WHERE t.run_id=$1 AND t.sequence>$2 ORDER BY t.sequence LIMIT $3", id, after, limit)
	if !dbError(w, err, "event") {
		respond(w, 200, data)
	}
}
