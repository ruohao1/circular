package migrate_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/agents"
	"github.com/ruohao1/circular/internal/migrate"
	"github.com/ruohao1/circular/internal/testsupport"
)

func TestRevisionNineAddsCreationReceiptsWithoutChangingRepositories(t *testing.T) {
	pool := legacyDatabase(t, 9)
	project, repository := uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(t.Context(), `WITH p AS (INSERT INTO projects(id,name) VALUES($1,'Existing project') RETURNING id) INSERT INTO repositories(id,project_id,name,clone_url,default_branch,external_refs) SELECT $2,id,'owner/repo','https://github.com/owner/repo.git','main','{"github":{"repository_id":"123","installation_id":"456"}}' FROM p`, project, repository); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrate.Up(t.Context(), pool); err != nil {
			t.Fatal(err)
		}
	}
	var name, providerID string
	var receipts int
	if err := pool.QueryRow(t.Context(), `SELECT name,external_refs->'github'->>'repository_id',(SELECT count(*) FROM github_repository_creations) FROM repositories WHERE id=$1`, repository).Scan(&name, &providerID, &receipts); err != nil || name != "owner/repo" || providerID != "123" || receipts != 0 {
		t.Fatal("migration changed existing repository or invented creation history", err)
	}
}

func TestRevisionSevenAddsLaunchKeysWithoutChangingExistingRuns(t *testing.T) {
	pool := legacyDatabase(t, 7)
	project, agent, task, run := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err := pool.Exec(t.Context(), `WITH p AS (INSERT INTO projects(id,name) VALUES($1,'Existing project') RETURNING id),
		a AS (INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) SELECT $2,id,'Engineer','fake','Keep instructions','{}',true FROM p RETURNING id),
		t AS (INSERT INTO tasks(id,project_id,title,description,status,external_refs) SELECT $3,id,'Existing task','Keep description','open','{}' FROM p RETURNING id)
		INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs) SELECT $4,t.id,a.id,'fake','succeeded',1,'{"keep":true}' FROM t,a`, project, agent, task, run)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrate.Up(t.Context(), pool); err != nil {
			t.Fatal(err)
		}
	}
	var status string
	var key *string
	var keep bool
	if err := pool.QueryRow(t.Context(), `SELECT status,request_key,(external_refs->>'keep')::boolean FROM runs WHERE id=$1`, run).Scan(&status, &key, &keep); err != nil || key != nil || status != "succeeded" || !keep {
		t.Fatal("existing Run changed", err)
	}
}

func TestRevisionSixPreservesAcceptedProposalAndAgentSettings(t *testing.T) {
	pool := legacyDatabase(t, 6)
	project, agent, task, run, proposal := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err := pool.Exec(t.Context(), `WITH p AS (INSERT INTO projects(id,name) VALUES($1,'Existing proposal project') RETURNING id),
		a AS (INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) SELECT $2,id,'Engineer','codex','Keep instructions','{"model":"gpt-5.6-terra","reasoning_effort":"high"}',true FROM p RETURNING id),
		t AS (INSERT INTO tasks(id,project_id,title,description,status,external_refs) SELECT $3,id,'Discovery','Existing task','open','{}' FROM p RETURNING id),
		r AS (INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs) SELECT $4,t.id,a.id,'codex','succeeded',1,'{}' FROM t,a RETURNING id,agent_id)
		INSERT INTO agent_proposals(id,run_id,fingerprint,name,purpose,instructions,backend_config,status,agent_id)
		SELECT $5,r.id,'existing-fingerprint','Original suggestion','Existing purpose','Original instructions','{"model":"gpt-5.6-terra","reasoning_effort":"high"}','created',r.agent_id FROM r`, project, agent, task, run, proposal)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrate.Up(t.Context(), pool); err != nil {
			t.Fatal(err)
		}
	}
	var status, reason, fingerprint, model, effort, instructions string
	var accepted uuid.UUID
	if err := pool.QueryRow(t.Context(), `SELECT p.status,p.model_reason,p.fingerprint,p.agent_id,a.backend_config->>'model',a.backend_config->>'reasoning_effort',a.instructions FROM agent_proposals p JOIN agents a ON a.id=p.agent_id WHERE p.id=$1`, proposal).Scan(&status, &reason, &fingerprint, &accepted, &model, &effort, &instructions); err != nil || status != "created" || reason != "" || fingerprint != "existing-fingerprint" || accepted != agent || model != "gpt-5.6-terra" || effort != "high" || instructions != "Keep instructions" {
		t.Fatal("upgrade changed an accepted proposal or agent", err)
	}
}

func TestRevisionFiveAddsProposalStorageWithoutChangingAgents(t *testing.T) {
	pool := legacyDatabase(t, 5)
	project, agent := uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(t.Context(), `WITH project AS (INSERT INTO projects(id,name) VALUES($1,'Existing') RETURNING id) INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) SELECT $2,id,'Custom','codex','Keep my instructions','{"model":"gpt-5.6-sol"}',false FROM project`, project, agent); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrate.Up(t.Context(), pool); err != nil {
			t.Fatal(err)
		}
	}
	var instructions, model string
	var enabled bool
	if err := pool.QueryRow(t.Context(), `SELECT instructions,backend_config->>'model',enabled FROM agents WHERE id=$1`, agent).Scan(&instructions, &model, &enabled); err != nil || instructions != "Keep my instructions" || model != "gpt-5.6-sol" || enabled {
		t.Fatal("existing agent changed", err)
	}
	var proposals, runs int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM agent_proposals),(SELECT count(*) FROM runs)`).Scan(&proposals, &runs); err != nil || proposals != 0 || runs != 0 {
		t.Fatal("migration created proposals or runs", err)
	}
}

func TestRevisionFourAddsDiscoveryWithoutChangingExistingAgents(t *testing.T) {
	pool := legacyDatabase(t, 4)
	project, custom := uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Existing project')`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'Repository discovery','fake','Keep this role','{"delay_ms":25}',false)`, custom, project); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var id, instructions, backend string
	var enabled bool
	if err := pool.QueryRow(t.Context(), `SELECT id,instructions,backend,enabled FROM agents WHERE project_id=$1 AND preset=$2`, project, agents.DiscoveryPreset).Scan(&id, &instructions, &backend, &enabled); err != nil || instructions != agents.DiscoveryInstructions || backend != "codex" || !enabled {
		t.Fatal("existing project did not receive discovery Agent", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT instructions,backend,enabled FROM agents WHERE id=$1`, custom).Scan(&instructions, &backend, &enabled); err != nil || instructions != "Keep this role" || backend != "fake" || enabled {
		t.Fatal("migration changed existing custom Agent", err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE agents SET instructions='Customized discovery',enabled=false WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var count, tasks, runs int
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM agents WHERE project_id=$1),(SELECT count(*) FROM tasks),(SELECT count(*) FROM runs)`, project).Scan(&count, &tasks, &runs); err != nil || count != 3 || tasks != 0 || runs != 0 {
		t.Fatal("upgrade duplicated Agents or started work", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT instructions,enabled FROM agents WHERE id=$1`, id).Scan(&instructions, &enabled); err != nil || instructions != "Customized discovery" || enabled {
		t.Fatal("repeat upgrade reset the default Agent", err)
	}
}

func TestRevisionThreePreservesConnectionsAndPendingAuthorizations(t *testing.T) {
	pool := legacyDatabase(t, 3)
	project, connection := uuid.New(), uuid.New()
	credential := []byte("existing encrypted credential bytes")
	if _, err := pool.Exec(t.Context(), `WITH project AS (INSERT INTO projects(id,name) VALUES($1,'existing connected project') RETURNING id) INSERT INTO integrations(id,project_id,provider,config,enabled,credentials,auth_generation) SELECT $2,id,'github','{"status":"connected"}',true,$3,7 FROM project`, project, connection, credential); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO integration_oauth_states(state_hash,browser_hash,integration_id,auth_generation,verifier,expires_at) VALUES('state','browser',$1,7,$2,now()+interval '5 minutes')`, connection, []byte("encrypted verifier")); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	var generation int64
	var stored []byte
	var status, clientID string
	if err := pool.QueryRow(t.Context(), `SELECT enabled,auth_generation,credentials,config->>'status' FROM integrations WHERE id=$1`, connection).Scan(&enabled, &generation, &stored, &status); err != nil || !enabled || generation != 7 || !bytes.Equal(stored, credential) || status != "connected" {
		t.Fatal("existing connection changed", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT app_client_id FROM integration_oauth_states WHERE state_hash='state'`).Scan(&clientID); err != nil || clientID != "" {
		t.Fatal("legacy authorization was lost", err)
	}
}

func TestConcurrentFreshMigrationAndRepeatPreserveData(t *testing.T) {
	pool := testsupport.EmptyDatabase(t)
	done := make(chan error, 6)
	for range cap(done) {
		go func() { done <- migrate.Up(t.Context(), pool) }()
	}
	for range cap(done) {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), "INSERT INTO projects(id,name,description) VALUES($1,'preserved','unchanged')", id); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var name, description, version string
	if err := pool.QueryRow(t.Context(), "SELECT name,description FROM projects WHERE id=$1", id).Scan(&name, &description); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), "SELECT version_num FROM alembic_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if name != "preserved" || description != "unchanged" || version != migrate.Head {
		t.Fatal("repeat migration changed stored data")
	}
}

func TestExistingRevisionOneIsUpgradedInPlace(t *testing.T) {
	pool := legacyDatabase(t, 1)
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'legacy data')`, id); err != nil {
		t.Fatal(err)
	}
	// Return the schema to the exact pre-lease shape without dropping any data.
	if err := migrate.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var name string
	var columns int
	if err := pool.QueryRow(t.Context(), "SELECT name FROM projects WHERE id=$1", id).Scan(&name); err != nil || name != "legacy data" {
		t.Fatal("existing record lost", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='runs' AND column_name IN ('lease_expires_at','recovery_attempts')`).Scan(&columns); err != nil || columns != 2 {
		t.Fatal("lease columns not installed", err)
	}
}

func TestUnknownOrMultipleHeadsAreRejectedWithoutMutation(t *testing.T) {
	for _, versions := range [][]string{{"future"}, {""}, {"0001", "0002"}} {
		pool := testsupport.EmptyDatabase(t)
		if _, err := pool.Exec(t.Context(), "CREATE TABLE alembic_version(version_num varchar(32) PRIMARY KEY)"); err != nil {
			t.Fatal(err)
		}
		for _, v := range versions {
			if _, err := pool.Exec(t.Context(), "INSERT INTO alembic_version VALUES($1)", v); err != nil {
				t.Fatal(err)
			}
		}
		if err := migrate.Up(t.Context(), pool); !errors.Is(err, migrate.ErrVersion) {
			t.Fatalf("unsupported ledger accepted: %v", err)
		}
		var tables int
		if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema()").Scan(&tables); err != nil || tables != 1 {
			t.Fatal("unknown schema mutated", err)
		}
	}
}

func TestRevisionTwoPreservesIntegrationMetadata(t *testing.T) {
	pool := legacyDatabase(t, 2)
	project, connection := uuid.New(), uuid.New()
	if _, err := pool.Exec(t.Context(), `WITH project AS (INSERT INTO projects(id,name) VALUES($1,'existing') RETURNING id) INSERT INTO integrations(id,project_id,provider,config,enabled) SELECT $2,id,'github','{"legacy":true}',true FROM project`, project, connection); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var legacy bool
	var generation int64
	var credentials []byte
	if err := pool.QueryRow(t.Context(), `SELECT (config->>'legacy')::boolean,auth_generation,credentials FROM integrations WHERE id=$1`, connection).Scan(&legacy, &generation, &credentials); err != nil || !legacy || generation != 0 || credentials != nil {
		t.Fatal("migration changed existing metadata", err)
	}
}

func TestFailedDDLAndLedgerChangesRollbackTogether(t *testing.T) {
	pool := testsupport.EmptyDatabase(t)
	if _, err := pool.Exec(t.Context(), "CREATE TABLE agents(sentinel text); INSERT INTO agents VALUES('keep me')"); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(t.Context(), pool); err == nil {
		t.Fatal("schema collision was ignored")
	}
	var tables int
	var value string
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema()").Scan(&tables); err != nil || tables != 1 {
		t.Fatal("partial migration escaped rollback", err)
	}
	if err := pool.QueryRow(t.Context(), "SELECT sentinel FROM agents").Scan(&value); err != nil || value != "keep me" {
		t.Fatal("preexisting data was overwritten", err)
	}
}

func TestRevisionEightKeepsConnectionsReadOnlyAndDoesNotQueueOldRuns(t *testing.T) {
	pool := legacyDatabase(t, 8)
	project, agent, task, run := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, err := pool.Exec(t.Context(), `WITH p AS (INSERT INTO projects(id,name) VALUES($1,'Existing linked project') RETURNING id),
 a AS (INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) SELECT $2,id,'Engineer','fake','Keep','{}',true FROM p),
 t AS (INSERT INTO tasks(id,project_id,title,description,status,external_refs) SELECT $3,id,'Existing task','Keep','open','{"linear":{"issue_id":"20000000-0000-4000-8000-000000000003","url":"https://linear.app/fixture/issue/TST-1"}}' FROM p)
 INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs) VALUES($4,$3,$2,'fake','succeeded',1,'{}')`, project, agent, task, run)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO integrations(id,project_id,provider,config,enabled,credentials) VALUES($1,$2,'linear','{"status":"connected","account_id":"20000000-0000-4000-8000-000000000004"}',true,$3)`, uuid.NewString(), project, []byte("keep encrypted token"))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrate.Up(t.Context(), pool); err != nil {
			t.Fatal(err)
		}
	}
	var status string
	var scopes []string
	var credential []byte
	var updates, settings int
	if err := pool.QueryRow(t.Context(), `SELECT i.granted_scopes,i.credentials,r.status,(SELECT count(*) FROM linear_run_updates),(SELECT count(*) FROM linear_run_update_settings) FROM integrations i JOIN tasks t ON t.project_id=i.project_id JOIN runs r ON r.task_id=t.id WHERE i.project_id=$1`, project).Scan(&scopes, &credential, &status, &updates, &settings); err != nil || len(scopes) != 0 || string(credential) != "keep encrypted token" || status != "succeeded" || updates != 0 || settings != 0 {
		t.Fatal("migration changed authorization or queued history", err)
	}
}

func TestDeliveryMigrationFrom0010PreservesProjectsAndDefaultsOff(t *testing.T) {
	pool := legacyDatabase(t, 10)
	project := uuid.NewString()
	if _, err := pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Preserved before delivery')`, project); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var name string
	var settings int
	if err := pool.QueryRow(t.Context(), `SELECT name,(SELECT count(*) FROM github_run_delivery_settings) FROM projects WHERE id=$1`, project).Scan(&name, &settings); err != nil || name != "Preserved before delivery" || settings != 0 {
		t.Fatal(name, settings, err)
	}
}
