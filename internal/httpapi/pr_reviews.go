package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/prreviews"
)

func (a *api) prReviewRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/projects/{project_id}/integrations/github/pr-reviews", a.prReviewSettings)
	mux.HandleFunc("POST /api/v1/projects/{project_id}/integrations/github/pr-reviews", a.setPRReviewSettings)
	mux.HandleFunc("GET /api/v1/runs/{run_id}/pr-reviews/prepare", a.preparePRReview)
	mux.HandleFunc("GET /api/v1/runs/{run_id}/pr-reviews", a.listPRReviews)
	mux.HandleFunc("POST /api/v1/runs/{run_id}/pr-reviews", a.launchPRReview)
	mux.HandleFunc("GET /api/v1/pr-reviews/{review_id}", a.getPRReview)
	mux.HandleFunc("POST /api/v1/pr-reviews/{review_id}/refresh", a.refreshPRReview)
	mux.HandleFunc("POST /api/v1/pr-reviews/{review_id}/publication/retry", a.retryPRReviewPublication)
}
func (a *api) prReviewSettings(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	result, err := a.integrations.PRReviewSettings(r.Context(), project)
	if !integrationError(w, err) {
		respond(w, 200, result)
	}
}
func (a *api) setPRReviewSettings(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	values, ok := strictBody(w, r, "PRReviewSettingsUpdate")
	if !ok {
		return
	}
	input := prreviews.SettingsUpdate{Automatic: values["automatic"].(bool)}
	if raw, exists := values["reviewer_id"]; exists {
		id, err := uuid.Parse(raw.(string))
		if err != nil || id == uuid.Nil {
			invalid(w, "body", "reviewer_id", "uuid_parsing", "Choose a nonzero reviewer UUID")
			return
		}
		input.ReviewerID = &id
	}
	result, err := a.integrations.SetPRReviewSettings(r.Context(), project, input)
	if !integrationError(w, err) {
		respond(w, 200, result)
	}
}
func (a *api) preparePRReview(w http.ResponseWriter, r *http.Request) {
	source, ok := a.githubDeliveryRun(w, r)
	if !ok {
		return
	}
	reviewer := r.URL.Query().Get("reviewer_id")
	if reviewer != "" {
		id, ok := identifier(w, reviewer, "query", "reviewer_id")
		if !ok {
			return
		}
		if id == uuid.Nil {
			invalid(w, "query", "reviewer_id", "uuid_parsing", "Choose a nonzero reviewer UUID")
			return
		}
	}
	result, err := a.integrations.PreparePRReview(r.Context(), source, reviewer)
	if !integrationError(w, err) {
		respond(w, 200, result)
	}
}
func (a *api) listPRReviews(w http.ResponseWriter, r *http.Request) {
	source, ok := a.githubDeliveryRun(w, r)
	if !ok {
		return
	}
	limit, ok := integerQuery(w, r, "limit", 20, 1, 100)
	if !ok {
		return
	}
	cursor := r.URL.Query().Get("cursor")
	if len(cursor) > 1024 {
		invalid(w, "query", "cursor", "string_too_long", "Cursor is too long")
		return
	}
	result, err := a.integrations.ListPRReviews(r.Context(), source, int(limit), cursor)
	if errors.Is(err, integrations.ErrAccess) {
		invalid(w, "query", "cursor", "value_error", "Review history could not be read with this cursor")
		return
	}
	if !integrationError(w, err) {
		respond(w, 200, result)
	}
}
func (a *api) launchPRReview(w http.ResponseWriter, r *http.Request) {
	source, ok := a.githubDeliveryRun(w, r)
	if !ok {
		return
	}
	values, ok := strictBody(w, r, "PRReviewLaunch")
	if !ok {
		return
	}
	var input prreviews.LaunchRequest
	raw, _ := json.Marshal(values)
	if json.Unmarshal(raw, &input) != nil || input.Validate() != nil {
		invalid(w, "body", "", "value_error", "Use a nonzero request key, prepared fingerprint, and normal mode; again requires the previous review UUID")
		return
	}
	if raw, exists := values["reviewer_id"]; exists && raw == uuid.Nil.String() {
		invalid(w, "body", "reviewer_id", "uuid_parsing", "Choose a nonzero reviewer UUID")
		return
	}
	result, err := a.integrations.LaunchPRReview(r.Context(), source, input)
	if !integrationError(w, err) {
		respond(w, 202, result)
	}
}
func (a *api) reviewID(w http.ResponseWriter, r *http.Request) (string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		origin := r.Header.Get("Origin")
		if origin != "" && origin != a.integrations.WebURL() && origin != a.integrations.APIURL() {
			problem(w, 403, "Request origin is not allowed")
			return "", false
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			problem(w, 415, "JSON request required")
			return "", false
		}
	}
	id, ok := identifier(w, r.PathValue("review_id"), "path", "review_id")
	if !ok {
		return "", false
	}
	var exists bool
	err := a.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM pr_reviews WHERE id=$1)`, id).Scan(&exists)
	if dbError(w, err, "PR review") {
		return "", false
	}
	if !exists {
		problem(w, 404, "PR review not found")
		return "", false
	}
	return id.String(), true
}
func (a *api) getPRReview(w http.ResponseWriter, r *http.Request) {
	id, ok := a.reviewID(w, r)
	if !ok {
		return
	}
	result, err := a.integrations.RefreshPRReview(r.Context(), id)
	if !integrationError(w, err) {
		respond(w, 200, result)
	}
}
func (a *api) refreshPRReview(w http.ResponseWriter, r *http.Request) {
	id, ok := a.reviewID(w, r)
	if !ok {
		return
	}
	if _, ok := strictBody(w, r, "PRReviewEmpty"); !ok {
		return
	}
	result, err := a.integrations.RefreshPRReview(r.Context(), id)
	if !integrationError(w, err) {
		respond(w, 200, result)
	}
}
func (a *api) retryPRReviewPublication(w http.ResponseWriter, r *http.Request) {
	id, ok := a.reviewID(w, r)
	if !ok {
		return
	}
	if _, ok := strictBody(w, r, "PRReviewEmpty"); !ok {
		return
	}
	result, err := a.integrations.RetryPRReviewPublication(r.Context(), id)
	if !integrationError(w, err) {
		respond(w, 200, result)
	}
}
