CREATE TABLE approval_oauth_states (
    state_hash TEXT PRIMARY KEY,
    browser_hash TEXT NOT NULL,
    verifier BYTEA NOT NULL,
    app_client_id TEXT NOT NULL,
    return_path TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE approval_sessions (
    token_hash TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    login TEXT NOT NULL,
    app_client_id TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX approval_oauth_states_expiry ON approval_oauth_states(expires_at);
CREATE INDEX approval_sessions_expiry ON approval_sessions(expires_at);
