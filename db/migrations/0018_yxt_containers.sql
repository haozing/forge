BEGIN;

ALTER TABLE content.containers
    ADD COLUMN IF NOT EXISTS parent_id uuid,
    ADD COLUMN IF NOT EXISTS sort_key text NOT NULL DEFAULT '';

ALTER TABLE content.containers
    ADD CONSTRAINT containers_parent_fk
    FOREIGN KEY (organization_id, parent_id)
    REFERENCES content.containers (organization_id, id);

CREATE TABLE content.container_assets (
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    container_id uuid NOT NULL,
    asset_id uuid NOT NULL REFERENCES asset.assets(id),
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (container_id, asset_id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
        ON DELETE CASCADE,
    FOREIGN KEY (organization_id, container_id)
        REFERENCES content.containers (organization_id, id)
        ON DELETE CASCADE
);

CREATE TABLE content.document_parents (
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    child_asset_id uuid PRIMARY KEY REFERENCES asset.assets(id) ON DELETE CASCADE,
    parent_asset_id uuid NOT NULL REFERENCES asset.assets(id) ON DELETE CASCADE,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (child_asset_id <> parent_asset_id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
        ON DELETE CASCADE
);

CREATE INDEX content_containers_parent_idx
    ON content.containers (organization_id, workspace_id, parent_id, sort_key, id);

CREATE INDEX content_container_assets_workspace_idx
    ON content.container_assets (organization_id, workspace_id, container_id, created_at DESC);

COMMIT;
