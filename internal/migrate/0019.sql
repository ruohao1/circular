-- Old writers must be stopped after draining coding execution and cleanup.
LOCK TABLE runs, workspaces IN SHARE ROW EXCLUSIVE MODE;
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM runs r WHERE r.kind='coding' AND (
            r.status NOT IN ('succeeded','failed','cancelled')
            OR r.worker_id IS NOT NULL OR r.lease_expires_at IS NOT NULL
            OR EXISTS(SELECT 1 FROM workspaces w WHERE w.run_id=r.id AND w.status<>'released')
        )
    ) THEN
        RAISE EXCEPTION 'Drain coding Runs and finish worker/Workspace cleanup before upgrading; stop old writers and retry';
    END IF;
END $$;

ALTER TABLE tasks
    ADD CONSTRAINT tasks_project_id_id_key UNIQUE(project_id,id),
    ADD COLUMN parent_task_id UUID,
    ADD CONSTRAINT tasks_parent_scope FOREIGN KEY(project_id,parent_task_id)
        REFERENCES tasks(project_id,id) DEFERRABLE INITIALLY DEFERRED,
    ADD CONSTRAINT tasks_parent_not_self CHECK(parent_task_id<>id);
CREATE INDEX tasks_parent ON tasks(parent_task_id) WHERE parent_task_id IS NOT NULL;
CREATE FUNCTION preserve_task_parent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.parent_task_id IS DISTINCT FROM OLD.parent_task_id THEN
        RAISE EXCEPTION 'Task parent is immutable' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER tasks_parent_immutable BEFORE UPDATE OF parent_task_id ON tasks
    FOR EACH ROW EXECUTE FUNCTION preserve_task_parent();

CREATE TABLE task_dependencies (
    project_id UUID NOT NULL,
    task_id UUID NOT NULL,
    prerequisite_task_id UUID NOT NULL,
    PRIMARY KEY(task_id,prerequisite_task_id),
    FOREIGN KEY(project_id,task_id) REFERENCES tasks(project_id,id) ON DELETE CASCADE,
    FOREIGN KEY(project_id,prerequisite_task_id) REFERENCES tasks(project_id,id) ON DELETE CASCADE,
    CHECK(task_id<>prerequisite_task_id)
);
CREATE INDEX task_dependencies_prerequisite ON task_dependencies(prerequisite_task_id);

ALTER TABLE runs ADD CONSTRAINT runs_task_id_id_kind_key UNIQUE(task_id,id,kind);
CREATE TABLE work_sessions (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL,
    task_id UUID NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('external','native')),
    run_id UUID UNIQUE,
    run_kind TEXT GENERATED ALWAYS AS (CASE WHEN kind='native' THEN 'coding' END) STORED,
    secret_digest BYTEA,
    admission_digest BYTEA NOT NULL CHECK(octet_length(admission_digest)=32),
    client_name VARCHAR(80) NOT NULL CHECK(length(btrim(client_name))>0),
    client_version VARCHAR(80) NOT NULL DEFAULT '',
    admitted_revision BIGINT NOT NULL CHECK(admitted_revision BETWEEN 1 AND 9007199254740991),
    admitted_prerequisites JSONB NOT NULL CHECK(jsonb_typeof(admitted_prerequisites)='array'),
    claimed_at TIMESTAMPTZ NOT NULL CHECK(isfinite(claimed_at)),
    started_at TIMESTAMPTZ CHECK(isfinite(started_at)),
    last_contact_at TIMESTAMPTZ CHECK(isfinite(last_contact_at) AND last_contact_at>=claimed_at),
    heartbeat_sequence BIGINT NOT NULL DEFAULT 0 CHECK(heartbeat_sequence BETWEEN 0 AND 9007199254740991),
    outcome TEXT CHECK(outcome IN ('succeeded','failed','cancelled')),
    finished_at TIMESTAMPTZ CHECK(isfinite(finished_at) AND finished_at>=claimed_at),
    released_at TIMESTAMPTZ CHECK(isfinite(released_at) AND released_at>=claimed_at),
    release_reason TEXT NOT NULL DEFAULT '',
    needs_user_resolution BOOLEAN NOT NULL DEFAULT false,
    FOREIGN KEY(project_id,task_id) REFERENCES tasks(project_id,id) ON DELETE CASCADE,
    FOREIGN KEY(task_id,run_id,run_kind) REFERENCES runs(task_id,id,kind) ON DELETE CASCADE,
    CHECK((kind='external' AND run_id IS NULL AND secret_digest IS NOT NULL AND octet_length(secret_digest)=32)
        OR (kind='native' AND run_id IS NOT NULL AND secret_digest IS NULL)),
    CHECK((outcome IS NULL)=(finished_at IS NULL)),
    CHECK((released_at IS NULL)=(release_reason='')),
    CHECK(kind='native' OR NOT needs_user_resolution)
);
CREATE UNIQUE INDEX work_sessions_current_task ON work_sessions(task_id) WHERE released_at IS NULL;
CREATE INDEX work_sessions_task_history ON work_sessions(task_id,claimed_at,id);

CREATE TABLE work_session_receipts (
    id UUID PRIMARY KEY,
    session_id UUID NOT NULL REFERENCES work_sessions(id) ON DELETE CASCADE,
    operation_id UUID NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('claimed','finished','released','reassigned','cancellation_requested')),
    input_digest BYTEA NOT NULL CHECK(octet_length(input_digest)=32),
    actor TEXT NOT NULL CHECK(actor IN ('external','worker','user','system')),
    reason TEXT NOT NULL DEFAULT '',
    at TIMESTAMPTZ NOT NULL CHECK(isfinite(at)),
    replacement_session_id UUID REFERENCES work_sessions(id),
    UNIQUE(session_id,operation_id)
);
