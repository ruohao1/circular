package execution

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/backends"
	git "github.com/ruohao1/circular/internal/git"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/runstate"
	"github.com/ruohao1/circular/internal/runtimes"
	"github.com/ruohao1/circular/internal/worker"
)

var ErrConfiguration = errors.New("invalid Run execution configuration")

// Config contains trusted worker settings, never Task-selected commands or roots.
// Docker.WorktreeRoot is the daemon-visible equivalent of Git.WorktreeRoot.
type Config struct {
	Git               git.Config
	Docker            runtimes.DockerConfig
	ArtifactRoot      string
	ReviewContextRoot string
	Image             string
	CPULimit          float64
	MemoryLimitMB     int64
	FakeDelayMS       int
	PollInterval      time.Duration
	CodexEnabled      bool
	CodexImage        string
	CodexAuthMode     string
	CodexAuthRoot     string
	CodexAPIKey       string              `json:"-"`
	Integrations      integrations.Config `json:"-"`
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
	connections, err := integrations.New(pool, config.Integrations)
	if err != nil {
		return nil, err
	}
	config.Git.Credential = connections.GitCredential
	retention, err := NewRetention(store, config.Git, config.ArtifactRoot)
	if err != nil {
		return nil, err
	}
	if config.Docker.WorktreeRoot == "" {
		config.Docker.WorktreeRoot = retention.worktreeRoot
	}
	if err := configureReviewRoots(&config, retention); err != nil {
		return nil, err
	}
	if config.CodexAuthRoot != "" {
		if err := validateCodexAuthRoot(config); err != nil {
			return nil, err
		}
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
	if config.CodexEnabled {
		invocation, err := (backends.Codex{AuthMode: config.CodexAuthMode, APIKey: config.CodexAPIKey}).Prepare(backends.Input{Config: []byte("{}")})
		if err != nil {
			return nil, err
		}
		if invocation.UseCredentials && config.CodexAuthRoot == "" {
			return nil, errors.New("ChatGPT subscription mode requires a dedicated Codex auth root")
		}
		if _, err := docker.Resolve(runtimes.Spec{RunID: uuid.Nil, Image: config.CodexImage, Worktree: filepath.Join(root, uuid.Nil.String()), CPULimit: config.CPULimit, MemoryLimitMB: config.MemoryLimitMB, NetworkEnabled: true, UseCredentials: invocation.UseCredentials, TemporaryStorageMB: 128}); err != nil {
			return nil, err
		}
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
	var backend preparedBackend
	handle, err := s.provision(ctx, id, &backend)
	if err != nil {
		return err
	}
	state, err := s.store.Read(ctx, id)
	if err != nil {
		return err
	}
	execution, stop, err := reviewExecutionContext(ctx, state)
	if err != nil {
		return err
	}
	defer stop()
	if err := s.ingest(execution, id, handle, backend); err != nil {
		if errors.Is(execution.Err(), context.DeadlineExceeded) {
			// This is the review's execution limit, not worker shutdown. Keep
			// its public reason instead of projecting generic cancellation.
			return executionFailure("PR review exceeded its 15-minute execution limit", nil)
		}
		return err
	}
	if err := s.store.WithRun(ctx, id, func(r *postgres.RunResources) error { return r.BeginFinalizing() }); err != nil {
		return executionFailure("could not begin Run finalization", err)
	}
	if state.Kind == runstate.PRReview {
		if err := s.retention.FinalizePRReview(ctx, id); err != nil {
			return executionFailure("could not retain PR review output", err)
		}
		if err := s.store.WithRun(ctx, id, func(r *postgres.RunResources) error { return r.CompletePRReview() }); err != nil {
			return executionFailure("could not persist PR review completion", err)
		}
	} else {
		if _, err := s.retention.Finalize(ctx, id); err != nil {
			return executionFailure("could not finalize Run output", err)
		}
		if err := s.store.WithRun(ctx, id, func(r *postgres.RunResources) error { return r.Complete() }); err != nil {
			return executionFailure("could not persist Run completion", err)
		}
	}

	return nil
}

func (s *Supervisor) provision(ctx context.Context, id uuid.UUID, backend *preparedBackend) (handle runtimes.Handle, result error) {
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
		if inputs.Kind != runstate.PRReview {
			*backend, err = s.prepareBackend(inputs)
			if err != nil {
				return err
			}
		} else if inputs.Backend != "codex" || !s.config.CodexEnabled {
			return &backends.Failure{Message: "PR reviews require an enabled Codex worker"}
		}
		_, err = r.CreatePending(path)
		return err
	})
	if err != nil {
		return runtimes.Handle{}, executionFailure("could not prepare Run Workspace", err)
	}
	var repository string
	if inputs.Kind == runstate.PRReview {
		captured, value, err := s.preparePRReviewContext(ctx, inputs)
		if err != nil {
			return handle, executionFailure("could not verify captured PR review source", err)
		}
		repository = captured.RepositoryPath
		inputs.ReviewContextSHA256, err = prreviews.Fingerprint(value)
		if err != nil {
			return handle, err
		}
		*backend, err = s.prepareBackend(inputs)
		if err != nil {
			return handle, err
		}
	} else {
		repository, err = s.retention.git.Checkout(ctx, inputs.RepositoryID, inputs.CloneURL)
		if err != nil {
			return handle, executionFailure("could not prepare Repository checkout", err)
		}
	}

	if err := s.withAllocation(ctx, inputs, func(operation context.Context, _ *postgres.RunResources) error {
		_, err := s.retention.git.Provision(operation, id, repository, inputs.BaseRef)
		return err
	}); err != nil {
		return runtimes.Handle{}, executionFailure("could not provision Run worktree", err)
	}
	err = s.withAllocation(ctx, inputs, func(operation context.Context, r *postgres.RunResources) error {
		var err error
		var reviewMount *runtimes.ReviewMount
		if inputs.Kind == runstate.PRReview {
			reviewMount = &runtimes.ReviewMount{ContextSHA256: inputs.ReviewContextSHA256}
		}
		handle, err = s.docker.Start(operation, runtimes.Spec{Kind: inputs.Kind, Review: reviewMount, RunID: id, Image: backend.image, Worktree: filepath.Join(s.config.Docker.WorktreeRoot, id.String()), Command: backend.invocation.Command, Stdin: backend.invocation.Stdin, CPULimit: s.config.CPULimit, MemoryLimitMB: s.config.MemoryLimitMB, NetworkEnabled: backend.invocation.NetworkEnabled, UseCredentials: backend.invocation.UseCredentials, TemporaryStorageMB: backend.invocation.TemporaryStorageMB})
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

type preparedBackend struct {
	name, image string
	invocation  backends.Invocation
}

func (s *Supervisor) prepareBackend(inputs postgres.ProvisioningContext) (preparedBackend, error) {
	var adapter backends.Backend
	prepared := preparedBackend{name: inputs.Backend, image: s.config.Image}
	switch inputs.Backend {
	case "fake":
		adapter = backends.Fake{DelayMS: s.config.FakeDelayMS}
	case "codex":
		if !s.config.CodexEnabled {
			return prepared, &backends.Failure{Message: "Codex backend is disabled on this worker"}
		}
		prepared.name, prepared.image = "Codex", s.config.CodexImage
		adapter = backends.Codex{AuthMode: s.config.CodexAuthMode, APIKey: s.config.CodexAPIKey}
	default:
		return prepared, &backends.Failure{Message: "unsupported Run backend"}
	}
	var err error
	prepared.invocation, err = adapter.Prepare(backends.Input{Kind: inputs.Kind, ReviewContextSHA256: inputs.ReviewContextSHA256, RunID: inputs.RunID, TaskTitle: inputs.TaskTitle, TaskDescription: inputs.TaskDescription, Instructions: inputs.Instructions, Config: inputs.BackendConfig})
	return prepared, err
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
	var backendFailure *backends.Failure
	if errors.As(err, &backendFailure) {
		return backendFailure.Message, backendFailure.Raw
	}
	var failure *runFailure
	if errors.As(err, &failure) {
		return failure.message, failure.raw
	}
	return "Run execution failed", nil
}
