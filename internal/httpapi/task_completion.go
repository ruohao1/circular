package httpapi

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
)

func (a *api) completionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/v1/tasks/{task_id}/completion-policy", a.setCompletionPolicy)
	mux.HandleFunc("GET /api/v1/tasks/{task_id}/completion", a.taskCompletion)
	mux.HandleFunc("POST /api/v1/tasks/{task_id}/completion/refresh", a.taskCompletion)
	mux.HandleFunc("PUT /api/v1/tasks/{task_id}/pull-request", a.bindTaskPullRequest)
	mux.HandleFunc("GET /api/v1/repositories/{repository_id}/completion-checks", a.completionChecks)
	mux.HandleFunc("PUT /api/v1/repositories/{repository_id}/completion-checks", a.completionChecks)
	mux.HandleFunc("GET /api/v1/tasks/{task_id}/pr-reviews/prepare", a.preparePRReview)
	mux.HandleFunc("GET /api/v1/tasks/{task_id}/pr-reviews", a.listPRReviews)
	mux.HandleFunc("POST /api/v1/tasks/{task_id}/pr-reviews", a.launchPRReview)
}
func completionBody(w http.ResponseWriter, r *http.Request, out any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	if err != nil {
		problem(w, 413, "request body exceeds 128 KiB")
		return false
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' || prreviews.DecodeStrict(raw, out) != nil {
		invalid(w, "body", "", "json_invalid", "One JSON object with unique, known fields is required")
		return false
	}
	return true
}
func (a *api) completionResource(w http.ResponseWriter, r *http.Request, name, table string) (uuid.UUID, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		if origin := r.Header.Get("Origin"); origin != "" && origin != a.integrations.WebURL() && origin != a.integrations.APIURL() {
			problem(w, 403, "Request origin is not allowed")
			return uuid.Nil, false
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			problem(w, 415, "JSON request required")
			return uuid.Nil, false
		}
	}
	id, ok := identifier(w, r.PathValue(name), "path", name)
	if !ok {
		return id, false
	}
	var exists bool
	if dbError(w, a.pool.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE id=$1)", id).Scan(&exists), table) {
		return id, false
	}
	if !exists {
		problem(w, 404, "Resource not found")
		return id, false
	}
	return id, true
}
func completionError(w http.ResponseWriter, err error) bool {
	if errors.Is(err, postgres.ErrTaskPlanningInput) || errors.Is(err, postgres.ErrTaskPlanningConflict) || errors.Is(err, pgx.ErrNoRows) {
		return planningError(w, err)
	}
	return integrationError(w, err)
}
func (a *api) setCompletionPolicy(w http.ResponseWriter, r *http.Request) {
	id, ok := a.completionResource(w, r, "task_id", "tasks")
	if !ok {
		return
	}
	var input struct {
		ExpectedRevision int64              `json:"expected_revision"`
		Policy           *completion.Policy `json:"policy"`
	}
	if !completionBody(w, r, &input) {
		return
	}
	if input.Policy == nil || completion.ValidatePolicy(*input.Policy) != nil {
		invalid(w, "body", "policy", "value_error", "Choose a completion policy and up to 20 uniquely keyed requirements")
		return
	}
	tx, err := a.pool.Begin(r.Context())
	if dbError(w, err, "Task") {
		return
	}
	defer rollback(r.Context(), tx)
	if planningError(w, postgres.SetTaskCompletionPolicy(r.Context(), tx, id, input.ExpectedRevision, *input.Policy)) {
		return
	}
	if dbError(w, tx.Commit(r.Context()), "Task") {
		return
	}
	v, err := a.integrations.InspectTaskCompletion(r.Context(), id)
	if !completionError(w, err) {
		respond(w, 200, v)
	}
}
func (a *api) taskCompletion(w http.ResponseWriter, r *http.Request) {
	id, ok := a.completionResource(w, r, "task_id", "tasks")
	if !ok {
		return
	}
	var v completion.View
	var err error
	if r.Method == http.MethodPost {
		if !completionBody(w, r, &struct{}{}) {
			return
		}
		v, err = a.integrations.RefreshTaskCompletion(r.Context(), id)
	} else {
		v, err = a.integrations.InspectTaskCompletion(r.Context(), id)
	}
	if !completionError(w, err) {
		respond(w, 200, v)
	}
}
func (a *api) bindTaskPullRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := a.completionResource(w, r, "task_id", "tasks")
	if !ok {
		return
	}
	var input struct {
		ExpectedRevision int64     `json:"expected_revision"`
		SessionID        uuid.UUID `json:"session_id"`
		Number           int       `json:"number"`
	}
	if !completionBody(w, r, &input) {
		return
	}
	v, err := a.integrations.BindTaskPullRequest(r.Context(), id, input.SessionID, input.ExpectedRevision, input.Number)
	if !completionError(w, err) {
		respond(w, 200, v)
	}
}
func (a *api) completionChecks(w http.ResponseWriter, r *http.Request) {
	id, ok := a.completionResource(w, r, "repository_id", "repositories")
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		v, err := a.integrations.RepositoryCompletionChecks(r.Context(), id)
		if !completionError(w, err) {
			respond(w, 200, v)
		}
		return
	}
	var input struct {
		ExpectedRevision *int64                     `json:"expected_revision"`
		Checks           []completion.RequiredCheck `json:"checks"`
	}
	if !completionBody(w, r, &input) {
		return
	}
	if input.ExpectedRevision == nil {
		invalid(w, "body", "expected_revision", "value_error", "Current check configuration revision is required (0 before setup)")
		return
	}
	v, err := a.integrations.SetRepositoryCompletionChecks(r.Context(), id, *input.ExpectedRevision, input.Checks)
	if !completionError(w, err) {
		respond(w, 200, v)
	}
}
