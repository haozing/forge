-- 0016_style_pages_comments.sql
-- Capability expansion phase 2 (docs/公开站点样式与页面能力扩展设计方案.md):
-- L2 custom CSS, org style presets, site comments, attachment roles (cover
-- images). Existing rows need no backfill: new columns carry defaults.

ALTER TABLE site.public_sites
    ADD COLUMN custom_css text NOT NULL DEFAULT '';
ALTER TABLE site.public_sites
    ADD COLUMN comments_mode text NOT NULL DEFAULT 'moderated'
        CHECK (comments_mode IN ('off', 'moderated', 'open'));

-- Org-level custom style presets ("save current look as a preset"). Applying
-- a preset is copy semantics resolved at write time; rows are data bundles.
CREATE TABLE site.style_presets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    name text NOT NULL CHECK (name ~ '^[^[:space:]]([^[:space:]]| ){0,30}[^[:space:]]$'),
    style_config jsonb NOT NULL,
    custom_css text NOT NULL DEFAULT '',
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, name)
);

-- Site comments v1: members only, flat, plain text, moderated by default.
CREATE TABLE site.site_comments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    site_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    display_path text NOT NULL,
    author_user_id uuid NOT NULL REFERENCES identity.users(id),
    body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 2000),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('visible', 'pending', 'rejected')),
    moderated_by uuid REFERENCES identity.users(id),
    moderated_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, site_id)
        REFERENCES site.public_sites (organization_id, id),
    FOREIGN KEY (organization_id, asset_id)
        REFERENCES asset.assets (organization_id, id)
);
CREATE INDEX site_comments_site_asset_idx
    ON site.site_comments (site_id, asset_id, created_at DESC);

-- Version attachment roles: one cover per asset version; covers are
-- versioned with their content (changing the cover = new version). The
-- draft link carries the role so commit materializes it into the version.
ALTER TABLE asset.asset_version_attachments
    ADD COLUMN role text NOT NULL DEFAULT 'body' CHECK (role IN ('body', 'cover'));
CREATE UNIQUE INDEX asset_version_one_cover
    ON asset.asset_version_attachments (asset_version_id) WHERE role = 'cover';
ALTER TABLE asset.asset_draft_attachments
    ADD COLUMN role text NOT NULL DEFAULT 'body' CHECK (role IN ('body', 'cover'));
