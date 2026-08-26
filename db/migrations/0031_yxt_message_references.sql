BEGIN;

CREATE TABLE content.message_references (
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    block_revision_id uuid NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    asset_id uuid NOT NULL,
    asset_version_id uuid NOT NULL,
    title text NOT NULL DEFAULT '',
    url text NOT NULL DEFAULT '',
    source_excerpt text NOT NULL DEFAULT '',
    updated_at_snapshot text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (block_revision_id, ordinal),
    FOREIGN KEY (organization_id, block_revision_id)
        REFERENCES content.block_revisions (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, asset_id)
        REFERENCES asset.assets (organization_id, id),
    FOREIGN KEY (organization_id, asset_version_id)
        REFERENCES asset.asset_versions (organization_id, id)
);

CREATE INDEX message_references_asset_idx
    ON content.message_references (organization_id, asset_id, asset_version_id);

COMMIT;
