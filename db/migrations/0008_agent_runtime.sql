-- 0008_agent_runtime.sql
-- Integration domain (agent applications, model endpoints) and automation
-- runtime, folded at their current field set.
BEGIN;

CREATE TABLE integration.model_endpoints (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    current_revision bigint NOT NULL DEFAULT 1 CHECK (current_revision > 0),
    status text NOT NULL DEFAULT 'unavailable'
        CHECK (status IN ('active', 'disabled', 'unavailable')),
    last_verified_at timestamptz,
    last_health_error_code text,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name),
    UNIQUE (organization_id, id)
);

CREATE TABLE integration.model_endpoint_revisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    model_endpoint_id uuid NOT NULL REFERENCES integration.model_endpoints(id) ON DELETE CASCADE,
    revision bigint NOT NULL CHECK (revision > 0),
    provider_type text NOT NULL CHECK (provider_type IN ('openai', 'openai_compatible')),
    base_url text NOT NULL CHECK (length(btrim(base_url)) BETWEEN 1 AND 2048),
    model_name text NOT NULL CHECK (length(btrim(model_name)) BETWEEN 1 AND 200),
    credential_mode text NOT NULL CHECK (credential_mode IN ('encrypted', 'secret_ref')),
    credential_ciphertext bytea,
    credential_key_id text,
    secret_ref text,
    options jsonb NOT NULL DEFAULT '{}'::jsonb,
    capabilities jsonb NOT NULL DEFAULT '{}'::jsonb,
    config_checksum text NOT NULL,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    UNIQUE (model_endpoint_id, revision),
    CHECK (
        (credential_mode = 'encrypted' AND credential_ciphertext IS NOT NULL AND secret_ref IS NULL)
        OR
        (credential_mode = 'secret_ref' AND credential_ciphertext IS NULL AND length(btrim(secret_ref)) > 0)
    )
);

ALTER TABLE integration.model_endpoints
    ADD CONSTRAINT model_endpoints_current_revision_fk
    FOREIGN KEY (id, current_revision)
    REFERENCES integration.model_endpoint_revisions(model_endpoint_id, revision)
    DEFERRABLE INITIALLY DEFERRED;

CREATE INDEX model_endpoints_org_status_idx
    ON integration.model_endpoints (organization_id, status, updated_at DESC);
CREATE INDEX model_endpoint_revisions_endpoint_idx
    ON integration.model_endpoint_revisions (model_endpoint_id, revision DESC);

CREATE TABLE integration.agent_applications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    bound_agent_user_id uuid NOT NULL REFERENCES identity.users(id),
    name text NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    capabilities jsonb NOT NULL DEFAULT '[]'::jsonb,
    model_endpoint_id uuid,
    runtime_mode text NOT NULL DEFAULT 'rag' CHECK (runtime_mode IN ('rag', 'react', 'workflow')),
    workflow_key text,
    instruction text NOT NULL DEFAULT '',
    instruction_version bigint NOT NULL DEFAULT 1 CHECK (instruction_version > 0),
    tool_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, model_endpoint_id)
        REFERENCES integration.model_endpoints(organization_id, id)
);

CREATE INDEX agent_applications_model_endpoint_idx
    ON integration.agent_applications (organization_id, model_endpoint_id)
    WHERE model_endpoint_id IS NOT NULL;

ALTER TABLE content.workspace_agent_applications
    ADD CONSTRAINT workspace_agent_applications_agent_app_fk
    FOREIGN KEY (organization_id, agent_application_id)
    REFERENCES integration.agent_applications (organization_id, id);

CREATE TABLE integration.agent_tasks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    agent_application_id uuid NOT NULL,
    agent_user_id uuid NOT NULL REFERENCES identity.users(id),
    operation text NOT NULL,
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    input_asset_ids uuid[] NOT NULL,
    candidate_version_id uuid,
    idempotency_key text NOT NULL,
    error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (organization_id, agent_user_id, idempotency_key)
);

CREATE TABLE integration.agent_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    agent_application_id uuid NOT NULL,
    initiator_user_id uuid NOT NULL REFERENCES identity.users(id),
    bound_agent_user_id uuid NOT NULL REFERENCES identity.users(id),
    idempotency_key text NOT NULL,
    status text NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'completed', 'failed', 'expired', 'revoked')),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (organization_id, initiator_user_id, agent_application_id, idempotency_key)
);

CREATE INDEX agent_sessions_initiator_time_idx
    ON integration.agent_sessions (organization_id, initiator_user_id, created_at DESC);

CREATE TABLE integration.agent_tool_invocations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    run_id uuid NOT NULL,
    session_id uuid,
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

CREATE INDEX agent_tool_invocations_run_idx
    ON integration.agent_tool_invocations (organization_id, run_id, created_at);

CREATE TABLE automation.jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    name text NOT NULL,
    operation text NOT NULL,
    agent_application_id uuid NOT NULL,
    trigger jsonb NOT NULL,
    timezone text NOT NULL,
    concurrency_policy text NOT NULL DEFAULT 'forbid'
        CHECK (concurrency_policy IN ('forbid', 'replace', 'allow')),
    input_scope jsonb NOT NULL DEFAULT '{}'::jsonb,
    max_attempts integer NOT NULL DEFAULT 3 CHECK (max_attempts BETWEEN 1 AND 20),
    retry_backoff jsonb NOT NULL DEFAULT '{}'::jsonb,
    enabled boolean NOT NULL DEFAULT true,
    idempotency_key text,
    request_hash text,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
        ON DELETE CASCADE
);

CREATE UNIQUE INDEX automation_jobs_idempotency_idx
    ON automation.jobs (organization_id, workspace_id, created_by, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE INDEX automation_jobs_workspace_idx
    ON automation.jobs (organization_id, workspace_id, enabled, updated_at DESC);

CREATE TABLE automation.runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    automation_job_id uuid,
    source text NOT NULL CHECK (source IN ('automation', 'manual', 'agent', 'chat')),
    operation text NOT NULL,
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'waiting_input', 'waiting_approval', 'retrying',
                          'succeeded', 'failed', 'cancel_requested', 'canceled', 'expired', 'paused')),
    progress numeric(5, 2) NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 100),
    input_asset_ids uuid[] NOT NULL DEFAULT '{}',
    candidate_version_ids uuid[] NOT NULL DEFAULT '{}',
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    error_code text,
    error_summary text,
    idempotency_key text,
    next_attempt_at timestamptz,
    cancel_requested boolean NOT NULL DEFAULT false,
    input_scope jsonb NOT NULL DEFAULT '{}'::jsonb,
    runtime_mode text,
    workflow_key text,
    workflow_code_version bigint,
    principal_id uuid,
    agent_user_id uuid,
    agent_application_id uuid,
    session_id uuid,
    model_endpoint_id uuid,
    model_endpoint_revision bigint,
    current_node text,
    input_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    execution_options jsonb NOT NULL DEFAULT '{}'::jsonb,
    input_checksum text,
    output_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    tool_call_count integer NOT NULL DEFAULT 0 CHECK (tool_call_count >= 0),
    input_tokens integer NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens integer NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    policy_revision bigint,
    eino_checkpoint_id uuid,
    checkpoint_sequence bigint NOT NULL DEFAULT 0,
    waiting_interaction_id uuid,
    agent_task_id uuid,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    completed_at timestamptz,
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
        ON DELETE CASCADE
);

CREATE UNIQUE INDEX automation_runs_idempotency_idx
    ON automation.runs (organization_id, workspace_id, created_by, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE INDEX automation_runs_workspace_idx
    ON automation.runs (organization_id, workspace_id, status, created_at DESC);
CREATE INDEX automation_runs_due_idx
    ON automation.runs (status, next_attempt_at, created_at)
    WHERE status = 'queued';
CREATE INDEX automation_runs_callback_credential_idx
    ON automation.runs ((input_scope->>'_run_credential_hash'))
    WHERE input_scope ? '_run_credential_hash';
CREATE INDEX automation_runs_agent_task_idx
    ON automation.runs (organization_id, agent_task_id, created_at DESC)
    WHERE agent_task_id IS NOT NULL;

CREATE TABLE automation.attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id uuid NOT NULL REFERENCES automation.runs(id) ON DELETE CASCADE,
    attempt_no integer NOT NULL CHECK (attempt_no > 0),
    status text NOT NULL CHECK (status IN ('started', 'succeeded', 'failed', 'canceled')),
    error_code text,
    error_summary text,
    claimed_by text,
    lease_expires_at timestamptz,
    next_retry_at timestamptz,
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, attempt_no)
);

CREATE INDEX automation_attempts_lease_idx
    ON automation.attempts (status, lease_expires_at)
    WHERE status = 'started';

CREATE TABLE automation.run_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    run_id uuid NOT NULL REFERENCES automation.runs(id) ON DELETE CASCADE,
    event_type text NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX automation_run_events_run_idx
    ON automation.run_events (run_id, id);

CREATE TABLE automation.interactions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    run_id uuid NOT NULL REFERENCES automation.runs(id) ON DELETE CASCADE,
    interaction_type text NOT NULL,
    status text NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'resolved', 'expired')),
    request_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    response_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    interrupt_id text,
    display_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    resume_schema jsonb NOT NULL DEFAULT '{}'::jsonb,
    resume_consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    UNIQUE (organization_id, id)
);

CREATE UNIQUE INDEX automation_interactions_interrupt_idx
    ON automation.interactions (run_id, interrupt_id)
    WHERE interrupt_id IS NOT NULL;

COMMIT;
