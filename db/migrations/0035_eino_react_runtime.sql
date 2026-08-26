BEGIN;

ALTER TABLE integration.agent_applications
    ADD COLUMN IF NOT EXISTS instruction text NOT NULL DEFAULT '';

ALTER TABLE automation.runs
    ADD COLUMN IF NOT EXISTS output_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS tool_call_count integer NOT NULL DEFAULT 0 CHECK (tool_call_count >= 0),
    ADD COLUMN IF NOT EXISTS input_tokens integer NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    ADD COLUMN IF NOT EXISTS output_tokens integer NOT NULL DEFAULT 0 CHECK (output_tokens >= 0);

ALTER TABLE automation.interactions
    ADD COLUMN IF NOT EXISTS interrupt_id text,
    ADD COLUMN IF NOT EXISTS display_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS resume_schema jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS resume_consumed_at timestamptz;

CREATE UNIQUE INDEX IF NOT EXISTS automation_interactions_interrupt_idx
    ON automation.interactions (run_id, interrupt_id)
    WHERE interrupt_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS integration.agent_tool_calls (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    run_id uuid NOT NULL REFERENCES automation.runs(id) ON DELETE CASCADE,
    session_id uuid REFERENCES integration.agent_sessions(id),
    tool_call_id text NOT NULL,
    tool_name text NOT NULL,
    arguments_summary jsonb NOT NULL DEFAULT '{}'::jsonb,
    result_summary jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL CHECK (status IN ('started', 'succeeded', 'failed', 'interrupted', 'rejected')),
    duration_ms bigint,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (run_id, tool_call_id)
);

CREATE INDEX IF NOT EXISTS agent_tool_calls_run_idx
    ON integration.agent_tool_calls (organization_id, run_id, created_at);

COMMIT;
