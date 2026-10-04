CREATE TABLE agent_proposals (
    id UUID PRIMARY KEY,
    run_id UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    fingerprint VARCHAR(64) NOT NULL,
    name VARCHAR(200) NOT NULL,
    purpose TEXT NOT NULL,
    instructions TEXT NOT NULL,
    backend_config JSON NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','created','dismissed')),
    agent_id UUID REFERENCES agents(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(run_id,fingerprint),
    CHECK ((status='created') = (agent_id IS NOT NULL))
);
CREATE INDEX ix_agent_proposals_run ON agent_proposals(run_id,created_at,id);
