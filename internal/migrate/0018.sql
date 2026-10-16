CREATE TABLE agent_delegation_targets (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    parent_agent_id UUID NOT NULL,
    target_agent_id UUID NOT NULL,
    PRIMARY KEY(parent_agent_id,target_agent_id),
    FOREIGN KEY(project_id,parent_agent_id) REFERENCES agents(project_id,id) ON DELETE CASCADE,
    FOREIGN KEY(project_id,target_agent_id) REFERENCES agents(project_id,id) ON DELETE CASCADE,
    CHECK(parent_agent_id <> target_agent_id)
);

ALTER TABLE delegations
    ADD COLUMN request_key VARCHAR(200),
    ADD COLUMN input_fingerprint TEXT,
    ADD COLUMN objective_json JSONB,
    ADD COLUMN checkpoint JSONB,
    ADD COLUMN result JSONB,
    ADD COLUMN error TEXT,
    ADD CONSTRAINT delegation_keyed_shape CHECK(request_key IS NULL OR (
        length(request_key)>0 AND input_fingerprint IS NOT NULL AND input_fingerprint ~ '^[0-9a-f]{64}$'
        AND objective_json IS NOT NULL AND jsonb_typeof(objective_json)='object'
        AND depth=1 AND status IN ('waiting','continuing','completed','cancelled','rejected')));
CREATE UNIQUE INDEX delegation_parent_once ON delegations(parent_run_id) WHERE request_key IS NOT NULL;
CREATE UNIQUE INDEX delegation_request_once ON delegations(parent_run_id,request_key) WHERE request_key IS NOT NULL;
CREATE UNIQUE INDEX delegation_child_once ON delegations(child_run_id) WHERE request_key IS NOT NULL AND child_run_id IS NOT NULL;

ALTER TABLE runs ADD COLUMN invocation INTEGER NOT NULL DEFAULT 1 CHECK(invocation IN (1,2));
CREATE TABLE run_invocations (
    run_id UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    number INTEGER NOT NULL CHECK(number IN (1,2)),
    input JSON NOT NULL CHECK(json_typeof(input)='object'),
    input_sha256 TEXT NOT NULL CHECK(input_sha256 ~ '^[0-9a-f]{64}$'),
    PRIMARY KEY(run_id,number)
);

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
    WHERE t.id=NEW.task_id AND repo.external_refs->'github'->>'repository_id' ~ '^[1-9][0-9]*$'
        AND repo.external_refs->'github'->>'installation_id' ~ '^[1-9][0-9]*$'
    ON CONFLICT(run_id) DO NOTHING;
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION queue_linear_run_update() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE linked RECORD;
BEGIN
    IF EXISTS(SELECT 1 FROM delegations WHERE child_run_id=NEW.id AND request_key IS NOT NULL) THEN RETURN NEW; END IF;
    IF NEW.kind <> 'coding' OR EXISTS(SELECT 1 FROM external_session_runs WHERE run_id=NEW.id) THEN RETURN NEW; END IF;
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

CREATE OR REPLACE FUNCTION queue_external_run_activity() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE request UUID;
BEGIN
    IF EXISTS(SELECT 1 FROM delegations WHERE child_run_id=NEW.id AND request_key IS NOT NULL) THEN RETURN NEW; END IF;
 IF NEW.status=OLD.status OR NEW.status NOT IN ('running','succeeded','failed','cancelled') THEN RETURN NEW; END IF;
 SELECT request_id INTO request FROM external_session_runs WHERE run_id=NEW.id;
 IF request IS NOT NULL AND NOT EXISTS(SELECT 1 FROM external_requests q WHERE q.id=request AND q.run_id=NEW.id) THEN PERFORM queue_native_linear_activity(request,'run:'||NEW.id::text||':'||NEW.status); END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION queue_native_linear_activity(request UUID, semantic TEXT) RETURNS void LANGUAGE sql AS $$
 INSERT INTO linear_agent_activities(id,request_id,identity_id,session_id,actor_id,semantic_key)
 SELECT gen_random_uuid(),q.id,q.identity_id,q.session_id,i.actor_id,semantic
 FROM external_requests q JOIN provider_identities i ON i.id=q.identity_id WHERE q.id=request
 AND NOT EXISTS(SELECT 1 FROM delegations d WHERE d.request_key IS NOT NULL AND
   (d.child_run_id=q.run_id OR ((semantic LIKE 'run:%' OR semantic LIKE 'pr:%') AND d.child_run_id::text=split_part(semantic,':',2))))
 ON CONFLICT(request_id,semantic_key) DO NOTHING
$$;
