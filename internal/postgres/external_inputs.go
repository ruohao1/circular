package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/backends"
)

// ExternalInput is trusted only through external_run_inputs + request/run FKs.
// It contains execution data, never connection tokens or provider webhooks.
type ExternalInput struct {
	RequestID          string          `json:"request_id"`
	ProjectID          string          `json:"project_id"`
	RepositoryID       string          `json:"repository_id"`
	AgentID            string          `json:"agent_id"`
	CloneURL           string          `json:"clone_url"`
	BaseRef            string          `json:"base_ref"`
	RepositoryRefs     json.RawMessage `json:"repository_refs"`
	TaskTitle          string          `json:"task_title"`
	TaskDescription    string          `json:"task_description"`
	Instructions       string          `json:"instructions"`
	AgentName          string          `json:"agent_name"`
	Backend            string          `json:"backend"`
	BackendConfig      json.RawMessage `json:"backend_config"`
	RouteID            string          `json:"route_id"`
	RouteGeneration    int64           `json:"route_generation"`
	IdentityGeneration int64           `json:"identity_generation"`
	Source             json.RawMessage `json:"source"`
}

func SealExternalInput(in ExternalInput) ([]byte, string, error) {
	for _, id := range []string{in.RequestID, in.ProjectID, in.RepositoryID, in.AgentID, in.RouteID} {
		if parsed, e := uuid.Parse(id); e != nil || parsed == uuid.Nil {
			return nil, "", ErrResourceConflict
		}
	}
	if in.CloneURL == "" || in.BaseRef == "" || len(in.TaskDescription) > 128<<10 || (in.Backend != "codex" && in.Backend != "fake") {
		return nil, "", ErrResourceConflict
	}
	for _, raw := range []*json.RawMessage{&in.RepositoryRefs, &in.BackendConfig, &in.Source} {
		var value any
		if json.Unmarshal(*raw, &value) != nil {
			return nil, "", ErrResourceConflict
		}
		*raw, _ = json.Marshal(value)
	}
	if in.Backend == "codex" {
		if _, e := backends.ValidateCodexConfig(in.BackendConfig); e != nil {
			return nil, "", ErrResourceConflict
		}
	}
	body, e := json.Marshal(in)
	if e != nil {
		return nil, "", e
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), nil
}
func (r *RunResources) externalProvisioningContext() (ProvisioningContext, bool, error) {
	var linked bool
	e := r.tx.QueryRow(r.ctx, `SELECT EXISTS(SELECT 1 FROM external_requests WHERE run_id=$1) OR EXISTS(SELECT 1 FROM external_session_runs WHERE run_id=$1)`, r.id).Scan(&linked)
	if e != nil || !linked {
		return ProvisioningContext{}, linked, e
	}
	var raw []byte
	var fingerprint string
	e = r.tx.QueryRow(r.ctx, `SELECT i.snapshot,i.fingerprint FROM external_run_inputs i JOIN external_requests q ON q.id=i.request_id AND q.run_id=i.run_id JOIN external_session_runs s ON s.run_id=i.run_id AND s.request_id=q.id WHERE i.run_id=$1`, r.id).Scan(&raw, &fingerprint)
	if e != nil {
		return ProvisioningContext{}, true, ErrResourceConflict
	}
	var in ExternalInput
	if json.Unmarshal(raw, &in) != nil {
		return ProvisioningContext{}, true, ErrResourceConflict
	}
	_, hash, e := SealExternalInput(in)
	if e != nil || hash != fingerprint || in.Backend != r.backend {
		return ProvisioningContext{}, true, ErrResourceConflict
	}
	var same bool
	e = r.tx.QueryRow(r.ctx, `SELECT project_id=$2 AND clone_url=$3 AND default_branch=$4 AND external_refs::jsonb=$5::jsonb FROM repositories WHERE id=$1`, in.RepositoryID, in.ProjectID, in.CloneURL, in.BaseRef, in.RepositoryRefs).Scan(&same)
	if e != nil || !same {
		return ProvisioningContext{}, true, ErrResourceConflict
	}
	return ProvisioningContext{RunID: r.id, WorkspaceID: WorkspaceID(r.id), RepositoryID: uuid.MustParse(in.RepositoryID), Kind: r.kind, CloneURL: in.CloneURL, BaseRef: in.BaseRef, Backend: in.Backend, TaskTitle: in.TaskTitle, TaskDescription: in.TaskDescription, Instructions: in.Instructions, BackendConfig: in.BackendConfig}, true, nil
}
