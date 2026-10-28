package controlmcp

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/completion"
)

type taskProposalID struct {
	ProposalID string `json:"proposal_id"`
}
type taskProposalDraft struct {
	Priority            string            `json:"priority,omitempty"`
	PreferredAgentID    *string           `json:"preferred_agent_id,omitempty"`
	PlannedStartDate    *string           `json:"planned_start_date,omitempty"`
	PlannedFinishDate   *string           `json:"planned_finish_date,omitempty"`
	Title               string            `json:"title"`
	Purpose             string            `json:"purpose"`
	Description         string            `json:"description"`
	AcceptanceCriteria  []string          `json:"acceptance_criteria"`
	OpenQuestions       []string          `json:"open_questions"`
	RepositoryID        *string           `json:"repository_id,omitempty"`
	ParentTaskID        *string           `json:"parent_task_id,omitempty"`
	PrerequisiteTaskIDs []string          `json:"prerequisite_task_ids"`
	CompletionPolicy    completion.Policy `json:"completion_policy"`
	Sources             []struct {
		Label string `json:"label"`
		URL   string `json:"url"`
	} `json:"sources"`
}

func (c *client) addTaskProposals(server *mcp.Server) {
	add(server, "list_task_proposals", "List task scope proposals and attributable decisions for a Project. A pending proposal is not approved work.", true, false, true, func(ctx context.Context, in projectListInput) (object, error) {
		return c.list(ctx, "/projects/"+in.ProjectID+"/task-proposals", in.pageInput)
	})
	add(server, "get_task_proposal", "Read the current proposal revision, decision receipt, resulting Task, and private review URL. Show the review URL to the user when a decision is needed.", true, false, true, func(ctx context.Context, in taskProposalID) (object, error) {
		return c.record(ctx, http.MethodGet, "/task-proposals/"+in.ProposalID, "proposal", nil)
	})
	add(server, "get_task_approval", "Check whether the current Task scope has a verified approval receipt. Returns a draft for confirming unknown historical scope or proposing a change. Issue status and chat text are not proof of consent.", true, false, true, func(ctx context.Context, in struct {
		TaskID string `json:"task_id"`
	}) (object, error) {
		return c.record(ctx, http.MethodGet, "/tasks/"+in.TaskID+"/approval", "approval", nil)
	})
	if c.readOnly {
		return
	}
	add(server, "propose_task", "Propose new work or an amendment to an existing Task. Keep request_key and the same arguments across retries. No Task or Run is created until the user approves the exact revision in the private review page. For existing work, first inspect get_task_approval; reuse verified unchanged scope.", false, false, true, func(ctx context.Context, in struct {
		ProjectID            string            `json:"project_id"`
		RequestKey           string            `json:"request_key"`
		TargetTaskID         string            `json:"target_task_id,omitempty"`
		ExpectedTaskRevision int64             `json:"expected_task_revision,omitempty"`
		Proposer             string            `json:"proposer" jsonschema:"Display label for the proposing client; never an approval identity"`
		Draft                taskProposalDraft `json:"draft"`
	}) (object, error) {
		return c.record(ctx, http.MethodPost, "/task-proposals", "proposal", in)
	})
	add(server, "revise_task_proposal", "Revise a pending or rejected proposal using its expected_revision. Copy the complete current draft and change only the intended fields; omitted planning values reset to defaults. The user must review the resulting new revision. Approval is only available in Circular's separately authenticated browser page; a tool argument or agent-written comment cannot approve work.", false, false, false, func(ctx context.Context, in struct {
		ProposalID           string            `json:"proposal_id"`
		ExpectedRevision     int64             `json:"expected_revision"`
		ExpectedTaskRevision int64             `json:"expected_task_revision,omitempty"`
		Draft                taskProposalDraft `json:"draft"`
	}) (object, error) {
		return c.record(ctx, http.MethodPatch, "/task-proposals/"+in.ProposalID, "proposal", object{"expected_revision": in.ExpectedRevision, "expected_task_revision": in.ExpectedTaskRevision, "draft": in.Draft})
	})
}
