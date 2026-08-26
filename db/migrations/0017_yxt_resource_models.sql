BEGIN;

ALTER TABLE model.resource_models
    ADD COLUMN IF NOT EXISTS workspace_id uuid,
    ADD COLUMN IF NOT EXISTS description text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS content_kind text NOT NULL DEFAULT 'record',
    ADD COLUMN IF NOT EXISTS model_capabilities jsonb NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE model.resource_models
    ADD CONSTRAINT resource_models_content_kind_check
    CHECK (content_kind IN ('record', 'document', 'faq'));

ALTER TABLE model.resource_models
    ADD CONSTRAINT resource_models_workspace_fk
    FOREIGN KEY (organization_id, workspace_id)
    REFERENCES content.workspaces (organization_id, id);

ALTER TABLE model.resource_model_versions
    ADD COLUMN IF NOT EXISTS list_schema jsonb NOT NULL DEFAULT '{"columns":[],"filters":[]}'::jsonb,
    ADD COLUMN IF NOT EXISTS schema_checksum text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS validated_at timestamptz,
    ADD COLUMN IF NOT EXISTS published_at timestamptz,
    ADD COLUMN IF NOT EXISTS retired_at timestamptz;

ALTER TABLE asset.assets
    ADD COLUMN IF NOT EXISTS workspace_id uuid,
    ADD COLUMN IF NOT EXISTS visibility text NOT NULL DEFAULT 'private';

ALTER TABLE asset.assets
    ADD CONSTRAINT assets_visibility_check
    CHECK (visibility IN ('private', 'workspace', 'internal'));

ALTER TABLE asset.assets
    ADD CONSTRAINT assets_workspace_fk
    FOREIGN KEY (organization_id, workspace_id)
    REFERENCES content.workspaces (organization_id, id);

ALTER TABLE asset.asset_versions
    ADD COLUMN IF NOT EXISTS workspace_id uuid,
    ADD COLUMN IF NOT EXISTS review_status text NOT NULL DEFAULT 'none',
    ADD COLUMN IF NOT EXISTS tags jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS source jsonb NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE asset.asset_versions
    ADD CONSTRAINT asset_versions_review_status_check
    CHECK (review_status IN ('none', 'pending', 'approved', 'rejected', 'superseded', 'cancelled'));

ALTER TABLE asset.asset_versions
    ADD CONSTRAINT asset_versions_workspace_fk
    FOREIGN KEY (organization_id, workspace_id)
    REFERENCES content.workspaces (organization_id, id);

CREATE TABLE model.resource_model_migrations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    resource_model_id uuid NOT NULL REFERENCES model.resource_models(id),
    from_version_id uuid REFERENCES model.resource_model_versions(id),
    to_version_id uuid NOT NULL REFERENCES model.resource_model_versions(id),
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

CREATE INDEX IF NOT EXISTS resource_models_workspace_idx
    ON model.resource_models (organization_id, workspace_id, status, updated_at DESC);

CREATE INDEX IF NOT EXISTS assets_workspace_model_idx
    ON asset.assets (organization_id, workspace_id, resource_model_id, publication_status, updated_at DESC);

CREATE INDEX IF NOT EXISTS asset_versions_workspace_review_idx
    ON asset.asset_versions (organization_id, workspace_id, review_status, created_at DESC);

COMMIT;
