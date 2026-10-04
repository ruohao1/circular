package codexlogin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fixtureID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fixtureCall struct {
	Args        []string `json:"args"`
	Environment []string `json:"environment"`
}

type fixtureAllocation struct {
	ID     string `json:"Id"`
	Name   string
	Config struct{ Labels map[string]string }
}

func TestCodexDockerHelper(t *testing.T) {
	index := slices.Index(os.Args, "--")
	if index < 0 {
		return
	}
	_ = os.Unsetenv("GORACE")
	_ = os.Unsetenv("PWD") // Added only by the test fixture's shell launcher.
	state, arguments := os.Args[index+1], os.Args[index+2:]
	if len(arguments) == 0 {
		os.Exit(90)
	}
	os.Exit(dockerHelper(state, arguments))
}

func dockerHelper(state string, arguments []string) int {
	write := func(name string, data []byte) { _ = os.WriteFile(filepath.Join(state, name), data, 0600) }
	read := func(name string) []byte { data, _ := os.ReadFile(filepath.Join(state, name)); return data }
	mode := string(read("mode"))
	if arguments[0] == "child" {
		for {
			time.Sleep(time.Hour)
		}
	}
	log, _ := os.OpenFile(filepath.Join(state, "calls.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if log != nil {
		_ = json.NewEncoder(log).Encode(fixtureCall{Args: arguments, Environment: os.Environ()})
		_ = log.Close()
	}
	var allocation fixtureAllocation
	_ = json.Unmarshal(read("allocation.json"), &allocation)
	switch arguments[0] {
	case "create":
		if mode == "create_failure" {
			fmt.Fprintln(os.Stderr, "private-control-error-do-not-echo")
			return 19
		}
		name, label := option(arguments, "--name"), option(arguments, "--label")
		key, value, _ := strings.Cut(label, "=")
		allocation.ID, allocation.Name = fixtureID, "/"+name
		allocation.Config.Labels = map[string]string{key: value}
		data, _ := json.Marshal(allocation)
		write("allocation.json", data)
		if mode == "lost_create_response" {
			fmt.Fprintln(os.Stdout, "private-control-error-do-not-echo")
			return 19
		}
		fmt.Println(fixtureID)
	case "start":
		if mode == "wait" {
			program, _ := os.Executable()
			child := exec.Command(program, "-test.run=^TestCodexDockerHelper$", "--", state, "child")
			if child.Start() != nil {
				return 91
			}
			write("child-pid", []byte(strconv.Itoa(child.Process.Pid)))
			write("attached-pid", []byte(strconv.Itoa(os.Getpid())))
			fmt.Fprintln(os.Stdout, "fixture device URL and one-time code")
			_ = child.Wait()
			return 0
		}
		fmt.Fprintln(os.Stdout, "fixture authentication output")
		fmt.Fprintln(os.Stderr, "fixture authentication diagnostic")
		if mode == "command_failure" {
			return 17
		}
	case "inspect":
		if allocation.ID == "" || mode == "inspect_failure" {
			fmt.Fprintln(os.Stderr, "private-control-error-do-not-echo")
			return 1
		}
		if mode == "foreign_label" {
			allocation.Config.Labels[ownershipLabel] = "another-owner"
		}
		if mode == "foreign_name" {
			allocation.Name = "/another-container"
		}
		if mode == "foreign_id" {
			allocation.ID = strings.Repeat("b", 64)
		}
		_ = json.NewEncoder(os.Stdout).Encode(allocation)
	case "container":
		if mode == "inspect_failure" {
			return 1
		}
		if allocation.ID != "" {
			fmt.Println(allocation.ID)
		}
	case "rm":
		if mode == "remove_failure" {
			fmt.Fprintln(os.Stderr, "private-control-error-do-not-echo")
			return 1
		}
		if len(arguments) != 3 || arguments[1] != "--force" || arguments[2] != fixtureID {
			return 92
		}
		_ = os.Remove(filepath.Join(state, "allocation.json"))
	default:
		return 93
	}
	return 0
}

func option(arguments []string, name string) string {
	index := slices.Index(arguments, name)
	if index < 0 || index+1 >= len(arguments) {
		return ""
	}
	return arguments[index+1]
}

func loginFixture(t *testing.T, mode string) (Config, string) {
	t.Helper()
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "mode"), []byte(mode), 0600); err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	launcher := "#!/bin/sh\nGORACE=atexit_sleep_ms=0 exec " + quote(program) + " -test.run='^TestCodexDockerHelper$' -- " + quote(state) + " \"$@\"\n"
	executable := filepath.Join(state, "docker-fixture")
	if err := os.WriteFile(executable, []byte(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	return Config{DockerExecutable: executable, Image: "circular-codex-runner:test", CredentialRoot: filepath.Join(state, "auth"), ContainerUser: "65532:65532"}, state
}

func readCalls(t *testing.T, state string) []fixtureCall {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(state, "calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	calls := []fixtureCall{}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var call fixtureCall
		if err := json.Unmarshal(line, &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	return calls
}

func TestAuthCommandsUseOnlyManagedCredentialMountAndIsolatedEnvironment(t *testing.T) {
	for _, variable := range []string{"DATABASE_URL", "CODEX_API_KEY", "OPENAI_API_KEY", "HOME", "DOCKER_HOST", "HTTP_PROXY", "CIRCULAR_CODEX_API_KEY", "PATH"} {
		t.Setenv(variable, "private-environment-never-forward")
	}
	for _, action := range []string{"login", "status", "logout"} {
		t.Run(action, func(t *testing.T) {
			config, state := loginFixture(t, "")
			var stdout, stderr bytes.Buffer
			if code := Run(t.Context(), config, action, &stdout, &stderr); code != 0 || stdout.String() != "fixture authentication output\n" || stderr.String() != "fixture authentication diagnostic\n" {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
			}
			calls := readCalls(t, state)
			if len(calls) != 4 || calls[0].Args[0] != "create" || calls[1].Args[0] != "start" || calls[2].Args[0] != "inspect" || calls[3].Args[0] != "rm" {
				t.Fatalf("unexpected authentication lifecycle: %+v", calls)
			}
			create := calls[0].Args
			name := option(create, "--name")
			label := option(create, "--label")
			if !strings.HasPrefix(name, "circular-codex-auth-") || label != ownershipLabel+"="+strings.TrimPrefix(name, "circular-codex-auth-") {
				t.Fatal("container has no unique ownership identity", create)
			}
			network := "none"
			if action == "login" {
				network = "bridge"
			}
			for flag, expected := range map[string]string{
				"--network": network, "--user": "65532:65532", "--cap-drop": "ALL", "--security-opt": "no-new-privileges", "--restart": "no",
				"--tmpfs": "/tmp:rw,nosuid,nodev,exec,size=128m,mode=1777", "--entrypoint": "/circular-codex-workload",
				"--mount": "type=bind,source=" + config.CredentialRoot + ",target=/codex-auth",
			} {
				if actual := option(create, flag); actual != expected {
					t.Fatalf("%s: %q, expected %q", flag, actual, expected)
				}
			}
			if !slices.Contains(create, "--read-only") || !slices.Equal(create[len(create)-2:], []string{config.Image, action}) || !slices.Equal(calls[1].Args, []string{"start", "--attach", fixtureID}) || !slices.Equal(calls[3].Args, []string{"rm", "--force", fixtureID}) {
				t.Fatalf("container isolation or immutable target changed: %+v", calls)
			}
			if calls[2].Args[len(calls[2].Args)-1] != name {
				t.Fatal("cleanup inspected a different container")
			}
			mounts := 0
			for _, argument := range create {
				if argument == "--mount" {
					mounts++
				}
				if argument == "--env" || argument == "-e" || argument == "--volume" || argument == "-v" || strings.Contains(argument, "/workspace") || strings.Contains(argument, "private-environment") {
					t.Fatal("authentication container received unintended access", create)
				}
			}
			if mounts != 1 {
				t.Fatal("authentication container received extra mounts")
			}
			for _, call := range calls {
				if !slices.Equal(call.Environment, []string{"PATH=" + systemPath}) {
					t.Fatalf("Docker inherited ambient environment: %v", call.Environment)
				}
			}
			if _, err := os.Stat(filepath.Join(state, "allocation.json")); !os.IsNotExist(err) {
				t.Fatal("authentication allocation survived completion")
			}
		})
	}
}

func TestAuthFailuresPreserveCLIExitAndSanitizeDockerDiagnostics(t *testing.T) {
	for _, test := range []struct {
		mode string
		code int
	}{
		{"command_failure", 17}, {"create_failure", 1}, {"lost_create_response", 1}, {"inspect_failure", 1}, {"remove_failure", 1},
	} {
		t.Run(test.mode, func(t *testing.T) {
			config, state := loginFixture(t, test.mode)
			var stdout, stderr bytes.Buffer
			if code := Run(t.Context(), config, "login", &stdout, &stderr); code != test.code {
				t.Fatalf("exit %d, expected %d: %s", code, test.code, stderr.String())
			}
			if strings.Contains(stdout.String()+stderr.String(), "private-control-error") {
				t.Fatal("Docker control error was reflected")
			}
			calls := readCalls(t, state)
			if test.mode == "command_failure" || test.mode == "lost_create_response" {
				if !slices.Equal(calls[len(calls)-1].Args, []string{"rm", "--force", fixtureID}) {
					t.Fatal("owned failed allocation was not removed")
				}
			}
		})
	}
}

func TestAuthCleanupRefusesForeignContainerIdentity(t *testing.T) {
	for _, mode := range []string{"foreign_label", "foreign_name", "foreign_id"} {
		t.Run(mode, func(t *testing.T) {
			config, state := loginFixture(t, mode)
			var stdout, stderr bytes.Buffer
			if code := Run(t.Context(), config, "status", &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "cleanup could not be confirmed") {
				t.Fatalf("foreign ownership did not fail closed: %d %s", code, stderr.String())
			}
			for _, call := range readCalls(t, state) {
				if call.Args[0] == "rm" {
					t.Fatal("cleanup tried to remove a foreign container")
				}
			}
		})
	}
}

func TestAuthCancellationKillsAttachedProcessGroupAndRemovesOwnedContainer(t *testing.T) {
	config, state := loginFixture(t, "wait")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- Run(ctx, config, "login", &stdout, &stderr) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(state, "attached-pid")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("authentication command never attached")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case code := <-done:
		if code != 1 || !strings.Contains(stderr.String(), "cancelled or timed out") {
			t.Fatalf("cancelled auth result: %d %s", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("authentication cancellation exceeded its cleanup bound")
	}
	calls := readCalls(t, state)
	if !slices.Equal(calls[len(calls)-1].Args, []string{"rm", "--force", fixtureID}) {
		t.Fatal("cancelled auth did not remove its exact owned container")
	}
	for _, filename := range []string{"attached-pid", "child-pid"} {
		data, err := os.ReadFile(filepath.Join(state, filename))
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(string(data))
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for alive(pid) {
			if time.Now().After(deadline) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Fatal("cancelled Docker CLI process survived")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func alive(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if os.IsNotExist(err) {
		return false
	}
	if err == nil {
		_, state, found := strings.Cut(string(data), ") ")
		if found && strings.HasPrefix(state, "Z ") {
			return false
		}
	}
	return syscall.Kill(pid, 0) == nil
}

func TestAuthInvalidConfigurationNeverInvokesDocker(t *testing.T) {
	for _, change := range []func(*Config){
		func(config *Config) { config.CredentialRoot = "/auth,target=/host" },
		func(config *Config) { config.CredentialRoot = "/" },
		func(config *Config) { config.CredentialRoot = "relative" },
		func(config *Config) { config.ContainerUser = "0:0" },
		func(config *Config) { config.Image = "--privileged" },
	} {
		config, state := loginFixture(t, "")
		change(&config)
		var stdout, stderr bytes.Buffer
		if code := Run(t.Context(), config, "login", &stdout, &stderr); code != 2 || stdout.Len() != 0 || stderr.String() != "invalid Codex authentication configuration\n" {
			t.Fatalf("invalid auth configuration: %d %s", code, stderr.String())
		}
		if _, err := os.Stat(filepath.Join(state, "calls.jsonl")); !os.IsNotExist(err) {
			t.Fatal("invalid configuration reached Docker")
		}
	}
}

func TestAuthAlreadyCancelledDoesNotAllocate(t *testing.T) {
	config, state := loginFixture(t, "")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var stdout, stderr bytes.Buffer
	if code := Run(ctx, config, "login", &stdout, &stderr); code != 1 || stdout.Len() != 0 {
		t.Fatal("cancelled auth unexpectedly completed")
	}
	if _, err := os.Stat(filepath.Join(state, "calls.jsonl")); !os.IsNotExist(err) {
		t.Fatal("already-cancelled authentication allocated a container")
	}
}
