ALTER TABLE integrations ADD COLUMN credentials BYTEA;
ALTER TABLE integrations ADD COLUMN auth_generation BIGINT NOT NULL DEFAULT 0;

CREATE TABLE integration_oauth_states (
    state_hash TEXT PRIMARY KEY,
    browser_hash TEXT NOT NULL,
    integration_id UUID NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
    auth_generation BIGINT NOT NULL,
    verifier BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX ix_integration_oauth_states_expiry ON integration_oauth_states(expires_at);

CREATE UNIQUE INDEX ix_repositories_github_identity
    ON repositories(project_id, (external_refs->'github'->>'repository_id'))
    WHERE external_refs->'github'->>'repository_id' IS NOT NULL;
CREATE UNIQUE INDEX ix_tasks_linear_identity
    ON tasks(project_id, (external_refs->'linear'->>'issue_id'))
    WHERE external_refs->'linear'->>'issue_id' IS NOT NULL;
