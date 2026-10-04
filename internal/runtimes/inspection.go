package runtimes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

const nonceLabel = "io.circular.create_nonce"

var containerID = regexp.MustCompile(`^[0-9a-f]{64}$`)

func decodeInspection(output string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.UseNumber()
	var response []map[string]any
	if err := decoder.Decode(&response); err != nil || len(response) != 1 || response[0] == nil {
		return nil, fmt.Errorf("%w: invalid container inspection", ErrOperation)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("%w: invalid container inspection", ErrOperation)
	}
	return response[0], nil
}

func (d *Docker) inspect(ctx context.Context, reference string) (map[string]any, error) {
	code, output, err := d.cli(ctx, "container", "inspect", reference)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("%w: could not inspect container", ErrOperation)
	}
	return decodeInspection(output)
}

func object(value any) map[string]any { result, _ := value.(map[string]any); return result }
func integer(value any) (int64, bool) {
	if number, ok := value.(json.Number); ok {
		n, err := number.Int64()
		return n, err == nil
	}
	return 0, false
}
func stringsEqual(value any, expected []string) bool {
	values, ok := value.([]any)
	if !ok || len(values) != len(expected) {
		return false
	}
	for i, text := range expected {
		if values[i] != text {
			return false
		}
	}
	return true
}

// Docker's --tmpfs mounts appear in HostConfig.Tmpfs, separately from Mounts.
// Only the exact bounded /tmp policy we create is eligible for startup/recovery;
// image volumes and all other mounts remain forbidden by the bind-mount check.
func inspectedTemporaryStorage(host map[string]any) (int64, bool) {
	if host == nil {
		return 0, false
	}
	if host["Tmpfs"] == nil {
		return 0, true
	}
	mounts, ok := host["Tmpfs"].(map[string]any)
	if !ok {
		return 0, false
	}
	if len(mounts) == 0 {
		return 0, true
	}
	options, ok := mounts["/tmp"].(string)
	if !ok || len(mounts) != 1 {
		return 0, false
	}
	size := strings.TrimSuffix(strings.TrimPrefix(options, "rw,nosuid,nodev,exec,size="), "m,mode=1777")
	megabytes, err := strconv.ParseInt(size, 10, 64)
	memory, validMemory := integer(host["Memory"])
	if err != nil || megabytes <= 0 || megabytes > math.MaxInt64/(1024*1024) ||
		!validMemory || megabytes*1024*1024 > memory || options != temporaryStorageOptions(megabytes) {
		return 0, false
	}
	return megabytes, true
}

// Each container has its Run worktree and optionally one credential directory.
// The caller must compare the returned credential source with its trusted plan
// or recovery configuration; arbitrary bind sources are never adopted.
func inspectedBindMounts(value any, worktree, reviewContext string) (string, bool) {
	mounts, ok := value.([]any)
	review := reviewContext != ""
	min, max := 1, 2
	if review {
		min, max = 2, 3
	}
	if !ok || len(mounts) < min || len(mounts) > max {
		return "", false
	}
	workspaceSeen, contextSeen, credentials := false, false, ""
	for _, value := range mounts {
		mount := object(value)
		if mount["Type"] != "bind" {
			return "", false
		}
		switch mount["Destination"] {
		case "/workspace":
			if workspaceSeen || mount["Source"] != worktree || mount["RW"] != !review {
				return "", false
			}
			workspaceSeen = true
		case "/review-context":
			if !review || contextSeen || mount["Source"] != reviewContext || mount["RW"] != false {
				return "", false
			}
			contextSeen = true
		case credentialDestination:
			source, ok := mount["Source"].(string)
			if !ok || source == "" || credentials != "" || mount["RW"] != true {
				return "", false
			}
			credentials = source
		default:
			return "", false
		}
	}
	return credentials, workspaceSeen && contextSeen == review
}

func (d *Docker) verifyPolicy(ctx context.Context, id string, plan Plan, nonce string) error {
	container, err := d.inspect(ctx, id)
	if err != nil {
		return err
	}
	config, host := object(container["Config"]), object(container["HostConfig"])
	credentials, mountsOK := inspectedBindMounts(container["Mounts"], plan.WorktreeSource, plan.ReviewContextSource)
	if !mountsOK || credentials != plan.CredentialSource {
		return fmt.Errorf("%w: container mount policy mismatch", ErrStart)
	}
	reserved := map[string]any{}
	for name, value := range object(config["Labels"]) {
		if strings.HasPrefix(name, "io.circular.") {
			reserved[name] = value
		}
	}
	labels := map[string]any{nonceLabel: nonce}
	for name, value := range plan.Labels {
		labels[name] = value
	}
	cpu, cpuOK := integer(host["NanoCpus"])
	memory, memoryOK := integer(host["Memory"])
	temporaryStorage, temporaryStorageOK := inspectedTemporaryStorage(host)
	roundedCPU, _ := strconv.ParseFloat(strconv.FormatFloat(plan.CPULimit, 'g', 15, 64), 64)
	restart := map[string]any{"Name": "no", "MaximumRetryCount": json.Number("0")}
	if container["Id"] != id ||
		!reflect.DeepEqual(reserved, labels) || config["User"] != plan.ContainerUser || config["WorkingDir"] != plan.WorkingDirectory ||
		host["NetworkMode"] != plan.NetworkMode || host["ReadonlyRootfs"] != true ||
		!stringsEqual(host["CapDrop"], plan.CapDrop) || !stringsEqual(host["SecurityOpt"], plan.SecurityOptions) ||
		!cpuOK || cpu != int64(roundedCPU*1e9) || !memoryOK || memory != plan.MemoryLimitMB*1024*1024 ||
		!temporaryStorageOK || temporaryStorage != plan.TemporaryStorageMB ||
		!reflect.DeepEqual(host["RestartPolicy"], restart) {
		return fmt.Errorf("%w: container policy does not match the resolved Run plan", ErrStart)
	}
	return nil
}

type containerState struct {
	status   string
	exitCode int
}

func (s containerState) terminal() bool { return s.status == "exited" || s.status == "dead" }

func (d *Docker) state(ctx context.Context, id string) (containerState, error) {
	code, output, err := d.cli(ctx, "container", "inspect", "--format", "{{.State.Status}} {{.State.ExitCode}}", id)
	if err != nil {
		return containerState{}, err
	}
	fields := strings.Fields(output)
	if code != 0 || len(fields) != 2 {
		return containerState{}, fmt.Errorf("%w: invalid container state", ErrOperation)
	}
	status := fields[0]
	exit, err := strconv.Atoi(fields[1])
	if err != nil || exit < 0 || exit > 255 || (status != "created" && status != "running" && status != "exited" && status != "dead") {
		return containerState{}, fmt.Errorf("%w: invalid container state", ErrOperation)
	}
	return containerState{status, exit}, nil
}
