-- Reception is passive until explicitly configured; never replay historical work.
CREATE TABLE integration_webhook_settings (
 provider TEXT NOT NULL CHECK(provider IN ('github','linear')),
 app_client_id TEXT NOT NULL,
 signing_keys BYTEA NOT NULL,
 public_origin TEXT NOT NULL DEFAULT '',
 previous_until TIMESTAMPTZ,
 last_verified_at TIMESTAMPTZ,
 configuration_status TEXT NOT NULL DEFAULT 'waiting' CHECK(configuration_status IN ('waiting','configured','needs_access','configuration_failed')),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(provider,app_client_id)
);
CREATE TABLE integration_webhook_deliveries (
 id UUID PRIMARY KEY,
 provider TEXT NOT NULL CHECK(provider IN ('github','linear')),
 app_client_id TEXT NOT NULL,
 delivery_id TEXT NOT NULL,
 event TEXT NOT NULL,
 body_sha256 BYTEA NOT NULL CHECK(octet_length(body_sha256)=32),
 payload BYTEA,
 received_at TIMESTAMPTZ NOT NULL,
 payload_expires_at TIMESTAMPTZ NOT NULL,
 status TEXT NOT NULL DEFAULT 'received' CHECK(status IN ('received','processing','processed','ignored','failed','expired')),
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 lease_owner UUID,
 lease_until TIMESTAMPTZ,
 last_error TEXT NOT NULL DEFAULT '',
 UNIQUE(provider,app_client_id,delivery_id)
);
CREATE INDEX integration_webhook_claim ON integration_webhook_deliveries(next_attempt_at,received_at) WHERE status IN ('received','processing');
