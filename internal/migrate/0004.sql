CREATE TABLE integration_apps (
    provider TEXT PRIMARY KEY CHECK (provider IN ('github','linear')),
    credentials BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE integration_app_states (
    state_hash TEXT PRIMARY KEY,
    browser_hash TEXT NOT NULL,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX ix_integration_app_states_expiry ON integration_app_states(expires_at);

ALTER TABLE integration_oauth_states ADD COLUMN app_client_id TEXT NOT NULL DEFAULT '';
