BEGIN;

CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS pgroonga;

CREATE SCHEMA IF NOT EXISTS organization;
CREATE SCHEMA IF NOT EXISTS identity;
CREATE SCHEMA IF NOT EXISTS "authorization";
CREATE SCHEMA IF NOT EXISTS model;
CREATE SCHEMA IF NOT EXISTS asset;
CREATE SCHEMA IF NOT EXISTS integration;
CREATE SCHEMA IF NOT EXISTS retrieval;
CREATE SCHEMA IF NOT EXISTS audit;
CREATE SCHEMA IF NOT EXISTS system;

CREATE TABLE IF NOT EXISTS system.schema_migrations (
    version text PRIMARY KEY,
    checksum text NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE organization.organizations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE identity.users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    user_type text NOT NULL CHECK (user_type IN ('member', 'agent')),
    login_name text,
    display_name text NOT NULL,
    password_hash text,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'deleted')),
    member_role text NOT NULL DEFAULT 'editor' CHECK (member_role IN ('admin', 'editor')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (user_type = 'agent' OR login_name IS NOT NULL),
    CHECK (user_type = 'member' OR password_hash IS NULL),
    UNIQUE (organization_id, login_name)
);

CREATE TABLE identity.api_keys (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES identity.users(id),
    name text NOT NULL,
    key_prefix text NOT NULL,
    key_hash text NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked', 'expired')),
    expires_at timestamptz,
    last_used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    capabilities jsonb NOT NULL DEFAULT '[]'::jsonb,
    UNIQUE (key_hash)
);

CREATE TABLE identity.sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES identity.users(id),
    session_hash text NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);

CREATE TABLE organization.invitations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    email text NOT NULL,
    invited_by uuid NOT NULL REFERENCES identity.users(id),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'expired', 'revoked')),
    token_hash text NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    accepted_at timestamptz
);

CREATE TABLE organization.departments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    parent_id uuid REFERENCES organization.departments(id),
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

CREATE TABLE organization.groups (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

CREATE TABLE organization.group_memberships (
    group_id uuid NOT NULL REFERENCES organization.groups(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, user_id)
);

CREATE TABLE organization.department_memberships (
    department_id uuid NOT NULL REFERENCES organization.departments(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (department_id, user_id)
);

CREATE TABLE organization.roles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

CREATE TABLE organization.role_memberships (
    role_id uuid NOT NULL REFERENCES organization.roles(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (role_id, user_id)
);

CREATE TABLE "authorization".policy_revisions (
    organization_id uuid PRIMARY KEY REFERENCES organization.organizations(id),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE model.resource_models (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    model_key text NOT NULL,
    name text NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('draft', 'active', 'archived')),
    current_version_id uuid,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, model_key)
);

CREATE TABLE model.resource_model_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    resource_model_id uuid NOT NULL REFERENCES model.resource_models(id),
    version_no integer NOT NULL CHECK (version_no > 0),
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'retired')),
    field_schema jsonb NOT NULL DEFAULT '{}'::jsonb,
    form_schema jsonb NOT NULL DEFAULT '{}'::jsonb,
    policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (resource_model_id, version_no)
);

ALTER TABLE model.resource_models
    ADD CONSTRAINT resource_models_current_version_fk
    FOREIGN KEY (current_version_id)
    REFERENCES model.resource_model_versions(id);

CREATE TABLE asset.raw_inputs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    submitted_by uuid NOT NULL REFERENCES identity.users(id),
    source_type text NOT NULL,
    content_type text,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    content_checksum text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE asset.assets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    resource_model_id uuid NOT NULL REFERENCES model.resource_models(id),
    current_working_version_id uuid,
    current_published_version_id uuid,
    publication_status text NOT NULL DEFAULT 'internal'
        CHECK (publication_status IN ('internal', 'published', 'archived')),
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE asset.asset_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    asset_id uuid NOT NULL REFERENCES asset.assets(id),
    resource_model_id uuid NOT NULL REFERENCES model.resource_models(id),
    resource_model_version_id uuid NOT NULL REFERENCES model.resource_model_versions(id),
    version_no integer NOT NULL CHECK (version_no > 0),
    workflow_status text NOT NULL DEFAULT 'draft'
        CHECK (workflow_status IN ('draft', 'pending_processing', 'processing')),
    processing_started_at timestamptz,
    quality text NOT NULL DEFAULT 'raw'
        CHECK (quality IN ('raw', 'agent_prepared', 'human_confirmed', 'high_quality')),
    title text,
    markdown text,
    fields jsonb NOT NULL DEFAULT '{}'::jsonb,
    source_raw_input_id uuid REFERENCES asset.raw_inputs(id),
    parent_version_id uuid REFERENCES asset.asset_versions(id),
    content_checksum text NOT NULL,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (asset_id, version_no)
);

ALTER TABLE asset.assets
    ADD CONSTRAINT assets_current_working_version_fk
    FOREIGN KEY (current_working_version_id)
    REFERENCES asset.asset_versions(id);

ALTER TABLE asset.assets
    ADD CONSTRAINT assets_current_published_version_fk
    FOREIGN KEY (current_published_version_id)
    REFERENCES asset.asset_versions(id);

CREATE INDEX assets_published_model_idx
    ON asset.assets (organization_id, resource_model_id)
    WHERE publication_status = 'published';

CREATE INDEX asset_versions_asset_status_idx
    ON asset.asset_versions (asset_id, workflow_status, created_at DESC);

CREATE TABLE asset.attachments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    asset_version_id uuid NOT NULL REFERENCES asset.asset_versions(id),
    object_key text NOT NULL UNIQUE,
    original_filename text NOT NULL,
    media_type text NOT NULL,
    byte_size bigint NOT NULL CHECK (byte_size >= 0),
    sha256 text NOT NULL,
    scan_status text NOT NULL DEFAULT 'pending'
        CHECK (scan_status IN ('pending', 'clean', 'blocked', 'failed')),
    extraction_status text NOT NULL DEFAULT 'pending'
        CHECK (extraction_status IN ('pending', 'processing', 'succeeded', 'failed')),
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX attachments_asset_version_idx
    ON asset.attachments (organization_id, asset_version_id, created_at DESC);

CREATE INDEX attachments_scan_status_idx
    ON asset.attachments (scan_status, extraction_status, created_at);

CREATE TABLE asset.attachment_texts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    attachment_id uuid NOT NULL UNIQUE REFERENCES asset.attachments(id) ON DELETE CASCADE,
    extractor text NOT NULL,
    extractor_version text NOT NULL,
    language text,
    text_content text NOT NULL DEFAULT '',
    checksum text NOT NULL,
    error_code text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE asset.asset_relations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    source_asset_version_id uuid NOT NULL REFERENCES asset.asset_versions(id),
    target_asset_version_id uuid NOT NULL REFERENCES asset.asset_versions(id),
    relation_type text NOT NULL,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_asset_version_id, target_asset_version_id, relation_type)
);

CREATE TABLE asset.import_batches (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    resource_model_id uuid NOT NULL REFERENCES model.resource_models(id),
    resource_model_version_id uuid NOT NULL REFERENCES model.resource_model_versions(id),
    submitted_by uuid NOT NULL REFERENCES identity.users(id),
    source_checksum text NOT NULL,
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'processing', 'succeeded', 'failed', 'cancelled')),
    summary jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

CREATE TABLE asset.import_rows (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    import_batch_id uuid NOT NULL REFERENCES asset.import_batches(id) ON DELETE CASCADE,
    row_number integer NOT NULL CHECK (row_number > 0),
    source_row jsonb NOT NULL,
    row_checksum text NOT NULL,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'accepted', 'rejected')),
    errors jsonb NOT NULL DEFAULT '[]'::jsonb,
    raw_input_id uuid REFERENCES asset.raw_inputs(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (import_batch_id, row_number),
    UNIQUE (import_batch_id, row_checksum)
);

CREATE TABLE asset.export_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    resource_model_id uuid NOT NULL REFERENCES model.resource_models(id),
    submitted_by uuid NOT NULL REFERENCES identity.users(id),
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'processing', 'succeeded', 'failed', 'cancelled')),
    query_snapshot jsonb NOT NULL,
    permission_scope jsonb NOT NULL,
    output_object_key text,
    error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

CREATE TABLE model.model_migration_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    resource_model_id uuid NOT NULL REFERENCES model.resource_models(id),
    from_version_id uuid REFERENCES model.resource_model_versions(id),
    to_version_id uuid NOT NULL REFERENCES model.resource_model_versions(id),
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'previewing', 'processing', 'succeeded', 'failed', 'cancelled')),
    preview jsonb NOT NULL DEFAULT '{}'::jsonb,
    error_summary text,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

CREATE TABLE integration.agent_applications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    bound_agent_user_id uuid NOT NULL REFERENCES identity.users(id),
    provider text NOT NULL DEFAULT 'dify',
    provider_key text NOT NULL DEFAULT '',
    name text NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    capabilities jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE integration.agent_tasks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    agent_application_id uuid NOT NULL REFERENCES integration.agent_applications(id),
    agent_user_id uuid NOT NULL REFERENCES identity.users(id),
    operation text NOT NULL,
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    input_asset_ids uuid[] NOT NULL,
    candidate_version_id uuid REFERENCES asset.asset_versions(id),
    idempotency_key text NOT NULL,
    error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (organization_id, agent_user_id, idempotency_key)
);

CREATE TABLE integration.agent_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    agent_application_id uuid NOT NULL REFERENCES integration.agent_applications(id),
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

CREATE TABLE integration.agent_task_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    agent_task_id uuid NOT NULL REFERENCES integration.agent_tasks(id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    short_credential_hash text NOT NULL UNIQUE,
    credential_expires_at timestamptz NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

CREATE TABLE retrieval.query_logs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid REFERENCES organization.organizations(id),
    actor_user_id uuid REFERENCES identity.users(id),
    initiator_user_id uuid REFERENCES identity.users(id),
    agent_application_id uuid REFERENCES integration.agent_applications(id),
    endpoint text NOT NULL,
    query_hash text NOT NULL,
    result_count integer NOT NULL DEFAULT 0,
    outcome text NOT NULL CHECK (outcome IN ('allowed', 'denied', 'error')),
    latency_ms integer,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE system.idempotency_keys (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    subject_id uuid NOT NULL REFERENCES identity.users(id),
    operation text NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    response_status integer,
    response_body jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    UNIQUE (organization_id, subject_id, operation, idempotency_key)
);

CREATE TABLE audit.outbox_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    event_type text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id uuid NOT NULL,
    aggregate_version bigint NOT NULL,
    payload_version integer NOT NULL DEFAULT 1,
    payload jsonb NOT NULL,
    payload_checksum text NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT now()
);

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
    actor_user_id uuid REFERENCES identity.users(id),
    initiator_user_id uuid REFERENCES identity.users(id),
    agent_application_id uuid REFERENCES integration.agent_applications(id),
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

INSERT INTO system.schema_migrations (version, checksum)
VALUES ('0001_core.sql', 'managed-by-migration-runner')
ON CONFLICT (version) DO NOTHING;

COMMIT;
