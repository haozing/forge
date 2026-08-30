-- 0006_tags.sql
-- Tag storage boundary fixed in phase 0 (service arrives in phase 2):
-- workspace-scoped definitions, draft relations, immutable version relations
-- and AI suggestion records.

CREATE TABLE asset.tags (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    normalized_key text NOT NULL CHECK (char_length(normalized_key) BETWEEN 1 AND 100),
    display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 100),
    slug text NOT NULL CHECK (char_length(slug) BETWEEN 1 AND 120),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_by uuid NOT NULL,
    updated_by uuid,
    archived_at timestamptz,
    archived_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, workspace_id, normalized_key),
    UNIQUE (organization_id, workspace_id, slug),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id),
    CHECK ((status = 'archived') = (archived_at IS NOT NULL))
);

CREATE INDEX tags_workspace_status_key_idx
    ON asset.tags (workspace_id, status, normalized_key, id);
CREATE INDEX tags_workspace_status_name_idx
    ON asset.tags (workspace_id, status, lower(display_name), id);
CREATE INDEX tags_workspace_created_idx
    ON asset.tags (workspace_id, created_at DESC, id DESC);

CREATE TABLE asset.asset_draft_tags (
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    asset_draft_id uuid NOT NULL,
    tag_id uuid NOT NULL,
    source text NOT NULL DEFAULT 'manual'
        CHECK (source IN ('manual', 'api', 'webhook', 'import', 'ai')),
    confidence numeric(4, 3),
    added_by uuid NOT NULL,
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (asset_draft_id, tag_id),
    CHECK ((source = 'ai') = (confidence IS NOT NULL)),
    CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
    FOREIGN KEY (organization_id, asset_draft_id)
        REFERENCES asset.asset_drafts (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, tag_id)
        REFERENCES asset.tags (organization_id, id)
);

CREATE INDEX asset_draft_tags_tag_idx
    ON asset.asset_draft_tags (tag_id, asset_draft_id);

CREATE TABLE asset.asset_version_tags (
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    asset_version_id uuid NOT NULL,
    tag_id uuid NOT NULL,
    source text NOT NULL DEFAULT 'manual'
        CHECK (source IN ('manual', 'api', 'webhook', 'import', 'ai')),
    confidence numeric(4, 3),
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (asset_version_id, tag_id),
    CHECK ((source = 'ai') = (confidence IS NOT NULL)),
    CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
    FOREIGN KEY (organization_id, asset_version_id)
        REFERENCES asset.asset_versions (organization_id, id),
    FOREIGN KEY (organization_id, tag_id)
        REFERENCES asset.tags (organization_id, id)
);

CREATE INDEX asset_version_tags_tag_idx
    ON asset.asset_version_tags (tag_id, asset_version_id);

CREATE TRIGGER asset_version_tags_sealed_guard
    BEFORE INSERT OR UPDATE OR DELETE ON asset.asset_version_tags
    FOR EACH ROW EXECUTE FUNCTION asset.reject_sealed_relation_change();

CREATE TABLE asset.asset_version_tag_suggestions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    source_version_id uuid NOT NULL,
    suggested_key text NOT NULL,
    suggested_display_name text NOT NULL DEFAULT '',
    resolved_tag_id uuid,
    confidence numeric(4, 3) NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'rejected')),
    -- Phase 4 task provenance: run_id arrives in 0008 (automation.runs does not
    -- exist yet at this point in the baseline, so its FK cannot be declared
    -- here); agent_application_id/citation/is_new land with the table.
    agent_application_id uuid,
    citation jsonb NOT NULL DEFAULT '{}'::jsonb,
    is_new boolean NOT NULL DEFAULT false,
    reviewed_by uuid,
    reviewed_at timestamptz,
    accepted_into_draft_id uuid,
    materialized_version_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    CHECK ((status = 'accepted')
        = (reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL
           AND resolved_tag_id IS NOT NULL AND accepted_into_draft_id IS NOT NULL)),
    CHECK ((status = 'rejected')
        = (reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL
           AND resolved_tag_id IS NULL AND accepted_into_draft_id IS NULL
           AND materialized_version_id IS NULL)),
    FOREIGN KEY (organization_id, source_version_id)
        REFERENCES asset.asset_versions (organization_id, id)
);

CREATE INDEX tag_suggestions_source_idx
    ON asset.asset_version_tag_suggestions (source_version_id, status, id);
CREATE INDEX tag_suggestions_draft_idx
    ON asset.asset_version_tag_suggestions (accepted_into_draft_id, status, id);

-- A version carries at most 100 tags; enforced for relations written before seal.
CREATE FUNCTION asset.enforce_version_tag_limit() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    tag_count integer;
    version_id uuid;
BEGIN
    version_id := COALESCE(NEW.asset_version_id, OLD.asset_version_id);
    SELECT count(*) INTO tag_count FROM asset.asset_version_tags WHERE asset_version_id = version_id;
    IF tag_count > 100 THEN
        RAISE EXCEPTION 'asset version exceeds 100 tags'
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER asset_version_tags_limit_guard
    AFTER INSERT ON asset.asset_version_tags
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION asset.enforce_version_tag_limit();

