-- Existing projects stay in user mode until a binding is explicitly created.
CREATE TABLE provider_identities (
    id UUID PRIMARY KEY,
    provider TEXT NOT NULL CHECK(provider IN ('github','linear')),
    app_client_id TEXT NOT NULL,
    account_id TEXT NOT NULL,
    account_name TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    actor_name TEXT NOT NULL,
    actor_login TEXT NOT NULL DEFAULT '',
    avatar_url TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK(status IN ('available','needs_access','reconnect_required')),
    enabled BOOLEAN NOT NULL DEFAULT true,
    credentials BYTEA,
    granted_scopes TEXT[] NOT NULL DEFAULT '{}',
    generation BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(provider,app_client_id,account_id),
    UNIQUE(id,provider)
);
CREATE TABLE integration_identity_bindings (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    identity_id UUID NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(project_id,provider),
    FOREIGN KEY(identity_id,provider) REFERENCES provider_identities(id,provider)
);

CREATE TABLE identity_authorization_generations (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    provider TEXT NOT NULL CHECK(provider='linear'),
    generation BIGINT NOT NULL DEFAULT 1,
    PRIMARY KEY(project_id,provider)
);
CREATE TABLE identity_oauth_states (
    state_hash TEXT PRIMARY KEY,
    browser_hash TEXT NOT NULL,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    provider TEXT NOT NULL CHECK(provider='linear'),
    purpose TEXT NOT NULL CHECK(purpose IN ('identity','agent')),
    generation BIGINT NOT NULL,
    app_client_id TEXT NOT NULL,
    expected_workspace TEXT NOT NULL DEFAULT '',
    verifier BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX ix_identity_oauth_states_expiry ON identity_oauth_states(expires_at);

ALTER TABLE github_run_deliveries ADD COLUMN publisher JSONB;
ALTER TABLE github_run_deliveries ADD COLUMN legacy_reconcile BOOLEAN NOT NULL DEFAULT false;
UPDATE github_run_deliveries SET legacy_reconcile=true WHERE status IN ('pending','uncertain','failed');
ALTER TABLE pr_review_publications ADD COLUMN publisher JSONB;
ALTER TABLE linear_run_updates ADD COLUMN publisher JSONB;
ALTER TABLE linear_run_updates ADD COLUMN started BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE linear_run_updates ADD COLUMN legacy_reconcile BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE linear_run_updates ADD COLUMN lease_owner UUID;
ALTER TABLE linear_run_updates ADD COLUMN lease_until TIMESTAMPTZ;
-- Old workers called Linear inside a transaction; zero attempts is not proof
-- that nothing reached the provider. Recovery must inspect the immutable UUID.
UPDATE linear_run_updates SET legacy_reconcile=true WHERE status IN ('pending','failed');
ALTER TABLE linear_run_updates DROP CONSTRAINT linear_run_updates_status_check;
ALTER TABLE linear_run_updates ADD CONSTRAINT linear_run_updates_status_check
    CHECK(status IN ('pending','delivered','failed','skipped','uncertain'));

CREATE VIEW linear_connection_state AS
SELECT b.project_id, b.enabled AND i.enabled AS enabled, i.account_id,
    COALESCE(old.config->>'account_url','') AS account_url,
    CASE WHEN i.status='available' THEN 'connected' ELSE i.status END AS status,
    i.granted_scopes, b.enabled AND i.enabled AND i.status='available' AND i.credentials IS NOT NULL
        AND i.granted_scopes && ARRAY['comments:create','write'] AS authorized
FROM integration_identity_bindings b JOIN provider_identities i ON i.id=b.identity_id
LEFT JOIN integrations old ON old.project_id=b.project_id AND old.provider='linear'
WHERE b.provider='linear'
UNION ALL
SELECT i.project_id,i.enabled,COALESCE(i.config->>'account_id',''),COALESCE(i.config->>'account_url',''),
    COALESCE(i.config->>'status',''),i.granted_scopes,
    i.enabled AND i.credentials IS NOT NULL AND i.config->>'status'='connected'
        AND i.granted_scopes && ARRAY['comments:create','write','admin']
FROM integrations i WHERE i.provider='linear' AND NOT EXISTS(
    SELECT 1 FROM integration_identity_bindings b WHERE b.project_id=i.project_id AND b.provider='linear');

CREATE OR REPLACE FUNCTION queue_linear_run_update() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE linked RECORD;
BEGIN
    IF NEW.kind <> 'coding' THEN RETURN NEW; END IF;
    IF NEW.status=OLD.status OR NEW.status NOT IN ('running','succeeded','failed','cancelled') THEN
        RETURN NEW;
    END IF;
    SELECT t.project_id,t.external_refs->'linear'->>'issue_id' AS issue_id,
        t.external_refs->'linear'->>'url' AS issue_url,
        t.external_refs->'linear'->>'account_id' AS imported_account,
        i.account_id AS account_id,i.account_url AS account_url
    INTO linked FROM tasks t
    JOIN linear_run_update_settings settings ON settings.project_id=t.project_id AND settings.enabled
    JOIN linear_connection_state i ON i.project_id=t.project_id AND i.enabled
    WHERE t.id=NEW.task_id;
    IF NOT FOUND OR linked.account_id IS NULL OR linked.account_id='' OR linked.issue_id IS NULL
        OR linked.issue_id !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        OR linked.issue_url IS NULL OR linked.issue_url NOT LIKE 'https://linear.app/%' THEN
        RETURN NEW;
    END IF;
    -- Older imports have no stored organization. Their original workspace URL
    -- must match; delivery also verifies access to the exact issue before posting.
    IF (linked.imported_account IS NOT NULL AND linked.imported_account<>linked.account_id)
       OR (linked.imported_account IS NULL AND (linked.account_url IS NULL OR
           left(linked.issue_url,length(linked.account_url)+1)<>linked.account_url||'/')) THEN
        RETURN NEW;
    END IF;
    INSERT INTO linear_run_updates(project_id,run_id,phase,outcome,account_id,issue_id,issue_url,summary)
    VALUES(linked.project_id,NEW.id,CASE WHEN NEW.status='running' THEN 'running' ELSE 'terminal' END,
        NEW.status,linked.account_id,linked.issue_id::uuid,linked.issue_url,
        CASE WHEN NEW.status='running' THEN '' ELSE COALESCE((SELECT left(data->>'content',12000)
            FROM events WHERE run_id=NEW.id AND type='agent.message.completed' ORDER BY sequence DESC LIMIT 1),'') END)
    ON CONFLICT(run_id,phase) DO NOTHING;
    RETURN NEW;
END $$;
