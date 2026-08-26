BEGIN;

ALTER TABLE content.workspaces
    ADD COLUMN IF NOT EXISTS description text NOT NULL DEFAULT '';

ALTER TABLE content.workspace_members
    DROP CONSTRAINT IF EXISTS workspace_members_role_check;

ALTER TABLE content.workspace_members
    ADD CONSTRAINT workspace_members_role_check
    CHECK (role IN ('owner', 'admin', 'editor', 'reviewer', 'viewer'));

CREATE TABLE IF NOT EXISTS content.member_preferences (
    user_id uuid PRIMARY KEY REFERENCES identity.users(id) ON DELETE CASCADE,
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    preferences jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS content.workspace_settings_revisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    settings jsonb NOT NULL DEFAULT '{}'::jsonb,
    changed_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, workspace_id, revision),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
        ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS asset.asset_reviews (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    asset_id uuid NOT NULL REFERENCES asset.assets(id),
    asset_version_id uuid NOT NULL REFERENCES asset.asset_versions(id),
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'rejected', 'superseded', 'cancelled')),
    submitted_by uuid NOT NULL REFERENCES identity.users(id),
    reviewed_by uuid REFERENCES identity.users(id),
    comment text NOT NULL DEFAULT '',
    submitted_at timestamptz NOT NULL DEFAULT now(),
    reviewed_at timestamptz,
    UNIQUE (organization_id, asset_version_id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
        ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS workspace_members_user_idx
    ON content.workspace_members (organization_id, user_id, workspace_id);

CREATE INDEX IF NOT EXISTS asset_reviews_workspace_status_idx
    ON asset.asset_reviews (organization_id, workspace_id, status, submitted_at DESC);

COMMIT;
