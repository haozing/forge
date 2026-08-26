BEGIN;

ALTER TABLE automation.runs
    ADD COLUMN IF NOT EXISTS output_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE INDEX IF NOT EXISTS automation_runs_callback_credential_idx
    ON automation.runs ((input_scope->>'_run_credential_hash'))
    WHERE input_scope ? '_run_credential_hash';

COMMIT;
