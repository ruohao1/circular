package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/runstate"
	"github.com/ruohao1/circular/internal/worker"
)

var (
	ErrResourceState     = errors.New("Run resources are not in the required state")
	ErrResourceConflict  = errors.New("Run resource identity conflicts with durable state")
	ErrRunUnavailable    = errors.New("Run is unavailable")
	ErrTransactionClosed = errors.New("Run resource transaction is closed")
)

type Workspace struct {
	ID, RunID    uuid.UUID
	WorktreePath string
	ContainerID  *string
	Status       string
}

type ResourceState struct {
	RunID        uuid.UUID
	Status       runstate.Status
	Backend      string
	RepositoryID *uuid.UUID
	Workspace    *Workspace
	Artifacts    []artifacts.Record
}

// Resources requires an explicit execution owner; it cannot bypass lease checks
// as an administrative/API session. Each mutation locks the Run before its data.
type Resources struct {
	pool  *pgxpool.Pool
	owner string
}

func NewResources(pool *pgxpool.Pool, owner string) (*Resources, error) {
	if pool == nil {
		return nil, ErrResourceState
	}
	if err := worker.ValidateID(owner); err != nil {
		return nil, err
	}
	return &Resources{pool: pool, owner: owner}, nil
}

// WithRun fences resource operations against recovery and commits state plus
// Events together. Invoke methods serially and return any error. The callback
// may perform a bounded external cleanup while the Run lock prevents takeover;
// do not retain the transaction or use it after the callback returns.
func (s *Resources) WithRun(ctx context.Context, id uuid.UUID, action func(*RunResources) error) error {
	if action == nil {
		return ErrResourceState
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	run := &RunResources{ctx: ctx, tx: tx, id: id, open: true}
	defer func() { run.open = false }()
	var owner *string
	var expires *time.Time
	err = tx.QueryRow(ctx, `SELECT status, backend, worker_id, lease_expires_at FROM runs WHERE id=$1 FOR UPDATE`, id).Scan(&run.status, &run.backend, &owner, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRunUnavailable
	}
	if err != nil {
		return err
	}
	if owner == nil || *owner != s.owner || expires == nil || !expires.After(time.Now().UTC()) {
		return ErrLeaseLost
	}
	if !run.status.Valid() {
		return ErrResourceState
	}
	if err := action(run); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Resources) Read(ctx context.Context, id uuid.UUID) (state ResourceState, err error) {
	err = s.WithRun(ctx, id, func(run *RunResources) error { var err error; state, err = run.State(); return err })
	return state, err
}

type RunResources struct {
	ctx     context.Context
	tx      pgx.Tx
	id      uuid.UUID
	status  runstate.Status
	backend string
	open    bool
}

func (r *RunResources) guard() error {
	if !r.open {
		return ErrTransactionClosed
	}
	return r.ctx.Err()
}

func (r *RunResources) State() (ResourceState, error) {
	if err := r.guard(); err != nil {
		return ResourceState{}, err
	}
	state := ResourceState{RunID: r.id, Status: r.status, Backend: r.backend}
	err := r.tx.QueryRow(r.ctx, `SELECT tasks.repository_id FROM tasks JOIN runs ON runs.task_id=tasks.id WHERE runs.id=$1`, r.id).Scan(&state.RepositoryID)
	if err != nil {
		return ResourceState{}, err
	}
	state.Workspace, err = r.workspace()
	if err != nil {
		return ResourceState{}, err
	}
	state.Artifacts, err = r.artifacts()
	return state, err
}

func (r *RunResources) workspace() (*Workspace, error) {
	if err := r.guard(); err != nil {
		return nil, err
	}
	w := &Workspace{RunID: r.id}
	err := r.tx.QueryRow(r.ctx, `SELECT id, worktree_path, container_id, status FROM workspaces WHERE run_id=$1`, r.id).Scan(&w.ID, &w.WorktreePath, &w.ContainerID, &w.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	switch w.Status {
	case "pending", "ready", "failed", "released":
	default:
		return nil, ErrResourceState
	}
	return w, nil
}

func (r *RunResources) event(kind, source string, data map[string]any) error {
	return r.eventWithRaw(kind, source, data, nil)
}

func (r *RunResources) eventWithRaw(kind, source string, data, raw map[string]any) error {
	if err := r.guard(); err != nil {
		return err
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	var original []byte
	if raw != nil {
		original, err = json.Marshal(raw)
		if err != nil {
			return err
		}
	}
	_, err = r.tx.Exec(r.ctx, `INSERT INTO events (id,run_id,sequence,type,source,data,raw,occurred_at)
		SELECT $1,$2,COALESCE(MAX(sequence),0)+1,$3,$4,$5::json,$6::json,$7 FROM events WHERE run_id=$2`, uuid.New(), r.id, kind, source, payload, original, time.Now().UTC())
	return err
}
