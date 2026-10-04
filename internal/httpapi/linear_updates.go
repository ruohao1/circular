package httpapi

import "net/http"

func (a *api) linearRunUpdates(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	settings, err := a.integrations.LinearRunUpdates(r.Context(), project)
	if !integrationError(w, err) {
		respond(w, http.StatusOK, settings)
	}
}

func (a *api) setLinearRunUpdates(w http.ResponseWriter, r *http.Request) {
	project, ok := a.integrationProject(w, r)
	if !ok {
		return
	}
	values, ok := body(w, r, "LinearRunUpdatesUpdate")
	if !ok {
		return
	}
	settings, err := a.integrations.SetLinearRunUpdates(r.Context(), project, values["enabled"].(bool))
	if !integrationError(w, err) {
		respond(w, http.StatusOK, settings)
	}
}

func (a *api) runLinearDelivery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	run, ok := identifier(w, r.PathValue("run_id"), "path", "run_id")
	if !ok {
		return
	}
	var exists bool
	err := a.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM runs WHERE id=$1)`, run).Scan(&exists)
	if dbError(w, err, "run") {
		return
	}
	if !exists {
		problem(w, http.StatusNotFound, "run not found")
		return
	}
	delivery, err := a.integrations.RunLinearDelivery(r.Context(), run.String())
	if !integrationError(w, err) {
		respond(w, http.StatusOK, delivery)
	}
}
