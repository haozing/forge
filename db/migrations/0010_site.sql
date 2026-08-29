-- 0010_site.sql
-- Public site storage boundary fixed in phase 0 (service arrives in phase 5).
-- Bindings store asset identity and display configuration only; the published
-- version is always resolved from the Asset's current published pointer.

CREATE TABLE site.public_sites (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    slug text NOT NULL,
    name text NOT NULL,
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

CREATE TABLE site.site_content_bindings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    site_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    display_config jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_id, asset_id),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, site_id)
        REFERENCES site.public_sites (organization_id, id),
    FOREIGN KEY (organization_id, asset_id)
        REFERENCES asset.assets (organization_id, id)
);

CREATE INDEX site_content_bindings_site_idx
    ON site.site_content_bindings (site_id, created_at);

