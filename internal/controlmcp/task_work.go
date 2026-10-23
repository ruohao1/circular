package controlmcp

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/completion"
)

type workSessionInput struct {
	SessionID string `json:"session_id" jsonschema:"Work session UUID retained before claim or read from get_task_work"`
}
type workCredentialInput struct {
	workSessionInput
	Secret string `json:"session_secret" jsonschema:"Retained 256-bit cryptographically random secret encoded as 64 lowercase hex characters; never put in a URL or log"`
}

func (c *client) addWorkReadTools(server *mcp.Server) {
	add(server, "get_task_work", "Inspect Project guidance, Task scope/revision, Repository, prerequisites and their stages, current owner/contact, and completion policy/evidence in one snapshot. Discover candidates with list_task_planning status=ready. Inspection does not reserve work; claim rechecks eligibility.", true, false, true, func(ctx context.Context, in taskPlanningID) (object, error) {
		return c.record(ctx, http.MethodGet, "/tasks/"+in.TaskID+"/work", "work", nil)
	})
	add(server, "get_work_session", "Inspect safe Work session contact, outcome and lifecycle receipts, including released sessions. Never returns a credential or reacquires ownership.", true, false, true, func(ctx context.Context, in workSessionInput) (object, error) {
		return c.record(ctx, http.MethodGet, "/work-sessions/"+in.SessionID, "work", nil)
	})
}
func (c *client) addWorkMutations(server *mcp.Server) {
	add(server, "claim_task_work", "Claim a prepared Ready Task for this already-running coding client. Creates no Run. First inspect get_task_work. Before calling, generate and securely retain a random session_id UUID and 256-bit session_secret using your local cryptographic RNG. Reuse those credentials, original expected_revision and all original arguments after uncertain responses or reconnects. Never invent a new identity just to retry. Client metadata is display-only. Ownership conflicts require inspection and user resolution, never automatic release.", false, false, true, func(ctx context.Context, in struct {
		TaskID string `json:"task_id"`
		workCredentialInput
		ExpectedRevision int64  `json:"expected_revision" jsonschema:"Current Task revision from get_task_work; retain the original value for claim retries"`
		ClientName       string `json:"client_name" jsonschema:"Display name of the coding client, 1–80 characters; not an authenticated identity"`
		ClientVersion    string `json:"client_version,omitempty" jsonschema:"Optional display version, at most 80 characters"`
	}) (object, error) {
		return c.record(ctx, http.MethodPost, "/tasks/"+in.TaskID+"/work/claim", "work", object{"session_id": in.SessionID, "session_secret": in.Secret, "expected_revision": in.ExpectedRevision, "client_name": in.ClientName, "client_version": in.ClientVersion})
	})
	add(server, "heartbeat_task_work", "Report contact for your active external claim every 30 seconds while working. Use a positive increasing sequence; retry the same sequence after an uncertain response. Duplicate/older sequences never advance contact time. Silence for 120 seconds shows attention but does not release ownership. Stop on work_claim_lost and reload get_task_work.", false, false, true, func(ctx context.Context, in struct {
		workCredentialInput
		Sequence int64 `json:"sequence" jsonschema:"Positive increasing safe integer; keep the same sequence for a retry"`
	}) (object, error) {
		return c.record(ctx, http.MethodPost, "/work-sessions/"+in.SessionID+"/heartbeat", "work", object{"session_secret": in.Secret, "sequence": in.Sequence})
	})
	add(server, "finish_task_work", "Finish your active external attempt with outcome succeeded, failed or cancelled and a nonblank summary. Success requires report.result equal to summary and evidence for every frozen requirement from get_task_work. Valid direct work becomes Done; review-required work becomes In Review, with PR binding and authoritative checks/review still required. Failure/cancellation releases ownership without declaring completion. Retain a random operation_id UUID and exact arguments for retries; replay never advances status again. Invalid evidence leaves the claim active. Never report for another session.", false, false, true, func(ctx context.Context, in struct {
		workCredentialInput
		OperationID string             `json:"operation_id" jsonschema:"Caller-generated random UUID retained before this finish; reuse with identical arguments on retry"`
		Outcome     string             `json:"outcome" jsonschema:"succeeded, failed, or cancelled"`
		Summary     string             `json:"summary" jsonschema:"Nonblank result summary, at most 4000 characters; must equal report.result when a report is supplied"`
		Report      *completion.Report `json:"report,omitempty" jsonschema:"Required for success; keyed evidence satisfies the frozen completion policy. PR number is a reference, not authoritative proof."`
	}) (object, error) {
		body := object{"session_secret": in.Secret, "operation_id": in.OperationID, "outcome": in.Outcome, "summary": in.Summary}
		if in.Report != nil {
			body["report"] = in.Report
		}
		return c.record(ctx, http.MethodPost, "/work-sessions/"+in.SessionID+"/finish", "work", body)
	})
	add(server, "release_task_work", "Administrator recovery: use only when the user explicitly asks to release this exact owner. A claim conflict or silent heartbeat is not user approval. Supply expected_session_id from get_task_work, a retained random operation_id UUID, and the user's reason. External revocation fences Circular reports but cannot stop local edits. Native release requests cancellation and retains ownership while cleanup is pending. Does not mark Done or make the Task Ready. Retry with identical arguments; never release a replacement owner automatically.", false, true, true, func(ctx context.Context, in struct {
		TaskID            string `json:"task_id"`
		ExpectedSessionID string `json:"expected_session_id"`
		OperationID       string `json:"operation_id"`
		Reason            string `json:"reason" jsonschema:"Nonblank reason for this user-authorized release, at most 4000 characters"`
	}) (object, error) {
		return c.record(ctx, http.MethodPost, "/tasks/"+in.TaskID+"/work/release", "work", object{"expected_session_id": in.ExpectedSessionID, "operation_id": in.OperationID, "reason": in.Reason})
	})
}
