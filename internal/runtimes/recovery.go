package runtimes

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Release reconciles a persisted allocation, including a crash before its ID was
// stored (resourceID == ""). Callers must hold the Run's durable lease/row lock
// while mutating resources; this module does not grant or transfer Run ownership.
func (d *Docker) Release(ctx context.Context, runID uuid.UUID, resourceID string) error {
	if resourceID != "" && !containerID.MatchString(resourceID) {
		return fmt.Errorf("%w: persisted identity is invalid", ErrDiscard)
	}
	name := "circular-run-" + strings.ReplaceAll(runID.String(), "-", "")
	unlock, err := d.startGate(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	e := d.executions[name]
	d.mu.Unlock()
	if e != nil {
		if resourceID != "" && resourceID != e.handle.ResourceID {
			return fmt.Errorf("%w: persisted identity does not match", ErrDiscard)
		}
		return d.Discard(ctx, e.handle)
	}
	reference := resourceID
	if reference == "" {
		reference = name
	}
	owned := context.WithoutCancel(ctx)
	code, output, err := d.cli(owned, "container", "inspect", reference)
	if err != nil {
		return errors.Join(ErrDiscard, err, ctx.Err())
	}
	if code != 0 {
		absent, err := d.nameAbsent(owned, name)
		if err != nil {
			return errors.Join(ErrDiscard, err, ctx.Err())
		}
		if absent {
			return ctx.Err()
		}
		return fmt.Errorf("%w: could not confirm that the Run container is absent", ErrDiscard)
	}
	container, err := decodeInspection(output)
	if err != nil {
		return errors.Join(ErrDiscard, err, ctx.Err())
	}
	id, validID := container["Id"].(string)
	labels := object(object(container["Config"])["Labels"])
	mounts, validMounts := container["Mounts"].([]any)
	if !validMounts || len(mounts) != 1 {
		return fmt.Errorf("%w: container ownership could not be verified", ErrDiscard)
	}
	mount := object(mounts[0])
	if !validID || !containerID.MatchString(id) || (resourceID != "" && resourceID != id) ||
		container["Name"] != "/"+name || labels["io.circular.managed"] != "true" || labels["io.circular.run_id"] != runID.String() ||
		mount["Type"] != "bind" || mount["Source"] != filepath.Join(d.config.WorktreeRoot, runID.String()) || mount["Destination"] != "/workspace" {
		return fmt.Errorf("%w: container ownership could not be verified", ErrDiscard)
	}
	if err := d.remove(owned, id); err != nil {
		return errors.Join(ErrDiscard, err, ctx.Err())
	}
	return ctx.Err()
}

type unresolvedCreate struct {
	nonce     string
	ambiguous bool
}

func (d *Docker) pending(name string) (unresolvedCreate, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	create, ok := d.unresolved[name]
	return create, ok
}
func (d *Docker) remember(name string, create unresolvedCreate) {
	d.mu.Lock()
	d.unresolved[name] = create
	d.mu.Unlock()
}
func (d *Docker) forget(name string) { d.mu.Lock(); delete(d.unresolved, name); d.mu.Unlock() }

func (d *Docker) cleanupPending(ctx context.Context, name string) error {
	create, ok := d.pending(name)
	if !ok {
		return nil
	}
	id, err := d.reconcile(ctx, name, create)
	if err != nil {
		return err
	}
	if id != "" {
		if err := d.remove(ctx, id); err != nil {
			return err
		}
	}
	d.forget(name)
	return nil
}

// A successful client response is not the only way a container can be created.
// Timeouts/cancellation leave a nonce until Docker proves its allocation removed
// or absent. No new create for that Run is allowed while reconciliation fails.
func (d *Docker) reconcile(ctx context.Context, name string, create unresolvedCreate) (string, error) {
	if !create.ambiguous {
		code, output, err := d.cli(ctx, "container", "inspect", name)
		if err != nil {
			return "", err
		}
		if code == 0 {
			container, err := decodeInspection(output)
			if err != nil {
				return "", err
			}
			return reconciledIdentity(container, name, create.nonce, true)
		}
		id, err := d.findNonce(ctx, name, create.nonce)
		if err != nil || id != "" {
			return id, err
		}
		absent, err := d.nameAbsent(ctx, name)
		if err != nil {
			return "", err
		}
		if absent {
			return "", nil
		}
		return "", fmt.Errorf("%w: could not reconcile new container", ErrOperation)
	}
	settle := min(max(d.config.OperationTimeout, 250*time.Millisecond), time.Second)
	deadline := time.Now().Add(settle)
	for {
		// Look for our nonce first: the deterministic name may now refer to an
		// unrelated replacement while our original immutable ID still exists.
		id, err := d.findNonce(ctx, name, create.nonce)
		if err != nil || id != "" {
			return id, err
		}
		code, output, err := d.cli(ctx, "container", "inspect", name)
		if err != nil {
			return "", err
		}
		if code == 0 {
			container, err := decodeInspection(output)
			if err != nil {
				return "", err
			}
			return reconciledIdentity(container, name, create.nonce, true)
		}
		if !time.Now().Before(deadline) {
			absent, err := d.nameAbsent(ctx, name)
			if err != nil {
				return "", err
			}
			if absent {
				return "", nil
			}
			return "", fmt.Errorf("%w: could not settle ambiguous container creation", ErrOperation)
		}
		if err := pause(ctx); err != nil {
			return "", err
		}
	}
}

func reconciledIdentity(container map[string]any, name, nonce string, conflict bool) (string, error) {
	id, ok := container["Id"].(string)
	if !ok || !containerID.MatchString(id) {
		return "", fmt.Errorf("%w: invalid reconciliation identity", ErrOperation)
	}
	config := object(container["Config"])
	if config == nil {
		return "", fmt.Errorf("%w: invalid container inspection", ErrOperation)
	}
	labels := object(config["Labels"])
	if labels[nonceLabel] != nonce {
		if conflict {
			return "", ErrNameConflict
		}
		return "", fmt.Errorf("%w: create nonce could not be verified", ErrOperation)
	}
	if container["Name"] != "/"+name {
		return "", fmt.Errorf("%w: container name could not be verified", ErrOperation)
	}
	return id, nil
}

func listedIDs(output string) ([]string, error) {
	if output == "" {
		return nil, nil
	}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if !containerID.MatchString(line) {
			return nil, fmt.Errorf("%w: invalid container listing", ErrOperation)
		}
		lines[i] = line
	}
	return lines, nil
}

func (d *Docker) findNonce(ctx context.Context, name, nonce string) (string, error) {
	code, output, err := d.cli(ctx, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label="+nonceLabel+"="+nonce)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("%w: could not reconcile new container", ErrOperation)
	}
	ids, err := listedIDs(output)
	if err != nil {
		return "", err
	}
	if len(ids) > 1 {
		return "", fmt.Errorf("%w: multiple allocations share a creation nonce", ErrOperation)
	}
	if len(ids) == 0 {
		return "", nil
	}
	container, err := d.inspect(ctx, ids[0])
	if err != nil {
		return "", err
	}
	id, err := reconciledIdentity(container, name, nonce, false)
	if err == nil && id != ids[0] {
		return "", fmt.Errorf("%w: reconciliation changed resource identity", ErrOperation)
	}
	return id, err
}

func (d *Docker) nameAbsent(ctx context.Context, name string) (bool, error) {
	code, output, err := d.cli(ctx, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "name=^/"+name+"$")
	if err != nil {
		return false, err
	}
	if code != 0 {
		return false, fmt.Errorf("%w: could not confirm container absence", ErrOperation)
	}
	ids, err := listedIDs(output)
	return len(ids) == 0, err
}
