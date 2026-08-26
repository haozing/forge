BEGIN;

ALTER TABLE automation.runs ADD COLUMN IF NOT EXISTS error_summary text;
ALTER TABLE automation.runs DROP CONSTRAINT IF EXISTS runs_status_check;
UPDATE automation.runs SET status = 'canceled' WHERE status = 'cancelled';
ALTER TABLE automation.runs
    ADD CONSTRAINT runs_status_check
    CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancel_requested', 'canceled', 'paused'));

ALTER TABLE automation.attempts DROP CONSTRAINT IF EXISTS attempts_status_check;
UPDATE automation.attempts SET status = 'canceled' WHERE status = 'cancelled';
ALTER TABLE automation.attempts
    ADD CONSTRAINT attempts_status_check
    CHECK (status IN ('started', 'succeeded', 'failed', 'canceled'));

COMMIT;
