package testsupport

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ruohao1/circular/internal/fakeworkload"
)

// DockerSimulator creates only an external CLI fixture; the real runtime and
// supervisor remain unchanged. The helper runs this package's Go test executable.
func DockerSimulator(t *testing.T, dir string, options map[string]any) (string, string) {
	t.Helper()
	state := filepath.Join(dir, "fake-docker-state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "docker-options.json")
	if err := os.WriteFile(config, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	path := filepath.Join(dir, "fake-docker")
	launcher := "#!/bin/sh\nGORACE=atexit_sleep_ms=0 exec " + quote(executable) + " -test.run='^TestDockerCLIHelper$' -- " + quote(state) + " " + quote(config) + " \"$@\"\n"
	if err := os.WriteFile(path, []byte(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	return path, state
}

// DockerCLIHelper must be called by TestDockerCLIHelper in each consuming package.
func DockerCLIHelper() {
	index := slices.Index(os.Args, "--")
	if index < 0 || len(os.Args) < index+4 {
		return
	}
	_ = os.Unsetenv("GORACE") // Test instrumentation is not part of the simulated CLI environment.
	state, config, args := os.Args[index+1], os.Args[index+2], os.Args[index+3:]
	data, err := os.ReadFile(config)
	if err != nil {
		os.Exit(90)
	}
	var options map[string]any
	if json.Unmarshal(data, &options) != nil {
		os.Exit(91)
	}
	os.Exit(simulateDocker(state, config, args, options))
}

func simulateDocker(state, config string, args []string, options map[string]any) int {
	path := func(name string) string { return filepath.Join(state, name) }
	exists := func(name string) bool { _, err := os.Stat(path(name)); return err == nil }
	read := func(name string) string { data, _ := os.ReadFile(path(name)); return string(data) }
	write := func(name, value string) {
		if err := os.WriteFile(path(name), []byte(value), 0600); err != nil {
			os.Exit(92)
		}
	}
	flag := func(name string) bool { v, _ := options[name].(bool); return v }
	text := func(name string) string { v, _ := options[name].(string); return v }
	duration := func(name string) time.Duration {
		v, _ := options[name].(float64)
		return time.Duration(v * float64(time.Second))
	}
	option := func(argv []string, name, fallback string) string {
		index := slices.Index(argv, name)
		if index >= 0 && index+1 < len(argv) {
			return argv[index+1]
		}
		return fallback
	}
	dump := func(v any) string { data, _ := json.Marshal(v); return string(data) }
	const id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if args[0] == "__late" {
		time.Sleep(duration("create_late_commit_delay"))
		write("created", "")
		write("create-argv.json", dump(args[1:]))
		return 0
	}
	if args[0] == "__linger" {
		time.Sleep(2 * time.Second)
		return 0
	}
	log, _ := os.OpenFile(path("calls.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if log != nil {
		_, _ = log.WriteString(dump(map[string]any{"argv": args}) + "\n")
		_ = log.Close()
	}
	spawn := func(mode string, extra []string, detached bool) {
		binary, _ := os.Executable()
		child := exec.Command(binary, append([]string{"-test.run=^TestDockerCLIHelper$", "--", state, config, mode}, extra...)...)
		child.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
		if detached {
			child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		} else {
			child.Stdout = os.Stdout
			child.Stderr = os.Stderr
		}
		if child.Start() != nil {
			os.Exit(93)
		}
	}
	switch args[0] {
	case "create":
		if exists("created") {
			return 17
		}
		write("create-started", "")
		if flag("create_hangs_without_commit_once") && !exists("create-hung-once") {
			write("create-hung-once", "")
			time.Sleep(duration("create_delay"))
			return 19
		}
		if duration("create_late_commit_delay") > 0 {
			spawn("__late", args, true)
			time.Sleep(duration("create_delay"))
			return 19
		}
		if !flag("create_hangs_after_creation") && !flag("create_hangs_without_commit_once") {
			time.Sleep(duration("create_delay"))
		}
		write("created", "")
		if flag("replace_name_on_create") {
			write("replacement", "")
		}
		if flag("owned_nonce_with_foreign_name") {
			write("owned-with-foreign-name", "")
		}
		environment := map[string]string{}
		for _, entry := range os.Environ() {
			key, value, _ := strings.Cut(entry, "=")
			environment[key] = value
		}
		write("create-environment.json", dump(environment))
		write("create-argv.json", dump(args))
		if flag("create_hangs_after_creation") {
			time.Sleep(duration("create_delay"))
		}
		if flag("create_returns_failure") {
			return 17
		}
		if flag("invalid_create_id") {
			fmt.Println("not-a-container-id")
		} else {
			fmt.Println(id)
		}
	case "inspect", "container":
		if slices.Contains(args, "{{.State.Status}} {{.State.ExitCode}}") {
			n, _ := strconv.Atoi(read("inspect-count"))
			write("inspect-count", strconv.Itoa(n+1))
			if flag("inspect_fails_after_ready") && exists("ready-inspected") {
				return 1
			}
			if exists("exit-code") {
				fmt.Println("exited " + read("exit-code"))
			} else if exists("start-attached") {
				fmt.Println("running 0")
				if flag("inspect_fails_after_ready") {
					write("ready-inspected", "")
				}
			} else {
				fmt.Println("created 0")
			}
		} else if len(args) > 1 && args[0] == "container" && args[1] == "ls" {
			if flag("reconciliation_unavailable") && !exists("reconciliation-available") {
				return 1
			}
			if exists("created") {
				filter := option(args, "--filter", "")
				foreign := exists("replacement") || exists("owned-with-foreign-name")
				if strings.HasPrefix(filter, "label=") {
					if !exists("replacement") {
						fmt.Println(id)
					}
				} else if foreign {
					fmt.Println(strings.Repeat("b", 64))
				} else {
					fmt.Println(id)
				}
			}
			return 0
		} else if len(args) == 3 && args[0] == "container" && args[1] == "inspect" {
			target := args[len(args)-1]
			if flag("reconciliation_unavailable") && strings.HasPrefix(target, "circular-run-") && !exists("reconciliation-available") {
				return 1
			}
			var argv []string
			_ = json.Unmarshal([]byte(read("create-argv.json")), &argv)
			labels := map[string]string{}
			for index, arg := range argv {
				if arg == "--label" && index+1 < len(argv) {
					key, value, _ := strings.Cut(argv[index+1], "=")
					labels[key] = value
				}
			}
			reported := id
			if (exists("replacement") || exists("owned-with-foreign-name")) && strings.HasPrefix(target, "circular-run-") {
				reported = strings.Repeat("b", 64)
				labels = map[string]string{}
			}
			mount := map[string]string{}
			for _, part := range strings.Split(option(argv, "--mount", "type=bind,src=/missing,dst=/workspace"), ",") {
				key, value, _ := strings.Cut(part, "=")
				mount[key] = value
			}
			mounts := []map[string]any{{"Type": mount["type"], "Source": mount["src"], "Destination": mount["dst"], "RW": true}}
			if flag("unexpected_mount") {
				mounts = append(mounts, map[string]any{"Type": "volume", "Source": "unexpected", "Destination": "/image-volume", "RW": true})
			}
			cpus, _ := strconv.ParseFloat(option(argv, "--cpus", "0"), 64)
			memory, _ := strconv.ParseInt(strings.TrimSuffix(option(argv, "--memory", "0m"), "m"), 10, 64)
			settings := map[string]any{"Labels": labels, "User": option(argv, "--user", ""), "WorkingDir": option(argv, "--workdir", "")}
			host := map[string]any{"NetworkMode": option(argv, "--network", "default"), "ReadonlyRootfs": slices.Contains(argv, "--read-only"), "CapDrop": []string{option(argv, "--cap-drop", "")}, "SecurityOpt": []string{option(argv, "--security-opt", "")}, "NanoCpus": int64(cpus * 1e9), "Memory": memory * 1024 * 1024, "RestartPolicy": map[string]any{"Name": option(argv, "--restart", ""), "MaximumRetryCount": 0}}
			switch text("policy_mismatch") {
			case "mount_type":
				mounts[0]["Type"] = "volume"
			case "mount_source":
				mounts[0]["Source"] = "/different"
			case "mount_destination":
				mounts[0]["Destination"] = "/different"
			case "mount_rw":
				mounts[0]["RW"] = false
			case "network":
				host["NetworkMode"] = "bridge"
			case "read_only":
				host["ReadonlyRootfs"] = false
			case "cap_drop":
				host["CapDrop"] = []string{}
			case "security":
				host["SecurityOpt"] = []string{}
			case "cpu":
				host["NanoCpus"] = int64(cpus*1e9) + 1
			case "memory":
				host["Memory"] = memory*1024*1024 + 1
			case "user":
				settings["User"] = "0:0"
			case "workdir":
				settings["WorkingDir"] = "/"
			case "restart":
				host["RestartPolicy"].(map[string]any)["Name"] = "always"
			case "managed_label":
				labels["io.circular.managed"] = "false"
			case "extra_label":
				labels["org.opencontainers.image.source"] = "image"
			case "extra_circular_label":
				labels["io.circular.unexpected"] = "unsafe"
			}
			fmt.Println(dump([]any{map[string]any{"Id": reported, "Name": "/" + option(argv, "--name", "missing"), "Mounts": mounts, "Config": settings, "HostConfig": host}}))
		}
		if !exists("created") {
			return 1
		}
	case "start":
		write("start-invoked", "")
		time.Sleep(duration("start_delay"))
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			return 1
		}
		write("stdin.bin", string(input))
		write("start-attached", "")
		if script := text("output_script"); script != "" {
			var document struct {
				Chunks   []struct{ Stream, Data string }
				ExitCode int `json:"exit_code"`
			}
			data, _ := os.ReadFile(script)
			if json.Unmarshal(data, &document) != nil {
				return 1
			}
			var request struct{ Run struct{ ID string } }
			_ = json.Unmarshal(input, &request)
			for _, chunk := range document.Chunks {
				data, err := base64.StdEncoding.DecodeString(chunk.Data)
				if err != nil {
					return 1
				}
				data = bytes.ReplaceAll(data, []byte("@RUN_ID@"), []byte(request.Run.ID))
				stream := os.Stdout
				if chunk.Stream == "stderr" {
					stream = os.Stderr
				}
				_, _ = stream.Write(data)
				time.Sleep(30 * time.Millisecond)
			}
			write("exit-code", strconv.Itoa(document.ExitCode))
			return document.ExitCode
		}
		if flag("fake_workload") {
			var argv []string
			_ = json.Unmarshal([]byte(read("create-argv.json")), &argv)
			for _, part := range strings.Split(option(argv, "--mount", ""), ",") {
				if strings.HasPrefix(part, "src=") {
					if os.Chdir(strings.TrimPrefix(part, "src=")) != nil {
						return 1
					}
				}
			}
			code := fakeworkload.Run(context.Background(), bytes.NewReader(input), os.Stdout, os.Stderr, true)
			write("exit-code", strconv.Itoa(code))
			return code
		}
		fmt.Fprint(os.Stdout, "first\n")
		if flag("attachment_fails") {
			return 125
		}
		if flag("waits_for_stop") {
			for range 500 {
				if exists("exit-code") {
					code, _ := strconv.Atoi(read("exit-code"))
					return code
				}
				time.Sleep(10 * time.Millisecond)
			}
			return 1
		}
		time.Sleep(30 * time.Millisecond)
		fmt.Fprint(os.Stderr, "warning\n")
		time.Sleep(30 * time.Millisecond)
		fmt.Fprint(os.Stdout, "last\n")
		if flag("output_pipe_linger") {
			write("exit-code", "0")
			spawn("__linger", nil, false)
			return 0
		}
		write("exit-code", "23")
		return 23
	case "wait":
		if !exists("start-attached") {
			fmt.Println("0")
			return 0
		}
		for range 500 {
			if exists("exit-code") {
				fmt.Println(read("exit-code"))
				return 0
			}
			time.Sleep(10 * time.Millisecond)
		}
		return 1
	case "stop", "kill":
		write(args[0]+"-started", "")
		time.Sleep(duration("stop_delay"))
		if flag("stop_fails") {
			return 1
		}
		write("exit-code", "137")
	case "rm":
		write("rm-started", "")
		time.Sleep(duration("remove_delay"))
		if flag("remove_fails") {
			return 1
		}
		target := args[len(args)-1]
		if target == id && exists("owned-with-foreign-name") {
			write("owned-removed", "")
			return 0
		}
		if target == id && exists("replacement") {
			return 1
		}
		_ = os.Remove(path("created"))
	default:
		return 2
	}
	return 0
}
