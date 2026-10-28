ALTER TABLE delegations
 ADD COLUMN proposal_id UUID UNIQUE REFERENCES task_proposals(id) ON DELETE RESTRICT,
 ADD COLUMN execution_intent JSONB,
 DROP CONSTRAINT delegation_keyed_shape,
 ADD CONSTRAINT delegation_keyed_shape CHECK(request_key IS NULL OR (
  length(request_key)>0 AND input_fingerprint IS NOT NULL AND input_fingerprint ~ '^[0-9a-f]{64}$'
  AND objective_json IS NOT NULL AND jsonb_typeof(objective_json)='object'
  AND depth=1 AND status IN ('awaiting_approval','waiting','continuing','completed','cancelled','rejected')));
