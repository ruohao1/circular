package httpapi

import (
	"net/http"
	"strings"
)

func (a *api) githubRunDeliverySettings(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	settings, err := a.integrations.GitHubRunDeliverySettings(r.Context(), project)
	if !integrationError(w, err) {
		respond(w, http.StatusOK, settings)
	}
}

func (a *api) setGitHubRunDeliverySettings(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	values, ok := body(w, r, "GitHubRunDeliverySettingsUpdate")
	if !ok {
		return
	}
	settings, err := a.integrations.SetGitHubRunDeliverySettings(r.Context(), project, values["enabled"].(bool))
	if !integrationError(w, err) {
		respond(w, http.StatusOK, settings)
	}
}

func (a *api) githubDeliveryRun(w http.ResponseWriter, r *http.Request) (string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		origin := r.Header.Get("Origin")
		if origin != "" && origin != a.integrations.WebURL() && origin != a.integrations.APIURL() {
			problem(w, http.StatusForbidden, "Request origin is not allowed")
			return "", false
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			problem(w, http.StatusUnsupportedMediaType, "JSON request required")
			return "", false
		}
	}
	id, ok := identifier(w, r.PathValue("run_id"), "path", "run_id")
	if !ok {
		return "", false
	}
	var exists bool
	err := a.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM runs WHERE id=$1)`, id).Scan(&exists)
	if dbError(w, err, "run") {
		return "", false
	}
	if !exists {
		problem(w, http.StatusNotFound, "run not found")
		return "", false
	}
	return id.String(), true
}

func (a *api) runGitHubDelivery(w http.ResponseWriter, r *http.Request) {
	run, ok := a.githubDeliveryRun(w, r)
	if !ok {
		return
	}
	delivery, err := a.integrations.GitHubRunDelivery(r.Context(), run)
	if !integrationError(w, err) {
		respond(w, http.StatusOK, delivery)
	}
}

func (a *api) publishRunGitHub(w http.ResponseWriter, r *http.Request) {
	run, ok := a.githubDeliveryRun(w, r)
	if !ok {
		return
	}
	if _, ok := body(w, r, "GitHubRunDeliveryRequest"); !ok {
		return
	}
	delivery, err := a.integrations.QueueGitHubRunDelivery(r.Context(), run)
	if !integrationError(w, err) {
		respond(w, http.StatusOK, delivery)
	}
}
