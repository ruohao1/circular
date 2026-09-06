-- Original control-plane schema, retained verbatim in meaning for existing data.
CREATE TABLE projects (
    id UUID PRIMARY KEY, name VARCHAR(200) NOT NULL, description TEXT,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL
);
CREATE TABLE repositories (
    id UUID PRIMARY KEY, project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name VARCHAR(200) NOT NULL, clone_url TEXT NOT NULL, default_branch VARCHAR(200) NOT NULL,
    external_refs JSON NOT NULL, created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL, UNIQUE(project_id,name)
);
CREATE INDEX ix_repositories_project_id ON repositories(project_id);
CREATE TABLE agents (
    id UUID PRIMARY KEY, project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name VARCHAR(200) NOT NULL, backend VARCHAR(100) NOT NULL, instructions TEXT NOT NULL,
    backend_config JSON NOT NULL, enabled BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL, updated_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    UNIQUE(project_id,name)
);
CREATE INDEX ix_agents_project_id ON agents(project_id);
CREATE TABLE tasks (
    id UUID PRIMARY KEY, project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    repository_id UUID REFERENCES repositories(id) ON DELETE SET NULL,
    title VARCHAR(500) NOT NULL, description TEXT NOT NULL, status VARCHAR(50) NOT NULL,
    external_refs JSON NOT NULL, created_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT now() NOT NULL
);
CREATE INDEX ix_tasks_project_id ON tasks(project_id);
CREATE INDEX ix_tasks_repository_id ON tasks(repository_id);
CREATE INDEX ix_tasks_status ON tasks(status);
CREATE TABLE runs (
    id UUID PRIMARY KEY, task_id UUID NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    parent_run_id UUID REFERENCES runs(id) ON DELETE SET NULL,
    backend VARCHAR(100) NOT NULL, status VARCHAR(50) NOT NULL, attempt INTEGER NOT NULL,
    worker_id VARCHAR(200), claimed_at TIMESTAMPTZ, started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ, error TEXT, external_refs JSON NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL, updated_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    UNIQUE(task_id,attempt)
);
CREATE INDEX ix_runs_task_id ON runs(task_id);
CREATE INDEX ix_runs_agent_id ON runs(agent_id);
CREATE INDEX ix_runs_parent_run_id ON runs(parent_run_id);
CREATE INDEX ix_runs_queue ON runs(status,created_at);
CREATE TABLE workspaces (
    id UUID PRIMARY KEY, run_id UUID NOT NULL UNIQUE REFERENCES runs(id) ON DELETE CASCADE,
    worktree_path TEXT NOT NULL, container_id VARCHAR(200), status VARCHAR(50) NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL, updated_at TIMESTAMPTZ DEFAULT now() NOT NULL
);
CREATE TABLE events (
    position BIGSERIAL PRIMARY KEY, id UUID NOT NULL UNIQUE,
    run_id UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE, sequence BIGINT NOT NULL,
    type VARCHAR(100) NOT NULL, source VARCHAR(100) NOT NULL, data JSON NOT NULL, raw JSON,
    occurred_at TIMESTAMPTZ NOT NULL, recorded_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    UNIQUE(run_id,sequence)
);
CREATE INDEX ix_events_run_sequence ON events(run_id,sequence);
CREATE TABLE approvals (
    id UUID PRIMARY KEY, run_id UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    action VARCHAR(200) NOT NULL, reason TEXT NOT NULL, status VARCHAR(50) NOT NULL,
    requested_payload JSON NOT NULL, resolved_by VARCHAR(200), resolved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL, updated_at TIMESTAMPTZ DEFAULT now() NOT NULL
);
CREATE INDEX ix_approvals_run_id ON approvals(run_id);
CREATE TABLE artifacts (
    id UUID PRIMARY KEY, run_id UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    kind VARCHAR(100) NOT NULL, uri TEXT NOT NULL, metadata JSON NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL, updated_at TIMESTAMPTZ DEFAULT now() NOT NULL
);
CREATE INDEX ix_artifacts_run_id ON artifacts(run_id);
CREATE TABLE delegations (
    id UUID PRIMARY KEY, parent_run_id UUID NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    target_agent_id UUID NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    child_task_id UUID REFERENCES tasks(id), child_run_id UUID REFERENCES runs(id),
    objective TEXT NOT NULL, depth INTEGER NOT NULL, status VARCHAR(50) NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL, updated_at TIMESTAMPTZ DEFAULT now() NOT NULL
);
CREATE INDEX ix_delegations_parent_run_id ON delegations(parent_run_id);
CREATE TABLE integrations (
    id UUID PRIMARY KEY, project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    provider VARCHAR(100) NOT NULL, config JSON NOT NULL, enabled BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now() NOT NULL, updated_at TIMESTAMPTZ DEFAULT now() NOT NULL,
    UNIQUE(project_id,provider)
);
CREATE INDEX ix_integrations_project_id ON integrations(project_id);
