// Package completion defines a Task's frozen completion contract and evidence.
package completion

import (
	"github.com/google/uuid"
	"time"
)

type Requirement struct {
	Key         string `json:"key"`
	Description string `json:"description"`
}
type Policy struct {
	Mode         string        `json:"mode"`
	Requirements []Requirement `json:"requirements"`
}
type Evidence struct {
	Key    string   `json:"key"`
	Detail string   `json:"detail"`
	Links  []string `json:"links"`
}
type Report struct {
	Result            string     `json:"result"`
	Evidence          []Evidence `json:"evidence"`
	PullRequestNumber *int       `json:"pull_request_number,omitempty"`
}
type RequiredCheck struct {
	WorkflowID   string `json:"workflow_id"`
	AppID        string `json:"app_id"`
	WorkflowPath string `json:"workflow_path"`
	Name         string `json:"name"`
}
type CheckConfig struct {
	Revision int64           `json:"revision"`
	Checks   []RequiredCheck `json:"checks"`
}
type Contract struct {
	Policy      Policy      `json:"policy"`
	CheckConfig CheckConfig `json:"check_config"`
	Fingerprint string      `json:"fingerprint"`
}

type Blocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Action  string `json:"action"`
}
type Input struct {
	Contract            *Contract `json:"contract"`
	Report              *Report   `json:"report"`
	SessionID           uuid.UUID `json:"session_id"`
	CurrentSessionID    uuid.UUID `json:"current_session_id"`
	Outcome             string    `json:"outcome"`
	Released            bool      `json:"released"`
	OwnerActive         bool      `json:"owner_active"`
	CleanupPending      bool      `json:"cleanup_pending"`
	NeedsUserResolution bool      `json:"needs_user_resolution"`
	Stage               string    `json:"stage"`
}
type Verdict struct {
	NextStage      string    `json:"next_stage"`
	MergeReadiness string    `json:"merge_readiness"`
	CanComplete    bool      `json:"can_complete"`
	Blockers       []Blocker `json:"blockers"`
	Advisories     []string  `json:"advisories"`
}
type View struct {
	Input
	TaskID     uuid.UUID `json:"task_id"`
	Revision   int64     `json:"revision"`
	Policy     *Policy   `json:"policy"`
	Verdict    Verdict   `json:"verdict"`
	ObservedAt time.Time `json:"observed_at"`
}
