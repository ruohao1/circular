package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/ruohao1/circular/internal/backends"
	"github.com/ruohao1/circular/internal/codexconfig"
)

func codexModels(w http.ResponseWriter, r *http.Request) {
	respond(w, 200, map[string]any{"default_model": codexconfig.DefaultModel, "models": codexconfig.Models()})
}

func (a *api) updateAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := identifier(w, r.PathValue("agent_id"), "path", "agent_id")
	if !ok {
		return
	}
	values, ok := body(w, r, "AgentUpdate")
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := a.pool.Begin(ctx)
	if dbError(w, err, "agent") {
		return
	}
	defer rollback(ctx, tx)
	var backend string
	err = tx.QueryRow(ctx, "SELECT backend FROM agents WHERE id=$1 FOR UPDATE", id).Scan(&backend)
	if dbError(w, err, "agent") {
		return
	}
	if backend != "codex" {
		problem(w, 422, "model settings are only available for Codex agents")
		return
	}
	config, _ := json.Marshal(values["backend_config"])
	settings, err := backends.ValidateCodexConfig(config)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	config, _ = json.Marshal(settings)
	_, err = tx.Exec(ctx, "UPDATE agents SET backend_config=$2,updated_at=now() WHERE id=$1", id, config)
	if dbError(w, err, "agent") {
		return
	}
	data, err := record(ctx, tx, "agents", "AgentRead", "WHERE t.id=$1", id)
	if dbError(w, err, "agent") || dbError(w, tx.Commit(ctx), "agent") {
		return
	}
	respond(w, 200, data)
}
