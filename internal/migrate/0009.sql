-- Delivery is opt-in per project. Existing credentials remain read-only until
-- a fresh authorization supplies the targeted comment grant.
ALTER TABLE integrations ADD COLUMN granted_scopes TEXT[] NOT NULL DEFAULT '{}';
CREATE TABLE linear_run_update_settings (
    project_id UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    enabled BOOLEAN NOT NULL DEFAULT false,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE linear_run_updates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    run_id UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    phase TEXT NOT NULL CHECK (phase IN ('running','terminal')),
    outcome TEXT NOT NULL,
    account_id TEXT NOT NULL,
    issue_id UUID NOT NULL,
    issue_url TEXT NOT NULL,
    summary TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','delivered','failed','skipped')),
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error TEXT NOT NULL DEFAULT '',
    delivered_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(run_id,phase)
);
CREATE INDEX ix_linear_run_updates_due ON linear_run_updates(next_attempt_at,created_at) WHERE status='pending';
CREATE INDEX ix_linear_run_updates_project ON linear_run_updates(project_id,created_at);
CREATE FUNCTION queue_linear_run_update() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE linked RECORD;
BEGIN
    IF NEW.status=OLD.status OR NEW.status NOT IN ('running','succeeded','failed','cancelled') THEN
        RETURN NEW;
    END IF;
    SELECT t.project_id,t.external_refs->'linear'->>'issue_id' AS issue_id,
        t.external_refs->'linear'->>'url' AS issue_url,
        t.external_refs->'linear'->>'account_id' AS imported_account,
        i.config->>'account_id' AS account_id,i.config->>'account_url' AS account_url
    INTO linked FROM tasks t
    JOIN linear_run_update_settings settings ON settings.project_id=t.project_id AND settings.enabled
    JOIN integrations i ON i.project_id=t.project_id AND i.provider='linear' AND i.enabled
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
CREATE TRIGGER queue_linear_run_update AFTER UPDATE OF status ON runs
    FOR EACH ROW EXECUTE FUNCTION queue_linear_run_update();
