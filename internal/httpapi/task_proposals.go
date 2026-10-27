package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
)

func proposalBody(w http.ResponseWriter, r *http.Request, out any, allowed ...string) bool {
	fields, ok := planningBody(w, r, allowed...)
	if !ok {
		return false
	}
	raw, _ := json.Marshal(fields)
	if prreviews.DecodeStrict(raw, out) != nil {
		problem(w, 422, "Use the documented proposal fields and types")
		return false
	}
	return true
}
func proposalError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, postgres.ErrTaskProposalSession):
		problem(w, 401, err.Error())
	case errors.Is(err, postgres.ErrTaskProposalInput), errors.Is(err, postgres.ErrTaskPlanningInput):
		problem(w, 422, err.Error())
	case errors.Is(err, postgres.ErrTaskProposalConflict), errors.Is(err, postgres.ErrTaskPlanningConflict), errors.Is(err, postgres.ErrTaskApprovalRequired):
		problem(w, 409, err.Error())
	default:
		dbError(w, err, "Task proposal")
	}
	return true
}
func (a *api) proposalResponse(w http.ResponseWriter, status int, p postgres.TaskProposal) {
	approvalHeaders(w)
	p.ReviewURL = a.integrations.WebURL() + "/proposals/" + p.ID.String() + "?revision=" + strconv.FormatInt(p.Revision, 10)
	respond(w, status, p)
}
func (a *api) taskProposalRoutes(mux *http.ServeMux) {
	a.approvalAuthRoutes(mux)
	mux.HandleFunc("POST /api/v1/task-proposals", func(w http.ResponseWriter, r *http.Request) {
		var in postgres.TaskProposalInput
		if !proposalBody(w, r, &in, "project_id", "request_key", "target_task_id", "expected_task_revision", "proposer", "draft") {
			return
		}
		tx, err := a.pool.Begin(r.Context())
		if dbError(w, err, "Task proposal") {
			return
		}
		defer tx.Rollback(r.Context())
		out, err := postgres.CreateTaskProposal(r.Context(), tx, in, nil)
		if proposalError(w, err) {
			return
		}
		if dbError(w, tx.Commit(r.Context()), "Task proposal") {
			return
		}
		a.proposalResponse(w, 201, out)
	})
	mux.HandleFunc("GET /api/v1/projects/{project_id}/task-proposals", func(w http.ResponseWriter, r *http.Request) {
		id, ok := identifier(w, r.PathValue("project_id"), "path", "project_id")
		if !ok {
			return
		}
		tx, err := a.pool.Begin(r.Context())
		if dbError(w, err, "Task proposals") {
			return
		}
		defer tx.Rollback(r.Context())
		out, err := postgres.ListTaskProposals(r.Context(), tx, id)
		if proposalError(w, err) {
			return
		}
		approvalHeaders(w)
		for i := range out {
			out[i].ReviewURL = a.integrations.WebURL() + "/proposals/" + out[i].ID.String() + "?revision=" + strconv.FormatInt(out[i].Revision, 10)
		}
		respond(w, 200, out)
	})
	mux.HandleFunc("GET /api/v1/task-proposals/{proposal_id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := identifier(w, r.PathValue("proposal_id"), "path", "proposal_id")
		if !ok {
			return
		}
		tx, err := a.pool.Begin(r.Context())
		if dbError(w, err, "Task proposal") {
			return
		}
		defer tx.Rollback(r.Context())
		out, err := postgres.ReadTaskProposal(r.Context(), tx, id)
		if proposalError(w, err) {
			return
		}
		a.proposalResponse(w, 200, out)
	})
	mux.HandleFunc("PATCH /api/v1/task-proposals/{proposal_id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := identifier(w, r.PathValue("proposal_id"), "path", "proposal_id")
		if !ok {
			return
		}
		var in struct {
			ExpectedRevision     int64                  `json:"expected_revision"`
			ExpectedTaskRevision int64                  `json:"expected_task_revision"`
			Draft                postgres.ProposalDraft `json:"draft"`
		}
		if !proposalBody(w, r, &in, "expected_revision", "expected_task_revision", "draft") {
			return
		}
		tx, err := a.pool.Begin(r.Context())
		if dbError(w, err, "Task proposal") {
			return
		}
		defer tx.Rollback(r.Context())
		out, err := postgres.ReviseTaskProposal(r.Context(), tx, id, in.ExpectedRevision, in.ExpectedTaskRevision, in.Draft)
		if proposalError(w, err) {
			return
		}
		if dbError(w, tx.Commit(r.Context()), "Task proposal") {
			return
		}
		a.proposalResponse(w, 200, out)
	})
	mux.HandleFunc("POST /api/v1/task-proposals/{proposal_id}/decision", func(w http.ResponseWriter, r *http.Request) {
		user, secret, ok := a.approvalBrowser(w, r)
		if !ok {
			return
		}
		id, ok := identifier(w, r.PathValue("proposal_id"), "path", "proposal_id")
		if !ok {
			return
		}
		var in struct {
			ExpectedRevision int64  `json:"expected_revision"`
			Decision         string `json:"decision"`
			Reason           string `json:"reason"`
		}
		if !proposalBody(w, r, &in, "expected_revision", "decision", "reason") {
			return
		}
		tx, err := a.pool.Begin(r.Context())
		if dbError(w, err, "Task proposal") {
			return
		}
		defer tx.Rollback(r.Context())
		sessionHash := sha256.Sum256([]byte(secret))
		out, err := postgres.DecideTaskProposal(r.Context(), tx, id, in.ExpectedRevision, in.Decision, postgres.TaskApprover{ID: user.UserID, Login: user.Login, SessionHash: hex.EncodeToString(sessionHash[:])}, in.Reason)
		if proposalError(w, err) {
			return
		}
		if dbError(w, tx.Commit(r.Context()), "Task proposal") {
			return
		}
		a.proposalResponse(w, 200, out)
	})
	mux.HandleFunc("GET /api/v1/tasks/{task_id}/approval", func(w http.ResponseWriter, r *http.Request) {
		id, ok := identifier(w, r.PathValue("task_id"), "path", "task_id")
		if !ok {
			return
		}
		tx, err := a.pool.Begin(r.Context())
		if dbError(w, err, "Task") {
			return
		}
		defer tx.Rollback(r.Context())
		draft, err := postgres.TaskProposalDraft(r.Context(), tx, id)
		if proposalError(w, err) {
			return
		}
		err = postgres.RequireTaskApproval(r.Context(), tx, id)
		if err != nil && !errors.Is(err, postgres.ErrTaskApprovalRequired) {
			dbError(w, err, "Task")
			return
		}
		approvalHeaders(w)
		respond(w, 200, map[string]any{"approved": err == nil, "proposal": draft})
	})
}

// Used by legacy creation/import routes to return the same reviewable record.
func (a *api) proposeTask(w http.ResponseWriter, r *http.Request, tx pgx.Tx, in postgres.TaskProposalInput, source json.RawMessage) bool {
	if in.RequestKey == "" {
		in.RequestKey = uuid.NewString()
	}
	out, err := postgres.CreateTaskProposal(r.Context(), tx, in, source)
	if proposalError(w, err) {
		return false
	}
	if dbError(w, tx.Commit(r.Context()), "Task proposal") {
		return false
	}
	a.proposalResponse(w, 202, out)
	return true
}
