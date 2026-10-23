package httpapi

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/runstate"
)

func (a *api) workRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/tasks/{task_id}/work", a.taskWork)
	mux.HandleFunc("GET /api/v1/work-sessions/{session_id}", a.workSession)
	mux.HandleFunc("POST /api/v1/tasks/{task_id}/work/claim", a.claimTaskWork)
	mux.HandleFunc("POST /api/v1/work-sessions/{session_id}/heartbeat", a.heartbeatTaskWork)
	mux.HandleFunc("POST /api/v1/work-sessions/{session_id}/finish", a.finishTaskWork)
	mux.HandleFunc("POST /api/v1/tasks/{task_id}/work/release", a.releaseTaskWork)
}

func (a *api) taskWork(w http.ResponseWriter, r *http.Request) {
	id, ok := a.completionResource(w, r, "task_id", "tasks")
	if !ok {
		return
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if dbError(w, err, "Task") {
		return
	}
	defer rollback(r.Context(), tx)
	var task, project, repository json.RawMessage
	var board int64
	err = tx.QueryRow(r.Context(), `SELECT `+projection("TaskRead", "t")+`,`+projection("ProjectRead", "p")+`,CASE WHEN repo.id IS NULL THEN NULL ELSE `+projection("RepositoryRead", "repo")+` END,p.task_board_revision FROM tasks t JOIN projects p ON p.id=t.project_id LEFT JOIN repositories repo ON repo.id=t.repository_id WHERE t.id=$1`, id).Scan(&task, &project, &repository, &board)
	if dbError(w, err, "Task") {
		return
	}
	prerequisites, err := records(r.Context(), tx, "tasks", "WorkPrerequisiteRead", `WHERE t.id IN (SELECT prerequisite_task_id FROM task_dependencies WHERE task_id=$1) ORDER BY t.id`, id)
	if dbError(w, err, "Task prerequisites") {
		return
	}
	now := time.Now().UTC()
	owner, err := postgres.CurrentTaskWork(r.Context(), tx, id, now)
	if dbError(w, err, "Task owner") {
		return
	}
	view, err := postgres.InspectTaskCompletion(r.Context(), tx, id, now)
	if dbError(w, err, "Task completion") {
		return
	}
	if dbError(w, tx.Commit(r.Context()), "Task") {
		return
	}
	respond(w, 200, map[string]any{"task": task, "project": project, "repository": repository, "prerequisites": prerequisites, "owner": owner, "completion": view, "board_revision": board})
}
func (a *api) workSession(w http.ResponseWriter, r *http.Request) {
	id, ok := a.completionResource(w, r, "session_id", "work_sessions")
	if !ok {
		return
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if dbError(w, err, "Work session") {
		return
	}
	defer rollback(r.Context(), tx)
	session, err := postgres.InspectWorkSession(r.Context(), tx, id, time.Now().UTC())
	if dbError(w, err, "Work session") {
		return
	}
	receipts, err := postgres.WorkSessionReceipts(r.Context(), tx, id)
	if dbError(w, err, "Work receipts") {
		return
	}
	if dbError(w, tx.Commit(r.Context()), "Work session") {
		return
	}
	respond(w, 200, map[string]any{"session": session, "receipts": receipts})
}

func workSecret(w http.ResponseWriter, text string) ([]byte, bool) {
	secret, err := hex.DecodeString(text)
	if err != nil || len(secret) != 32 || text != strings.ToLower(text) {
		invalid(w, "body", "session_secret", "value_error", "A retained 256-bit random secret encoded as 64 lowercase hex characters is required")
		return nil, false
	}
	return secret, true
}

func workError(w http.ResponseWriter, err error, reload string) bool {
	if err == nil {
		return false
	}
	status, code := 422, "invalid_work_input"
	detail := err.Error()
	out := map[string]any{"action": "Reload with get_task_work or get_work_session before a new intent; retry an uncertain accepted operation with exactly the same IDs and arguments", "reload_path": reload}
	var owner *postgres.WorkOwnershipConflict
	var ineligible *postgres.WorkIneligibleError
	var blocked *postgres.CompletionConflict
	switch {
	case errors.As(err, &owner):
		status, code = 409, "task_owned"
		out["owner"] = owner.Owner
	case errors.As(err, &ineligible):
		code = "task_not_ready"
		out["reasons"], out["blocking_task_ids"] = ineligible.Reasons, ineligible.Blockers
	case errors.As(err, &blocked):
		status, code = 409, "completion_blocked"
		out["blockers"] = blocked.Blockers
	case errors.Is(err, postgres.ErrWorkCredential):
		status, code = 403, "work_credential_invalid"
		out["action"] = "Use the session ID and secret saved before the claim; inspection never recovers a secret"
	case errors.Is(err, postgres.ErrWorkRevoked):
		status, code = 409, "work_claim_lost"
		out["action"] = "Stop reporting for this session; reload with get_task_work and ask the user to resolve ownership"
	case errors.Is(err, postgres.ErrWorkReplayConflict):
		status, code = 409, "work_replay_conflict"
		out["action"] = "Reuse the original operation ID and exact original arguments; inspect current state before a new intent"
	case errors.Is(err, postgres.ErrTaskPlanningConflict):
		status, code = 409, "task_revision_conflict"
	case errors.Is(err, completion.ErrReport):
		code = "invalid_completion_report"
		out["action"] = "Reload get_task_completion and provide the result and evidence for every frozen requirement; the claim remains active"
	case errors.Is(err, postgres.ErrWorkInput):
		detail = "Supply valid session/operation IDs, a current revision or positive sequence, bounded client metadata, and a terminal outcome with matching summary/report"
	default:
		return dbError(w, err, "Work session")
	}
	out["detail"], out["code"] = detail, code
	respond(w, status, out)
	return true
}

// Read revisions before commit, so successful mutations return the state they saved.
func finishWorkResponse(w http.ResponseWriter, r *http.Request, tx pgx.Tx, task uuid.UUID, status int, out map[string]any) {
	var revision, board int64
	err := tx.QueryRow(r.Context(), `SELECT t.revision,p.task_board_revision FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.id=$1`, task).Scan(&revision, &board)
	if dbError(w, err, "Task") || dbError(w, tx.Commit(r.Context()), "Work session") {
		return
	}
	out["task_revision"], out["board_revision"] = revision, board
	respond(w, status, out)
}
func (a *api) claimTaskWork(w http.ResponseWriter, r *http.Request) {
	id, ok := a.completionResource(w, r, "task_id", "tasks")
	if !ok {
		return
	}
	var in struct {
		SessionID        uuid.UUID `json:"session_id"`
		Secret           string    `json:"session_secret"`
		ExpectedRevision int64     `json:"expected_revision"`
		ClientName       string    `json:"client_name"`
		ClientVersion    string    `json:"client_version"`
	}
	if !completionBody(w, r, &in) {
		return
	}
	secret, ok := workSecret(w, in.Secret)
	if !ok {
		return
	}
	tx, err := a.pool.Begin(r.Context())
	if dbError(w, err, "Work session") {
		return
	}
	defer rollback(r.Context(), tx)
	out, err := postgres.ClaimExternalWork(r.Context(), tx, postgres.ExternalWorkClaim{TaskID: id, SessionID: in.SessionID, Secret: secret, ExpectedRevision: in.ExpectedRevision, ClientName: in.ClientName, ClientVersion: in.ClientVersion}, time.Now().UTC())
	if workError(w, err, "/api/v1/tasks/"+id.String()+"/work") {
		return
	}
	status := 200
	if out.Created {
		status = 201
	}
	finishWorkResponse(w, r, tx, id, status, map[string]any{"session": out.Session, "created": out.Created, "credential": map[string]any{"session_id": out.Session.ID, "session_secret": in.Secret}})
}
func (a *api) heartbeatTaskWork(w http.ResponseWriter, r *http.Request) {
	id, ok := a.completionResource(w, r, "session_id", "work_sessions")
	if !ok {
		return
	}
	var in struct {
		Secret   string `json:"session_secret"`
		Sequence int64  `json:"sequence"`
	}
	if !completionBody(w, r, &in) {
		return
	}
	secret, ok := workSecret(w, in.Secret)
	if !ok {
		return
	}
	tx, err := a.pool.Begin(r.Context())
	if dbError(w, err, "Work session") {
		return
	}
	defer rollback(r.Context(), tx)
	out, err := postgres.HeartbeatWork(r.Context(), tx, postgres.WorkCredential{SessionID: id, Secret: secret}, in.Sequence, time.Now().UTC())
	if workError(w, err, "/api/v1/work-sessions/"+id.String()) || dbError(w, tx.Commit(r.Context()), "Work session") {
		return
	}
	respond(w, 200, map[string]any{"session": out})
}
func (a *api) finishTaskWork(w http.ResponseWriter, r *http.Request) {
	id, ok := a.completionResource(w, r, "session_id", "work_sessions")
	if !ok {
		return
	}
	var in struct {
		Secret      string             `json:"session_secret"`
		OperationID uuid.UUID          `json:"operation_id"`
		Outcome     runstate.Status    `json:"outcome"`
		Summary     string             `json:"summary"`
		Report      *completion.Report `json:"report,omitempty"`
	}
	if !completionBody(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Summary) == "" {
		invalid(w, "body", "summary", "value_error", "A nonblank summary is required for every outcome")
		return
	}
	secret, ok := workSecret(w, in.Secret)
	if !ok {
		return
	}
	tx, err := a.pool.Begin(r.Context())
	if dbError(w, err, "Work session") {
		return
	}
	defer rollback(r.Context(), tx)
	out, err := postgres.FinishExternalWork(r.Context(), tx, postgres.WorkFinishInput{Credential: postgres.WorkCredential{SessionID: id, Secret: secret}, OperationID: in.OperationID, Outcome: in.Outcome, Summary: in.Summary, Report: in.Report}, time.Now().UTC())
	if workError(w, err, "/api/v1/work-sessions/"+id.String()) {
		return
	}
	finishWorkResponse(w, r, tx, out.Session.TaskID, 200, workChangeFields(out))
}
func (a *api) releaseTaskWork(w http.ResponseWriter, r *http.Request) {
	id, ok := a.completionResource(w, r, "task_id", "tasks")
	if !ok {
		return
	}
	var in struct {
		ExpectedSessionID uuid.UUID `json:"expected_session_id"`
		OperationID       uuid.UUID `json:"operation_id"`
		Reason            string    `json:"reason"`
	}
	if !completionBody(w, r, &in) {
		return
	}
	tx, err := a.pool.Begin(r.Context())
	if dbError(w, err, "Work session") {
		return
	}
	defer rollback(r.Context(), tx)
	out, err := postgres.ReleaseTaskWork(r.Context(), tx, postgres.WorkAdminInput{TaskID: id, ExpectedSessionID: in.ExpectedSessionID, OperationID: in.OperationID, Reason: in.Reason}, time.Now().UTC())
	if workError(w, err, "/api/v1/tasks/"+id.String()+"/work") {
		return
	}
	finishWorkResponse(w, r, tx, id, 200, workChangeFields(out))
}
func workChangeFields(out postgres.WorkChangeResult) map[string]any {
	return map[string]any{"session": out.Session, "receipt": out.Receipt, "replacement": out.Replacement, "pending_cleanup": out.PendingCleanup}
}
