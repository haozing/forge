-- 0022_delivery_seo_scheduled_patterns.sql
-- Delivery gap-filling plan (docs/公开站点投递补齐与Agent技能扩展实施方案-2026-09-03.md):
-- G4 scheduled publishing, G2 path redirects, G6 cover alt text,
-- G8 content patterns. Existing rows need no backfill: every new column
-- carries a default or stays NULL-only-for-legacy.

-- ① G4 scheduled publishing. A scheduled row is an *approved* publication
-- intent waiting for its moment: approval semantics live in the review flow
-- exactly as before; the worker switches the published pointer at due time.
ALTER TABLE asset.publication_requests
    ADD COLUMN scheduled_at timestamptz,
    ADD COLUMN executed_at timestamptz;
ALTER TABLE asset.publication_requests
    DROP CONSTRAINT publication_requests_status_check;
ALTER TABLE asset.publication_requests
    ADD CONSTRAINT publication_requests_status_check
        CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled', 'scheduled'));
ALTER TABLE asset.publication_requests
    DROP CONSTRAINT publication_requests_cancel_reason_check;
ALTER TABLE asset.publication_requests
    ADD CONSTRAINT publication_requests_cancel_reason_check
        CHECK (cancel_reason IN
            ('user_cancelled', 'new_version', 'asset_archived', 'admin_cancelled', 'execution_failed'));
-- Due-row scan for the worker periodic job (partial: only scheduled rows).
CREATE INDEX publication_requests_scheduled_due_idx
    ON asset.publication_requests (scheduled_at) WHERE status = 'scheduled';
-- One open intent per asset at a time: a pending submission or a scheduled
-- (already approved / direct-deferred) row, never both.
CREATE UNIQUE INDEX publication_requests_open_per_asset_idx
    ON asset.publication_requests (asset_id) WHERE status IN ('pending', 'scheduled');

-- ② G2 path redirects. Written automatically when a binding's display_path
-- changes; both endpoints mirror the display_path CHECK so a redirect can
-- only ever target a same-site path (no open redirects by construction).
CREATE TABLE site.path_redirects (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    site_id uuid NOT NULL,
    from_path text NOT NULL CHECK (from_path ~ '^[a-z0-9][a-z0-9/-]{0,120}[a-z0-9]$'),
    to_path text NOT NULL CHECK (to_path ~ '^[a-z0-9][a-z0-9/-]{0,120}[a-z0-9]$'),
    binding_id uuid,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (site_id, from_path),
    FOREIGN KEY (organization_id, site_id)
        REFERENCES site.public_sites (organization_id, id),
    FOREIGN KEY (organization_id, binding_id)
        REFERENCES site.site_content_bindings (organization_id, id)
);

-- ③ G6 cover alt text. Alt is per-use semantics (one attachment may serve
-- many assets), so it lives on the draft link and freezes into the version
-- link at commit — same shape as the role column from 0016.
ALTER TABLE asset.asset_draft_attachments
    ADD COLUMN alt_text text NOT NULL DEFAULT '' CHECK (char_length(alt_text) <= 500);
ALTER TABLE asset.asset_version_attachments
    ADD COLUMN alt_text text NOT NULL DEFAULT '' CHECK (char_length(alt_text) <= 500);

-- ④ G8 content patterns. Org-level reusable block-tree skeletons; applying
-- a pattern appends its blocks into the target's live tree (single batched
-- transaction) and freezes via the normal commit flow.
CREATE TABLE content.patterns (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    name text NOT NULL CHECK (name ~ '^[^[:space:]]([^[:space:]]| ){0,30}[^[:space:]]$'),
    description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 500),
    blocks jsonb NOT NULL,
    source_asset_id uuid,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, name),
    FOREIGN KEY (organization_id, source_asset_id)
        REFERENCES asset.assets (organization_id, id)
);
