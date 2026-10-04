ALTER TABLE runs ADD COLUMN kind TEXT NOT NULL DEFAULT 'coding'
    CHECK (kind IN ('coding','pr_review'));
CREATE TABLE pr_review_settings (
    project_id UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    automatic BOOLEAN NOT NULL DEFAULT false,
    reviewer_id UUID REFERENCES agents(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE pr_reviews (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    repository_id UUID NOT NULL REFERENCES repositories(id) ON DELETE RESTRICT,
    source_run_id UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    run_id UUID NOT NULL UNIQUE REFERENCES runs(id) ON DELETE CASCADE,
    reviewer_id UUID NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    previous_review_id UUID REFERENCES pr_reviews(id) ON DELETE SET NULL,
    identity_key TEXT NOT NULL,
    attempt INTEGER NOT NULL CHECK (attempt > 0),
    automatic BOOLEAN NOT NULL DEFAULT false,
    snapshot JSONB NOT NULL,
    context JSONB,
    context_sha256 TEXT NOT NULL DEFAULT '',
    candidate_report JSONB,
    report_sha256 TEXT NOT NULL DEFAULT '',
    report_artifact_id UUID REFERENCES artifacts(id) ON DELETE SET NULL,
    assessment TEXT NOT NULL DEFAULT 'pending'
        CHECK (assessment IN ('pending','findings','no_blocking_findings','incomplete')),
    report_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(identity_key,attempt),
    CHECK (run_id <> source_run_id)
);
CREATE INDEX ix_pr_reviews_source ON pr_reviews(source_run_id,created_at DESC,id);
CREATE TABLE pr_review_launch_intents (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source_run_id UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    request_key UUID NOT NULL,
    parameters JSONB NOT NULL,
    parameters_sha256 TEXT NOT NULL,
    automatic BOOLEAN NOT NULL DEFAULT false,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','launched','cancelled','failed')),
    review_id UUID REFERENCES pr_reviews(id) ON DELETE SET NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_owner UUID,
    lease_until TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(project_id,source_run_id,request_key)
);
CREATE UNIQUE INDEX ux_pr_review_automatic_intent ON pr_review_launch_intents(source_run_id)
    WHERE automatic;
CREATE INDEX ix_pr_review_launch_due ON pr_review_launch_intents(next_attempt_at)
    WHERE status='pending';
CREATE TABLE pr_review_publications (
    review_id UUID PRIMARY KEY REFERENCES pr_reviews(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','published','retrying','uncertain','skipped','failed')),
    marker TEXT NOT NULL UNIQUE,
    expected_author_id TEXT NOT NULL DEFAULT '',
    started BOOLEAN NOT NULL DEFAULT false,
    github_review_id TEXT NOT NULL DEFAULT '',
    github_review_url TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_owner UUID,
    lease_until TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE pr_review_freshness (
    repository_id UUID NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    github_repository_id TEXT NOT NULL,
    pull_request_number INTEGER NOT NULL CHECK (pull_request_number > 0),
    observed_base_sha TEXT NOT NULL DEFAULT '',
    observed_head_sha TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'unknown'
        CHECK (state IN ('unknown','open','closed','merged','unavailable')),
    checked_at TIMESTAMPTZ,
    next_check_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_owner UUID,
    lease_until TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(repository_id,github_repository_id,pull_request_number)
);
ALTER TABLE linear_run_updates DROP CONSTRAINT linear_run_updates_phase_check;
ALTER TABLE linear_run_updates ADD CONSTRAINT linear_run_updates_phase_check
    CHECK (phase IN ('running','terminal','pull_request','pr_review'));

-- Deferred so the launcher can create both records atomically. A purpose cannot
-- be forged by inserting a run alone, attaching a review to coding execution,
-- or deleting only the review record while its run survives.
CREATE FUNCTION enforce_pr_review_identity() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target UUID; previous UUID;
BEGIN
    IF TG_TABLE_NAME='runs' THEN
        target=NEW.id;
    ELSIF TG_OP='DELETE' THEN
        target=OLD.run_id;
    ELSE
        target=NEW.run_id;
        IF TG_OP='UPDATE' THEN previous=OLD.run_id; END IF;
    END IF;
    IF EXISTS (
        SELECT 1 FROM runs r WHERE (r.id=target OR r.id=previous) AND r.kind='pr_review'
        AND NOT EXISTS (
            SELECT 1 FROM pr_reviews p JOIN runs source ON source.id=p.source_run_id
            JOIN tasks t ON t.id=r.task_id JOIN agents a ON a.id=r.agent_id
            JOIN repositories repo ON repo.id=p.repository_id
            WHERE p.run_id=r.id AND source.kind='coding' AND source.task_id=r.task_id
                AND p.reviewer_id=r.agent_id AND source.agent_id<>r.agent_id
                AND t.project_id=p.project_id AND a.project_id=p.project_id
                AND repo.project_id=p.project_id AND t.repository_id=repo.id
        )
    ) OR EXISTS (SELECT 1 FROM pr_reviews p JOIN runs r ON r.id=p.run_id
        WHERE (r.id=target OR r.id=previous) AND r.kind<>'pr_review') THEN
        RAISE EXCEPTION 'PR review execution requires its matching source and review identity'
            USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER enforce_pr_review_run AFTER INSERT OR UPDATE ON runs
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION enforce_pr_review_identity();
CREATE CONSTRAINT TRIGGER enforce_pr_review_record AFTER INSERT OR UPDATE OR DELETE ON pr_reviews
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION enforce_pr_review_identity();

CREATE OR REPLACE FUNCTION queue_github_run_delivery() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.kind <> 'coding' THEN RETURN NEW; END IF;
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
