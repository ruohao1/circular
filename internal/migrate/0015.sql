-- Native Linear requests are opt-in. This migration schedules no existing work.
CREATE TABLE linear_request_routes (
 id UUID PRIMARY KEY,
 identity_id UUID NOT NULL REFERENCES provider_identities(id) ON DELETE CASCADE,
 scope_type TEXT NOT NULL CHECK(scope_type IN ('project','team')),
 scope_id UUID NOT NULL,
 scope_name TEXT NOT NULL DEFAULT '',
 project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 repository_id UUID NOT NULL REFERENCES repositories(id) ON DELETE RESTRICT,
 agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
 mode TEXT NOT NULL CHECK(mode IN ('automatic','approval')),
 enabled BOOLEAN NOT NULL DEFAULT false,
 generation BIGINT NOT NULL DEFAULT 1,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(identity_id,scope_type,scope_id)
);
CREATE TABLE external_requests (
 id UUID PRIMARY KEY,
 identity_id UUID NOT NULL REFERENCES provider_identities(id) ON DELETE CASCADE,
 session_id UUID NOT NULL,
 issue_id UUID,
 source_url TEXT NOT NULL DEFAULT '', requester_id TEXT NOT NULL DEFAULT '', requester_name TEXT NOT NULL DEFAULT '',
 project_id UUID REFERENCES projects(id) ON DELETE RESTRICT,
 repository_id UUID REFERENCES repositories(id) ON DELETE RESTRICT,
 agent_id UUID REFERENCES agents(id) ON DELETE RESTRICT,
 route_id UUID REFERENCES linear_request_routes(id) ON DELETE RESTRICT,
 route_generation BIGINT NOT NULL DEFAULT 0,
 identity_generation BIGINT NOT NULL DEFAULT 0,
 run_id UUID UNIQUE REFERENCES runs(id) ON DELETE RESTRICT,
 status TEXT NOT NULL DEFAULT 'needs_routing' CHECK(status IN ('needs_routing','awaiting_approval','ready','waiting_for_active_run','queued','running','succeeded','failed','stopped','unsupported','rejected','needs_access')),
 reason TEXT NOT NULL DEFAULT '',
 title TEXT NOT NULL DEFAULT '', prompt TEXT NOT NULL DEFAULT '' CHECK(octet_length(prompt)<=131072),
 source JSONB NOT NULL DEFAULT '{}',
 prepared JSONB,
 input_fingerprint TEXT NOT NULL,
 stopped_at TIMESTAMPTZ,
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(identity_id,session_id)
);
CREATE UNIQUE INDEX external_requests_active_issue ON external_requests(identity_id,issue_id) WHERE status IN ('queued','running');
CREATE INDEX external_requests_project ON external_requests(project_id,created_at DESC,id);
CREATE INDEX external_requests_ready ON external_requests(next_attempt_at,created_at,id) WHERE status='ready';
CREATE TABLE external_request_messages (
 id UUID PRIMARY KEY, request_id UUID NOT NULL REFERENCES external_requests(id) ON DELETE CASCADE,
 activity_id UUID NOT NULL, body TEXT NOT NULL CHECK(octet_length(body)<=131072), created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(request_id,activity_id)
);
CREATE TABLE external_run_inputs (
 run_id UUID PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
 request_id UUID UNIQUE NOT NULL REFERENCES external_requests(id) ON DELETE CASCADE,
 snapshot JSONB NOT NULL, fingerprint TEXT NOT NULL
);
CREATE TABLE external_session_runs (
 run_id UUID PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
 request_id UUID NOT NULL REFERENCES external_requests(id) ON DELETE CASCADE
);
CREATE TABLE linear_agent_activities (
 id UUID PRIMARY KEY,
 request_id UUID NOT NULL REFERENCES external_requests(id) ON DELETE CASCADE,
 identity_id UUID NOT NULL REFERENCES provider_identities(id) ON DELETE CASCADE,
 session_id UUID NOT NULL, actor_id TEXT NOT NULL,
 semantic_key TEXT NOT NULL,
 content JSONB,
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','delivered','uncertain','failed','cancelled')),
 started BOOLEAN NOT NULL DEFAULT false,
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 lease_owner UUID, lease_until TIMESTAMPTZ,
 receipt_id TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(request_id,semantic_key)
);
CREATE INDEX linear_agent_activity_claim ON linear_agent_activities(next_attempt_at,created_at) WHERE status IN ('pending','uncertain');

CREATE FUNCTION queue_native_linear_activity(request UUID, semantic TEXT) RETURNS void LANGUAGE sql AS $$
 INSERT INTO linear_agent_activities(id,request_id,identity_id,session_id,actor_id,semantic_key)
 SELECT gen_random_uuid(),q.id,q.identity_id,q.session_id,i.actor_id,semantic
 FROM external_requests q JOIN provider_identities i ON i.id=q.identity_id WHERE q.id=request
 ON CONFLICT(request_id,semantic_key) DO NOTHING
$$;
CREATE FUNCTION queue_external_request_activity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN PERFORM queue_native_linear_activity(NEW.id,'ack');
 ELSIF NEW.status<>OLD.status THEN PERFORM queue_native_linear_activity(NEW.id,'request:'||NEW.status);
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER external_request_activity AFTER INSERT OR UPDATE OF status ON external_requests FOR EACH ROW EXECUTE FUNCTION queue_external_request_activity();
CREATE FUNCTION link_review_session() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO external_session_runs(run_id,request_id) SELECT NEW.run_id,request_id FROM external_session_runs WHERE run_id=NEW.source_run_id ON CONFLICT DO NOTHING;
 RETURN NEW;
END $$;
CREATE TRIGGER pr_review_session AFTER INSERT ON pr_reviews FOR EACH ROW EXECUTE FUNCTION link_review_session();
CREATE FUNCTION queue_external_run_activity() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE request UUID;
BEGIN
 IF NEW.status=OLD.status OR NEW.status NOT IN ('running','succeeded','failed','cancelled') THEN RETURN NEW; END IF;
 SELECT request_id INTO request FROM external_session_runs WHERE run_id=NEW.id;
 IF request IS NOT NULL AND NOT EXISTS(SELECT 1 FROM external_requests q WHERE q.id=request AND q.run_id=NEW.id) THEN PERFORM queue_native_linear_activity(request,'run:'||NEW.id::text||':'||NEW.status); END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER external_run_activity AFTER UPDATE OF status ON runs FOR EACH ROW EXECUTE FUNCTION queue_external_run_activity();

CREATE OR REPLACE FUNCTION queue_linear_run_update() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE linked RECORD;
BEGIN
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

CREATE TABLE external_effect_reservations (
 id UUID PRIMARY KEY, request_id UUID NOT NULL REFERENCES external_requests(id) ON DELETE CASCADE,
 run_id UUID REFERENCES runs(id) ON DELETE CASCADE, effect TEXT NOT NULL,
 reserved_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX external_effect_reservations_run ON external_effect_reservations(run_id,effect);
ALTER TABLE integration_webhook_deliveries ADD COLUMN priority INTEGER NOT NULL DEFAULT 10;
