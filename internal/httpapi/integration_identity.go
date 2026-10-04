package httpapi

import (
	"net/http"
	"strings"

	"github.com/ruohao1/circular/internal/integrations"
)

func (a *api) identityRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/projects/{project_id}/integrations/{provider}/identity", a.integrationIdentity)
	mux.HandleFunc("POST /api/v1/projects/{project_id}/integrations/github/identity/key", a.githubIdentityKey)
	mux.HandleFunc("POST /api/v1/projects/{project_id}/integrations/linear/identity/connect", a.linearIdentityConnect)
	mux.HandleFunc("POST /api/v1/projects/{project_id}/integrations/{provider}/identity/bind", a.integrationIdentityBind)
	mux.HandleFunc("POST /api/v1/projects/{project_id}/integrations/{provider}/identity/detach", a.integrationIdentityDetach)
}
func (a *api) integrationIdentity(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	provider, ok := integrationProvider(w, r)
	if !ok {
		return
	}
	value, err := a.integrations.Identity(r.Context(), project, provider)
	if !integrationError(w, err) {
		respond(w, 200, value)
	}
}
func (a *api) githubIdentityKey(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 96<<10)
	values, ok := body(w, r, "GitHubIdentityKey")
	if !ok {
		return
	}
	value, err := a.integrations.SaveGitHubIdentity(r.Context(), project, []byte(values["private_key"].(string)))
	if !integrationError(w, err) {
		respond(w, 200, value)
	}
}
func (a *api) linearIdentityConnect(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	values, ok := body(w, r, "LinearIdentityConnect")
	if !ok {
		return
	}
	auth, err := a.integrations.BeginLinearAppAuthorization(r.Context(), project, values["purpose"].(string))
	if integrationError(w, err) {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: integrations.CookieName("linear", auth.State), Value: auth.Browser, Path: "/api/v1/integrations/linear/callback", MaxAge: 600, HttpOnly: true, Secure: strings.HasPrefix(a.integrations.APIURL(), "https://"), SameSite: http.SameSiteLaxMode})
	respond(w, 200, map[string]string{"authorization_url": auth.URL})
}
func (a *api) integrationIdentityBind(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	provider, ok := integrationProvider(w, r)
	if !ok {
		return
	}
	values, ok := body(w, r, "IdentityBind")
	if !ok {
		return
	}
	value, err := a.integrations.BindIdentity(r.Context(), project, provider, values["identity_id"].(string))
	if !integrationError(w, err) {
		respond(w, 200, value)
	}
}
func (a *api) integrationIdentityDetach(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	provider, ok := integrationProvider(w, r)
	if !ok {
		return
	}
	if _, ok := body(w, r, "IdentityDetach"); !ok {
		return
	}
	if integrationError(w, a.integrations.DetachIdentity(r.Context(), project, provider)) {
		return
	}
	value, err := a.integrations.Identity(r.Context(), project, provider)
	if !integrationError(w, err) {
		respond(w, 200, value)
	}
}
