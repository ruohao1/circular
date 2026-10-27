package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/completion"
)

var (
	ErrTaskProposalInput    = errors.New("invalid task proposal; resolve open questions and provide the work to review")
	ErrTaskProposalConflict = errors.New("the proposal or Task changed; read the current revision before deciding")
	ErrTaskProposalSession  = errors.New("The review session expired; sign in again")
	ErrTaskApprovalRequired = errors.New("this Task scope needs user approval in Circular before work can start")
)

type ProposalSource struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}
type ProposalDraft struct {
	Priority            string            `json:"priority"`
	PreferredAgentID    *uuid.UUID        `json:"preferred_agent_id"`
	PlannedStartDate    *string           `json:"planned_start_date"`
	PlannedFinishDate   *string           `json:"planned_finish_date"`
	Title               string            `json:"title"`
	Purpose             string            `json:"purpose"`
	Description         string            `json:"description"`
	AcceptanceCriteria  []string          `json:"acceptance_criteria"`
	OpenQuestions       []string          `json:"open_questions"`
	RepositoryID        *uuid.UUID        `json:"repository_id"`
	ParentTaskID        *uuid.UUID        `json:"parent_task_id"`
	PrerequisiteTaskIDs []uuid.UUID       `json:"prerequisite_task_ids"`
	CompletionPolicy    completion.Policy `json:"completion_policy"`
	Sources             []ProposalSource  `json:"sources"`
}
type TaskProposalInput struct {
	ProjectID            uuid.UUID     `json:"project_id"`
	RequestKey           string        `json:"request_key"`
	TargetTaskID         *uuid.UUID    `json:"target_task_id"`
	ExpectedTaskRevision int64         `json:"expected_task_revision"`
	Proposer             string        `json:"proposer"`
	Draft                ProposalDraft `json:"draft"`
}
type TaskApprover struct {
	SessionHash string `json:"-"`
	ID          string `json:"id"`
	Login       string `json:"login"`
}
type TaskProposalDecision struct {
	Revision      int64     `json:"revision"`
	Decision      string    `json:"decision"`
	ApproverID    string    `json:"approver_id"`
	ApproverLogin string    `json:"approver_login"`
	Reason        string    `json:"reason"`
	CreatedAt     time.Time `json:"created_at"`
}
type TaskProposal struct {
	ID                   uuid.UUID             `json:"id"`
	ProjectID            uuid.UUID             `json:"project_id"`
	TargetTaskID         *uuid.UUID            `json:"target_task_id"`
	TaskID               *uuid.UUID            `json:"task_id"`
	Revision             int64                 `json:"revision"`
	ExpectedTaskRevision int64                 `json:"expected_task_revision"`
	Status               string                `json:"status"`
	Proposer             string                `json:"proposer"`
	Source               json.RawMessage       `json:"source"`
	Draft                ProposalDraft         `json:"draft"`
	Decision             *TaskProposalDecision `json:"decision"`
	CreatedAt            time.Time             `json:"created_at"`
	UpdatedAt            time.Time             `json:"updated_at"`
	ReviewURL            string                `json:"review_url,omitempty"`
}

func normalizeProposal(d ProposalDraft) (ProposalDraft, error) {
	d.Title = strings.TrimSpace(d.Title)
	if d.Priority == "" {
		d.Priority = "none"
	}
	if !ValidTaskPriority(d.Priority) || ValidateTaskSchedule(d.PlannedStartDate, d.PlannedFinishDate) != nil {
		return d, ErrTaskProposalInput
	}
	text := func(s string, n int) bool {
		return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && utf8.RuneCountInString(s) <= n
	}
	if d.Title == "" || !text(d.Title, 500) || !text(d.Purpose, 4000) || !text(d.Description, 20000) || len(d.AcceptanceCriteria) > 20 || len(d.OpenQuestions) > 20 || len(d.PrerequisiteTaskIDs) > 100 || len(d.Sources) > 20 {
		return d, ErrTaskProposalInput
	}
	for _, list := range [][]string{d.AcceptanceCriteria, d.OpenQuestions} {
		for _, s := range list {
			if strings.TrimSpace(s) == "" || !text(s, 2000) {
				return d, ErrTaskProposalInput
			}
		}
	}
	for _, s := range d.Sources {
		u, e := url.Parse(s.URL)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || !text(s.Label, 200) || !text(s.URL, 2048) {
			return d, ErrTaskProposalInput
		}
	}
	for _, id := range []*uuid.UUID{d.RepositoryID, d.ParentTaskID, d.PreferredAgentID} {
		if id != nil && *id == uuid.Nil {
			return d, ErrTaskProposalInput
		}
	}
	d.PrerequisiteTaskIDs = slices.Clone(d.PrerequisiteTaskIDs)
	slices.SortFunc(d.PrerequisiteTaskIDs, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	for i, id := range d.PrerequisiteTaskIDs {
		if id == uuid.Nil || i > 0 && id == d.PrerequisiteTaskIDs[i-1] {
			return d, ErrTaskProposalInput
		}
	}
	if d.CompletionPolicy.Mode == "" {
		d.CompletionPolicy.Mode = "unresolved"
	}
	if d.CompletionPolicy.Mode != "unresolved" {
		if completion.ValidatePolicy(d.CompletionPolicy) != nil {
			return d, ErrTaskProposalInput
		}
	} else if len(d.CompletionPolicy.Requirements) != 0 {
		return d, ErrTaskProposalInput
	}
	if d.AcceptanceCriteria == nil {
		d.AcceptanceCriteria = []string{}
	}
	if d.OpenQuestions == nil {
		d.OpenQuestions = []string{}
	}
	if d.Sources == nil {
		d.Sources = []ProposalSource{}
	}
	if d.PrerequisiteTaskIDs == nil {
		d.PrerequisiteTaskIDs = []uuid.UUID{}
	}
	if d.CompletionPolicy.Requirements == nil {
		d.CompletionPolicy.Requirements = []completion.Requirement{}
	}
	return d, nil
}
func proposalProjectLock(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	var found uuid.UUID
	return tx.QueryRow(ctx, `SELECT id FROM projects WHERE id=$1 FOR NO KEY UPDATE`, id).Scan(&found)
}
func validateProposalLinks(ctx context.Context, tx pgx.Tx, project uuid.UUID, target *uuid.UUID, d ProposalDraft) error {
	if d.RepositoryID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM repositories WHERE id=$1 AND project_id=$2)`, d.RepositoryID, project).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrTaskProposalInput
		}
	}
	if d.PreferredAgentID != nil {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=$1 AND project_id=$2 AND enabled)`, d.PreferredAgentID, project).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrTaskProposalInput
		}
	}
	ids := slices.Clone(d.PrerequisiteTaskIDs)
	if d.ParentTaskID != nil {
		ids = append(ids, *d.ParentTaskID)
	}
	for _, id := range ids {
		if target != nil && id == *target {
			return ErrTaskProposalInput
		}
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(DISTINCT id) FROM tasks WHERE project_id=$1 AND id=ANY($2::uuid[])`, project, ids).Scan(&count); err != nil {
		return err
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
	if count != len(slices.Compact(ids)) {
		return ErrTaskProposalInput
	}
	return nil
}
func CreateTaskProposal(ctx context.Context, tx pgx.Tx, in TaskProposalInput, source json.RawMessage) (TaskProposal, error) {
	var out TaskProposal
	var err error
	in.Draft, err = normalizeProposal(in.Draft)
	if err != nil {
		return out, err
	}
	if in.ProjectID == uuid.Nil || strings.TrimSpace(in.RequestKey) == "" || utf8.RuneCountInString(in.RequestKey) > 200 || !utf8.ValidString(in.RequestKey) || strings.ContainsRune(in.RequestKey, 0) || utf8.RuneCountInString(in.Proposer) > 200 || !utf8.ValidString(in.Proposer) || strings.ContainsRune(in.Proposer, 0) || in.TargetTaskID != nil && (*in.TargetTaskID == uuid.Nil || in.ExpectedTaskRevision < 1) || in.TargetTaskID == nil && in.ExpectedTaskRevision != 0 {
		return out, ErrTaskProposalInput
	}
	if len(source) == 0 {
		source = json.RawMessage(`{}`)
	}
	if err = proposalProjectLock(ctx, tx, in.ProjectID); err != nil {
		return out, err
	}
	raw, _ := json.Marshal(in)
	hash := sha256.Sum256(append(raw, source...))
	fingerprint := hex.EncodeToString(hash[:])
	var existing uuid.UUID
	var previous string
	err = tx.QueryRow(ctx, `SELECT id,input_digest FROM task_proposals WHERE project_id=$1 AND request_key=$2`, in.ProjectID, in.RequestKey).Scan(&existing, &previous)
	if err == nil {
		if previous != fingerprint {
			return out, ErrTaskProposalConflict
		}
		return ReadTaskProposal(ctx, tx, existing)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if in.TargetTaskID != nil {
		var project uuid.UUID
		var revision int64
		if err = tx.QueryRow(ctx, `SELECT project_id,revision FROM tasks WHERE id=$1`, in.TargetTaskID).Scan(&project, &revision); err != nil {
			return out, err
		}
		if project != in.ProjectID || revision != in.ExpectedTaskRevision {
			return out, ErrTaskProposalConflict
		}
	}
	if err = validateProposalLinks(ctx, tx, in.ProjectID, in.TargetTaskID, in.Draft); err != nil {
		return out, err
	}
	id := uuid.New()
	draft, _ := json.Marshal(in.Draft)
	if _, err = tx.Exec(ctx, `INSERT INTO task_proposals(id,project_id,request_key,input_digest,target_task_id,proposer,source) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, in.ProjectID, in.RequestKey, fingerprint, in.TargetTaskID, in.Proposer, source); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO task_proposal_revisions(proposal_id,revision,expected_task_revision,draft) VALUES($1,1,$2,$3)`, id, in.ExpectedTaskRevision, draft); err != nil {
		return out, err
	}
	return ReadTaskProposal(ctx, tx, id)
}
func ReadTaskProposal(ctx context.Context, tx pgx.Tx, id uuid.UUID) (TaskProposal, error) {
	var out TaskProposal
	var draft, decision []byte
	err := tx.QueryRow(ctx, `SELECT p.id,p.project_id,p.target_task_id,p.task_id,p.revision,r.expected_task_revision,p.status,p.proposer,p.source,r.draft,CASE WHEN d.proposal_id IS NULL THEN NULL ELSE to_jsonb(d)-'approved_scope'-'task_id'-'proposal_id' END,p.created_at,p.updated_at FROM task_proposals p JOIN task_proposal_revisions r ON r.proposal_id=p.id AND r.revision=p.revision LEFT JOIN task_proposal_decisions d ON d.proposal_id=p.id AND d.revision=p.revision WHERE p.id=$1`, id).Scan(&out.ID, &out.ProjectID, &out.TargetTaskID, &out.TaskID, &out.Revision, &out.ExpectedTaskRevision, &out.Status, &out.Proposer, &out.Source, &draft, &decision, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(draft, &out.Draft); err != nil {
		return out, err
	}
	if len(decision) > 0 {
		err = json.Unmarshal(decision, &out.Decision)
	}
	return out, err
}
func lockTaskProposal(ctx context.Context, tx pgx.Tx, id uuid.UUID) (TaskProposal, error) {
	var project uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT project_id FROM task_proposals WHERE id=$1`, id).Scan(&project); err != nil {
		return TaskProposal{}, err
	}
	if err := proposalProjectLock(ctx, tx, project); err != nil {
		return TaskProposal{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM task_proposals WHERE id=$1 FOR UPDATE`, id); err != nil {
		return TaskProposal{}, err
	}
	return ReadTaskProposal(ctx, tx, id)
}
func ReviseTaskProposal(ctx context.Context, tx pgx.Tx, id uuid.UUID, expected, expectedTask int64, draft ProposalDraft) (TaskProposal, error) {
	p, err := lockTaskProposal(ctx, tx, id)
	if err != nil {
		return p, err
	}
	if (p.Status != "pending" && p.Status != "rejected") || p.Revision != expected || expected >= maxPlanningRevision {
		return p, ErrTaskProposalConflict
	}
	draft, err = normalizeProposal(draft)
	if err != nil {
		return p, err
	}
	if p.TargetTaskID != nil {
		var rev int64
		if err = tx.QueryRow(ctx, `SELECT revision FROM tasks WHERE id=$1`, p.TargetTaskID).Scan(&rev); err != nil {
			return p, err
		}
		if rev != expectedTask {
			return p, ErrTaskProposalConflict
		}
	} else if expectedTask != 0 {
		return p, ErrTaskProposalInput
	}
	if err = validateProposalLinks(ctx, tx, p.ProjectID, p.TargetTaskID, draft); err != nil {
		return p, err
	}
	raw, _ := json.Marshal(draft)
	if _, err = tx.Exec(ctx, `INSERT INTO task_proposal_revisions(proposal_id,revision,expected_task_revision,draft) VALUES($1,$2,$3,$4)`, id, expected+1, expectedTask, raw); err != nil {
		return p, err
	}
	if _, err = tx.Exec(ctx, `UPDATE task_proposals SET revision=revision+1,status='pending',updated_at=now() WHERE id=$1`, id); err != nil {
		return p, err
	}
	return ReadTaskProposal(ctx, tx, id)
}

// Scope excludes scheduling and execution state, which do not authorize new work.
const taskApprovalScopeSQL = `jsonb_build_object('project_id',t.project_id,'title',t.title,'description',t.description,'repository_id',t.repository_id,'parent_task_id',t.parent_task_id,'approval_context',t.approval_context,'completion_policy',t.completion_policy,'prerequisite_task_ids',COALESCE((SELECT jsonb_agg(d.prerequisite_task_id ORDER BY d.prerequisite_task_id) FROM task_dependencies d WHERE d.task_id=t.id),'[]'::jsonb),'source',jsonb_build_object('linear',t.external_refs::jsonb->'linear','circular',t.external_refs::jsonb->'circular'))`

func RequireTaskApproval(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM task_proposal_decisions d WHERE d.task_id=t.id AND d.decision='approved' AND d.approved_scope=`+taskApprovalScopeSQL+`) FROM tasks t WHERE t.id=$1`, id).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrTaskApprovalRequired
	}
	return nil
}

func DecideTaskProposal(ctx context.Context, tx pgx.Tx, id uuid.UUID, revision int64, decision string, actor TaskApprover, reason string) (TaskProposal, error) {
	p, err := lockTaskProposal(ctx, tx, id)
	if err != nil {
		return p, err
	}
	if actor.SessionHash != "" {
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM approval_sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp())`, actor.SessionHash, actor.ID).Scan(&valid); err != nil {
			return p, err
		}
		if !valid {
			return p, ErrTaskProposalSession
		}
	}
	user, e := strconv.ParseUint(actor.ID, 10, 64)
	if e != nil || user == 0 || strconv.FormatUint(user, 10) != actor.ID || strings.TrimSpace(actor.Login) == "" || len(actor.Login) > 100 || !utf8.ValidString(actor.Login) || strings.ContainsRune(actor.Login, 0) || utf8.RuneCountInString(reason) > 4000 || !utf8.ValidString(reason) || strings.ContainsRune(reason, 0) || (decision != "approved" && decision != "rejected") {
		return p, ErrTaskProposalInput
	}
	if p.Revision != revision {
		return p, ErrTaskProposalConflict
	}
	if p.Status != "pending" {
		if p.Decision != nil && p.Decision.Decision == decision && p.Decision.ApproverID == actor.ID && p.Decision.Reason == reason {
			return p, nil
		}
		return p, ErrTaskProposalConflict
	}
	var taskID *uuid.UUID
	if decision == "approved" {
		d := p.Draft
		if err = validateProposalSource(ctx, tx, p); err != nil {
			return p, err
		}
		if strings.TrimSpace(d.Purpose) == "" || strings.TrimSpace(d.Description) == "" || len(d.AcceptanceCriteria) == 0 || len(d.OpenQuestions) > 0 || completion.ValidatePolicy(d.CompletionPolicy) != nil {
			return p, ErrTaskProposalInput
		}
		if err = validateProposalLinks(ctx, tx, p.ProjectID, p.TargetTaskID, d); err != nil {
			return p, err
		}
		contextJSON, _ := json.Marshal(map[string]any{"purpose": d.Purpose, "acceptance_criteria": d.AcceptanceCriteria})
		taskID = p.TargetTaskID
		if taskID == nil {
			created := uuid.New()
			taskID = &created
			status := "open"
			var blocked bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE id=ANY($1::uuid[]) AND status<>'completed')`, d.PrerequisiteTaskIDs).Scan(&blocked); err != nil {
				return p, err
			}
			if d.RepositoryID != nil && !blocked {
				status = "ready"
			}
			policy, _ := json.Marshal(d.CompletionPolicy)
			if _, err = tx.Exec(ctx, `INSERT INTO tasks(id,project_id,repository_id,parent_task_id,title,description,status,external_refs,completion_policy,priority,preferred_agent_id,planned_start_date,planned_finish_date,approval_context) VALUES($1,$2,$3,$4,$5,$6,$13,$7,$8,$9,$10,$11::text::date,$12::text::date,$14)`, created, p.ProjectID, d.RepositoryID, d.ParentTaskID, d.Title, d.Description, p.Source, policy, d.Priority, d.PreferredAgentID, d.PlannedStartDate, d.PlannedFinishDate, status, contextJSON); err != nil {
				return p, err
			}
			t, err := lockPlanningTask(ctx, tx, created)
			if err != nil {
				return p, err
			}
			if _, err = replaceTaskPrerequisites(ctx, tx, t, d.PrerequisiteTaskIDs); err != nil {
				return p, err
			}

		} else {
			t, err := lockPlanningTask(ctx, tx, *taskID)
			if err != nil {
				return p, err
			}
			if t.revision != p.ExpectedTaskRevision {
				return p, ErrTaskProposalConflict
			}
			var parent *uuid.UUID
			var contextChanged, active bool
			if err = tx.QueryRow(ctx, `SELECT approval_context<>$2::jsonb,EXISTS(SELECT 1 FROM runs WHERE task_id=$1 AND status IN ('queued','provisioning','running','waiting_for_approval','waiting_for_input','finalizing')) OR EXISTS(SELECT 1 FROM work_sessions WHERE task_id=$1 AND released_at IS NULL) FROM tasks WHERE id=$1`, taskID, contextJSON).Scan(&contextChanged, &active); err != nil {
				return p, err
			}
			if contextChanged && active {
				return p, ErrTaskPlanningConflict
			}
			var policy []byte
			if err = tx.QueryRow(ctx, `SELECT parent_task_id,completion_policy FROM tasks WHERE id=$1`, taskID).Scan(&parent, &policy); err != nil {
				return p, err
			}
			if !sameOptionalUUID(parent, d.ParentTaskID) {
				return p, ErrTaskProposalConflict
			}
			approved, err := sourceProposalContext(ctx, tx, p)
			if err != nil {
				return p, err
			}
			result, err := UpdateTaskPlanning(approved, tx, *taskID, t.revision, TaskPlanningPatch{Title: &d.Title, Description: &d.Description, RepositorySet: true, RepositoryID: d.RepositoryID, PrerequisitesSet: true, PrerequisiteTaskIDs: d.PrerequisiteTaskIDs, Priority: &d.Priority, PreferredAgentSet: true, PreferredAgentID: d.PreferredAgentID, ExpectedBoardRevision: &t.board, PlannedStartSet: true, PlannedFinishSet: true, PlannedStartDate: d.PlannedStartDate, PlannedFinishDate: d.PlannedFinishDate})
			if err != nil {
				return p, err
			}
			var current completion.Policy
			if len(policy) > 0 && json.Unmarshal(policy, &current) != nil {
				return p, ErrTaskProposalConflict
			}
			if current.Requirements == nil {
				current.Requirements = []completion.Requirement{}
			}
			old, _ := json.Marshal(current)
			next, _ := json.Marshal(d.CompletionPolicy)
			changed := result.Changed
			if string(old) != string(next) {
				if err = SetTaskCompletionPolicy(approved, tx, *taskID, result.Revision, d.CompletionPolicy); err != nil {
					return p, err
				}
				changed = true
			}
			if contextChanged {
				if _, err = tx.Exec(ctx, `UPDATE tasks SET approval_context=$2,revision=revision+CASE WHEN $3 THEN 0 ELSE 1 END,updated_at=now() WHERE id=$1`, taskID, contextJSON, changed); err != nil {
					return p, err
				}
				if !changed {
					if _, err = tx.Exec(ctx, `UPDATE projects SET task_board_revision=task_board_revision+1 WHERE id=$1`, p.ProjectID); err != nil {
						return p, err
					}
				}
			}
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO task_proposal_decisions(proposal_id,revision,decision,approver_id,approver_login,reason,task_id,approved_scope) VALUES($1,$2,$3,$4,$5,$6,$7,(SELECT `+taskApprovalScopeSQL+` FROM tasks t WHERE t.id=$7))`, id, revision, decision, actor.ID, actor.Login, reason, taskID); err != nil {
		return p, err
	}
	if _, err = tx.Exec(ctx, `UPDATE task_proposals SET status=$2,task_id=$3,updated_at=now() WHERE id=$1`, id, decision, taskID); err != nil {
		return p, err
	}
	return ReadTaskProposal(ctx, tx, id)
}

func ListTaskProposals(ctx context.Context, tx pgx.Tx, project uuid.UUID) ([]TaskProposal, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM task_proposals WHERE project_id=$1 ORDER BY created_at DESC,id DESC`, project)
	if err != nil {
		return nil, err
	}
	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := []TaskProposal{}
	for _, id := range ids {
		p, err := ReadTaskProposal(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func TaskProposalDraft(ctx context.Context, tx pgx.Tx, id uuid.UUID) (TaskProposalInput, error) {
	var in TaskProposalInput
	var raw, contextJSON []byte
	in.TargetTaskID = &id
	in.RequestKey = uuid.NewString()
	err := tx.QueryRow(ctx, `SELECT project_id,revision,title,description,repository_id,parent_task_id,completion_policy,priority,preferred_agent_id,to_char(planned_start_date,'YYYY-MM-DD'),to_char(planned_finish_date,'YYYY-MM-DD'),approval_context FROM tasks WHERE id=$1`, id).Scan(&in.ProjectID, &in.ExpectedTaskRevision, &in.Draft.Title, &in.Draft.Description, &in.Draft.RepositoryID, &in.Draft.ParentTaskID, &raw, &in.Draft.Priority, &in.Draft.PreferredAgentID, &in.Draft.PlannedStartDate, &in.Draft.PlannedFinishDate, &contextJSON)
	if err != nil {
		return in, err
	}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &in.Draft.CompletionPolicy); err != nil {
			return in, err
		}
	}
	if in.Draft.PrerequisiteTaskIDs, err = TaskPrerequisites(ctx, tx, id); err != nil {
		return in, err
	}
	if err = json.Unmarshal(contextJSON, &in.Draft); err != nil {
		return in, err
	}
	// Source links provide review context; they are not an approval credential.
	var previous []byte
	err = tx.QueryRow(ctx, `SELECT r.draft FROM task_proposal_decisions d JOIN task_proposal_revisions r ON r.proposal_id=d.proposal_id AND r.revision=d.revision JOIN tasks t ON t.id=d.task_id WHERE d.task_id=$1 AND d.decision='approved' ORDER BY (d.approved_scope=`+taskApprovalScopeSQL+`) DESC,d.created_at DESC LIMIT 1`, id).Scan(&previous)
	if err == nil {
		var d ProposalDraft
		if err = json.Unmarshal(previous, &d); err != nil {
			return in, err
		}
		in.Draft.Sources = d.Sources
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return in, err
	}
	in.Draft, err = normalizeProposal(in.Draft)
	return in, err
}
