ALTER TABLE pr_reviews ALTER COLUMN source_run_id DROP NOT NULL;
ALTER TABLE pr_review_launch_intents ALTER COLUMN source_run_id DROP NOT NULL;
ALTER TABLE pr_reviews ADD COLUMN source_work_session_id UUID REFERENCES work_sessions(id) ON DELETE RESTRICT,
    ADD CONSTRAINT pr_review_one_source CHECK ((source_run_id IS NULL) <> (source_work_session_id IS NULL));
ALTER TABLE pr_review_launch_intents ADD COLUMN source_work_session_id UUID REFERENCES work_sessions(id) ON DELETE RESTRICT,
    ADD CONSTRAINT pr_review_intent_one_source CHECK ((source_run_id IS NULL) <> (source_work_session_id IS NULL)),
    ADD CONSTRAINT pr_review_external_manual CHECK (source_work_session_id IS NULL OR NOT automatic);
CREATE INDEX pr_reviews_work_source ON pr_reviews(source_work_session_id,created_at DESC,id);
CREATE UNIQUE INDEX pr_review_external_request ON pr_review_launch_intents(source_work_session_id,request_key) WHERE source_work_session_id IS NOT NULL;
ALTER TABLE task_completions ADD COLUMN latest_review_id UUID REFERENCES pr_reviews(id) ON DELETE SET NULL,
    ADD COLUMN review_revision BIGINT NOT NULL DEFAULT 0 CHECK (review_revision BETWEEN 0 AND 9007199254740991);

CREATE TABLE task_completion_evidence (
    session_id UUID PRIMARY KEY REFERENCES task_completions(session_id) ON DELETE CASCADE,
    pull_request_number INTEGER CHECK(pull_request_number>0),
    binding_revision BIGINT NOT NULL DEFAULT 0 CHECK(binding_revision BETWEEN 0 AND 9007199254740991),
    observation JSONB CHECK(observation IS NULL OR jsonb_typeof(observation)='object'),
    accepted_observation JSONB CHECK(accepted_observation IS NULL OR jsonb_typeof(accepted_observation)='object'),
    lease_owner UUID,
    lease_until TIMESTAMPTZ,
    next_check_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK((lease_owner IS NULL)=(lease_until IS NULL))
);
INSERT INTO task_completion_evidence(session_id) SELECT session_id FROM task_completions;
CREATE VIEW task_completion_pull_requests AS
SELECT c.session_id,COALESCE(e.pull_request_number,(c.report->>'pull_request_number')::integer,
    CASE WHEN d.status='delivered' AND d.repository_id=t.repository_id
        AND d.github_repository_id=repo.external_refs->'github'->>'repository_id'
        AND d.installation_id=repo.external_refs->'github'->>'installation_id'
        THEN NULLIF(d.pull_request_number,0) END) AS number,e.binding_revision
FROM task_completions c JOIN task_completion_evidence e ON e.session_id=c.session_id
JOIN work_sessions w ON w.id=c.session_id JOIN tasks t ON t.id=c.task_id
LEFT JOIN repositories repo ON repo.id=t.repository_id
LEFT JOIN github_run_deliveries d ON d.run_id=w.run_id;

CREATE OR REPLACE FUNCTION enforce_pr_review_identity() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target UUID; previous UUID;
BEGIN
    IF TG_TABLE_NAME='runs' THEN target=NEW.id;
    ELSIF TG_OP='DELETE' THEN target=OLD.run_id;
    ELSE target=NEW.run_id; IF TG_OP='UPDATE' THEN previous=OLD.run_id; END IF;
    END IF;
    IF EXISTS (
        SELECT 1 FROM runs r WHERE (r.id=target OR r.id=previous) AND r.kind='pr_review'
        AND NOT EXISTS (
            SELECT 1 FROM pr_reviews p JOIN tasks t ON t.id=r.task_id JOIN agents a ON a.id=r.agent_id
            JOIN repositories repo ON repo.id=p.repository_id
            LEFT JOIN runs source ON source.id=p.source_run_id
            LEFT JOIN work_sessions w ON w.id=p.source_work_session_id
            WHERE p.run_id=r.id AND p.reviewer_id=r.agent_id AND t.project_id=p.project_id
                AND a.project_id=p.project_id AND repo.project_id=p.project_id AND t.repository_id=repo.id
                AND ((p.source_run_id IS NOT NULL AND source.kind='coding' AND source.task_id=r.task_id AND source.agent_id<>r.agent_id)
                  OR (p.source_work_session_id IS NOT NULL AND w.kind='external' AND w.run_id IS NULL
                    AND w.task_id=r.task_id AND r.parent_run_id IS NULL
                    AND p.snapshot->>'version'='2' AND p.snapshot->'source'->>'work_session_id'=w.id::text))
        )
    ) OR EXISTS(SELECT 1 FROM pr_reviews p JOIN runs r ON r.id=p.run_id WHERE (r.id=target OR r.id=previous) AND r.kind<>'pr_review') THEN
        RAISE EXCEPTION 'PR review execution requires its matching source and review identity' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END $$;

CREATE FUNCTION enforce_external_review_intent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.source_work_session_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM work_sessions w JOIN tasks t ON t.id=w.task_id
        WHERE w.id=NEW.source_work_session_id AND w.kind='external' AND w.run_id IS NULL AND t.project_id=NEW.project_id
        AND (NEW.review_id IS NULL OR EXISTS(SELECT 1 FROM pr_reviews p WHERE p.id=NEW.review_id AND p.source_work_session_id=w.id AND p.project_id=t.project_id))
    ) THEN RAISE EXCEPTION 'Review intent requires its matching external source' USING ERRCODE='23514'; END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER external_review_intent_scope AFTER INSERT OR UPDATE ON pr_review_launch_intents
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION enforce_external_review_intent();

CREATE OR REPLACE FUNCTION link_review_session() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.source_run_id IS NOT NULL THEN
        INSERT INTO external_session_runs(run_id,request_id) SELECT NEW.run_id,request_id FROM external_session_runs WHERE run_id=NEW.source_run_id ON CONFLICT DO NOTHING;
    END IF;
    RETURN NEW;
END $$;

-- Frozen completion contracts require an explicit publication request. Existing deliveries remain recoverable.
CREATE OR REPLACE FUNCTION queue_github_run_delivery() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS(SELECT 1 FROM delegations WHERE child_run_id=NEW.id AND request_key IS NOT NULL) THEN RETURN NEW; END IF;
    IF NEW.kind <> 'coding' THEN RETURN NEW; END IF;
    IF NEW.status=OLD.status OR NEW.status<>'succeeded' THEN RETURN NEW; END IF;
    INSERT INTO github_run_deliveries(run_id,project_id,repository_id,github_repository_id,installation_id,repository_name,base_branch,branch,title,automatic)
    SELECT NEW.id,t.project_id,repo.id,repo.external_refs->'github'->>'repository_id',
        repo.external_refs->'github'->>'installation_id',repo.name,repo.default_branch,'circular/run/'||NEW.id::text,t.title,true
    FROM tasks t JOIN repositories repo ON repo.id=t.repository_id AND repo.project_id=t.project_id
    JOIN github_run_delivery_settings settings ON settings.project_id=t.project_id AND settings.enabled
    WHERE t.id=NEW.task_id AND t.completion_contract IS NULL AND repo.external_refs->'github'->>'repository_id' ~ '^[1-9][0-9]*$'
        AND repo.external_refs->'github'->>'installation_id' ~ '^[1-9][0-9]*$'
    ON CONFLICT(run_id) DO NOTHING;
    RETURN NEW;
END $$;
