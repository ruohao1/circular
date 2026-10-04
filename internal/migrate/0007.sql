-- Reasons explain proposed model settings without changing existing proposals.
ALTER TABLE agent_proposals ADD COLUMN model_reason TEXT NOT NULL DEFAULT '';
