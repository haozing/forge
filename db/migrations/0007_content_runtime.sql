-- 0007_content_runtime.sql
-- Conversation, note, derivation and processing runtime tables required by the
-- not-yet-migrated conversation/automation modules.

CREATE TABLE content.conversations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    container_id uuid NOT NULL UNIQUE,
    initiator_user_id uuid NOT NULL REFERENCES identity.users(id),
    agent_application_id uuid NOT NULL,
    bound_agent_user_id uuid NOT NULL REFERENCES identity.users(id),
    agent_session_id uuid,
    parent_conversation_id uuid REFERENCES content.conversations(id),
    origin_derivation_id uuid,
    title text NOT NULL DEFAULT '',
    source text NOT NULL DEFAULT 'chat_interface'
        CHECK (source IN ('chat_interface', 'document', 'asset', 'automation')),
    status text NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'completed', 'archived', 'failed')),
    visibility text NOT NULL DEFAULT 'workspace'
        CHECK (visibility IN ('workspace', 'organization', 'public')),
    source_summary jsonb NOT NULL DEFAULT '{}'::jsonb,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id),
    FOREIGN KEY (organization_id, container_id)
        REFERENCES content.containers (organization_id, id)
);

CREATE TABLE content.message_blocks (
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    block_revision_id uuid PRIMARY KEY,
    conversation_id uuid NOT NULL REFERENCES content.conversations(id),
    role text NOT NULL CHECK (role IN ('user', 'assistant', 'system', 'tool', 'transcription')),
    provider_conversation_id text,
    provider_message_id text,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'completed', 'failed', 'cancelled')),
    reply_to_block_id uuid,
    sequence_no bigint NOT NULL CHECK (sequence_no > 0),
    reference_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, block_revision_id),
    UNIQUE (conversation_id, sequence_no),
    FOREIGN KEY (organization_id, block_revision_id)
        REFERENCES content.block_revisions (organization_id, id),
    FOREIGN KEY (organization_id, reply_to_block_id)
        REFERENCES content.blocks (organization_id, id)
);

CREATE INDEX content_message_blocks_conversation_idx
    ON content.message_blocks (organization_id, conversation_id, sequence_no);

CREATE TABLE content.note_bindings (
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    conversation_id uuid PRIMARY KEY REFERENCES content.conversations(id),
    note_container_id uuid NOT NULL,
    note_asset_id uuid NOT NULL,
    last_synced_message_sequence bigint NOT NULL DEFAULT 0 CHECK (last_synced_message_sequence >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, note_container_id)
        REFERENCES content.containers (organization_id, id),
    FOREIGN KEY (organization_id, note_asset_id)
        REFERENCES asset.assets (organization_id, id)
);

CREATE TABLE content.derivations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    source_conversation_id uuid NOT NULL REFERENCES content.conversations(id),
    target_conversation_id uuid REFERENCES content.conversations(id),
    status text NOT NULL DEFAULT 'requested'
        CHECK (status IN ('requested', 'discussing', 'result_ready', 'finalizing', 'completed', 'failed', 'cancelled')),
    operation text NOT NULL CHECK (operation IN ('create_chat', 'link', 'merge')),
    context_policy text NOT NULL DEFAULT 'summary_only'
        CHECK (context_policy IN ('summary_only', 'selected_only', 'full')),
    context_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
);

CREATE TABLE content.derivation_sources (
    derivation_id uuid NOT NULL REFERENCES content.derivations(id),
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    source_container_id uuid NOT NULL,
    source_container_version_id uuid NOT NULL,
    source_block_revision_id uuid NOT NULL,
    source_excerpt text,
    context_role text NOT NULL CHECK (context_role IN ('selected', 'adjacent', 'summary')),
    PRIMARY KEY (derivation_id, ordinal),
    UNIQUE (derivation_id, source_block_revision_id, context_role),
    FOREIGN KEY (source_container_id) REFERENCES content.containers(id),
    FOREIGN KEY (source_container_version_id) REFERENCES content.container_versions(id),
    FOREIGN KEY (source_block_revision_id) REFERENCES content.block_revisions(id)
);

CREATE TABLE content.asset_relations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    source_type text NOT NULL CHECK (source_type IN ('container', 'block', 'block_revision', 'asset_version')),
    source_id uuid NOT NULL,
    target_type text NOT NULL CHECK (target_type IN ('container', 'block', 'block_revision', 'asset', 'asset_version')),
    target_id uuid NOT NULL,
    derivation_id uuid REFERENCES content.derivations(id),
    relation_type text NOT NULL CHECK (relation_type IN ('derived_from', 'cites', 'continues_from', 'references', 'related_to')),
    navigation_visible boolean NOT NULL DEFAULT true,
    is_active boolean NOT NULL DEFAULT true,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    disconnected_at timestamptz,
    UNIQUE (organization_id, id)
);

CREATE TABLE content.asset_relation_blocks (
    relation_id uuid NOT NULL REFERENCES content.asset_relations(id) ON DELETE CASCADE,
    side text NOT NULL CHECK (side IN ('source', 'target')),
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    block_revision_id uuid NOT NULL REFERENCES content.block_revisions(id),
    PRIMARY KEY (relation_id, side, ordinal),
    UNIQUE (relation_id, side, block_revision_id)
);

CREATE TABLE content.processing_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid,
    job_type text NOT NULL CHECK (job_type IN ('note_sync', 'derivation_candidate', 'projection', 'transcription')),
    source_type text NOT NULL,
    source_id uuid NOT NULL,
    output_version_id uuid,
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    idempotency_key text NOT NULL,
    input_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    output_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    completed_at timestamptz,
    UNIQUE (organization_id, idempotency_key),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
);

CREATE INDEX content_processing_jobs_status_idx
    ON content.processing_jobs (organization_id, status, created_at);

CREATE TABLE content.conversation_media (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    conversation_id uuid NOT NULL REFERENCES content.conversations(id),
    attachment_id uuid NOT NULL,
    media_kind text NOT NULL CHECK (media_kind IN ('audio', 'video')),
    status text NOT NULL DEFAULT 'registered'
        CHECK (status IN ('registered', 'transcribing', 'transcribed', 'failed')),
    language text,
    duration_ms bigint CHECK (duration_ms IS NULL OR duration_ms >= 0),
    transcription_block_revision_id uuid,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, attachment_id)
);

CREATE INDEX content_conversation_media_conversation_idx
    ON content.conversation_media (organization_id, conversation_id, created_at DESC);
CREATE INDEX content_conversation_media_status_idx
    ON content.conversation_media (organization_id, status, updated_at);

CREATE TABLE content.message_references (
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    block_revision_id uuid NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    asset_id uuid NOT NULL,
    asset_version_id uuid NOT NULL,
    title text NOT NULL DEFAULT '',
    url text NOT NULL DEFAULT '',
    source_excerpt text NOT NULL DEFAULT '',
    updated_at_snapshot text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (block_revision_id, ordinal),
    FOREIGN KEY (organization_id, block_revision_id)
        REFERENCES content.block_revisions (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, asset_id)
        REFERENCES asset.assets (organization_id, id),
    FOREIGN KEY (organization_id, asset_version_id)
        REFERENCES asset.asset_versions (organization_id, id)
);

CREATE INDEX message_references_asset_idx
    ON content.message_references (organization_id, asset_id, asset_version_id);

CREATE TABLE content.notifications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    recipient_user_id uuid NOT NULL,
    kind text NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    read_at timestamptz,
    stream_id bigint GENERATED ALWAYS AS IDENTITY,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (stream_id)
);

CREATE INDEX notifications_stream_scope_idx
    ON content.notifications (organization_id, workspace_id, recipient_user_id, stream_id);

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

