-- Repository publication is a separate, durable opt-in from agent execution.
CREATE TABLE github_run_delivery_settings (
    project_id UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    enabled BOOLEAN NOT NULL DEFAULT false,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE github_run_deliveries (
    run_id UUID PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    repository_id UUID NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    github_repository_id TEXT NOT NULL,
    installation_id TEXT NOT NULL,
    repository_name TEXT NOT NULL,
    base_branch TEXT NOT NULL,
    branch TEXT NOT NULL,
    title TEXT NOT NULL,
    automatic BOOLEAN NOT NULL DEFAULT false,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','delivered','failed','uncertain','no_changes')),
    base_commit TEXT NOT NULL DEFAULT '',
    commit_sha TEXT NOT NULL DEFAULT '',
    pull_request_started BOOLEAN NOT NULL DEFAULT false,
    pull_request_url TEXT NOT NULL DEFAULT '',
    pull_request_number INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ix_github_run_deliveries_due ON github_run_deliveries(next_attempt_at,created_at) WHERE status='pending';
CREATE FUNCTION queue_github_run_delivery() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status=OLD.status OR NEW.status<>'succeeded' THEN RETURN NEW; END IF;
    INSERT INTO github_run_deliveries(run_id,project_id,repository_id,github_repository_id,installation_id,repository_name,base_branch,branch,title,automatic)
    SELECT NEW.id,t.project_id,repo.id,repo.external_refs->'github'->>'repository_id',
        repo.external_refs->'github'->>'installation_id',repo.name,repo.default_branch,'circular/run/'||NEW.id::text,t.title,true
    FROM tasks t JOIN repositories repo ON repo.id=t.repository_id AND repo.project_id=t.project_id
    JOIN github_run_delivery_settings settings ON settings.project_id=t.project_id AND settings.enabled
    WHERE t.id=NEW.task_id AND repo.external_refs->'github'->>'repository_id' ~ '^[1-9][0-9]*$'
        AND repo.external_refs->'github'->>'installation_id' ~ '^[1-9][0-9]*$'
    ON CONFLICT(run_id) DO NOTHING;
    RETURN NEW;
END $$;
CREATE TRIGGER queue_github_run_delivery AFTER UPDATE OF status ON runs FOR EACH ROW EXECUTE FUNCTION queue_github_run_delivery();
ALTER TABLE linear_run_updates DROP CONSTRAINT linear_run_updates_phase_check;
ALTER TABLE linear_run_updates ADD CONSTRAINT linear_run_updates_phase_check CHECK (phase IN ('running','terminal','pull_request'));
