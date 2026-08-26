BEGIN;

ALTER TABLE automation.runs
    ADD COLUMN IF NOT EXISTS runtime_mode text,
    ADD COLUMN IF NOT EXISTS workflow_key text,
    ADD COLUMN IF NOT EXISTS workflow_code_version bigint,
    ADD COLUMN IF NOT EXISTS principal_id uuid,
    ADD COLUMN IF NOT EXISTS agent_user_id uuid,
    ADD COLUMN IF NOT EXISTS agent_application_id uuid,
    ADD COLUMN IF NOT EXISTS session_id uuid,
    ADD COLUMN IF NOT EXISTS model_endpoint_id uuid,
    ADD COLUMN IF NOT EXISTS model_endpoint_revision bigint,
    ADD COLUMN IF NOT EXISTS current_node text,
    ADD COLUMN IF NOT EXISTS input_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS execution_options jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS input_checksum text,
    ADD COLUMN IF NOT EXISTS policy_revision bigint,
    ADD COLUMN IF NOT EXISTS eino_checkpoint_id uuid,
    ADD COLUMN IF NOT EXISTS checkpoint_sequence bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS waiting_interaction_id uuid,
    ADD COLUMN IF NOT EXISTS error_summary text;

ALTER TABLE automation.runs DROP CONSTRAINT IF EXISTS runs_source_check;
ALTER TABLE automation.runs
    ADD CONSTRAINT runs_source_check
    CHECK (source IN ('automation', 'manual', 'agent', 'chat'));

ALTER TABLE automation.runs DROP CONSTRAINT IF EXISTS runs_status_check;
ALTER TABLE automation.runs
    ADD CONSTRAINT runs_status_check
    CHECK (status IN ('queued', 'running', 'waiting_input', 'waiting_approval', 'retrying',
                      'succeeded', 'failed', 'cancel_requested', 'canceled', 'expired', 'paused'));

ALTER TABLE automation.runs
    DROP CONSTRAINT IF EXISTS runs_runtime_mode_check,
    DROP CONSTRAINT IF EXISTS runs_principal_fk,
    DROP CONSTRAINT IF EXISTS runs_agent_user_fk,
    DROP CONSTRAINT IF EXISTS runs_agent_application_fk,
    DROP CONSTRAINT IF EXISTS runs_session_fk,
    DROP CONSTRAINT IF EXISTS runs_model_endpoint_fk;

ALTER TABLE automation.runs
    ADD CONSTRAINT runs_runtime_mode_check
    CHECK (runtime_mode IS NULL OR runtime_mode IN ('react', 'workflow'));

ALTER TABLE automation.runs
    ADD CONSTRAINT runs_principal_fk
    FOREIGN KEY (principal_id) REFERENCES identity.users(id),
    ADD CONSTRAINT runs_agent_user_fk
    FOREIGN KEY (agent_user_id) REFERENCES identity.users(id),
    ADD CONSTRAINT runs_agent_application_fk
    FOREIGN KEY (agent_application_id) REFERENCES integration.agent_applications(id),
    ADD CONSTRAINT runs_session_fk
    FOREIGN KEY (session_id) REFERENCES integration.agent_sessions(id),
    ADD CONSTRAINT runs_model_endpoint_fk
    FOREIGN KEY (organization_id, model_endpoint_id)
    REFERENCES integration.model_endpoints(organization_id, id);

ALTER TABLE automation.runs
    DROP CONSTRAINT IF EXISTS runs_model_revision_pair_check,
    DROP CONSTRAINT IF EXISTS runs_model_revision_fk;

ALTER TABLE automation.runs
    ADD CONSTRAINT runs_model_revision_pair_check
    CHECK ((model_endpoint_id IS NULL AND model_endpoint_revision IS NULL)
        OR (model_endpoint_id IS NOT NULL AND model_endpoint_revision IS NOT NULL AND model_endpoint_revision > 0)),
    ADD CONSTRAINT runs_model_revision_fk
    FOREIGN KEY (model_endpoint_id, model_endpoint_revision)
    REFERENCES integration.model_endpoint_revisions(model_endpoint_id, revision);

CREATE INDEX IF NOT EXISTS automation_runs_runtime_idx
    ON automation.runs (organization_id, agent_application_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS automation_runs_checkpoint_idx
    ON automation.runs (organization_id, eino_checkpoint_id)
    WHERE eino_checkpoint_id IS NOT NULL;

ALTER TABLE automation.attempts DROP CONSTRAINT IF EXISTS attempts_status_check;
ALTER TABLE automation.attempts
    ADD CONSTRAINT attempts_status_check
    CHECK (status IN ('started', 'waiting', 'succeeded', 'failed', 'canceled'));

CREATE TABLE IF NOT EXISTS automation.checkpoints (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    run_id uuid NOT NULL REFERENCES automation.runs(id) ON DELETE CASCADE,
    sequence bigint NOT NULL CHECK (sequence > 0),
    checkpoint_key text NOT NULL,
    payload_ciphertext bytea NOT NULL,
    payload_checksum text NOT NULL,
    graph_code_version bigint,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, sequence)
);

ALTER TABLE automation.checkpoints
    DROP CONSTRAINT IF EXISTS checkpoints_run_id_checkpoint_key_key;

CREATE INDEX IF NOT EXISTS automation_checkpoints_run_idx
    ON automation.checkpoints (organization_id, run_id, sequence DESC);

CREATE TABLE IF NOT EXISTS automation.interactions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    run_id uuid NOT NULL REFERENCES automation.runs(id) ON DELETE CASCADE,
    interaction_type text NOT NULL CHECK (interaction_type IN ('input', 'approval')),
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'rejected', 'expired', 'cancelled')),
    prompt text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    response jsonb,
    requested_at timestamptz NOT NULL DEFAULT now(),
    responded_at timestamptz,
    responded_by uuid REFERENCES identity.users(id),
    idempotency_key text,
    UNIQUE (run_id, idempotency_key)
);

CREATE INDEX IF NOT EXISTS automation_interactions_pending_idx
    ON automation.interactions (organization_id, run_id, status, requested_at)
    WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS automation.workflow_definitions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid REFERENCES organization.organizations(id),
    workflow_key text NOT NULL,
    code_version bigint NOT NULL CHECK (code_version > 0),
    code_checksum text NOT NULL,
    input_schema jsonb NOT NULL DEFAULT '{}'::jsonb,
    output_schema jsonb NOT NULL DEFAULT '{}'::jsonb,
    policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, workflow_key, code_version)
);

COMMIT;
