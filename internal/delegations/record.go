package delegations

import "github.com/google/uuid"

type Record struct {
	ProposalID    *uuid.UUID `json:"proposal_id"`
	ID            uuid.UUID  `json:"id"`
	ParentRunID   uuid.UUID  `json:"parent_run_id"`
	TargetAgentID uuid.UUID  `json:"target_agent_id"`
	ChildTaskID   *uuid.UUID `json:"child_task_id"`
	ChildRunID    *uuid.UUID `json:"child_run_id"`
	RequestKey    string     `json:"request_key"`
	Status        string     `json:"status"`
}
