-- 0003_workspace.sql
-- Workspace is the content permission boundary. Membership roles are exactly
-- admin/editor/reviewer/viewer; there is no workspace owner. Containers and
-- blocks (content organization structures, never permission carriers) and the
-- not-yet-migrated invitation/preference runtime tables live here.

CREATE TABLE content.workspaces (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    slug text NOT NULL,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 1000),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    default_agent_application_id uuid,
    default_resource_model_id uuid,
    settings jsonb NOT NULL DEFAULT '{}'::jsonb,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_by uuid NOT NULL REFERENCES identity.users(id),
    archived_at timestamptz,
    archived_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, slug),
    UNIQUE (organization_id, id)
);

CREATE INDEX content_workspaces_org_status_idx
    ON content.workspaces (organization_id, status, created_at, id);

-- identity.users (organization_id, id) unique key for composite foreign keys.
ALTER TABLE identity.users
    ADD CONSTRAINT identity_users_org_id_uq UNIQUE (organization_id, id);

CREATE TABLE content.workspace_members (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role text NOT NULL CHECK (role IN ('admin', 'editor', 'reviewer', 'viewer')),
    granted_by uuid NOT NULL REFERENCES identity.users(id),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (workspace_id, user_id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id),
    FOREIGN KEY (organization_id, user_id)
        REFERENCES identity.users (organization_id, id)
);

CREATE INDEX workspace_members_user_idx
    ON content.workspace_members (user_id, workspace_id);
CREATE INDEX workspace_members_role_idx
    ON content.workspace_members (workspace_id, role, id);

CREATE TABLE organization.member_invitations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    email text NOT NULL,
    display_name text,
    organization_role text NOT NULL CHECK (organization_role IN ('admin', 'member')),
    authority_scope text NOT NULL CHECK (authority_scope IN ('organization', 'workspace')),
    scope_workspace_id uuid,
    token_hash text NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'revoked', 'expired')),
    expires_at timestamptz NOT NULL,
    invited_by uuid NOT NULL REFERENCES identity.users(id),
    accepted_by uuid REFERENCES identity.users(id),
    accepted_at timestamptz,
    revoked_at timestamptz,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((authority_scope = 'workspace') = (scope_workspace_id IS NOT NULL)),
    FOREIGN KEY (organization_id, scope_workspace_id)
        REFERENCES content.workspaces (organization_id, id)
);

CREATE UNIQUE INDEX member_invitations_pending_email_idx
    ON organization.member_invitations (email)
    WHERE status = 'pending';
CREATE INDEX member_invitations_org_status_idx
    ON organization.member_invitations (organization_id, status, created_at DESC, id);

CREATE TABLE organization.invitation_workspace_grants (
    invitation_id uuid NOT NULL REFERENCES organization.member_invitations(id) ON DELETE CASCADE,
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    role text NOT NULL CHECK (role IN ('admin', 'editor', 'reviewer', 'viewer')),
    PRIMARY KEY (invitation_id, workspace_id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
);

CREATE TABLE content.workspace_agent_applications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    agent_application_id uuid NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (workspace_id, agent_application_id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
);

CREATE TABLE content.workspace_settings_revisions (
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

CREATE TABLE content.member_preferences (
    user_id uuid PRIMARY KEY REFERENCES identity.users(id) ON DELETE CASCADE,
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    preferences jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE content.workspace_invitations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    email text NOT NULL,
    role text NOT NULL CHECK (role IN ('admin', 'editor', 'reviewer', 'viewer')),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'expired', 'revoked')),
    token_hash text NOT NULL UNIQUE,
    invited_by uuid NOT NULL REFERENCES identity.users(id),
    expires_at timestamptz NOT NULL,
    accepted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
);

CREATE TABLE content.containers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid,
    kind text NOT NULL CHECK (kind IN ('chat', 'note', 'document', 'faq', 'content_field', 'custom')),
    title text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    visibility text NOT NULL DEFAULT 'workspace'
        CHECK (visibility IN ('workspace', 'organization', 'public')),
    parent_id uuid,
    sort_key text NOT NULL DEFAULT '',
    current_version_id uuid,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
);

CREATE INDEX content_containers_workspace_idx
    ON content.containers (organization_id, workspace_id, status, updated_at DESC);
CREATE INDEX content_containers_parent_idx
    ON content.containers (organization_id, workspace_id, parent_id, sort_key, id);

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

CREATE INDEX content_container_versions_container_idx
    ON content.container_versions (organization_id, container_id, version_no DESC);

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

CREATE INDEX content_block_placements_version_idx
    ON content.block_placements (organization_id, container_version_id, parent_placement_id, position);

