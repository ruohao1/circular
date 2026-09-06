package runtimes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

func (d *Docker) verifyPolicy(ctx context.Context, id string, plan Plan, nonce string) error {
	container, err := d.inspect(ctx, id)
	if err != nil {
		return err
	}
	config, host := object(container["Config"]), object(container["HostConfig"])
	mounts, ok := container["Mounts"].([]any)
	if !ok || len(mounts) != 1 {
		return fmt.Errorf("%w: container mount policy mismatch", ErrStart)
	}
	mount := object(mounts[0])
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
	roundedCPU, _ := strconv.ParseFloat(strconv.FormatFloat(plan.CPULimit, 'g', 15, 64), 64)
	restart := map[string]any{"Name": "no", "MaximumRetryCount": json.Number("0")}
	if container["Id"] != id || mount["Type"] != "bind" || mount["Source"] != plan.WorktreeSource ||
		mount["Destination"] != plan.WorktreeDestination || mount["RW"] != true ||
		!reflect.DeepEqual(reserved, labels) || config["User"] != plan.ContainerUser || config["WorkingDir"] != plan.WorkingDirectory ||
		host["NetworkMode"] != plan.NetworkMode || host["ReadonlyRootfs"] != true ||
		!stringsEqual(host["CapDrop"], plan.CapDrop) || !stringsEqual(host["SecurityOpt"], plan.SecurityOptions) ||
		!cpuOK || cpu != int64(roundedCPU*1e9) || !memoryOK || memory != plan.MemoryLimitMB*1024*1024 ||
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
