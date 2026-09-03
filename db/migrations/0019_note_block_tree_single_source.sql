-- 0019: conversation notes converge on the block tree as the single editable
-- source of truth (design: docs/会话笔记单一事实源整改设计方案.md).
--
-- One conversation owns one note document (container + blocks). Chat messages
-- ARE blocks of that document — appended in place, no per-message container
-- version copies, no separate chat container. Markdown loses its second
-- master: it is only the frozen render of the tree at commit time, stored on
-- the immutable asset version together with a reference-style block snapshot.
-- Dev-phase rebuild: legacy runtime rows are purged, no data migration.
-- Order matters: every FK that points into the purged tables is dropped with
-- its column/table first, then the rows go, then the structural ALTERs run
-- against the emptied tables.

-- 1. Drop referencing columns/tables so the purge below is legal.
ALTER TABLE content.derivation_sources
    DROP CONSTRAINT IF EXISTS derivation_sources_source_container_version_id_fkey,
    DROP COLUMN source_container_version_id,
    DROP CONSTRAINT IF EXISTS derivation_sources_source_container_id_fkey;
DROP TABLE content.asset_version_content_fields;
ALTER TABLE content.conversations DROP CONSTRAINT IF EXISTS conversations_organization_id_container_id_fkey;
ALTER TABLE content.conversations DROP COLUMN container_id;
ALTER TABLE content.note_bindings
    DROP CONSTRAINT IF EXISTS note_bindings_note_container_fkey,
    DROP COLUMN note_container_id,
    DROP COLUMN last_synced_message_sequence;

-- 2. Purge legacy runtime rows.
DELETE FROM content.derivation_sources;
DELETE FROM content.block_placements;
UPDATE content.containers SET current_version_id = NULL;
DELETE FROM content.container_versions;
DELETE FROM content.containers;

-- 3. The document container belongs to its asset and carries the live-tree
--    optimistic-lock revision. Kind collapses to 'note': every surviving
--    container is a conversation note document.
ALTER TABLE content.containers
    ADD COLUMN asset_id uuid NOT NULL REFERENCES asset.assets(id),
    ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0);
ALTER TABLE content.containers DROP CONSTRAINT IF EXISTS containers_kind_check;
ALTER TABLE content.containers
    ADD CONSTRAINT containers_kind_check CHECK (kind IN ('note')),
    ADD CONSTRAINT containers_asset_unique UNIQUE (asset_id);

-- 4. Placements hang off the live tree; ordering is the sparse position.
ALTER TABLE content.block_placements
    ADD COLUMN container_id uuid NOT NULL;
ALTER TABLE content.block_placements
    DROP CONSTRAINT IF EXISTS block_placements_container_version_id_fkey,
    DROP COLUMN container_version_id,
    ADD CONSTRAINT block_placements_container_fkey
        FOREIGN KEY (container_id) REFERENCES content.containers(id);
CREATE INDEX block_placements_container_idx
    ON content.block_placements (container_id, position);

-- 5. Frozen tree snapshots live on the immutable version; container_versions
--    and the versioned-placement copy semantics are gone.
ALTER TABLE asset.asset_versions ADD COLUMN blocks jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE content.containers DROP COLUMN IF EXISTS current_version_id;
DROP TABLE content.container_versions;

-- 6. Visibility matches the asset three-tier enum (the service default
--    'private' violated this CHECK and 500ed creation).
ALTER TABLE content.conversations DROP CONSTRAINT conversations_visibility_check;
ALTER TABLE content.conversations
    ADD CONSTRAINT conversations_visibility_check
    CHECK (visibility IN ('workspace', 'organization', 'public'));

-- 7. Restore the derivation source integrity against the rebuilt containers.
ALTER TABLE content.derivation_sources
    ADD CONSTRAINT derivation_sources_source_container_id_fkey
        FOREIGN KEY (source_container_id) REFERENCES content.containers(id);
