package runtimes_test

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/runtimes"
)

var runID = uuid.MustParse("00000000-0000-4000-8000-000000000171")

func TestResolvePreservesPythonPolicyContract(t *testing.T) {
	root := "/circular-policy-fixture/工作区"
	d, err := runtimes.NewDocker(runtimes.DockerConfig{
		WorktreeRoot: root, AllowedEnvironmentNames: []string{"TASK_SCOPE_TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	spec := runtimes.Spec{
		RunID: runID, Image: "circular-fake-agent-workload:test",
		Worktree: filepath.Join(root, runID.String()), Command: []string{"echo", "héllo <&>"},
		Stdin: []byte("private request"), CPULimit: 1.5, MemoryLimitMB: 384,
		Environment: map[string]string{"TASK_SCOPE_TOKEN": "private token"},
	}
	plan, err := d.Resolve(spec)
	if err != nil {
		t.Fatal(err)
	}
	// Golden produced by the existing Python DockerRuntime.resolve, including
	// Python's canonical ASCII JSON encoding of non-ASCII paths/arguments.
	const digest = "ac78fe8b82d1fede182b4e34e3bc5e2f9ac92265cff30ebf8b7e413537f498d1"
	if plan.PolicyDigest != digest || plan.ContainerName != "circular-run-00000000000040008000000000000171" {
		t.Fatalf("Python identity contract changed: %+v", plan)
	}
	if !reflect.DeepEqual(plan.Labels, map[string]string{
		"io.circular.managed": "true", "io.circular.run_id": runID.String(),
		"io.circular.policy_digest": digest,
	}) {
		t.Fatal("durable labels changed")
	}
	if plan.WorktreeSource != spec.Worktree || plan.WorktreeDestination != "/workspace" ||
		plan.WorktreeReadOnly || plan.WorkingDirectory != "/workspace" ||
		plan.ContainerUser != "65532:65532" || plan.NetworkMode != "none" || !plan.RootReadOnly ||
		plan.CPULimit != 1.5 || plan.MemoryLimitMB != 384 ||
		!reflect.DeepEqual(plan.CapDrop, []string{"ALL"}) ||
		!reflect.DeepEqual(plan.SecurityOptions, []string{"no-new-privileges"}) {
		t.Fatalf("container isolation policy changed: %+v", plan)
	}
	if strings.Contains(fmt.Sprintf("%+v", plan), "private") {
		t.Fatal("resolved policy exposed environment or stdin values")
	}
	spec.Command = []string{"echo", "\x7f🙂\n\t"}
	plan, err = d.Resolve(spec)
	if err != nil {
		t.Fatal(err)
	}
	const escapedDigest = "52bad3f27d11696c225d04f8d2f24b6f036da97d8de8e14f54d94d35d9cf42ac"
	if plan.PolicyDigest != escapedDigest {
		t.Fatal("Python DEL, control-character, or surrogate-pair encoding changed")
	}
}

func TestResolveRejectsUnsafeRequestsBeforeDockerAccess(t *testing.T) {
	root := filepath.Join(t.TempDir(), "worktrees")
	d, err := runtimes.NewDocker(runtimes.DockerConfig{WorktreeRoot: root, AllowedEnvironmentNames: []string{"TASK_SCOPE_TOKEN"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*runtimes.Spec){
		"sibling":                 func(s *runtimes.Spec) { s.Worktree = filepath.Join(root, uuid.NewString()) },
		"traversal":               func(s *runtimes.Spec) { s.Worktree = root + "/other/../" + s.RunID.String() },
		"nested":                  func(s *runtimes.Spec) { s.Worktree += "/nested" },
		"image-option":            func(s *runtimes.Spec) { s.Image = "--privileged" },
		"image-space":             func(s *runtimes.Spec) { s.Image = "unsafe image" },
		"image-newline":           func(s *runtimes.Spec) { s.Image += "\n" },
		"command-nul":             func(s *runtimes.Spec) { s.Command = []string{"invalid\x00"} },
		"command-utf8":            func(s *runtimes.Spec) { s.Command = []string{string([]byte{0xff})} },
		"environment-unapproved":  func(s *runtimes.Spec) { s.Environment = map[string]string{"NEW_TOKEN": "private token"} },
		"environment-nul":         func(s *runtimes.Spec) { s.Environment = map[string]string{"TASK_SCOPE_TOKEN": "private token\x00"} },
		"cpu-zero":                func(s *runtimes.Spec) { s.CPULimit = 0 },
		"cpu-nan":                 func(s *runtimes.Spec) { s.CPULimit = math.NaN() },
		"cpu-infinite":            func(s *runtimes.Spec) { s.CPULimit = math.Inf(1) },
		"cpu-overflow":            func(s *runtimes.Spec) { s.CPULimit = math.MaxFloat64 },
		"cpu-rounds-to-unlimited": func(s *runtimes.Spec) { s.CPULimit = 1e-12 },
		"memory-negative":         func(s *runtimes.Spec) { s.MemoryLimitMB = -1 },
		"memory-overflow":         func(s *runtimes.Spec) { s.MemoryLimitMB = math.MaxInt64 },
	} {
		t.Run(name, func(t *testing.T) {
			spec := runtimes.Spec{RunID: runID, Worktree: filepath.Join(root, runID.String()), Image: "fixture:test", CPULimit: 1, MemoryLimitMB: 256}
			change(&spec)
			_, err := d.Resolve(spec)
			if !errors.Is(err, runtimes.ErrInvalidSpec) || strings.Contains(err.Error(), "private token") {
				t.Fatalf("unsafe request was not safely rejected: %v", err)
			}
		})
	}
}

func TestConfigurationRejectsSensitiveEnvironmentAndUnsafeRoots(t *testing.T) {
	for _, name := range []string{"PATH", "HOME", "DATABASE_URL", "CIRCULAR_PLATFORM_TOKEN", "DOCKER_HOST", "SSH_AUTH_SOCK", "XDG_CONFIG_HOME", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "HTTP_PROXY", "https_proxy", "ALL_PROXY", "NO_PROXY", "PYTHONPATH", "SSL_CERT_FILE", "SSLKEYLOGFILE", "GODEBUG", "GOMAXPROCS", "GITHUB_TOKEN", "BAD=NAME"} {
		t.Run(name, func(t *testing.T) {
			_, err := runtimes.NewDocker(runtimes.DockerConfig{WorktreeRoot: t.TempDir(), AllowedEnvironmentNames: []string{name}})
			if !errors.Is(err, runtimes.ErrInvalidConfiguration) {
				t.Fatalf("unsafe allowlist accepted: %v", err)
			}
		})
	}
	for _, root := range []string{"", ".", "/", "/tmp/root,with-comma", "/tmp/root\x00", "/tmp/" + string([]byte{0xff})} {
		if _, err := runtimes.NewDocker(runtimes.DockerConfig{WorktreeRoot: root}); !errors.Is(err, runtimes.ErrInvalidConfiguration) {
			t.Fatalf("unsafe root accepted: %v", err)
		}
	}
	_, err := runtimes.NewDocker(runtimes.DockerConfig{WorktreeRoot: t.TempDir(), StopTimeout: time.Duration(math.MaxInt64), OperationTimeout: time.Second})
	if !errors.Is(err, runtimes.ErrInvalidConfiguration) {
		t.Fatalf("overflowing cleanup deadlines accepted: %v", err)
	}
}

func TestResolveRejectsWorktreeSymlinksAndReplacedAncestors(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "worktrees")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	d, err := runtimes.NewDocker(runtimes.DockerConfig{WorktreeRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	spec := runtimes.Spec{RunID: runID, Worktree: filepath.Join(root, runID.String()), Image: "fixture:test", CPULimit: 1, MemoryLimitMB: 256}
	if err := os.Symlink(filepath.Join(base, "absent"), spec.Worktree); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Resolve(spec); !errors.Is(err, runtimes.ErrInvalidSpec) {
		t.Fatalf("worktree symlink accepted: %v", err)
	}
	if err := os.Rename(root, root+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root+"-original", root); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Resolve(spec); !errors.Is(err, runtimes.ErrInvalidSpec) {
		t.Fatalf("replaced ancestor accepted: %v", err)
	}
	if _, err := runtimes.NewDocker(runtimes.DockerConfig{WorktreeRoot: root}); !errors.Is(err, runtimes.ErrInvalidConfiguration) {
		t.Fatalf("symlink root accepted: %v", err)
	}
}
