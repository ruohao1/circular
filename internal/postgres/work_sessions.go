package postgres

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/runstate"
)

var (
	ErrWorkInput          = errors.New("invalid work session input")
	ErrWorkCredential     = errors.New("invalid work session credential")
	ErrWorkReplayConflict = errors.New("operation identity was already used with different inputs")
	ErrWorkRevoked        = errors.New("work session no longer owns this Task")
)

type ExternalWorkClaim struct {
	SessionID, TaskID         uuid.UUID
	ExpectedRevision          int64
	ClientName, ClientVersion string
	Secret                    []byte `json:"-"`
}
type WorkCredential struct {
	SessionID uuid.UUID
	Secret    []byte `json:"-"`
}
type WorkSessionView struct {
	ID                  uuid.UUID        `json:"id"`
	ProjectID           uuid.UUID        `json:"project_id"`
	TaskID              uuid.UUID        `json:"task_id"`
	RunID               *uuid.UUID       `json:"run_id"`
	AgentID             *uuid.UUID       `json:"agent_id"`
	Kind                string           `json:"kind"`
	ClientName          string           `json:"client_name"`
	ClientVersion       string           `json:"client_version"`
	ClaimedAt           time.Time        `json:"claimed_at"`
	StartedAt           *time.Time       `json:"started_at"`
	LastContactAt       *time.Time       `json:"last_contact_at"`
	FinishedAt          *time.Time       `json:"finished_at"`
	ReleasedAt          *time.Time       `json:"released_at"`
	HeartbeatSequence   int64            `json:"heartbeat_sequence"`
	Outcome             *runstate.Status `json:"outcome"`
	ReleaseReason       string           `json:"release_reason"`
	NeedsUserResolution bool             `json:"needs_user_resolution"`
	ContactState        string           `json:"contact_state"`
}
type WorkClaimResult struct {
	Session WorkSessionView
	Created bool
}
type WorkOwnershipConflict struct{ Owner WorkSessionView }

func (e *WorkOwnershipConflict) Error() string {
	return fmt.Sprintf("Task is owned by %s (session %s); resolve its ownership before starting work", e.Owner.ClientName, e.Owner.ID)
}

type WorkIneligibleError struct {
	Reasons  []string
	Blockers []uuid.UUID
}

func (e *WorkIneligibleError) Error() string {
	return "Task cannot start: " + strings.Join(e.Reasons, "; ")
}

type workSessionRow struct {
	view                          WorkSessionView
	secretDigest, admissionDigest []byte
}

func validWorkText(s string, limit int) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && utf8.RuneCountInString(s) <= limit
}
func workDigest(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	return hash[:], nil
}
func credentialDigest(secret []byte) []byte { hash := sha256.Sum256(secret); return hash[:] }
func authenticateWork(s workSessionRow, c WorkCredential) error {
	if c.SessionID == uuid.Nil || len(c.Secret) != 32 || s.view.Kind != "external" || s.view.ID != c.SessionID || subtle.ConstantTimeCompare(s.secretDigest, credentialDigest(c.Secret)) != 1 {
		return ErrWorkCredential
	}
	return nil
}

func readWorkSession(ctx context.Context, tx pgx.Tx, id uuid.UUID, lock bool, now time.Time) (workSessionRow, error) {
	var row workSessionRow
	v := &row.view
	var runStatus string
	var worker *string
	var expires, claimed *time.Time
	var waiting bool
	query := `SELECT s.id,s.project_id,s.task_id,s.kind,s.run_id,r.agent_id,s.client_name,s.client_version,
		s.claimed_at,s.started_at,s.last_contact_at,s.finished_at,s.released_at,s.heartbeat_sequence,s.outcome,
		s.release_reason,s.needs_user_resolution,s.secret_digest,s.admission_digest,
		COALESCE(r.status,''),r.worker_id,r.lease_expires_at,r.claimed_at,
		EXISTS(SELECT 1 FROM delegations d WHERE d.parent_run_id=r.id AND d.request_key IS NOT NULL AND d.status='waiting')
		FROM work_sessions s LEFT JOIN runs r ON r.id=s.run_id WHERE s.id=$1`
	if lock {
		query += ` FOR UPDATE OF s`
	}
	err := tx.QueryRow(ctx, query, id).Scan(&v.ID, &v.ProjectID, &v.TaskID, &v.Kind, &v.RunID, &v.AgentID, &v.ClientName, &v.ClientVersion,
		&v.ClaimedAt, &v.StartedAt, &v.LastContactAt, &v.FinishedAt, &v.ReleasedAt, &v.HeartbeatSequence, &v.Outcome,
		&v.ReleaseReason, &v.NeedsUserResolution, &row.secretDigest, &row.admissionDigest,
		&runStatus, &worker, &expires, &claimed, &waiting)
	if err != nil {
		return row, err
	}
	v.ContactState = "queued"
	expired := v.Kind == "native" && worker != nil && (expires != nil && !expires.After(now) || expires == nil && (claimed == nil || !claimed.Add(LeaseDuration).After(now)))
	switch {
	case v.ReleasedAt != nil:
		v.ContactState = "released"
	case v.NeedsUserResolution || expired:
		v.ContactState = "needs_attention"
	case v.Outcome != nil:
		v.ContactState = "cleanup_pending"
	case runStatus == "waiting_for_input" && worker == nil && waiting:
		v.ContactState = "waiting_for_child"
	case v.LastContactAt != nil:
		v.ContactState = "current"
		if v.Kind == "external" && !now.Before(v.LastContactAt.Add(120*time.Second)) {
			v.ContactState = "needs_attention"
		}
	}
	return row, nil
}

func InspectWorkSession(ctx context.Context, tx pgx.Tx, id uuid.UUID, now time.Time) (WorkSessionView, error) {
	row, err := readWorkSession(ctx, tx, id, false, now)
	return row.view, err
}
func CurrentTaskWork(ctx context.Context, tx pgx.Tx, taskID uuid.UUID, now time.Time) (*WorkSessionView, error) {
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM work_sessions WHERE task_id=$1 AND released_at IS NULL`, taskID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	view, err := InspectWorkSession(ctx, tx, id, now)
	return &view, err
}

func workTime(now time.Time, v WorkSessionView) time.Time {
	at := now.UTC()
	for _, stamp := range []*time.Time{&v.ClaimedAt, v.StartedAt, v.LastContactAt, v.FinishedAt, v.ReleasedAt} {
		if stamp != nil && stamp.After(at) {
			at = *stamp
		}
	}
	return at
}
