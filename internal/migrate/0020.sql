-- Stop old writers and drain ownership and execution cleanup before upgrading.
LOCK TABLE runs, workspaces, work_sessions IN SHARE ROW EXCLUSIVE MODE;
DO $$
BEGIN
    IF EXISTS(SELECT 1 FROM work_sessions WHERE released_at IS NULL) OR EXISTS(
        SELECT 1 FROM runs r WHERE r.kind='coding' AND (
            r.status NOT IN ('succeeded','failed','cancelled') OR r.worker_id IS NOT NULL
            OR r.lease_expires_at IS NOT NULL
            OR EXISTS(SELECT 1 FROM workspaces w WHERE w.run_id=r.id AND w.status<>'released')
        )
    ) THEN
        RAISE EXCEPTION 'Stop old writers, release external ownership and drain coding Run/Workspace cleanup before upgrading';
    END IF;
END $$;

ALTER TABLE repositories ADD COLUMN completion_check_config JSONB
    CHECK(completion_check_config IS NULL OR (
        jsonb_typeof(completion_check_config)='object'
        AND (completion_check_config->>'revision')::bigint BETWEEN 1 AND 9007199254740991
        AND jsonb_typeof(completion_check_config->'checks')='array'
        AND jsonb_array_length(completion_check_config->'checks') BETWEEN 1 AND 50
    ));
ALTER TABLE work_sessions ADD CONSTRAINT work_sessions_task_id_id_key UNIQUE(task_id,id);
ALTER TABLE tasks
    ADD COLUMN completion_policy JSONB CHECK(completion_policy IS NULL OR (
        jsonb_typeof(completion_policy)='object'
        AND completion_policy->>'mode' IN ('review_required','direct_completion')
        AND jsonb_typeof(completion_policy->'requirements')='array'
        AND jsonb_array_length(completion_policy->'requirements')<=20
    )),
    ADD COLUMN completion_contract JSONB CHECK(completion_contract IS NULL OR (
        jsonb_typeof(completion_contract)='object'
        AND completion_policy IS NOT NULL AND completion_contract->'policy'=completion_policy
        AND completion_contract->>'fingerprint' ~ '^[a-f0-9]{64}$'
    )),
    ADD COLUMN completion_session_id UUID,
    ADD CONSTRAINT tasks_completion_session_scope FOREIGN KEY(id,completion_session_id)
        REFERENCES work_sessions(task_id,id) DEFERRABLE INITIALLY DEFERRED;

CREATE FUNCTION preserve_task_completion_contract() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.completion_contract IS NOT NULL AND (
        NEW.completion_contract IS DISTINCT FROM OLD.completion_contract
        OR NEW.completion_policy IS DISTINCT FROM OLD.completion_policy
    ) THEN
        RAISE EXCEPTION 'Completion contract is frozen; plan a new Task to change it' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER tasks_completion_contract_immutable BEFORE UPDATE OF completion_policy,completion_contract ON tasks
    FOR EACH ROW EXECUTE FUNCTION preserve_task_completion_contract();

CREATE TABLE task_completions (
    session_id UUID PRIMARY KEY,
    task_id UUID NOT NULL,
    report JSONB CHECK(report IS NULL OR (jsonb_typeof(report)='object' AND octet_length(report::text)<=131072)),
    report_invocation INTEGER CHECK(report_invocation>0),
    reconciliation_pending BOOLEAN NOT NULL DEFAULT false,
    decision JSONB CHECK(decision IS NULL OR jsonb_typeof(decision)='object'),
    applied_at TIMESTAMPTZ CHECK(isfinite(applied_at)),
    FOREIGN KEY(task_id,session_id) REFERENCES work_sessions(task_id,id) ON DELETE CASCADE
);
CREATE INDEX task_completions_pending ON task_completions(task_id) WHERE reconciliation_pending;
CREATE FUNCTION preserve_completion_report() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.session_id<>OLD.session_id OR NEW.task_id<>OLD.task_id OR
       (OLD.report IS NOT NULL AND (NEW.report IS DISTINCT FROM OLD.report OR NEW.report_invocation IS DISTINCT FROM OLD.report_invocation)) THEN
        RAISE EXCEPTION 'Accepted completion report is immutable' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER task_completion_report_immutable BEFORE UPDATE ON task_completions
    FOR EACH ROW EXECUTE FUNCTION preserve_completion_report();
