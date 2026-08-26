BEGIN;

ALTER TABLE system.idempotency_keys
    ADD COLUMN IF NOT EXISTS response_headers jsonb,
    ADD COLUMN IF NOT EXISTS response_bytes bytea;

COMMIT;
