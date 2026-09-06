ALTER TABLE runs ADD COLUMN lease_expires_at TIMESTAMPTZ;
ALTER TABLE runs ADD COLUMN recovery_attempts INTEGER DEFAULT '0' NOT NULL;
CREATE INDEX ix_runs_lease_expires_at ON runs(lease_expires_at);
