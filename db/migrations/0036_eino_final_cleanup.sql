BEGIN;

ALTER TABLE automation.runs
    ADD COLUMN IF NOT EXISTS agent_task_id uuid;

ALTER TABLE automation.runs
    DROP CONSTRAINT IF EXISTS runs_agent_task_fk;

ALTER TABLE automation.runs
    ADD CONSTRAINT runs_agent_task_fk
    FOREIGN KEY (agent_task_id) REFERENCES integration.agent_tasks(id);

CREATE INDEX IF NOT EXISTS automation_runs_agent_task_idx
    ON automation.runs (organization_id, agent_task_id, created_at DESC)
    WHERE agent_task_id IS NOT NULL;

DROP TABLE IF EXISTS integration.agent_task_runs;

ALTER TABLE integration.agent_applications
    DROP COLUMN IF EXISTS provider,
    DROP COLUMN IF EXISTS provider_key;

COMMIT;
