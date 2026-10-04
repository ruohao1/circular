-- A receipt is recorded before the external create request. Replaying a key
-- never repeats an ambiguous POST, including after process or network failure.
CREATE TABLE github_repository_creations (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    request_key TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    installation_id TEXT NOT NULL,
    owner TEXT NOT NULL,
    name TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('submitted','created','attached','rejected')),
    error_code TEXT NOT NULL DEFAULT '',
    provider_repository JSONB,
    repository_id UUID REFERENCES repositories(id) ON DELETE SET NULL,
    management_url TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY(project_id,request_key)
);
