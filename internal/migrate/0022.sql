ALTER TABLE tasks ADD COLUMN approval_context JSONB NOT NULL DEFAULT '{}';

CREATE TABLE task_proposals (
    id UUID PRIMARY KEY,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    request_key TEXT NOT NULL,
    input_digest TEXT NOT NULL,
    target_task_id UUID REFERENCES tasks(id) DEFERRABLE INITIALLY DEFERRED,
    task_id UUID REFERENCES tasks(id) DEFERRABLE INITIALLY DEFERRED,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision BETWEEN 1 AND 9007199254740991),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected')),
    proposer TEXT NOT NULL DEFAULT '',
    source JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(project_id,request_key)
);
CREATE TABLE task_proposal_revisions (
    proposal_id UUID NOT NULL REFERENCES task_proposals(id) ON DELETE CASCADE,
    revision BIGINT NOT NULL,
    expected_task_revision BIGINT NOT NULL DEFAULT 0,
    draft JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(proposal_id,revision)
);
CREATE TABLE task_proposal_decisions (
    proposal_id UUID NOT NULL,
    revision BIGINT NOT NULL,
    decision TEXT NOT NULL CHECK (decision IN ('approved','rejected')),
    approver_id TEXT NOT NULL,
    approver_login TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    task_id UUID REFERENCES tasks(id) DEFERRABLE INITIALLY DEFERRED,
    approved_scope JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(proposal_id,revision),
    FOREIGN KEY(proposal_id,revision) REFERENCES task_proposal_revisions(proposal_id,revision) ON DELETE CASCADE
);
CREATE INDEX task_proposals_project_status ON task_proposals(project_id,status,created_at,id);
CREATE INDEX task_proposal_decisions_task ON task_proposal_decisions(task_id) WHERE decision='approved';
