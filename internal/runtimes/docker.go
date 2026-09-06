package runtimes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	ErrStart        = errors.New("container start failed")
	ErrNameConflict = errors.New("Run container name is already occupied")
	ErrStop         = errors.New("container stop failed")
	ErrDiscard      = errors.New("container removal failed")
)

type operation struct {
	done chan struct{}
	err  error
}
type stopDecision struct {
	done    chan struct{}
	stopped bool
}

type execution struct {
	handle        Handle
	launch        launch
	process       *cliProcess
	output        *outputQueue
	mu            sync.Mutex
	control       sync.Mutex
	outputClaimed bool
	terminal      bool
	stopDecision  *stopDecision
	stop          *operation
	discard       *operation
	discardErr    error
	ready         chan struct{}
	readyOnce     sync.Once
	readyErr      error
	done          chan struct{}
	doneOnce      sync.Once
	result        Result
	resultErr     error
	monitorDone   chan struct{}
	cancelMonitor context.CancelFunc
}

func (e *execution) setReady(err error) { e.readyOnce.Do(func() { e.readyErr = err; close(e.ready) }) }
func (e *execution) complete(result Result, err error) {
	e.doneOnce.Do(func() { e.result, e.resultErr = result, err; close(e.done) })
}
func (e *execution) markTerminal() { e.mu.Lock(); e.terminal = true; e.mu.Unlock() }

func (d *Docker) execution(handle Handle) (*execution, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e := d.executions[handle.ID]
	if e == nil || e.handle != handle {
		return nil, ErrUnknownHandle
	}
	return e, nil
}

func (d *Docker) startGate(ctx context.Context, name string) (func(), error) {
	d.mu.Lock()
	gate := d.starts[name]
	if gate == nil {
		gate = make(chan struct{}, 1)
		gate <- struct{}{}
		d.starts[name] = gate
	}
	d.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-gate:
		return func() { gate <- struct{}{} }, nil
	}
}

// Start returns only after the container is running or has already exited. The
// same Run and launch specification share one handle; a changed launch conflicts.
// Failed or cancelled startup compensates owned allocations before returning.
func (d *Docker) Start(ctx context.Context, spec Spec) (Handle, error) {
	resolved, err := d.resolve(spec) // Snapshot caller-owned slices/maps before the first wait.
	if err != nil {
		return Handle{}, err
	}
	unlock, err := d.startGate(ctx, resolved.plan.ContainerName)
	if err != nil {
		return Handle{}, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return Handle{}, err
	}
	d.mu.Lock()
	existing := d.executions[resolved.plan.ContainerName]
	d.mu.Unlock()
	if existing != nil {
		existing.mu.Lock()
		discard, failure := existing.discard, existing.discardErr
		existing.mu.Unlock()
		if discard != nil {
			if err := finishOperation(ctx, discard); err != nil {
				return Handle{}, err
			}
		} else {
			if failure != nil {
				return Handle{}, failure
			}
			if !reflect.DeepEqual(existing.launch, resolved) {
				return Handle{}, ErrNameConflict
			}
			return existing.handle, nil
		}
	}
	if err := d.cleanupPending(context.WithoutCancel(ctx), resolved.plan.ContainerName); err != nil {
		return Handle{}, errors.Join(err, ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		return Handle{}, err
	}
	return d.startNew(ctx, resolved)
}

func (d *Docker) startNew(ctx context.Context, resolved launch) (handle Handle, failure error) {
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return Handle{}, fmt.Errorf("%w: allocation identity unavailable", ErrStart)
	}
	nonce := hex.EncodeToString(nonceBytes)
	name := resolved.plan.ContainerName
	d.remember(name, unresolvedCreate{nonce: nonce, ambiguous: true})
	id := ""
	var e *execution
	// Once a resource may exist, its compensation is independent of caller
	// cancellation, but each Docker operation retains a bounded deadline.
	owned := context.WithoutCancel(ctx)
	defer func() {
		if failure == nil {
			return
		}
		if e != nil {
			e.cancelMonitor()
			<-e.monitorDone
			d.mu.Lock()
			delete(d.executions, e.handle.ID)
			d.mu.Unlock()
		}
		if id == "" {
			pending, _ := d.pending(name)
			var err error
			id, err = d.reconcile(owned, name, pending)
			if err != nil {
				failure = errors.Join(failure, err)
				return
			}
		}
		if id != "" {
			if err := d.remove(owned, id); err != nil {
				failure = errors.Join(failure, err)
				return
			}
		}
		d.forget(name)
	}()
	code, output, err := d.runCLI(owned, createArguments(resolved.plan, nonce), resolved.environment, d.config.OperationTimeout)
	if ctx.Err() != nil {
		return Handle{}, errors.Join(ctx.Err(), err)
	}
	if err != nil {
		return Handle{}, err
	}
	if code != 0 {
		d.remember(name, unresolvedCreate{nonce: nonce})
		return Handle{}, fmt.Errorf("%w: Docker could not create the Run container", ErrStart)
	}
	id = strings.TrimSpace(output)
	if !containerID.MatchString(id) {
		id = ""
		return Handle{}, fmt.Errorf("%w: invalid container identity", ErrStart)
	}
	d.remember(name, unresolvedCreate{nonce: nonce})
	if err := d.verifyPolicy(owned, id, resolved.plan, nonce); err != nil {
		return Handle{}, err
	}
	if err := ctx.Err(); err != nil {
		return Handle{}, err
	}
	cmd, err := d.command([]string{"start", "--attach", "--interactive", id}, nil)
	if err != nil {
		return Handle{}, err
	}
	queue := newOutputQueue()
	cmd.Stdout, cmd.Stderr = outputWriter{queue, Stdout}, outputWriter{queue, Stderr}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Handle{}, fmt.Errorf("%w: Docker input unavailable", ErrStart)
	}
	p, err := startCLI(cmd)
	if err != nil {
		_ = stdin.Close()
		return Handle{}, err
	}
	monitorCtx, cancel := context.WithCancel(context.Background())
	e = &execution{
		handle: Handle{ID: resolved.plan.ContainerName, ResourceID: id}, launch: resolved, process: p,
		output: queue, ready: make(chan struct{}), done: make(chan struct{}),
		monitorDone: make(chan struct{}), cancelMonitor: cancel,
	}
	d.mu.Lock()
	d.executions[e.handle.ID] = e
	d.mu.Unlock()
	go d.monitor(monitorCtx, e, stdin)
	select {
	case <-ctx.Done():
		return Handle{}, ctx.Err()
	case <-e.ready:
		if e.readyErr != nil {
			return Handle{}, e.readyErr
		}
		d.forget(name)
		return e.handle, nil
	}
}

func createArguments(plan Plan, nonce string) []string {
	args := []string{"create", "--name", plan.ContainerName}
	for _, name := range []string{"io.circular.managed", "io.circular.run_id", "io.circular.policy_digest"} {
		args = append(args, "--label", name+"="+plan.Labels[name])
	}
	args = append(args, "--label", nonceLabel+"="+nonce, "--network", plan.NetworkMode,
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--cpus", strconv.FormatFloat(plan.CPULimit, 'g', 15, 64), "--memory", strconv.FormatInt(plan.MemoryLimitMB, 10)+"m",
		"--user", plan.ContainerUser, "--workdir", plan.WorkingDirectory, "--restart", "no",
		"--mount", "type=bind,src="+plan.WorktreeSource+",dst="+plan.WorktreeDestination, "--interactive")
	for _, name := range plan.EnvironmentNames {
		args = append(args, "--env", name)
	}
	args = append(args, plan.Image)
	return append(args, plan.Command...)
}

func (d *Docker) monitor(ctx context.Context, e *execution, stdin io.WriteCloser) {
	defer close(e.monitorDone)
	defer e.output.close()
	defer e.cancelMonitor()
	err := d.observe(ctx, e, stdin)
	if err != nil {
		e.process.terminate()
		e.setReady(err)
		e.complete(Result{}, err)
	}
}

func (d *Docker) observe(ctx context.Context, e *execution, stdin io.WriteCloser) error {
	inputDone := make(chan error, 1)
	go func() {
		_, err := stdin.Write(e.launch.stdin)
		closeErr := stdin.Close()
		if err == nil {
			err = closeErr
		}
		if errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) {
			err = nil
		}
		inputDone <- err
	}()
	inputCtx, cancel := context.WithTimeout(ctx, d.config.OperationTimeout)
	defer cancel()
	select {
	case err := <-inputDone:
		if err != nil {
			return fmt.Errorf("%w: Docker input delivery failed", ErrStart)
		}
	case <-inputCtx.Done():
		e.process.terminate()
		_ = stdin.Close()
		<-inputDone
		return fmt.Errorf("%w: Docker input delivery exceeded its deadline", ErrStart)
	}
	initial, err := d.awaitStarted(ctx, e)
	if err != nil {
		return err
	}
	if initial.terminal() {
		e.markTerminal()
	}
	e.setReady(nil)
	select {
	case <-ctx.Done():
		return fmt.Errorf("%w: Run observation cancelled", ErrStart)
	case <-e.process.done:
	}
	var exit *exec.ExitError
	if e.process.err != nil && !errors.As(e.process.err, &exit) {
		return fmt.Errorf("%w: Docker output could not be fully read", ErrStart)
	}
	final, err := d.state(ctx, e.handle.ResourceID)
	if err != nil {
		return err
	}
	if final.terminal() {
		e.markTerminal()
	} else if final.status == "running" {
		e.control.Lock()
		e.mu.Lock()
		terminal := e.terminal
		e.mu.Unlock()
		if !terminal {
			err = d.stopRunning(ctx, e.handle.ResourceID)
			if err == nil {
				e.markTerminal()
			}
		}
		e.control.Unlock()
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: Docker lost the Run container attachment", ErrStart)
	} else {
		return fmt.Errorf("%w: Docker could not start the Run container", ErrStart)
	}
	e.mu.Lock()
	decision := e.stopDecision
	e.mu.Unlock()
	result := Result{Reason: Exited, ExitCode: &final.exitCode}
	if decision != nil {
		<-decision.done
		if decision.stopped {
			result = Result{Reason: Stopped}
		}
	}
	e.complete(result, nil)
	return nil
}

func (d *Docker) awaitStarted(ctx context.Context, e *execution) (containerState, error) {
	ctx, cancel := context.WithTimeout(ctx, d.config.OperationTimeout)
	defer cancel()
	createdAfterExit := 0
	for {
		state, err := d.state(ctx, e.handle.ResourceID)
		if err != nil {
			return containerState{}, err
		}
		if state.status == "running" || state.terminal() {
			return state, nil
		}
		select {
		case <-e.process.done:
			createdAfterExit++
		default:
			createdAfterExit = 0
		}
		if createdAfterExit >= 3 {
			return containerState{}, fmt.Errorf("%w: container remained created", ErrStart)
		}
		if err := pause(ctx); err != nil {
			return containerState{}, fmt.Errorf("%w: container did not become ready", ErrStart)
		}
	}
}

func pause(ctx context.Context) error {
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func finishOperation(ctx context.Context, operation *operation) error {
	<-operation.done // The operation owns bounded, cancellation-independent cleanup.
	return errors.Join(operation.err, ctx.Err())
}

// Stop settles an owned container without removing its allocation. Concurrent
// calls share one operation, and a natural completion observed first is retained.
// Once requested, bounded termination finishes even if the caller is cancelled.
func (d *Docker) Stop(ctx context.Context, handle Handle) error {
	e, err := d.execution(handle)
	if err != nil {
		return err
	}
	e.mu.Lock()
	op := e.stop
	if op == nil {
		op = &operation{done: make(chan struct{})}
		e.stop = op
		go func() { op.err = d.stopExecution(e); close(op.done) }()
	}
	e.mu.Unlock()
	return finishOperation(ctx, op)
}

func (d *Docker) stopExecution(e *execution) error {
	err := func() error {
		e.control.Lock()
		defer e.control.Unlock()
		e.mu.Lock()
		if e.terminal {
			e.mu.Unlock()
			return nil
		}
		decision := &stopDecision{done: make(chan struct{})}
		e.stopDecision = decision
		e.mu.Unlock()
		defer close(decision.done)
		ctx := context.Background()
		state, err := d.state(ctx, e.handle.ResourceID)
		if err == nil && state.terminal() {
			e.markTerminal()
			return nil
		}
		if err == nil && state.status == "created" {
			e.process.terminate()
			err = d.remove(ctx, e.handle.ResourceID)
		} else {
			err = d.stopRunning(ctx, e.handle.ResourceID)
		}
		if err != nil {
			return fmt.Errorf("%w: could not terminate the Run container", ErrStop)
		}
		decision.stopped = true
		e.markTerminal()
		return nil
	}()
	if err != nil {
		return err
	}
	timer := time.NewTimer(d.config.StopTimeout + d.config.OperationTimeout)
	defer timer.Stop()
	select {
	case <-e.monitorDone:
	case <-timer.C:
		e.cancelMonitor()
		e.process.terminate()
		<-e.monitorDone
	}
	return nil
}

func (d *Docker) stopRunning(ctx context.Context, id string) error {
	seconds := strconv.FormatFloat(math.Max(1, math.Ceil(d.config.StopTimeout.Seconds())), 'f', 0, 64)
	code, _, err := d.runCLI(ctx, []string{"stop", "--time", seconds, id}, nil, d.config.StopTimeout+d.config.OperationTimeout)
	if err == nil && code == 0 {
		return nil
	}
	code, _, err = d.cli(ctx, "kill", id)
	if err == nil && code == 0 {
		return nil
	}
	return fmt.Errorf("%w: Docker could not stop or kill the container", ErrStop)
}

// Discard permanently removes an owned allocation by immutable ID, even when
// Stop fails. It invalidates the live handle but remains idempotent itself; a
// failed removal retains ownership and can be retried with the original handle.
func (d *Docker) Discard(ctx context.Context, handle Handle) error {
	d.mu.Lock()
	discarded := d.discarded[handle]
	e := d.executions[handle.ID]
	d.mu.Unlock()
	if discarded {
		return ctx.Err()
	}
	if e == nil || e.handle != handle {
		return ErrUnknownHandle
	}
	e.mu.Lock()
	op := e.discard
	if op == nil {
		op = &operation{done: make(chan struct{})}
		e.discard = op
		go func() {
			_ = d.Stop(context.Background(), handle)
			err := d.remove(context.Background(), handle.ResourceID)
			if err != nil {
				err = errors.Join(ErrDiscard, err)
				e.mu.Lock()
				e.discardErr = err
				e.discard = nil
				e.mu.Unlock()
			} else {
				// A successful forced removal can follow a failed Stop. Do not
				// leave the old attachment or its output observers behind after
				// relinquishing the live handle.
				e.cancelMonitor()
				e.process.terminate()
				<-e.monitorDone
				d.mu.Lock()
				delete(d.executions, handle.ID)
				d.discarded[handle] = true
				d.mu.Unlock()
			}
			op.err = err
			close(op.done)
		}()
	}
	e.mu.Unlock()
	return finishOperation(ctx, op)
}

func (d *Docker) remove(ctx context.Context, id string) error {
	code, _, err := d.cli(ctx, "rm", "--force", "--volumes", id)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%w: Docker could not remove the new Run container", ErrStart)
	}
	return nil
}
