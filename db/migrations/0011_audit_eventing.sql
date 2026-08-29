-- 0011_audit_eventing.sql
-- Outbox, delivery tracking, audit log, HTTP idempotency and worker
-- heartbeats. Event envelope carries workspace_id when applicable and a
-- payload_version; the unique envelope is internal/eventing.Event.
BEGIN;

CREATE TABLE audit.outbox_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid,
    event_type text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id uuid NOT NULL,
    aggregate_version bigint NOT NULL,
    payload_version integer NOT NULL DEFAULT 1,
    actor jsonb NOT NULL DEFAULT '{}'::jsonb,
    payload jsonb NOT NULL,
    payload_checksum text NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX outbox_events_type_time_idx
    ON audit.outbox_events (organization_id, event_type, occurred_at DESC);

CREATE TABLE audit.event_deliveries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id uuid NOT NULL REFERENCES audit.outbox_events(id) ON DELETE CASCADE,
    consumer_key text NOT NULL,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'processing', 'succeeded', 'retry_wait', 'dead')),
    attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_token text,
    lease_until timestamptz,
    last_error_code text,
    last_error_summary text,
    completed_at timestamptz,
    UNIQUE (event_id, consumer_key)
);

CREATE INDEX event_deliveries_due_idx
    ON audit.event_deliveries (status, next_attempt_at, id);

CREATE TABLE audit.event_delivery_attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    event_delivery_id uuid NOT NULL REFERENCES audit.event_deliveries(id) ON DELETE CASCADE,
    attempt_no integer NOT NULL CHECK (attempt_no > 0),
    status text NOT NULL CHECK (status IN ('started', 'succeeded', 'failed')),
    error_code text,
    duration_ms integer,
    processor_version text,
    trace_id text,
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (event_delivery_id, attempt_no)
);

CREATE TABLE audit.audit_log (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid REFERENCES organization.organizations(id),
    workspace_id uuid,
    actor_user_id uuid,
    initiator_user_id uuid,
    agent_application_id uuid,
    action text NOT NULL,
    resource_type text,
    resource_id uuid,
    request_id text,
    result text NOT NULL CHECK (result IN ('allowed', 'denied', 'error')),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_log_actor_time_idx
    ON audit.audit_log (organization_id, actor_user_id, created_at DESC);
CREATE INDEX audit_log_workspace_time_idx
    ON audit.audit_log (organization_id, workspace_id, created_at DESC);
CREATE INDEX audit_log_resource_idx
    ON audit.audit_log (organization_id, resource_type, resource_id, created_at DESC);

CREATE TABLE system.idempotency_keys (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    subject_id uuid NOT NULL REFERENCES identity.users(id),
    operation text NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    response_status integer,
    response_body jsonb,
    response_headers jsonb,
    response_bytes bytea,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    UNIQUE (organization_id, subject_id, operation, idempotency_key)
);

CREATE INDEX idempotency_keys_expiry_idx
    ON system.idempotency_keys (expires_at);

CREATE TABLE system.worker_heartbeats (
    worker_id text NOT NULL,
    role text NOT NULL,
    manifest_fingerprint text NOT NULL,
    handler_manifest jsonb NOT NULL DEFAULT '[]'::jsonb,
    status text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'draining', 'stopped')),
    started_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (worker_id, role)
);

CREATE INDEX worker_heartbeats_seen_idx
    ON system.worker_heartbeats (role, last_seen_at DESC);

COMMIT;
