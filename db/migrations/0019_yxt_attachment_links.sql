BEGIN;

ALTER TABLE asset.attachments
    ADD COLUMN IF NOT EXISTS deleted_at timestamptz;

CREATE TABLE asset.attachment_links (
    attachment_id uuid NOT NULL REFERENCES asset.attachments(id) ON DELETE CASCADE,
    asset_version_id uuid NOT NULL REFERENCES asset.asset_versions(id) ON DELETE CASCADE,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (attachment_id, asset_version_id)
);

CREATE INDEX asset_attachments_active_idx
    ON asset.attachments (organization_id, asset_version_id, created_at DESC)
    WHERE deleted_at IS NULL;

COMMIT;
