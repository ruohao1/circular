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

func TestMissingPythonIsRejectedBeforeClaiming(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("DATABASE_URL", "postgresql+psycopg://circular:circular@127.0.0.1:1/unreachable")
	t.Setenv("CIRCULAR_EXECUTOR_PYTHON", "/nonexistent/circular-python")
	err := run(t.Context(), false)
	if err == nil || !strings.Contains(err.Error(), "Python execution bridge is unavailable") {
		t.Fatalf("worker did not fail before starting its queue: %v", err)
	}
}
