BEGIN;

CREATE TABLE content.deletion_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    resource_type text NOT NULL CHECK (resource_type IN ('asset', 'workspace')),
    resource_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'completed', 'failed')),
    requested_by uuid NOT NULL REFERENCES identity.users(id),
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    error_code text,
    error_summary text,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    completed_at timestamptz,
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id),
    UNIQUE (organization_id, requested_by, resource_type, idempotency_key)
);

CREATE INDEX deletion_jobs_pending_idx
    ON content.deletion_jobs (status, created_at, id)
    WHERE status IN ('queued', 'running');

CREATE INDEX deletion_jobs_scope_idx
    ON content.deletion_jobs (organization_id, workspace_id, created_at DESC);

ALTER TABLE content.notifications
    ADD COLUMN stream_id bigint GENERATED ALWAYS AS IDENTITY;

CREATE UNIQUE INDEX notifications_stream_id_idx
    ON content.notifications (stream_id);

CREATE INDEX notifications_stream_scope_idx
    ON content.notifications (organization_id, workspace_id, recipient_user_id, stream_id);

COMMIT;
