-- 0004_resource_models.sql
-- ResourceModel and its immutable versions. The published policy JSON uses the
-- final visibility/channels/retrieval/publishing structure only.
BEGIN;

CREATE TABLE model.resource_models (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid,
    model_key text NOT NULL,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    content_kind text NOT NULL DEFAULT 'record' CHECK (content_kind IN ('record', 'document', 'faq', 'note')),
    model_capabilities jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('draft', 'active', 'archived')),
    current_version_id uuid,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, model_key),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
);

CREATE INDEX resource_models_workspace_idx
    ON model.resource_models (organization_id, workspace_id, status, updated_at DESC);

CREATE TABLE model.resource_model_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    resource_model_id uuid NOT NULL,
    version_no integer NOT NULL CHECK (version_no > 0),
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'retired')),
    field_schema jsonb NOT NULL DEFAULT '{}'::jsonb,
    form_schema jsonb NOT NULL DEFAULT '{}'::jsonb,
    list_schema jsonb NOT NULL DEFAULT '{"columns":[],"filters":[]}'::jsonb,
    policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    schema_checksum text NOT NULL DEFAULT '',
    validated_at timestamptz,
    published_at timestamptz,
    retired_at timestamptz,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (resource_model_id, version_no),
    FOREIGN KEY (organization_id, resource_model_id)
        REFERENCES model.resource_models (organization_id, id)
);

ALTER TABLE model.resource_models
    ADD CONSTRAINT resource_models_current_version_fk
    FOREIGN KEY (organization_id, current_version_id)
    REFERENCES model.resource_model_versions (organization_id, id);

CREATE TABLE model.resource_model_migrations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    resource_model_id uuid NOT NULL,
    from_version_id uuid,
    to_version_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'previewing', 'processing', 'succeeded', 'failed', 'cancelled')),
    preview jsonb NOT NULL DEFAULT '{}'::jsonb,
    input_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    error_summary text,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
        ON DELETE CASCADE
);

CREATE INDEX resource_model_migrations_due_idx
    ON model.resource_model_migrations (status, created_at, id)
    WHERE status = 'queued';

-- Agent access policies grant a controlled action subset per resource model.
CREATE TABLE content.agent_access_policies (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid,
    agent_user_id uuid NOT NULL,
    resource_model_id uuid NOT NULL,
    actions text[] NOT NULL DEFAULT '{}',
    field_allowlist text[] NOT NULL DEFAULT '{}',
    draft_scope text NOT NULL DEFAULT 'none' CHECK (draft_scope IN ('none', 'read', 'write')),
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, agent_user_id, workspace_id, resource_model_id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id),
    FOREIGN KEY (organization_id, resource_model_id)
        REFERENCES model.resource_models (organization_id, id)
);

COMMIT;
