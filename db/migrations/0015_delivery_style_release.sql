-- 0015_delivery_style_release.sql
-- SSR delivery layer (docs/公开站点SSR投递与样式参数空间设计方案.md §7.4/§12):
-- the L1 style parameter space, immutable site config releases and the
-- cache invalidation queue. Existing rows need no backfill: style_config
-- defaults to '{}', published_release_id stays NULL and the public render
-- falls back to the working columns until the first release is published.

CREATE TABLE site.site_releases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    site_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    -- Immutable config snapshot: {homepage_config, navigation_config,
    -- style_config, template}. Content is never pinned here; bindings keep
    -- resolving the live current_published_version_id pointer (v2 §7.2).
    config jsonb NOT NULL,
    published_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (site_id, revision),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id),
    FOREIGN KEY (organization_id, site_id)
        REFERENCES site.public_sites (organization_id, id)
);

ALTER TABLE site.public_sites
    ADD COLUMN style_config jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE site.public_sites
    ADD COLUMN published_release_id uuid;
ALTER TABLE site.public_sites
    ADD CONSTRAINT public_sites_published_release_fk
    FOREIGN KEY (organization_id, published_release_id)
    REFERENCES site.site_releases (organization_id, id);

CREATE INDEX site_releases_site_idx ON site.site_releases (site_id, revision DESC);

-- The cache invalidation queue: the worker's delivery.cache consumer appends
-- rows, the api process polls and applies them, then stamps processed_at.
-- tier '' means both visitor bands; route_prefix '' means the whole site.
CREATE SCHEMA IF NOT EXISTS delivery;

CREATE TABLE delivery.cache_invalidations (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id uuid NOT NULL,
    site_id uuid NOT NULL,
    tier text NOT NULL DEFAULT '' CHECK (tier IN ('', 'anon', 'member')),
    route_prefix text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    FOREIGN KEY (organization_id, site_id)
        REFERENCES site.public_sites (organization_id, id)
);

CREATE INDEX cache_invalidations_pending_idx
    ON delivery.cache_invalidations (id) WHERE processed_at IS NULL;
