package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/backends"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/delegations"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/runstate"
)

func sealInvocation(input ProvisioningContext) ([]byte, string, error) {
	if input.Invocation == 2 {
		if input.Kind != runstate.Coding || input.SourceCheckpoint != nil || len(input.DelegationTargets) > 0 || !prreviews.DigestPattern.MatchString(input.DelegationContextSHA256) {
			return nil, "", ErrResourceConflict
		}
	} else if input.DelegationContextSHA256 != "" {
		return nil, "", ErrResourceConflict
	}
	if input.SourceCheckpoint != nil {
		if input.SourceCheckpoint.Validate(input.SourceRunID) != nil || input.BaseRef != input.SourceCheckpoint.SourceCommit || len(input.DelegationTargets) != 0 {
			return nil, "", ErrResourceConflict
		}
	} else if input.SourceRunID != uuid.Nil {
		return nil, "", ErrResourceConflict
	}
	if input.RunID == uuid.Nil || input.WorkspaceID != WorkspaceID(input.RunID) || input.RepositoryID == uuid.Nil || input.Invocation < 1 || input.Invocation > 2 || input.CloneURL == "" || input.BaseRef == "" || delegations.ValidateTargets(input.DelegationTargets) != nil {
		return nil, "", ErrResourceConflict
	}
	if input.Kind == "" {
		input.Kind = runstate.Coding
	}
	if input.Kind != runstate.Coding && input.Kind != runstate.PRReview {
		return nil, "", ErrResourceConflict
	}
	if input.CompletionContract != nil && (input.Kind != runstate.Coding || completion.ValidateContract(*input.CompletionContract) != nil) {
		return nil, "", ErrResourceConflict
	}
	if input.Backend == "codex" {
		settings, err := backends.ValidateCodexConfig(input.BackendConfig)
		if err != nil {
			return nil, "", err
		}
		input.BackendConfig, _ = json.Marshal(settings)
	} else if input.Backend == "fake" {
		// Validate before normalization discards duplicate keys or number syntax.
		if _, err := (backends.Fake{}).Prepare(backends.Input{Config: input.BackendConfig, Kind: input.Kind, CompletionContract: input.CompletionContract}); err != nil {
			return nil, "", err
		}
	} else {
		return nil, "", ErrResourceConflict
	}
	var config any
	if json.Unmarshal(input.BackendConfig, &config) != nil {
		return nil, "", ErrResourceConflict
	}
	input.BackendConfig, _ = json.Marshal(config)
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > 16*1024*1024 {
		return nil, "", ErrResourceConflict
	}
	digest := sha256.Sum256(raw)
	return raw, hex.EncodeToString(digest[:]), nil
}

// SaveInvocationInput seals resolved public inputs, never workload stdin or credentials.
func SaveInvocationInput(ctx context.Context, tx pgx.Tx, input ProvisioningContext) error {
	raw, digest, err := sealInvocation(input)
	if err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `INSERT INTO run_invocations(run_id,number,input,input_sha256) VALUES($1,$2,$3,$4) ON CONFLICT(run_id,number) DO NOTHING`, input.RunID, input.Invocation, raw, digest)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 1 {
		return nil
	}
	var stored string
	if err = tx.QueryRow(ctx, `SELECT input_sha256 FROM run_invocations WHERE run_id=$1 AND number=$2`, input.RunID, input.Invocation).Scan(&stored); err != nil {
		return err
	}
	if stored != digest {
		return ErrResourceConflict
	}
	return nil
}

func readInvocation(ctx context.Context, tx pgx.Tx, id uuid.UUID, number int) (ProvisioningContext, error) {
	var raw []byte
	var digest string
	var input ProvisioningContext
	if err := tx.QueryRow(ctx, `SELECT input,input_sha256 FROM run_invocations WHERE run_id=$1 AND number=$2`, id, number).Scan(&raw, &digest); err != nil {
		return input, err
	}
	if len(raw) > 16*1024*1024 || json.Unmarshal(raw, &input) != nil {
		return input, ErrResourceConflict
	}
	_, expected, err := sealInvocation(input)
	if err != nil || expected != digest || input.RunID != id || input.Invocation != number {
		return input, ErrResourceConflict
	}
	return input, nil
}

func (r *RunResources) InvocationInput() (ProvisioningContext, error) {
	if err := r.guard(); err != nil {
		return ProvisioningContext{}, err
	}
	input, err := readInvocation(r.ctx, r.tx, r.id, r.invocation)
	if err == nil {
		if input.Backend != r.backend {
			return input, ErrResourceConflict
		}
		return input, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) || r.invocation != 1 {
		return input, err
	}
	input, err = r.ProvisioningContext()
	if err != nil {
		return input, err
	}
	input.Invocation = r.invocation
	if input.Kind == runstate.Coding {
		var agent uuid.UUID
		var child bool
		if err = r.tx.QueryRow(r.ctx, `SELECT agent_id,EXISTS(SELECT 1 FROM delegations WHERE child_run_id=runs.id AND request_key IS NOT NULL) FROM runs WHERE id=$1`, r.id).Scan(&agent, &child); err != nil {
			return input, err
		}
		if !child {
			input.DelegationTargets, err = DelegationTargets(r.ctx, r.tx, agent)
			if err != nil {
				return input, err
			}
		}
	}
	if err = SaveInvocationInput(r.ctx, r.tx, input); err != nil {
		return input, err
	}
	return readInvocation(r.ctx, r.tx, r.id, r.invocation)
}
