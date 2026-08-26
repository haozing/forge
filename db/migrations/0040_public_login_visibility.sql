BEGIN;

-- Public resources are readable without a member session. Keep private as a
-- backwards-compatible alias while new model policies use public/login.
ALTER TABLE content.workspaces DROP CONSTRAINT IF EXISTS workspaces_default_visibility_check;
ALTER TABLE content.workspaces ADD CONSTRAINT workspaces_default_visibility_check
    CHECK (default_visibility IN ('public', 'login', 'private', 'workspace', 'internal'));

ALTER TABLE content.containers DROP CONSTRAINT IF EXISTS containers_visibility_check;
ALTER TABLE content.containers ADD CONSTRAINT containers_visibility_check
    CHECK (visibility IN ('public', 'login', 'private', 'workspace', 'internal'));

ALTER TABLE asset.assets DROP CONSTRAINT IF EXISTS assets_visibility_check;
ALTER TABLE asset.assets ADD CONSTRAINT assets_visibility_check
    CHECK (visibility IN ('public', 'login', 'private', 'workspace', 'internal'));

COMMIT;
