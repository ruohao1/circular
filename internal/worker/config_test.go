package worker_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ruohao1/circular/internal/worker"
)

func TestConfigurationUsesExistingWorkerEnvironment(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":                   "postgresql+psycopg://test:test@localhost/test",
		"CIRCULAR_WORKER_ID":             "go-worker",
		"CIRCULAR_POLL_INTERVAL_SECONDS": "0.125",
		"CIRCULAR_EXECUTOR_PYTHON":       "/trusted/.venv/bin/python",
	}
	config, err := worker.LoadConfig(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if config.DatabaseURL != env["DATABASE_URL"] || config.WorkerID != "go-worker" ||
		config.Poll != 125*time.Millisecond || config.Python != env["CIRCULAR_EXECUTOR_PYTHON"] {
		t.Fatal("configuration did not preserve the worker contract")
	}
}

func TestDefaultWorkerIDsAreDistinct(t *testing.T) {
	empty := func(string) string { return "" }
	first, err := worker.LoadConfig(empty)
	if err != nil {
		t.Fatal(err)
	}
	second, err := worker.LoadConfig(empty)
	if err != nil {
		t.Fatal(err)
	}
	if first.WorkerID == second.WorkerID || first.Poll != time.Second {
		t.Fatal("default workers must have unique identities and the existing poll interval")
	}
}

func TestInvalidPollingIntervalsAreRejected(t *testing.T) {
	for _, value := range []string{"0", "-1", "NaN", "+Inf", "1e50", "1e-50", "invalid"} {
		t.Run(value, func(t *testing.T) {
			_, err := worker.LoadConfig(func(key string) string {
				if key == "CIRCULAR_POLL_INTERVAL_SECONDS" {
					return value
				}
				return ""
			})
			if err == nil {
				t.Fatal("invalid polling duration was accepted")
			}
		})
	}
}

func TestWorkerIdentityValidation(t *testing.T) {
	for _, value := range []string{"", strings.Repeat("x", 201), "a\x00b", string([]byte{0xff})} {
		if worker.ValidateID(value) == nil {
			t.Fatal("invalid worker identity accepted")
		}
	}
	for _, value := range []string{"--recovery", "worker with spaces", strings.Repeat("é", 200)} {
		if err := worker.ValidateID(value); err != nil {
			t.Fatal(err)
		}
	}
}
