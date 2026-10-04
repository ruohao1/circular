package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/prreviews"
)

func (a *api) integrationRoutes(mux *http.ServeMux) {
	a.identityRoutes(mux)
	a.webhookRoutes(mux)
	a.externalRequestRoutes(mux)
	a.prReviewRoutes(mux)
	mux.HandleFunc("GET /api/v1/projects/{project_id}/integrations/github/run-delivery", a.githubRunDeliverySettings)
	mux.HandleFunc("POST /api/v1/projects/{project_id}/integrations/github/run-delivery", a.setGitHubRunDeliverySettings)
	mux.HandleFunc("GET /api/v1/runs/{run_id}/github-delivery", a.runGitHubDelivery)
	mux.HandleFunc("POST /api/v1/runs/{run_id}/github-delivery", a.publishRunGitHub)
	mux.HandleFunc("GET /api/v1/projects/{project_id}/integrations", a.connectionList)
	mux.HandleFunc("POST /api/v1/projects/{project_id}/integrations/{provider}/connect", a.connectionBegin)
	mux.HandleFunc("POST /api/v1/projects/{project_id}/integrations/{provider}/disconnect", a.connectionDisconnect)
	mux.HandleFunc("POST /api/v1/projects/{project_id}/integrations/github/register", a.githubRegistrationBegin)
	mux.HandleFunc("POST /api/v1/projects/{project_id}/integrations/github/app", a.githubAppSave)
	mux.HandleFunc("GET /api/v1/integrations/github/registration-callback", a.githubRegistrationCallback)
	mux.HandleFunc("POST /api/v1/projects/{project_id}/integrations/linear/app", a.linearAppSave)
	mux.HandleFunc("GET /api/v1/integrations/{provider}/callback", a.connectionCallback)
	mux.HandleFunc("GET /api/v1/projects/{project_id}/integrations/github/installations", a.githubInstallations)
	mux.HandleFunc("GET /api/v1/projects/{project_id}/integrations/github/repositories", a.githubRepositories)
	mux.HandleFunc("POST /api/v1/projects/{project_id}/integrations/github/repositories", a.githubCreateRepository)
	mux.HandleFunc("POST /api/v1/projects/{project_id}/integrations/github/import", a.githubImport)
	mux.HandleFunc("GET /api/v1/projects/{project_id}/integrations/linear/teams", a.linearTeams)
	mux.HandleFunc("GET /api/v1/projects/{project_id}/integrations/linear/projects", a.linearProjects)
	mux.HandleFunc("GET /api/v1/projects/{project_id}/integrations/linear/issues", a.linearIssues)
	mux.HandleFunc("POST /api/v1/projects/{project_id}/integrations/linear/import", a.linearImport)
	mux.HandleFunc("GET /api/v1/projects/{project_id}/integrations/linear/run-updates", a.linearRunUpdates)
	mux.HandleFunc("POST /api/v1/projects/{project_id}/integrations/linear/run-updates", a.setLinearRunUpdates)
	mux.HandleFunc("GET /api/v1/runs/{run_id}/linear-delivery", a.runLinearDelivery)
}

func integrationError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, integrations.ErrRequestNotFound):
		problem(w, 404, err.Error())
	case errors.Is(err, integrations.ErrRequestConflict), errors.Is(err, integrations.ErrRequestUnavailable):
		problem(w, 409, err.Error())
	case errors.Is(err, integrations.ErrRequestInput):
		problem(w, 422, err.Error())
	case errors.Is(err, integrations.ErrIdentityConflict), errors.Is(err, integrations.ErrIdentityUnavailable):
		problem(w, 409, err.Error())
	case errors.Is(err, prreviews.ErrRequestConflict), errors.Is(err, prreviews.ErrSnapshotChanged), errors.Is(err, prreviews.ErrReviewUnavailable), errors.Is(err, prreviews.ErrSourceInvalid):
		problem(w, 409, err.Error())
	case errors.Is(err, integrations.ErrConfiguration):
		problem(w, 503, integrations.ErrConfiguration.Error())
	case errors.Is(err, integrations.ErrReconnect):
		problem(w, 409, integrations.ErrReconnect.Error())
	case errors.Is(err, integrations.ErrAccess):
		problem(w, 403, integrations.ErrAccess.Error())
	case errors.Is(err, integrations.ErrRepositoryPermission):
		problem(w, 403, err.Error())
	case errors.Is(err, integrations.ErrDeliveryPermission):
		problem(w, 403, err.Error())
	case errors.Is(err, integrations.ErrDeliveryNotReady):
		problem(w, 409, err.Error())
	case errors.Is(err, integrations.ErrCreationKeyConflict):
		problem(w, 409, err.Error())
	case errors.Is(err, integrations.ErrRepositoryInput), errors.Is(err, integrations.ErrRepositoryExists):
		problem(w, 422, err.Error())
	case errors.Is(err, integrations.ErrConflict):
		problem(w, 409, integrations.ErrConflict.Error())
	case errors.Is(err, integrations.ErrAppConfigured), errors.Is(err, integrations.ErrAppInUse):
		problem(w, 409, err.Error())
	case errors.Is(err, integrations.ErrAppInput), errors.Is(err, integrations.ErrAppIdentity):
		problem(w, 422, err.Error())
	case errors.Is(err, integrations.ErrEmptyRepository), errors.Is(err, integrations.ErrIssueTitle):
		problem(w, 422, err.Error())
	case errors.Is(err, integrations.ErrProvider):
		problem(w, 502, integrations.ErrProvider.Error())
	default:
		problem(w, 500, "Could not complete the integration request")
	}
	return true
}

func (a *api) githubAppSave(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	values, ok := body(w, r, "GitHubAppUpdate")
	if !ok {
		return
	}
	if integrationError(w, a.integrations.SaveGitHubAppURL(r.Context(), project, values["app_url"].(string))) {
		return
	}
	respond(w, 200, map[string]bool{"configured": true})
}

func (a *api) githubRegistrationBegin(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	values, ok := body(w, r, "GitHubAppRegistration")
	if !ok {
		return
	}
	registration, err := a.integrations.BeginGitHubRegistration(r.Context(), project, strings.TrimSpace(values["organization"].(string)))
	if integrationError(w, err) {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: integrations.CookieName("github_app", registration.State), Value: registration.Browser, Path: "/api/v1/integrations/github/registration-callback", MaxAge: 600, HttpOnly: true, Secure: strings.HasPrefix(a.integrations.APIURL(), "https://"), SameSite: http.SameSiteLaxMode})
	respond(w, 200, registration)
}

func (a *api) githubRegistrationCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	state := r.URL.Query().Get("state")
	name := integrations.CookieName("github_app", state)
	browser := ""
	if cookie, err := r.Cookie(name); err == nil {
		browser = cookie.Value
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/api/v1/integrations/github/registration-callback", MaxAge: -1, HttpOnly: true, Secure: strings.HasPrefix(a.integrations.APIURL(), "https://"), SameSite: http.SameSiteLaxMode})
	project, installation, err := a.integrations.CompleteGitHubRegistration(r.Context(), state, browser, r.URL.Query().Get("code"))
	if err == nil {
		http.Redirect(w, r, installation, http.StatusSeeOther)
		return
	}
	values := url.Values{"section": {"integrations"}, "provider": {"github"}, "connection_result": {"failed"}}
	if project != "" {
		values.Set("project_id", project)
	}
	http.Redirect(w, r, a.integrations.WebURL()+"/setup?"+values.Encode(), http.StatusSeeOther)
}

func (a *api) linearAppSave(w http.ResponseWriter, r *http.Request) {
	_, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	values, ok := body(w, r, "LinearAppSetup")
	if !ok {
		return
	}
	if integrationError(w, a.integrations.SaveLinearApp(r.Context(), strings.TrimSpace(values["client_id"].(string)))) {
		return
	}
	respond(w, 200, map[string]bool{"configured": true})
}

func (a *api) integrationProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != "GET" {
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
	id, ok := identifier(w, r.PathValue("project_id"), "path", "project_id")
	if !ok {
		return "", false
	}
	var exists bool
	err := a.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM projects WHERE id=$1)`, id).Scan(&exists)
	if dbError(w, err, "project") {
		return "", false
	}
	if !exists {
		problem(w, 404, "project not found")
		return "", false
	}
	return id.String(), true
}

func integrationProvider(w http.ResponseWriter, r *http.Request) (string, bool) {
	provider := r.PathValue("provider")
	if provider != "github" && provider != "linear" {
		problem(w, 404, "integration provider not found")
		return "", false
	}
	return provider, true
}

func (a *api) connectionList(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	values, err := a.integrations.Connections(r.Context(), project)
	if !integrationError(w, err) {
		respond(w, 200, values)
	}
}

func (a *api) connectionBegin(w http.ResponseWriter, r *http.Request) {
	provider, ok := integrationProvider(w, r)
	if !ok {
		return
	}
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	value, err := a.integrations.Begin(r.Context(), project, provider)
	if integrationError(w, err) {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: integrations.CookieName(provider, value.State), Value: value.Browser, Path: "/api/v1/integrations/" + provider + "/callback", MaxAge: 600, HttpOnly: true, Secure: strings.HasPrefix(a.integrations.APIURL(), "https://"), SameSite: http.SameSiteLaxMode})
	respond(w, 200, map[string]string{"authorization_url": value.URL})
}

func (a *api) connectionCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	provider, ok := integrationProvider(w, r)
	if !ok {
		return
	}
	state := r.URL.Query().Get("state")
	name := integrations.CookieName(provider, state)
	browser := ""
	if cookie, err := r.Cookie(name); err == nil {
		browser = cookie.Value
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/api/v1/integrations/" + provider + "/callback", MaxAge: -1, HttpOnly: true, Secure: strings.HasPrefix(a.integrations.APIURL(), "https://"), SameSite: http.SameSiteLaxMode})
	code := r.URL.Query().Get("code")
	if r.URL.Query().Get("error") != "" {
		code = ""
	}
	project, err := a.integrations.Complete(r.Context(), provider, state, browser, code)
	result := "connected"
	if errors.Is(err, integrations.ErrDenied) {
		result = "cancelled"
	} else if err != nil {
		result = "failed"
	}
	values := url.Values{"section": {"integrations"}, "provider": {provider}, "connection_result": {result}}
	if project != "" {
		values.Set("project_id", project)
	}
	http.Redirect(w, r, a.integrations.WebURL()+"/setup?"+values.Encode(), http.StatusSeeOther)
}

func (a *api) connectionDisconnect(w http.ResponseWriter, r *http.Request) {
	provider, ok := integrationProvider(w, r)
	if !ok {
		return
	}
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	revoked, err := a.integrations.Disconnect(r.Context(), project, provider)
	if !integrationError(w, err) {
		respond(w, 200, map[string]any{"disconnected": true, "revocation_confirmed": revoked})
	}
}

func integrationPage(w http.ResponseWriter, r *http.Request) (int, bool) {
	value := r.URL.Query().Get("page")
	if value == "" {
		value = "1"
	}
	page, err := strconv.Atoi(value)
	if err != nil || page < 1 || page > 10000 {
		problem(w, 422, "page must be between 1 and 10000")
		return 0, false
	}
	return page, true
}

func integrationNumber(w http.ResponseWriter, value, field string) bool {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != value {
		problem(w, 422, field+" must be a positive numeric identifier")
		return false
	}
	return true
}

func (a *api) githubInstallations(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	page, ok := integrationPage(w, r)
	if !ok {
		return
	}
	data, err := a.integrations.GitHubInstallations(r.Context(), project, page)
	if !integrationError(w, err) {
		respond(w, 200, data)
	}
}

func (a *api) githubRepositories(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	page, ok := integrationPage(w, r)
	if !ok {
		return
	}
	installation := r.URL.Query().Get("installation_id")
	if !integrationNumber(w, installation, "installation_id") {
		return
	}
	data, err := a.integrations.GitHubRepositories(r.Context(), project, installation, page)
	if !integrationError(w, err) {
		respond(w, 200, data)
	}
}

func (a *api) githubImport(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	values, ok := body(w, r, "GitHubImport")
	if !ok {
		return
	}
	installation, repository := values["installation_id"].(string), values["repository_id"].(string)
	if !integrationNumber(w, installation, "installation_id") || !integrationNumber(w, repository, "repository_id") {
		return
	}
	pageText := values["page"].(string)
	page, err := strconv.Atoi(pageText)
	if err != nil || page < 1 || page > 10000 {
		problem(w, 422, "invalid repository page")
		return
	}
	id, created, err := a.integrations.ImportGitHub(r.Context(), project, installation, repository, page)
	if integrationError(w, err) {
		return
	}
	data, err := record(r.Context(), a.pool, "repositories", "RepositoryRead", "WHERE t.id=$1", id)
	if dbError(w, err, "repository") {
		return
	}
	status := 200
	if created {
		status = 201
	}
	respond(w, status, data)
}

func integrationCursor(w http.ResponseWriter, r *http.Request) (string, bool) {
	cursor := r.URL.Query().Get("cursor")
	if len(cursor) > 1000 {
		problem(w, 422, "invalid pagination cursor")
		return "", false
	}
	return cursor, true
}

func (a *api) linearTeams(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	cursor, ok := integrationCursor(w, r)
	if !ok {
		return
	}
	data, err := a.integrations.LinearTeams(r.Context(), project, cursor)
	if !integrationError(w, err) {
		respond(w, 200, data)
	}
}

func (a *api) linearProjects(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	cursor, ok := integrationCursor(w, r)
	if !ok {
		return
	}
	data, err := a.integrations.LinearProjects(r.Context(), project, cursor)
	if !integrationError(w, err) {
		respond(w, 200, data)
	}
}

func (a *api) linearIssues(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	cursor, ok := integrationCursor(w, r)
	if !ok {
		return
	}
	team, ok := identifier(w, r.URL.Query().Get("team_id"), "query", "team_id")
	if !ok {
		return
	}
	externalProject := r.URL.Query().Get("linear_project_id")
	if externalProject != "" {
		id, ok := identifier(w, externalProject, "query", "linear_project_id")
		if !ok {
			return
		}
		externalProject = id.String()
	}
	data, err := a.integrations.LinearIssues(r.Context(), project, team.String(), externalProject, cursor)
	if !integrationError(w, err) {
		respond(w, 200, data)
	}
}

func (a *api) linearImport(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	values, ok := body(w, r, "LinearImport")
	if !ok {
		return
	}
	id, created, err := a.integrations.ImportLinear(r.Context(), project, values["repository_id"].(string), values["issue_id"].(string))
	if integrationError(w, err) {
		return
	}
	data, err := record(r.Context(), a.pool, "tasks", "TaskRead", "WHERE t.id=$1", id)
	if dbError(w, err, "task") {
		return
	}
	status := 200
	if created {
		status = 201
	}
	respond(w, status, data)
}

func (a *api) task(w http.ResponseWriter, r *http.Request) {
	id, ok := identifier(w, r.PathValue("task_id"), "path", "task_id")
	if !ok {
		return
	}
	data, err := record(r.Context(), a.pool, "tasks", "TaskRead", "WHERE t.id=$1", id)
	if !dbError(w, err, "task") {
		respond(w, 200, data)
	}
}
