-- 0010_site.sql
-- Public site storage boundary fixed in phase 0 (service arrives in phase 5);
-- the phase 5 (P5-2) baseline rewrite extends both tables in place per the
-- stage 5 plan section 2.1. No compatibility shims: the baseline is rewritten
-- and empty databases are rebuilt.
-- Bindings store asset identity and display configuration only; the published
-- version is always resolved from the Asset's current published pointer.

CREATE TABLE site.public_sites (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    slug text NOT NULL,
    name text NOT NULL,
    -- Custom domain is stored for deployment-layer routing only; phase 5
    -- routes by slug (plan section 8.3). Uniqueness is global like slug.
    domain text UNIQUE CHECK (domain ~ '^[a-z0-9.-]+$'),
    homepage_config jsonb NOT NULL DEFAULT '{}'::jsonb,
    navigation_config jsonb NOT NULL DEFAULT '{}'::jsonb,
    template text NOT NULL DEFAULT 'blog' CHECK (template IN ('blog', 'pro')),
    -- Visibility ceiling of the site (plan D5'): public < organization <
    -- workspace. The compiler narrows it by the visitor tier at read time.
    default_content_scope text NOT NULL DEFAULT 'public'
        CHECK (default_content_scope IN ('public', 'organization', 'workspace')),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (slug),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
);

-- Slug rule (service-layer validation, mirrored nowhere in SQL):
-- ^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$  (3..64 chars, no leading/trailing dash)
-- The database keeps only the global UNIQUE(slug) constraint; malformed slugs
-- are rejected by internal/site before they reach this table.

CREATE TABLE site.site_content_bindings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    site_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    -- Public path segment under the site, e.g. "posts/hello-world".
    -- CHECK mirrors the service-layer rule: 2..122 chars, lowercase
    -- alphanumerics with interior slashes and dashes only.
    display_path text NOT NULL CHECK (display_path ~ '^[a-z0-9][a-z0-9/-]{0,120}[a-z0-9]$'),
    content_type text NOT NULL DEFAULT 'article'
        CHECK (content_type IN ('article', 'featured', 'about')),
    section_slug text NOT NULL DEFAULT '',
    sort_order integer NOT NULL DEFAULT 0,
    on_homepage boolean NOT NULL DEFAULT false,
    on_navigation boolean NOT NULL DEFAULT false,
    display_config jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- Denormalized mirror of the bound asset's published_at, set at binding
    -- time and refreshed by the worker on asset.published events. Reads use
    -- COALESCE(binding.display_published_at, asset.published_at): the
    -- double-source semantics is fixed by the plan (section 8.4).
    display_published_at timestamptz,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_id, asset_id),
    UNIQUE (site_id, display_path),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, site_id)
        REFERENCES site.public_sites (organization_id, id),
    FOREIGN KEY (organization_id, asset_id)
        REFERENCES asset.assets (organization_id, id)
);

CREATE INDEX site_content_bindings_site_idx
    ON site.site_content_bindings (site_id, created_at);

-- Section listing and homepage section queries of the phase 5 presentation
-- layer filter along these two shapes.
CREATE INDEX site_content_bindings_section_idx
    ON site.site_content_bindings (site_id, content_type, section_slug, sort_order);

CREATE INDEX site_content_bindings_homepage_idx
    ON site.site_content_bindings (site_id, on_homepage);
