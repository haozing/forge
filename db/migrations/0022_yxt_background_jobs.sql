BEGIN;

ALTER TABLE asset.import_batches
    ADD COLUMN IF NOT EXISTS idempotency_key text,
    ADD COLUMN IF NOT EXISTS source_name text NOT NULL DEFAULT '';

ALTER TABLE asset.export_jobs
    ADD COLUMN IF NOT EXISTS idempotency_key text,
    ADD COLUMN IF NOT EXISTS output_content_type text,
    ADD COLUMN IF NOT EXISTS output_size bigint,
    ADD COLUMN IF NOT EXISTS output_checksum text;

CREATE UNIQUE INDEX IF NOT EXISTS import_batches_idempotency_idx
    ON asset.import_batches (organization_id, submitted_by, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS export_jobs_idempotency_idx
    ON asset.export_jobs (organization_id, submitted_by, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

ALTER TABLE automation.runs
    ADD COLUMN IF NOT EXISTS input_scope jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE INDEX IF NOT EXISTS resource_model_migrations_due_idx
    ON model.resource_model_migrations (status, created_at, id)
    WHERE status = 'queued';

COMMIT;
