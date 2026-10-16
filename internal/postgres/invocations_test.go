package postgres_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/backends"
	"github.com/ruohao1/circular/internal/postgres"
)

func TestDelegationInvocationSnapshot(t *testing.T) {
	pool := database(t)
	run := seed(t, pool, 1)[0]
	acquire(t, postgres.NewQueue(pool), "snapshot-owner")
	input := postgres.ProvisioningContext{RunID: run, WorkspaceID: postgres.WorkspaceID(run), RepositoryID: uuid.New(), Invocation: 1, CloneURL: "/fixture", BaseRef: "main", Backend: "codex", TaskTitle: "Task", Instructions: "Original instructions", BackendConfig: json.RawMessage(`{"model":"gpt-5.6-terra","reasoning_effort":"high"}`)}
	// The durable Run selects the backend, even if the Agent changes later.
	if _, err := pool.Exec(t.Context(), `UPDATE runs SET backend='codex' WHERE id=$1`, run); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = postgres.SaveInvocationInput(t.Context(), tx, input); err != nil {
			t.Fatal(err)
		}
	}
	changed := input
	changed.Instructions = "changed"
	if err = postgres.SaveInvocationInput(t.Context(), tx, changed); !errors.Is(err, postgres.ErrResourceConflict) {
		t.Fatal("changed immutable input", err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE agents SET instructions='Mutated',backend_config='{}' WHERE id=(SELECT agent_id FROM runs WHERE id=$1)`, run); err != nil {
		t.Fatal(err)
	}
	store, err := postgres.NewResources(pool, "snapshot-owner")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.WithRun(t.Context(), run, func(r *postgres.RunResources) error {
		frozen, err := r.InvocationInput()
		if err != nil {
			return err
		}
		if frozen.Instructions != input.Instructions || !bytes.Equal(frozen.BackendConfig, input.BackendConfig) {
			t.Fatalf("mutable input: %+v", frozen)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDelegationInvocationValidatesFakeConfig(t *testing.T) {
	pool := database(t)
	run := seed(t, pool, 1)[0]
	request := `{"agent_id":"11111111-1111-1111-1111-111111111111","request_key":"inspect","objective":{"title":"Inspect","description":"Check the source"}}`
	valid := `{"delegation_request":` + request + `}`
	for _, test := range []struct {
		name, config string
		invalid      bool
	}{
		{"request key repeated", strings.Replace(valid, `"request_key":"inspect"`, `"request_key":"first","request_key":"inspect"`, 1), true},
		{"request repeated", `{"delegation_request":` + request + `,"delegation_request":` + request + `}`, true},
		{"escaped objective key repeated", strings.Replace(valid, `"title":"Inspect"`, `"title":"First","t\u0069tle":"Inspect"`, 1), true},
		{"fractional delay", `{"delay_ms":1.0}`, true},
		{"valid request", valid, false},
		{"defaults", `{}`, false},
		{"null", `null`, false},
		{"unknown option", `{"future_option":true}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := json.RawMessage(test.config)
			if _, err := (backends.Fake{}).Prepare(backends.Input{Config: config}); (err != nil) != test.invalid {
				t.Fatalf("direct preparation error = %v, invalid = %v", err, test.invalid)
			}
			tx, err := pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(t.Context())
			input := postgres.ProvisioningContext{RunID: run, WorkspaceID: postgres.WorkspaceID(run), RepositoryID: uuid.New(), Invocation: 1, CloneURL: "/fixture", BaseRef: "main", Backend: "fake", BackendConfig: config}
			if err := postgres.SaveInvocationInput(t.Context(), tx, input); (err != nil) != test.invalid {
				t.Fatalf("sealed input error = %v, invalid = %v", err, test.invalid)
			}
		})
	}
}
