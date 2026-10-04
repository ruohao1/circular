package httpapi

import (
	"net/http"
	"strings"
)

func (a *api) webhookRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/integrations/{provider}/webhooks", a.webhookSettings)
	mux.HandleFunc("POST /api/v1/integrations/{provider}/webhooks", a.webhookSettings)
	mux.HandleFunc("POST /api/v1/integrations/{provider}/webhooks/check", a.checkWebhookSettings)
}
func (a *api) privateIntegrationRequest(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == "GET" {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin != "" && origin != a.integrations.WebURL() && origin != a.integrations.APIURL() {
		problem(w, 403, "Request origin is not allowed")
		return false
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		problem(w, 415, "JSON request required")
		return false
	}
	return true
}
func (a *api) webhookSettings(w http.ResponseWriter, r *http.Request) {
	if !a.privateIntegrationRequest(w, r) {
		return
	}
	provider, ok := integrationProvider(w, r)
	if !ok {
		return
	}
	if r.Method == "GET" {
		out, err := a.integrations.WebhookSettings(r.Context(), provider)
		if !integrationError(w, err) {
			respond(w, 200, out)
		}
		return
	}
	values, ok := body(w, r, "WebhookSettingsUpdate")
	if !ok {
		return
	}
	secret, _ := values["signing_secret"].(string)
	out, err := a.integrations.SaveWebhookSettings(r.Context(), provider, values["public_origin"].(string), secret)
	if !integrationError(w, err) {
		respond(w, 200, out)
	}
}
func (a *api) checkWebhookSettings(w http.ResponseWriter, r *http.Request) {
	if !a.privateIntegrationRequest(w, r) {
		return
	}
	provider, ok := integrationProvider(w, r)
	if !ok {
		return
	}
	if _, ok := body(w, r, "IdentityDetach"); !ok {
		return
	}
	out, err := a.integrations.CheckWebhookSettings(r.Context(), provider)
	if !integrationError(w, err) {
		respond(w, 200, out)
	}
}
