package httpapi

import (
	"encoding/json"
	"github.com/ruohao1/circular/internal/integrations"
	"net/http"
	"strconv"
	"strings"
)

func (a *api) externalRequestRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/projects/{project_id}/integrations/linear/request-routes", a.linearRequestRoutes)
	mux.HandleFunc("POST /api/v1/projects/{project_id}/integrations/linear/request-routes", a.linearRequestRoutes)
	mux.HandleFunc("POST /api/v1/projects/{project_id}/integrations/linear/request-routes/{route_id}", a.linearRequestRoutes)
	mux.HandleFunc("GET /api/v1/external-requests", a.externalRequests)
	mux.HandleFunc("GET /api/v1/external-requests/{request_id}", a.externalRequest)
	for _, action := range []string{"route", "start", "stop"} {
		mux.HandleFunc("POST /api/v1/external-requests/{request_id}/"+action, a.externalRequest)
	}
}
func (a *api) linearRequestRoutes(w http.ResponseWriter, r *http.Request) {
	if !a.privateIntegrationRequest(w, r) {
		return
	}
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	if r.Method == "GET" {
		out, e := a.integrations.RequestRoutes(r.Context(), project)
		if !integrationError(w, e) {
			respond(w, 200, out)
		}
		return
	}
	route := integrations.RequestRoute{ProjectID: project}
	schema := "LinearRequestRouteCreate"
	if id := r.PathValue("route_id"); id != "" {
		if _, ok := identifier(w, id, "path", "route_id"); !ok {
			return
		}
		routes, e := a.integrations.RequestRoutes(r.Context(), project)
		if integrationError(w, e) {
			return
		}
		for _, item := range routes {
			if item.ID == id {
				route = item
				break
			}
		}
		if route.ID == "" {
			problem(w, 404, "Route not found")
			return
		}
		schema = "LinearRequestRouteUpdate"
	}
	values, ok := strictBody(w, r, schema)
	if !ok {
		return
	}
	if route.ID == "" {
		route.IdentityID = values["identity_id"].(string)
		route.ScopeType = values["scope_type"].(string)
		route.ScopeID = values["scope_id"].(string)
	} else {
		number, ok := values["expected_generation"].(json.Number)
		generation, e := number.Int64()
		if !ok || e != nil || generation < 1 {
			invalid(w, "body", "expected_generation", "int_type", "A positive generation is required")
			return
		}
		route.Generation = generation
	}
	route.RepositoryID = values["repository_id"].(string)
	route.AgentID = values["agent_id"].(string)
	route.Mode = values["mode"].(string)
	route.Enabled = values["enabled"].(bool)
	out, e := a.integrations.SaveRequestRoute(r.Context(), route)
	if !integrationError(w, e) {
		respond(w, 200, out)
	}
}
func (a *api) externalRequests(w http.ResponseWriter, r *http.Request) {
	if !a.privateIntegrationRequest(w, r) {
		return
	}
	q := r.URL.Query()
	limit := 20
	if q.Get("limit") != "" {
		v, e := strconv.Atoi(q.Get("limit"))
		if e != nil || v < 1 || v > 100 {
			invalid(w, "query", "limit", "int_type", "Choose a limit from 1 to 100")
			return
		}
		limit = v
	}
	if q.Get("unrouted") != "" && q.Get("unrouted") != "true" {
		invalid(w, "query", "unrouted", "bool_type", "Use unrouted=true")
		return
	}
	if attention := q.Get("attention"); attention != "" && attention != "true" && attention != "false" {
		invalid(w, "query", "attention", "bool_type", "Use attention=true or attention=false")
		return
	}
	out, e := a.integrations.ExternalRequests(r.Context(), integrations.RequestListQuery{ProjectID: q.Get("project_id"), Unrouted: q.Get("unrouted") == "true", Attention: q.Get("attention") == "true", Cursor: q.Get("cursor"), Limit: limit})
	if !integrationError(w, e) {
		respond(w, 200, out)
	}
}
func (a *api) externalRequest(w http.ResponseWriter, r *http.Request) {
	if !a.privateIntegrationRequest(w, r) {
		return
	}
	id := r.PathValue("request_id")
	if _, ok := identifier(w, id, "path", "request_id"); !ok {
		return
	}
	if r.Method == "GET" {
		out, e := a.integrations.ExternalRequest(r.Context(), id)
		if !integrationError(w, e) {
			respond(w, 200, out)
		}
		return
	}
	action := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	schema := "ExternalRequestStart"
	if action == "route" {
		schema = "ExternalRequestRoute"
	}
	if action == "stop" {
		schema = "IdentityDetach"
	}
	v, ok := strictBody(w, r, schema)
	if !ok {
		return
	}
	var out integrations.ExternalRequest
	var e error
	switch action {
	case "route":
		out, e = a.integrations.RouteExternalRequest(r.Context(), id, v["route_id"].(string), v["expected_input_fingerprint"].(string))
	case "start":
		out, e = a.integrations.StartExternalRequest(r.Context(), id, v["expected_input_fingerprint"].(string))
	case "stop":
		out, e = a.integrations.StopExternalRequest(r.Context(), id)
	}
	if !integrationError(w, e) {
		respond(w, 200, out)
	}
}
