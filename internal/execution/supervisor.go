package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	git "github.com/ruohao1/circular/internal/git"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/runstate"
	"github.com/ruohao1/circular/internal/runtimes"
	"github.com/ruohao1/circular/internal/worker"
)

var ErrConfiguration = errors.New("invalid Run execution configuration")

// Config contains trusted worker settings, never Task-selected commands or roots.
// Docker.WorktreeRoot is the daemon-visible equivalent of Git.WorktreeRoot.
type Config struct {
	Git           git.Config
	Docker        runtimes.DockerConfig
	ArtifactRoot  string
	Image         string
	CPULimit      float64
	MemoryLimitMB int64
	FakeDelayMS   int
	PollInterval  time.Duration
}

// Supervisor implements the worker's one-claim execution seam. Call Execute
// serially for this worker identity; recovered attempts are cleanup-only.
type Supervisor struct {
	store     *postgres.Resources
	retention *Retention
	docker    *runtimes.Docker
	owner     string
	config    Config
}

// NewSupervisor validates configuration without connecting to PostgreSQL,
// contacting Docker, or allocating resources. All execution is native Go.
func NewSupervisor(pool *pgxpool.Pool, owner string, config Config) (*Supervisor, error) {
	store, err := postgres.NewResources(pool, owner)
	if err != nil {
		return nil, err
	}
	retention, err := NewRetention(store, config.Git, config.ArtifactRoot)
	if err != nil {
		return nil, err
	}
	if config.Docker.WorktreeRoot == "" {
		config.Docker.WorktreeRoot = retention.worktreeRoot
	}
	docker, err := runtimes.NewDocker(config.Docker)
	if err != nil {
		return nil, err
	}
	root, err := resolve(filepath.Clean(config.Docker.WorktreeRoot))
	if err != nil {
		return nil, ErrConfiguration
	}
	config.Docker.WorktreeRoot = root
	if config.PollInterval == 0 {
		config.PollInterval = 250 * time.Millisecond
	}
	if config.PollInterval < 0 || config.PollInterval >= postgres.LeaseDuration/2 || config.FakeDelayMS < 0 || config.FakeDelayMS > 10000 {
		return nil, ErrConfiguration
	}
	if _, err := docker.Resolve(runtimes.Spec{RunID: uuid.Nil, Image: config.Image, Worktree: filepath.Join(root, uuid.Nil.String()), Command: []string{"--write-output"}, CPULimit: config.CPULimit, MemoryLimitMB: config.MemoryLimitMB}); err != nil {
		return nil, err
	}
	return &Supervisor{store: store, retention: retention, docker: docker, owner: owner, config: config}, nil
}

func (s *Supervisor) Execute(ctx context.Context, claim worker.Claim, owner string) error {
	if owner != s.owner || claim.RunID == uuid.Nil {
		return postgres.ErrLeaseLost
	}
	preflight, preflightCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	status, err := s.store.Heartbeat(preflight, claim.RunID)
	preflightCancel()
	if err != nil {
		return err
	}
	if status != runstate.Cancelled && (claim.Recovery && !status.Terminal() || !claim.Recovery && status != runstate.Provisioning) {
		return postgres.ErrResourceState
	}
	executing, cancelExecution := context.WithCancel(ctx)
	defer cancelExecution()
	monitor, cancelMonitor := context.WithCancel(context.WithoutCancel(ctx))
	monitorDone := make(chan struct{})
	go func() { defer close(monitorDone); s.watch(monitor, claim.RunID, cancelExecution) }()
	defer func() { cancelMonitor(); <-monitorDone }()
	var executionErr error
	if ctx.Err() != nil {
		executionErr = ctx.Err()
	} else if !claim.Recovery && !status.Terminal() {
		executionErr = s.execute(executing, claim.RunID)
	}
	message := "execution ended without a terminal outcome"
	var raw map[string]any
	if executionErr != nil {
		message, raw = failureProjection(executionErr)
	}
	for range 2 {
		settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err = s.store.WithRun(settle, claim.RunID, func(r *postgres.RunResources) error { return r.RecordFailure(message, raw) })
		cancel()
		if err == nil || errors.Is(err, postgres.ErrLeaseLost) {
			break
		}
	}
	if err != nil {
		return errors.Join(executionErr, err)
	}
	if err := s.retention.Cleanup(ctx, claim.RunID, s.docker); err != nil {
		return errors.Join(executionErr, err)
	}
	release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err = s.store.WithRun(release, claim.RunID, func(r *postgres.RunResources) error {
		state, err := r.State()
		if err != nil {
			return err
		}
		status = state.Status
		return r.ReleaseClaim()
	})
	if status == runstate.Cancelled {
		return err
	}
	return errors.Join(executionErr, err)
}

// The monitor has a separate lifetime from execution so cancellation/shutdown
// cannot disable heartbeats during retained-output publication and cleanup.
func (s *Supervisor) watch(ctx context.Context, id uuid.UUID, stop context.CancelFunc) {
	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		// Resource cleanup may hold the Run row lock through bounded Docker/Git
		// operations. Let the heartbeat wait for that fence, not time out after
		// a short poll and mistake our own cleanup for lease loss.
		beat, cancel := context.WithTimeout(ctx, postgres.LeaseDuration)
		status, err := s.store.Heartbeat(beat, id)
		cancel()
		if err != nil {
			stop()
			return
		}
		if status == runstate.Cancelled {
			stop()
		}
	}
}

func (s *Supervisor) execute(ctx context.Context, id uuid.UUID) error {
	handle, err := s.provision(ctx, id)
	if err != nil {
		return err
	}
	if err := s.ingest(ctx, id, handle); err != nil {
		return err
	}
	if err := s.store.WithRun(ctx, id, func(r *postgres.RunResources) error { return r.BeginFinalizing() }); err != nil {
		return executionFailure("could not begin Run finalization", err)
	}
	if _, err := s.retention.Finalize(ctx, id); err != nil {
		return executionFailure("could not finalize Run output", err)
	}
	if err := s.store.WithRun(ctx, id, func(r *postgres.RunResources) error { return r.Complete() }); err != nil {
		return executionFailure("could not persist Run completion", err)
	}
	return nil
}

func (s *Supervisor) provision(ctx context.Context, id uuid.UUID) (handle runtimes.Handle, result error) {
	identityRecorded := false
	defer func() {
		if result == nil {
			return
		}
		message, _ := failureProjection(result)
		record, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err := s.store.WithRun(record, id, func(r *postgres.RunResources) error { return r.FailProvisioning(message, handle.ResourceID) })
		cancel()
		if err == nil {
			return
		}
		result = errors.Join(result, executionFailure("could not persist provisioning failure", err))
		// A live handle proves this exact uncommitted allocation. Never stop a
		// replacement owner's durable allocation after a known lease takeover.
		if handle.ResourceID != "" && !identityRecorded && !errors.Is(err, postgres.ErrLeaseLost) {
			if discard := s.docker.Discard(context.WithoutCancel(ctx), handle); discard != nil {
				result = errors.Join(result, executionFailure("uncommitted runtime allocation could not be discarded", discard))
			}
		}
	}()
	var inputs postgres.ProvisioningContext
	path := filepath.Join(s.retention.worktreeRoot, id.String())
	err := s.store.WithRun(ctx, id, func(r *postgres.RunResources) error {
		var err error
		inputs, err = r.ProvisioningContext()
		if err != nil {
			return err
		}
		if inputs.Backend != "fake" {
			return errors.New("unsupported Run backend")
		}
		_, err = r.CreatePending(path)
		return err
	})
	if err != nil {
		return runtimes.Handle{}, executionFailure("could not prepare Run Workspace", err)
	}
	repository, err := s.retention.git.Checkout(ctx, inputs.RepositoryID, inputs.CloneURL)
	if err != nil {
		return runtimes.Handle{}, executionFailure("could not prepare Repository checkout", err)
	}
	if err := s.withAllocation(ctx, inputs, func(operation context.Context, _ *postgres.RunResources) error {
		_, err := s.retention.git.Provision(operation, id, repository, inputs.BaseRef)
		return err
	}); err != nil {
		return runtimes.Handle{}, executionFailure("could not provision Run worktree", err)
	}
	behavior, err := s.fakeBehavior(inputs.BackendConfig)
	if err != nil {
		return runtimes.Handle{}, err
	}
	request := map[string]any{"protocol_version": 1, "run": map[string]any{"id": id.String(), "task_title": inputs.TaskTitle, "task_description": inputs.TaskDescription, "instructions": inputs.Instructions}, "behavior": behavior}
	stdin, err := json.Marshal(request)
	if err != nil {
		return runtimes.Handle{}, executionFailure("could not encode fake workload input", err)
	}
	err = s.withAllocation(ctx, inputs, func(operation context.Context, r *postgres.RunResources) error {
		var err error
		handle, err = s.docker.Start(operation, runtimes.Spec{RunID: id, Image: s.config.Image, Worktree: filepath.Join(s.config.Docker.WorktreeRoot, id.String()), Command: []string{"--write-output"}, Stdin: append(stdin, '\n'), CPULimit: s.config.CPULimit, MemoryLimitMB: s.config.MemoryLimitMB})
		if err != nil {
			return executionFailure("could not start Run container", err)
		}
		_, err = r.RecordContainer(handle.ResourceID)
		return err
	})
	if err != nil {
		return handle, executionFailure("could not persist Run container identity", err)
	}
	identityRecorded = true
	if err := s.store.WithRun(ctx, id, func(r *postgres.RunResources) error { _, err := r.MarkRunning(); return err }); err != nil {
		return handle, executionFailure("could not mark Run Workspace ready", err)
	}
	return handle, nil
}

// Fence Run-owned allocation and immutable identity handoff against recovery.
// Repository cache refresh is shared, but worktree/container allocation must not
// outlive the Run lock and appear after a replacement worker finished cleanup.
func (s *Supervisor) withAllocation(ctx context.Context, inputs postgres.ProvisioningContext, action func(context.Context, *postgres.RunResources) error) error {
	operation, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	// Caller cancellation stops allocation, not the owned identity write or
	// runtime compensation. Their transaction has its own bounded lifetime.
	locked, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*postgres.LeaseDuration)
	defer cancel()
	return s.store.WithRun(locked, inputs.RunID, func(r *postgres.RunResources) error {
		if err := operation.Err(); err != nil {
			return err
		}
		state, err := r.State()
		if err != nil {
			return err
		}
		if state.Status != runstate.Provisioning || state.RepositoryID == nil || *state.RepositoryID != inputs.RepositoryID || state.Workspace == nil || state.Workspace.Status != "pending" || state.Workspace.ContainerID != nil || state.Workspace.WorktreePath != filepath.Join(s.retention.worktreeRoot, inputs.RunID.String()) {
			return postgres.ErrResourceState
		}
		if err := action(operation, r); err != nil {
			return err
		}
		return r.RenewLease()
	})
}

func (s *Supervisor) fakeBehavior(raw json.RawMessage) (map[string]any, error) {
	var config map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&config); err != nil {
		return nil, executionFailure("invalid fake backend configuration", err)
	}
	delay := s.config.FakeDelayMS
	if value, ok := config["delay_ms"]; ok {
		if !nonnegativeInteger(value) {
			return nil, executionFailure("fake delay_ms must be an integer from 0 through 10000", nil)
		}
		var err error
		delay, err = strconv.Atoi(string(value.(json.Number)))
		if err != nil || delay < 0 || delay > 10000 {
			return nil, executionFailure("fake delay_ms must be an integer from 0 through 10000", nil)
		}
	}
	failure := "none"
	if value, ok := config["failure"]; ok {
		failure, _ = value.(string)
		if failure != "none" && failure != "before_events" && failure != "after_first_event" {
			return nil, executionFailure("unsupported fake failure mode", nil)
		}
	}
	return map[string]any{"delay_ms": delay, "failure": failure}, nil
}

type runFailure struct {
	message string
	raw     map[string]any
	cause   error
}

func (e *runFailure) Error() string { return e.message }
func (e *runFailure) Unwrap() error { return e.cause }
func executionFailure(message string, cause error) error {
	return &runFailure{message: message, cause: cause}
}
func failureProjection(err error) (string, map[string]any) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "worker execution stopped", nil
	}
	var failure *runFailure
	if errors.As(err, &failure) {
		return failure.message, failure.raw
	}
	return "Run execution failed", nil
}
