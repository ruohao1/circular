package httpapi

import (
	"net/http"

	"github.com/ruohao1/circular/internal/integrations"
)

func (a *api) githubCreateRepository(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	values, ok := body(w, r, "GitHubRepositoryCreate")
	if !ok {
		return
	}
	installation := values["installation_id"].(string)
	if !integrationNumber(w, installation, "installation_id") {
		return
	}
	result, err := a.integrations.CreateGitHubRepository(r.Context(), project, integrations.GitHubRepositoryCreate{
		InstallationID: installation,
		Name:           values["name"].(string),
		Description:    values["description"].(string),
		Visibility:     values["visibility"].(string),
		RequestKey:     values["request_key"].(string),
	})
	if !integrationError(w, err) {
		respond(w, http.StatusOK, result)
	}
}
