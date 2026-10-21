// Package completion defines a Task's frozen completion contract and evidence.
package completion

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
