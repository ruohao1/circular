package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type TaskProposalNeeded struct{ Proposal TaskProposal }

func (e *TaskProposalNeeded) Error() string { return ErrTaskApprovalRequired.Error() }
func (e *TaskProposalNeeded) Unwrap() error { return ErrTaskApprovalRequired }

// Only trusted provider readers call this after fetching issue content and its
// workspace. Public proposal inputs cannot supply these source bindings.
func ProposeLinearTask(ctx context.Context, tx pgx.Tx, project, repository uuid.UUID, title, description string, source json.RawMessage) (uuid.UUID, *TaskProposal, error) {
	var refs struct {
		Linear struct {
			IssueID    string `json:"issue_id"`
			AccountID  string `json:"account_id"`
			Identifier string `json:"identifier"`
			URL        string `json:"url"`
		} `json:"linear"`
	}
	if json.Unmarshal(source, &refs) != nil || refs.Linear.IssueID == "" || refs.Linear.AccountID == "" {
		return uuid.Nil, nil, ErrTaskProposalInput
	}
	if err := proposalProjectLock(ctx, tx, project); err != nil {
		return uuid.Nil, nil, err
	}
	in := TaskProposalInput{ProjectID: project, Proposer: "Linear", Draft: ProposalDraft{Title: title, Description: description, RepositoryID: &repository, Purpose: "Complete " + refs.Linear.Identifier, Sources: []ProposalSource{{Label: refs.Linear.Identifier, URL: refs.Linear.URL}}}}
	var id uuid.UUID
	var account string
	err := tx.QueryRow(ctx, `SELECT id,COALESCE(external_refs->'linear'->>'account_id','') FROM tasks WHERE project_id=$1 AND external_refs->'linear'->>'issue_id'=$2`, project, refs.Linear.IssueID).Scan(&id, &account)
	if err == nil {
		if account != refs.Linear.AccountID {
			return uuid.Nil, nil, ErrTaskProposalConflict
		}
		in, err = TaskProposalDraft(ctx, tx, id)
		if err != nil {
			return uuid.Nil, nil, err
		}
		if !sameOptionalUUID(in.Draft.RepositoryID, &repository) {
			return uuid.Nil, nil, ErrTaskProposalConflict
		}
		if in.Draft.Title == title && in.Draft.Description == description {
			err = RequireTaskApproval(ctx, tx, id)
			if err == nil {
				return id, nil, nil
			}
			if !errors.Is(err, ErrTaskApprovalRequired) {
				return uuid.Nil, nil, err
			}
		}
		in.Proposer = "Linear"
		in.Draft.Title = title
		in.Draft.Description = description
		in.Draft.RepositoryID = &repository
		if in.Draft.Purpose == "" {
			in.Draft.Purpose = "Complete " + refs.Linear.Identifier
		}
		in.Draft.Sources = []ProposalSource{{Label: refs.Linear.Identifier, URL: refs.Linear.URL}}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil, err
	}
	in.RequestKey = ""
	raw, _ := json.Marshal(in)
	hash := sha256.Sum256(append(raw, source...))
	in.RequestKey = "linear:" + hex.EncodeToString(hash[:])
	p, err := CreateTaskProposal(ctx, tx, in, source)
	if err != nil {
		return uuid.Nil, nil, err
	}
	if p.Status == "approved" && p.TaskID != nil && RequireTaskApproval(ctx, tx, *p.TaskID) == nil {
		return *p.TaskID, nil, nil
	}
	return uuid.Nil, &p, nil
}

type trustedProposalSourceContext struct{}

func trustedProposalSource(ctx context.Context) bool {
	v, _ := ctx.Value(trustedProposalSourceContext{}).(bool)
	return v
}
func sourceProposalContext(ctx context.Context, tx pgx.Tx, p TaskProposal) (context.Context, error) {
	if p.TargetTaskID == nil {
		return ctx, nil
	}
	var same bool
	if err := tx.QueryRow(ctx, `SELECT $2::jsonb->'linear'->>'issue_id' IS NOT NULL AND external_refs->'linear'->>'issue_id'=$2::jsonb->'linear'->>'issue_id' AND external_refs->'linear'->>'account_id'=$2::jsonb->'linear'->>'account_id' FROM tasks WHERE id=$1`, p.TargetTaskID, p.Source).Scan(&same); err != nil {
		return ctx, err
	}
	if same {
		return context.WithValue(ctx, trustedProposalSourceContext{}, true), nil
	}
	return ctx, nil
}

func validateProposalSource(ctx context.Context, tx pgx.Tx, p TaskProposal) error {
	var bound bool
	if err := tx.QueryRow(ctx, `SELECT source->'linear'->>'issue_id' IS NOT NULL FROM task_proposals WHERE id=$1`, p.ID).Scan(&bound); err != nil {
		return err
	}
	if !bound {
		return nil
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT draft FROM task_proposal_revisions WHERE proposal_id=$1 AND revision=1`, p.ID).Scan(&raw); err != nil {
		return err
	}
	var original ProposalDraft
	if json.Unmarshal(raw, &original) != nil {
		return ErrTaskProposalConflict
	}
	if p.Draft.Title != original.Title || p.Draft.Description != original.Description || !sameOptionalUUID(p.Draft.RepositoryID, original.RepositoryID) {
		return ErrTaskProposalConflict
	}
	if p.TargetTaskID == nil {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tasks t WHERE project_id=$1 AND t.external_refs->'linear'->>'issue_id'=$2::jsonb->'linear'->>'issue_id')`, p.ProjectID, p.Source).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return ErrTaskProposalConflict
		}
	}
	return nil
}
