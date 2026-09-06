// Package runtimes owns Run container execution, not queue claims or Git resources.
package runtimes

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrInvalidConfiguration = errors.New("invalid Docker runtime configuration")
	ErrInvalidSpec          = errors.New("invalid container specification")
	imageReference          = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]+)?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}|@sha256:[0-9a-f]{64})?$`)
	environmentName         = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	containerUser           = regexp.MustCompile(`^[1-9][0-9]*:[1-9][0-9]*$`)
)

type Spec struct {
	RunID          uuid.UUID
	Image          string
	Worktree       string
	Command        []string
	Stdin          []byte
	CPULimit       float64
	MemoryLimitMB  int64
	Environment    map[string]string
	NetworkEnabled bool
}

// Plan is a side-effect-free policy snapshot. It excludes environment values and
// stdin entirely, so diagnostic formatting cannot expose the request or secrets.
type Plan struct {
	RunID               uuid.UUID
	ContainerName       string
	Labels              map[string]string
	PolicyDigest        string
	Image               string
	Command             []string
	EnvironmentNames    []string
	WorktreeSource      string
	WorktreeDestination string
	WorktreeReadOnly    bool
	WorkingDirectory    string
	ContainerUser       string
	NetworkMode         string
	RootReadOnly        bool
	CapDrop             []string
	SecurityOptions     []string
	CPULimit            float64
	MemoryLimitMB       int64
}

// Zero-valued optional configuration fields use the existing Python defaults.
// DockerExecutable must be an absolute path or a name in the fixed system PATH;
// the caller's PATH, Docker configuration, credentials and proxy env are not used.
type DockerConfig struct {
	WorktreeRoot            string
	AllowedEnvironmentNames []string
	DockerExecutable        string
	ContainerUser           string
	StopTimeout             time.Duration
	OperationTimeout        time.Duration
}

type Docker struct {
	config     DockerConfig
	allowlist  map[string]bool
	mu         sync.Mutex
	starts     map[string]chan struct{}
	executions map[string]*execution
	discarded  map[Handle]bool
	unresolved map[string]unresolvedCreate
}

type launch struct {
	plan        Plan
	stdin       []byte
	environment map[string]string
}

// NewDocker validates and snapshots trusted worker configuration without
// contacting Docker or creating filesystem resources.
func NewDocker(config DockerConfig) (*Docker, error) {
	root, err := validatedRoot(config.WorktreeRoot)
	if err != nil {
		return nil, err
	}
	config.WorktreeRoot = root
	if config.DockerExecutable == "" {
		config.DockerExecutable = "docker"
	}
	if !safeText(config.DockerExecutable) {
		return nil, fmt.Errorf("%w: Docker executable is invalid", ErrInvalidConfiguration)
	}
	if config.ContainerUser == "" {
		config.ContainerUser = "65532:65532"
	}
	if !containerUser.MatchString(config.ContainerUser) {
		return nil, fmt.Errorf("%w: container user must be a non-root numeric UID:GID", ErrInvalidConfiguration)
	}
	if config.StopTimeout == 0 {
		config.StopTimeout = 5 * time.Second
	}
	if config.OperationTimeout == 0 {
		config.OperationTimeout = 30 * time.Second
	}
	if config.StopTimeout < 0 || config.OperationTimeout < 0 || config.StopTimeout > time.Duration(math.MaxInt64)-config.OperationTimeout {
		return nil, fmt.Errorf("%w: timeouts must be positive", ErrInvalidConfiguration)
	}
	allowlist := make(map[string]bool, len(config.AllowedEnvironmentNames))
	for _, name := range config.AllowedEnvironmentNames {
		if !environmentName.MatchString(name) || sensitiveEnvironment(name) {
			return nil, fmt.Errorf("%w: environment allowlist contains a forbidden name", ErrInvalidConfiguration)
		}
		allowlist[name] = true
	}
	config.AllowedEnvironmentNames = slices.Clone(config.AllowedEnvironmentNames)
	return &Docker{config: config, allowlist: allowlist, starts: make(map[string]chan struct{}),
		executions: make(map[string]*execution), discarded: make(map[Handle]bool),
		unresolved: make(map[string]unresolvedCreate)}, nil
}

// Resolve validates a launch without allocation and returns its non-secret
// policy. The returned slices and maps are independent of caller-owned storage.
func (d *Docker) Resolve(spec Spec) (Plan, error) {
	resolved, err := d.resolve(spec)
	return resolved.plan, err
}

func (d *Docker) resolve(spec Spec) (launch, error) {
	expected := filepath.Join(d.config.WorktreeRoot, spec.RunID.String())
	if spec.Worktree != expected || isSymlink(spec.Worktree) {
		return launch{}, fmt.Errorf("%w: worktree must be the direct Run UUID child of its root", ErrInvalidSpec)
	}
	resolved, err := resolveMissing(spec.Worktree)
	if err != nil || resolved != expected || !safePath(resolved) {
		return launch{}, fmt.Errorf("%w: worktree must remain inside its managed root", ErrInvalidSpec)
	}
	if !imageReference.MatchString(spec.Image) {
		return launch{}, fmt.Errorf("%w: image reference is invalid", ErrInvalidSpec)
	}
	for _, arg := range spec.Command {
		if !safeText(arg) {
			return launch{}, fmt.Errorf("%w: command contains an invalid argument", ErrInvalidSpec)
		}
	}
	if math.IsNaN(spec.CPULimit) || math.IsInf(spec.CPULimit, 0) || spec.CPULimit <= 0 {
		return launch{}, fmt.Errorf("%w: CPU limit must be finite and positive", ErrInvalidSpec)
	}
	roundedCPU, _ := strconv.ParseFloat(strconv.FormatFloat(spec.CPULimit, 'g', 15, 64), 64)
	if roundedCPU*1e9 < 1 || roundedCPU*1e9 >= float64(math.MaxInt64) {
		return launch{}, fmt.Errorf("%w: CPU limit is not representable in Docker nanocpus", ErrInvalidSpec)
	}
	if spec.MemoryLimitMB <= 0 || spec.MemoryLimitMB > math.MaxInt64/(1024*1024) {
		return launch{}, fmt.Errorf("%w: memory limit must be a positive representable byte count", ErrInvalidSpec)
	}
	names := make([]string, 0, len(spec.Environment))
	environment := make(map[string]string, len(spec.Environment))
	for name, value := range spec.Environment {
		if !environmentName.MatchString(name) || sensitiveEnvironment(name) || !d.allowlist[name] || !safeText(value) {
			return launch{}, fmt.Errorf("%w: environment contains a forbidden name or invalid value", ErrInvalidSpec)
		}
		names = append(names, name)
		environment[name] = value
	}
	slices.Sort(names)
	command := append([]string{}, spec.Command...)
	// Keep this document identical to Python's persisted policy labels. Values
	// and stdin are deliberately absent: labels must not be secret-value oracles.
	digest := policyDigest(map[string]any{
		"command": command, "container_user": d.config.ContainerUser,
		"cpu_limit":         strconv.FormatFloat(spec.CPULimit, 'g', 15, 64),
		"environment_names": names, "image": spec.Image, "memory_limit_mb": spec.MemoryLimitMB,
		"network_enabled": spec.NetworkEnabled, "run_id": spec.RunID.String(), "worktree": resolved,
	})
	network := "none"
	if spec.NetworkEnabled {
		network = "bridge"
	}
	plan := Plan{
		RunID: spec.RunID, ContainerName: "circular-run-" + strings.ReplaceAll(spec.RunID.String(), "-", ""),
		Labels:       map[string]string{"io.circular.managed": "true", "io.circular.run_id": spec.RunID.String(), "io.circular.policy_digest": digest},
		PolicyDigest: digest, Image: spec.Image, Command: command, EnvironmentNames: names,
		WorktreeSource: resolved, WorktreeDestination: "/workspace", WorkingDirectory: "/workspace",
		ContainerUser: d.config.ContainerUser, NetworkMode: network, RootReadOnly: true,
		CapDrop: []string{"ALL"}, SecurityOptions: []string{"no-new-privileges"},
		CPULimit: spec.CPULimit, MemoryLimitMB: spec.MemoryLimitMB,
	}
	return launch{plan: plan, stdin: append([]byte{}, spec.Stdin...), environment: environment}, nil
}

func safeText(value string) bool { return utf8.ValidString(value) && !strings.ContainsRune(value, 0) }
func safePath(value string) bool { return safeText(value) && !strings.Contains(value, ",") }

func isSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

func validatedRoot(root string) (string, error) {
	if !filepath.IsAbs(root) || !safePath(root) || isSymlink(root) {
		return "", fmt.Errorf("%w: worktree root must be an absolute non-symlink path", ErrInvalidConfiguration)
	}
	resolved, err := resolveMissing(root)
	if err != nil || resolved == string(filepath.Separator) || !safePath(resolved) {
		return "", fmt.Errorf("%w: worktree root is not a managed directory", ErrInvalidConfiguration)
	}
	return resolved, nil
}

// Docker-host-visible paths may not exist in the worker's namespace. Resolve
// existing ancestors without requiring the unallocated Run directory to exist.
func resolveMissing(path string) (string, error) {
	path = filepath.Clean(path)
	_, err := os.Lstat(path)
	if err == nil {
		return filepath.EvalSymlinks(path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err := resolveMissing(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}

func sensitiveEnvironment(name string) bool {
	name = strings.ToUpper(name)
	for _, prefix := range []string{"CIRCULAR_", "DOCKER_", "SSH_", "XDG_", "LD_", "DYLD_", "SSL_CERT_", "PYTHON"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	switch name {
	case "DATABASE_URL", "GITHUB_TOKEN", "GODEBUG", "GOMAXPROCS", "HOME", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "PATH", "SSLKEYLOGFILE":
		return true
	}
	return false
}

func policyDigest(document map[string]any) string {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	// All values are validated primitives; encoding cannot fail.
	if err := encoder.Encode(document); err != nil {
		panic("validated policy could not be encoded")
	}
	var ascii strings.Builder
	for _, char := range strings.TrimSuffix(encoded.String(), "\n") {
		if char < 127 {
			ascii.WriteRune(char)
		} else if char <= 0xffff {
			fmt.Fprintf(&ascii, "\\u%04x", char)
		} else {
			hi, lo := utf16.EncodeRune(char)
			fmt.Fprintf(&ascii, "\\u%04x\\u%04x", hi, lo)
		}
	}
	sum := sha256.Sum256([]byte(ascii.String()))
	return hex.EncodeToString(sum[:])
}
