BEGIN;

-- Final-state contract. There are no compatibility states after this migration.
ALTER TABLE identity.users
    ADD COLUMN IF NOT EXISTS avatar_url text;

ALTER TABLE model.resource_models
    DROP CONSTRAINT IF EXISTS resource_models_content_kind_check;
ALTER TABLE model.resource_models
    ADD CONSTRAINT resource_models_content_kind_check
    CHECK (content_kind IN ('record', 'document', 'faq', 'note'));

ALTER TABLE asset.assets
    DROP CONSTRAINT IF EXISTS assets_publication_status_check;
UPDATE asset.assets SET publication_status = 'draft' WHERE publication_status = 'internal';
ALTER TABLE asset.assets
    ALTER COLUMN publication_status SET DEFAULT 'draft';
ALTER TABLE asset.assets
    ADD COLUMN IF NOT EXISTS deleted_at timestamptz;
ALTER TABLE asset.assets
    ADD CONSTRAINT assets_publication_status_check
    CHECK (publication_status IN ('draft', 'published', 'archived'));

ALTER TABLE asset.asset_versions
    DROP CONSTRAINT IF EXISTS asset_versions_workflow_status_check;
UPDATE asset.asset_versions
SET workflow_status = CASE workflow_status
    WHEN 'pending_processing' THEN 'submitted'
    WHEN 'processing' THEN 'submitted'
    WHEN 'ready' THEN 'approved'
    WHEN 'failed' THEN 'rejected'
    ELSE workflow_status
END
WHERE workflow_status IN ('pending_processing', 'processing', 'ready', 'failed');
ALTER TABLE asset.asset_versions
    ADD CONSTRAINT asset_versions_workflow_status_check
    CHECK (workflow_status IN ('draft', 'submitted', 'approved', 'rejected', 'published', 'retired'));

ALTER TABLE asset.asset_versions
    DROP CONSTRAINT IF EXISTS asset_versions_quality_check;
UPDATE asset.asset_versions
SET quality = CASE quality
    WHEN 'agent_prepared' THEN 'ai_generated'
    WHEN 'high_quality' THEN 'human_confirmed'
    ELSE quality
END
WHERE quality IN ('agent_prepared', 'high_quality');
ALTER TABLE asset.asset_versions
    ADD CONSTRAINT asset_versions_quality_check
    CHECK (quality IN ('raw', 'ai_generated', 'human_confirmed'));

ALTER TABLE content.containers
    DROP CONSTRAINT IF EXISTS containers_kind_check;
ALTER TABLE content.containers
    ADD CONSTRAINT containers_kind_check
    CHECK (kind IN ('chat', 'note', 'document', 'faq', 'content_field', 'custom'));

ALTER TABLE content.conversations
    DROP CONSTRAINT IF EXISTS conversations_source_check;
ALTER TABLE content.conversations
    ADD CONSTRAINT conversations_source_check
    CHECK (source IN ('chat_interface', 'document', 'asset', 'automation'));

CREATE TABLE IF NOT EXISTS content.workspace_invitations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    email text NOT NULL,
    role text NOT NULL DEFAULT 'member'
        CHECK (role IN ('admin', 'editor', 'reviewer', 'viewer', 'member')),
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'accepted', 'expired', 'revoked')),
    token_hash text NOT NULL UNIQUE,
    invited_by uuid NOT NULL REFERENCES identity.users(id),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    accepted_at timestamptz,
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
        ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS workspace_invitations_scope_idx
    ON content.workspace_invitations (organization_id, workspace_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS content.notifications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    recipient_user_id uuid NOT NULL REFERENCES identity.users(id),
    type text NOT NULL,
    title text NOT NULL,
    body text NOT NULL DEFAULT '',
    object_type text,
    object_id uuid,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    read_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
        ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS notifications_recipient_idx
    ON content.notifications (organization_id, workspace_id, recipient_user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS notifications_unread_idx
    ON content.notifications (organization_id, workspace_id, recipient_user_id, created_at DESC)
    WHERE read_at IS NULL;

CREATE TABLE IF NOT EXISTS automation.run_events (
    id bigserial PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    run_id uuid NOT NULL REFERENCES automation.runs(id) ON DELETE CASCADE,
    event_type text NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS automation_run_events_idx
    ON automation.run_events (organization_id, run_id, id);

COMMIT;
