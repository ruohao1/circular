package main

import (
	"strings"
	"testing"
)

func TestInvalidDatabaseURLIsRedactedBeforeExecutorStartup(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("DATABASE_URL", "://must-not-leak-credential")
	t.Setenv("CIRCULAR_EXECUTOR_PYTHON", "/nonexistent/circular-python")
	err := run(t.Context(), true)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is not a valid") ||
		strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("preflight did not safely reject the database URL: %v", err)
	}
}

func TestRemovedExecutionFallbackIsRejectedBeforeClaiming(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("DATABASE_URL", "postgresql+psycopg://circular:circular@127.0.0.1:1/unreachable")
	t.Setenv("CIRCULAR_EXECUTOR_PYTHON", "/nonexistent/circular-python")
	t.Setenv("CIRCULAR_GO_EXECUTOR", "python")
	err := run(t.Context(), false)
	if err == nil || !strings.Contains(err.Error(), "CIRCULAR_GO_EXECUTOR is retired") {
		t.Fatalf("worker did not fail before starting its queue: %v", err)
	}
}

func TestNativeExecutionPreflightDoesNotRequirePythonOrLiveDependencies(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("DATABASE_URL", "postgresql://circular:circular@127.0.0.1:1/unreachable?pool_min_conns=1")
	t.Setenv("CIRCULAR_EXECUTOR_PYTHON", "/nonexistent/circular-python")
	t.Setenv("CIRCULAR_GO_EXECUTOR", "go")
	if err := run(t.Context(), true); err != nil {
		t.Fatalf("native execution preflight still requires the Python bridge: %v", err)
	}
}

func TestNativeExecutionIsTheDefault(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("DATABASE_URL", "postgresql://circular:circular@127.0.0.1:1/unreachable")
	t.Setenv("CIRCULAR_EXECUTOR_PYTHON", "/nonexistent/circular-python")
	t.Setenv("CIRCULAR_GO_EXECUTOR", "")
	if err := run(t.Context(), true); err != nil {
		t.Fatalf("default worker still requires the Python bridge: %v", err)
	}
}

func TestInvalidNativeConfigurationIsRejectedBeforeClaiming(t *testing.T) {
	for _, test := range []struct{ name, value string }{
		{"CIRCULAR_GO_EXECUTOR", "unknown-must-not-leak"},
		{"CIRCULAR_RUNNER_CPU_LIMIT", "NaN"},
		{"CIRCULAR_RUNNER_CPU_LIMIT", "1e50"},
		{"CIRCULAR_RUNNER_MEMORY_LIMIT_MB", "0"},
		{"CIRCULAR_RUNNER_MEMORY_LIMIT_MB", "1.5"},
		{"CIRCULAR_FAKE_DELAY_SECONDS", "+Inf"},
		{"CIRCULAR_FAKE_DELAY_SECONDS", "11"},
		{"CIRCULAR_DOCKER_WORKTREE_ROOT", "relative-must-not-leak"},
		{"CIRCULAR_DOCKER_REVIEW_CONTEXT_ROOT", "relative-must-not-leak"},
		{"CIRCULAR_REVIEW_CONTEXT_ROOT", ".circular/worktrees"},
		{"CIRCULAR_WORKTREE_ROOT", ".circular/repositories"},
		{"CIRCULAR_ARTIFACT_ROOT", ".circular/worktrees/artifacts"},
		{"CIRCULAR_RUNNER_IMAGE", "--must-not-leak"},
	} {
		t.Run(test.name+"/"+test.value, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("DATABASE_URL", "postgresql://circular:must-not-leak@127.0.0.1:1/unreachable")
			t.Setenv("CIRCULAR_GO_EXECUTOR", "go")
			t.Setenv(test.name, test.value)
			err := run(t.Context(), false)
			if err == nil || strings.Contains(err.Error(), "must-not-leak") ||
				strings.Contains(err.Error(), "worker database operation failed") {
				t.Fatalf("invalid configuration reached the queue or leaked input: %v", err)
			}
		})
	}
}

func TestCodexPreflightValidatesOptInBeforeClaiming(t *testing.T) {
	for _, test := range []struct {
		name, enabled, mode, key, image, memory string
		valid                                   bool
	}{
		{"disabled", "false", "", "", "", "2048", true},
		{"subscription_default", "true", "", "", "circular-codex-runner:test", "128", true},
		{"subscription_explicit", "true", "chatgpt", "", "circular-codex-runner:test", "128", true},
		{"subscription_no_api_fallback", "true", "", "synthetic-must-not-leak", "circular-codex-runner:test", "128", false},
		{"invalid_mode", "true", "must-not-leak", "", "circular-codex-runner:test", "128", false},
		{"api_key", "true", "api_key", "synthetic-must-not-leak", "circular-codex-runner:test", "128", true},
		{"missing_key", "true", "api_key", "", "circular-codex-runner:test", "2048", false},
		{"invalid_boolean", "must-not-leak", "", "", "", "2048", false},
		{"invalid_image", "true", "api_key", "synthetic-must-not-leak", "--must-not-leak", "2048", false},
		{"insufficient_memory", "true", "api_key", "synthetic-must-not-leak", "circular-codex-runner:test", "127", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("DATABASE_URL", "postgresql://circular:circular@127.0.0.1:1/unreachable")
			t.Setenv("CIRCULAR_CODEX_ENABLED", test.enabled)
			t.Setenv("CIRCULAR_CODEX_AUTH_MODE", test.mode)
			t.Setenv("CIRCULAR_CODEX_API_KEY", test.key)
			t.Setenv("CIRCULAR_CODEX_IMAGE", test.image)
			t.Setenv("CIRCULAR_RUNNER_MEMORY_LIMIT_MB", test.memory)
			err := run(t.Context(), true)
			if (err == nil) != test.valid || err != nil && strings.Contains(err.Error(), "must-not-leak") {
				t.Fatalf("unexpected preflight result: %v", err)
			}
		})
	}
}
