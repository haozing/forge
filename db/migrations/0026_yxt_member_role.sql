BEGIN;

ALTER TABLE content.workspace_members
    DROP CONSTRAINT IF EXISTS workspace_members_role_check;
ALTER TABLE content.workspace_members
    ADD CONSTRAINT workspace_members_role_check
    CHECK (role IN ('owner', 'admin', 'editor', 'reviewer', 'member', 'viewer'));

ALTER TABLE content.workspace_invitations
    DROP CONSTRAINT IF EXISTS workspace_invitations_role_check;
ALTER TABLE content.workspace_invitations
    ADD CONSTRAINT workspace_invitations_role_check
    CHECK (role IN ('admin', 'editor', 'reviewer', 'member', 'viewer'));

COMMIT;
