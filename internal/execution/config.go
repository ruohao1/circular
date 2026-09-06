package execution

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	git "github.com/ruohao1/circular/internal/git"
	"github.com/ruohao1/circular/internal/runtimes"
)

// LoadConfig maps the established worker environment onto the native
// supervisor. It reads trusted settings only and does not create directories.
func LoadConfig(getenv func(string) string) (Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return Config{}, ErrConfiguration
	}
	value := func(name, fallback string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return fallback
	}
	root := func(name, fallback string, absolute bool) (string, error) {
		path := value(name, fallback)
		if path == "~" || strings.HasPrefix(path, "~/") {
			directory, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("%s cannot resolve the user's directory", name)
			}
			path = filepath.Join(directory, strings.TrimPrefix(path, "~/"))
			if value(name, fallback) == "~" {
				path = directory
			}
		}
		if absolute && !filepath.IsAbs(path) {
			return "", fmt.Errorf("%s must be an absolute host path", name)
		}
		resolved, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("%s is not a valid execution root", name)
		}
		return resolved, nil
	}
	base := filepath.Join(cwd, ".circular")
	cache, err := root("CIRCULAR_REPOSITORY_CACHE_ROOT", filepath.Join(base, "repositories"), false)
	if err != nil {
		return Config{}, err
	}
	worktrees, err := root("CIRCULAR_WORKTREE_ROOT", filepath.Join(base, "worktrees"), false)
	if err != nil {
		return Config{}, err
	}
	artifacts, err := root("CIRCULAR_ARTIFACT_ROOT", filepath.Join(base, "artifacts"), false)
	if err != nil {
		return Config{}, err
	}
	host, err := root("CIRCULAR_DOCKER_WORKTREE_ROOT", worktrees, true)
	if err != nil {
		return Config{}, err
	}
	cpu, err := strconv.ParseFloat(strings.TrimSpace(value("CIRCULAR_RUNNER_CPU_LIMIT", "1")), 64)
	if err != nil || math.IsNaN(cpu) || math.IsInf(cpu, 0) || cpu <= 0 {
		return Config{}, fmt.Errorf("CIRCULAR_RUNNER_CPU_LIMIT must be finite and positive")
	}
	memory, err := strconv.ParseInt(strings.TrimSpace(value("CIRCULAR_RUNNER_MEMORY_LIMIT_MB", "2048")), 10, 64)
	if err != nil || memory <= 0 {
		return Config{}, fmt.Errorf("CIRCULAR_RUNNER_MEMORY_LIMIT_MB must be a positive integer")
	}
	delay, err := strconv.ParseFloat(strings.TrimSpace(value("CIRCULAR_FAKE_DELAY_SECONDS", "0.05")), 64)
	if err != nil || math.IsNaN(delay) || math.IsInf(delay, 0) || delay < 0 || delay > 10 {
		return Config{}, fmt.Errorf("CIRCULAR_FAKE_DELAY_SECONDS must be finite and between zero and ten")
	}
	uid, gid := os.Getuid(), os.Getgid()
	var owner *git.FileOwner
	if uid == 0 {
		uid, gid = 65532, 65532
		owner = &git.FileOwner{UID: uid, GID: gid}
	}
	return Config{
		Git:          git.Config{RepositoryCacheRoot: cache, WorktreeRoot: worktrees, Owner: owner},
		Docker:       runtimes.DockerConfig{WorktreeRoot: host, ContainerUser: fmt.Sprintf("%d:%d", uid, gid)},
		ArtifactRoot: artifacts, Image: value("CIRCULAR_RUNNER_IMAGE", "circular-runner:dev"),
		CPULimit: cpu, MemoryLimitMB: memory, FakeDelayMS: int(math.RoundToEven(delay * 1000)),
	}, nil
}
