CREATE SCHEMA IF NOT EXISTS content;

ALTER TABLE asset.assets
    ADD CONSTRAINT assets_organization_id_id_uq UNIQUE (organization_id, id);

ALTER TABLE asset.asset_versions
    ADD CONSTRAINT asset_versions_organization_id_id_uq UNIQUE (organization_id, id),
    ADD CONSTRAINT asset_versions_scope_uq UNIQUE (organization_id, id, asset_id, resource_model_id);

CREATE TABLE content.workspaces (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    name text NOT NULL CHECK (length(btrim(name)) > 0),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    default_agent_application_id uuid REFERENCES integration.agent_applications(id),
    default_resource_model_id uuid REFERENCES model.resource_models(id),
    default_visibility text NOT NULL DEFAULT 'private'
        CHECK (default_visibility IN ('private', 'workspace', 'internal')),
    settings jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id)
);

CREATE TABLE content.workspace_members (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL REFERENCES identity.users(id),
    role text NOT NULL DEFAULT 'editor' CHECK (role IN ('admin', 'editor')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (workspace_id, user_id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
);

CREATE TABLE content.workspace_agent_applications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    agent_application_id uuid NOT NULL REFERENCES integration.agent_applications(id),
    enabled boolean NOT NULL DEFAULT true,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (workspace_id, agent_application_id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
);

CREATE TABLE content.containers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid,
    kind text NOT NULL CHECK (kind IN ('chat', 'note', 'document', 'faq', 'content_field')),
    title text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    visibility text NOT NULL DEFAULT 'private'
        CHECK (visibility IN ('private', 'workspace', 'internal')),
    current_version_id uuid,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
);

CREATE TABLE content.container_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    container_id uuid NOT NULL,
    version_no bigint NOT NULL CHECK (version_no > 0),
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    content_checksum text NOT NULL CHECK (length(content_checksum) = 64),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, container_id, version_no),
    FOREIGN KEY (organization_id, container_id)
        REFERENCES content.containers (organization_id, id)
);

ALTER TABLE content.containers
    ADD CONSTRAINT containers_current_version_fk
    FOREIGN KEY (organization_id, current_version_id)
    REFERENCES content.container_versions (organization_id, id);

CREATE TABLE content.blocks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    block_type text NOT NULL CHECK (block_type IN (
        'text', 'paragraph', 'heading', 'list', 'code', 'quote', 'qa',
        'question', 'answer', 'message', 'link', 'attachment', 'callout'
    )),
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    retired_at timestamptz,
    UNIQUE (organization_id, id)
);

CREATE TABLE content.block_revisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    block_id uuid NOT NULL,
    revision_no bigint NOT NULL CHECK (revision_no > 0),
    content text NOT NULL DEFAULT '',
    content_format text NOT NULL DEFAULT 'plain_text'
        CHECK (content_format IN ('plain_text', 'markdown', 'json')),
    props jsonb NOT NULL DEFAULT '{}'::jsonb,
    origin_block_revision_id uuid,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    content_checksum text NOT NULL CHECK (length(content_checksum) = 64),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, block_id, revision_no),
    FOREIGN KEY (organization_id, block_id)
        REFERENCES content.blocks (organization_id, id),
    FOREIGN KEY (organization_id, origin_block_revision_id)
        REFERENCES content.block_revisions (organization_id, id)
);

CREATE TABLE content.block_placements (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    container_version_id uuid NOT NULL,
    block_revision_id uuid NOT NULL,
    parent_placement_id uuid,
    position numeric(20, 6) NOT NULL CHECK (position >= 0),
    role_in_parent text,
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, container_version_id, parent_placement_id, position),
    FOREIGN KEY (organization_id, container_version_id)
        REFERENCES content.container_versions (organization_id, id),
    FOREIGN KEY (organization_id, block_revision_id)
        REFERENCES content.block_revisions (organization_id, id),
    FOREIGN KEY (organization_id, parent_placement_id)
        REFERENCES content.block_placements (organization_id, id)
);

CREATE TABLE content.conversations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    container_id uuid NOT NULL UNIQUE,
    initiator_user_id uuid NOT NULL REFERENCES identity.users(id),
    agent_application_id uuid NOT NULL REFERENCES integration.agent_applications(id),
    bound_agent_user_id uuid NOT NULL REFERENCES identity.users(id),
    agent_session_id uuid REFERENCES integration.agent_sessions(id),
    parent_conversation_id uuid REFERENCES content.conversations(id),
    origin_derivation_id uuid,
    title text NOT NULL DEFAULT '',
    source text NOT NULL DEFAULT 'chat_interface'
        CHECK (source IN ('chat_interface', 'voice', 'meeting', 'imported')),
    status text NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'completed', 'archived', 'failed')),
    visibility text NOT NULL DEFAULT 'private'
        CHECK (visibility IN ('private', 'workspace', 'internal')),
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
    role text NOT NULL CHECK (role IN ('human', 'assistant', 'system', 'tool', 'transcription')),
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
    FOREIGN KEY (note_asset_id) REFERENCES asset.assets(id)
);

CREATE TABLE content.asset_version_content_fields (
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    asset_version_id uuid NOT NULL,
    field_key text NOT NULL CHECK (length(btrim(field_key)) > 0),
    container_id uuid NOT NULL,
    container_version_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (asset_version_id, field_key),
    FOREIGN KEY (organization_id, asset_version_id)
        REFERENCES asset.asset_versions (organization_id, id),
    FOREIGN KEY (organization_id, container_id)
        REFERENCES content.containers (organization_id, id),
    FOREIGN KEY (organization_id, container_version_id)
        REFERENCES content.container_versions (organization_id, id)
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

CREATE TABLE content.agent_access_policies (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid,
    agent_user_id uuid NOT NULL REFERENCES identity.users(id),
    resource_model_id uuid NOT NULL REFERENCES model.resource_models(id),
    actions text[] NOT NULL DEFAULT '{}',
    field_allowlist text[] NOT NULL DEFAULT '{}',
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, agent_user_id, workspace_id, resource_model_id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
);

CREATE INDEX content_containers_workspace_idx
    ON content.containers (organization_id, workspace_id, status, updated_at DESC);

CREATE INDEX content_container_versions_container_idx
    ON content.container_versions (organization_id, container_id, version_no DESC);

CREATE INDEX content_block_placements_version_idx
    ON content.block_placements (organization_id, container_version_id, parent_placement_id, position);

CREATE INDEX content_message_blocks_conversation_idx
    ON content.message_blocks (organization_id, conversation_id, sequence_no);

CREATE INDEX content_processing_jobs_status_idx
    ON content.processing_jobs (organization_id, status, created_at);
