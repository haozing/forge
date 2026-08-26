BEGIN;

CREATE SCHEMA IF NOT EXISTS automation;

CREATE TABLE automation.jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    name text NOT NULL,
    operation text NOT NULL,
    agent_application_id uuid NOT NULL REFERENCES integration.agent_applications(id),
    trigger jsonb NOT NULL,
    timezone text NOT NULL,
    concurrency_policy text NOT NULL DEFAULT 'forbid'
        CHECK (concurrency_policy IN ('forbid', 'replace', 'allow')),
    input_scope jsonb NOT NULL DEFAULT '{}'::jsonb,
    max_attempts integer NOT NULL DEFAULT 3 CHECK (max_attempts BETWEEN 1 AND 20),
    retry_backoff jsonb NOT NULL DEFAULT '{}'::jsonb,
    enabled boolean NOT NULL DEFAULT true,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
        ON DELETE CASCADE
);

CREATE TABLE automation.runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    automation_job_id uuid REFERENCES automation.jobs(id) ON DELETE SET NULL,
    source text NOT NULL CHECK (source IN ('automation', 'manual', 'agent')),
    operation text NOT NULL,
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    progress numeric(5, 2) NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 100),
    input_asset_ids uuid[] NOT NULL DEFAULT '{}',
    candidate_version_ids uuid[] NOT NULL DEFAULT '{}',
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    error_code text,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    completed_at timestamptz,
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
        ON DELETE CASCADE
);

CREATE TABLE automation.attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id uuid NOT NULL REFERENCES automation.runs(id) ON DELETE CASCADE,
    attempt_no integer NOT NULL CHECK (attempt_no > 0),
    status text NOT NULL CHECK (status IN ('started', 'succeeded', 'failed', 'cancelled')),
    error_code text,
    error_summary text,
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (run_id, attempt_no)
);

CREATE INDEX automation_jobs_workspace_idx ON automation.jobs (organization_id, workspace_id, enabled, updated_at DESC);
CREATE INDEX automation_runs_workspace_idx ON automation.runs (organization_id, workspace_id, status, created_at DESC);

COMMIT;
