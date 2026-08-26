BEGIN;

ALTER TABLE automation.runs
    ADD COLUMN IF NOT EXISTS idempotency_key text,
    ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz,
    ADD COLUMN IF NOT EXISTS cancel_requested boolean NOT NULL DEFAULT false;

ALTER TABLE automation.jobs
    ADD COLUMN IF NOT EXISTS idempotency_key text,
    ADD COLUMN IF NOT EXISTS request_hash text;

CREATE UNIQUE INDEX IF NOT EXISTS automation_jobs_idempotency_idx
    ON automation.jobs (organization_id, workspace_id, created_by, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS automation_runs_idempotency_idx
    ON automation.runs (organization_id, workspace_id, created_by, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

ALTER TABLE automation.attempts
    ADD COLUMN IF NOT EXISTS claimed_by text,
    ADD COLUMN IF NOT EXISTS lease_expires_at timestamptz,
    ADD COLUMN IF NOT EXISTS next_retry_at timestamptz,
    ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();

CREATE INDEX IF NOT EXISTS automation_attempts_lease_idx
    ON automation.attempts (status, lease_expires_at)
    WHERE status = 'started';

CREATE INDEX IF NOT EXISTS automation_runs_due_idx
    ON automation.runs (status, next_attempt_at, created_at)
    WHERE status = 'queued';

COMMIT;
