-- A launch intent survives MCP/client retries, including process restarts.
-- Null keys preserve the existing API's new-attempt behavior.
ALTER TABLE runs ADD COLUMN request_key VARCHAR(200);
CREATE UNIQUE INDEX ix_runs_request_key ON runs(task_id,request_key) WHERE request_key IS NOT NULL;
