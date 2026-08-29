-- 0005_assets.sql
-- Asset carries the only publication status; AssetDraft is the single shared
-- editable working copy; AssetVersion is an immutable, sealed snapshot.
-- Processing state lives in content.processing_jobs, publication approval in
-- asset.publication_requests.

CREATE TABLE asset.raw_inputs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid,
    submitted_by uuid,
    source_type text NOT NULL,
    content_type text,
    external_ref text,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    content_checksum text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE asset.assets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    resource_model_id uuid NOT NULL,
    current_working_version_id uuid,
    current_published_version_id uuid,
    publication_status text NOT NULL DEFAULT 'draft'
        CHECK (publication_status IN ('draft', 'published', 'archived')),
    visibility text NOT NULL DEFAULT 'workspace'
        CHECK (visibility IN ('workspace', 'organization', 'public')),
    draft_id uuid,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    published_at timestamptz,
    deleted_at timestamptz,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    CHECK ((publication_status = 'published') = (current_published_version_id IS NOT NULL)),
    CHECK ((publication_status = 'published') = (published_at IS NOT NULL))
);

-- The working pointer and the shared draft must exist by commit time; the
-- deferral lets create transactions insert asset, version and draft in order.
-- A deferred constraint trigger keeps the tuple captured at INSERT time, so
-- the check must re-read the row's current value instead of using NEW.
CREATE FUNCTION asset.require_asset_pointers() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    working uuid;
    draft uuid;
BEGIN
    SELECT current_working_version_id, draft_id INTO working, draft
    FROM asset.assets WHERE id = NEW.id;
    IF working IS NULL THEN
        RAISE EXCEPTION 'asset requires a current working version'
            USING ERRCODE = 'not_null_violation';
    END IF;
    IF draft IS NULL THEN
        RAISE EXCEPTION 'asset requires a shared draft'
            USING ERRCODE = 'not_null_violation';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER asset_pointers_required
    AFTER INSERT ON asset.assets
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION asset.require_asset_pointers();

CREATE INDEX assets_workspace_model_idx
    ON asset.assets (organization_id, workspace_id, resource_model_id, publication_status, updated_at DESC);
CREATE INDEX assets_workspace_updated_idx
    ON asset.assets (organization_id, workspace_id, updated_at DESC, id)
    WHERE deleted_at IS NULL;
CREATE INDEX assets_published_model_idx
    ON asset.assets (organization_id, resource_model_id)
    WHERE publication_status = 'published';

CREATE TABLE asset.asset_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    resource_model_id uuid NOT NULL,
    resource_model_version_id uuid NOT NULL,
    version_no integer NOT NULL CHECK (version_no > 0),
    origin text NOT NULL DEFAULT 'human'
        CHECK (origin IN ('human', 'imported', 'ai_generated', 'ai_assisted')),
    confirmation_status text NOT NULL DEFAULT 'unconfirmed'
        CHECK (confirmation_status IN ('unconfirmed', 'human_confirmed')),
    confirmed_by uuid,
    confirmed_at timestamptz,
    title text NOT NULL DEFAULT '',
    summary text NOT NULL DEFAULT '',
    markdown text NOT NULL DEFAULT '',
    fields jsonb NOT NULL DEFAULT '{}'::jsonb,
    source_raw_input_id uuid REFERENCES asset.raw_inputs(id),
    parent_version_id uuid,
    content_checksum text NOT NULL,
    sealed_at timestamptz,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, asset_id, id),
    UNIQUE (asset_id, version_no),
    CHECK ((confirmation_status = 'human_confirmed')
        = (confirmed_by IS NOT NULL AND confirmed_at IS NOT NULL))
);

CREATE INDEX asset_versions_asset_idx
    ON asset.asset_versions (asset_id, version_no DESC);
CREATE INDEX asset_versions_workspace_idx
    ON asset.asset_versions (organization_id, workspace_id, created_at DESC);

-- Composite tenant-scoped foreign keys for the asset pointer graph.
ALTER TABLE asset.assets
    ADD CONSTRAINT assets_workspace_fk
    FOREIGN KEY (organization_id, workspace_id)
    REFERENCES content.workspaces (organization_id, id);
ALTER TABLE asset.assets
    ADD CONSTRAINT assets_model_fk
    FOREIGN KEY (organization_id, resource_model_id)
    REFERENCES model.resource_models (organization_id, id);
ALTER TABLE asset.assets
    ADD CONSTRAINT assets_working_version_fk
    FOREIGN KEY (organization_id, current_working_version_id)
    REFERENCES asset.asset_versions (organization_id, id);
ALTER TABLE asset.assets
    ADD CONSTRAINT assets_published_version_fk
    FOREIGN KEY (organization_id, current_published_version_id)
    REFERENCES asset.asset_versions (organization_id, id);
ALTER TABLE asset.asset_versions
    ADD CONSTRAINT asset_versions_workspace_fk
    FOREIGN KEY (organization_id, workspace_id)
    REFERENCES content.workspaces (organization_id, id);
ALTER TABLE asset.asset_versions
    ADD CONSTRAINT asset_versions_asset_fk
    FOREIGN KEY (organization_id, asset_id)
    REFERENCES asset.assets (organization_id, id);
ALTER TABLE asset.asset_versions
    ADD CONSTRAINT asset_versions_model_fk
    FOREIGN KEY (organization_id, resource_model_id)
    REFERENCES model.resource_models (organization_id, id);
ALTER TABLE asset.asset_versions
    ADD CONSTRAINT asset_versions_model_version_fk
    FOREIGN KEY (organization_id, resource_model_version_id)
    REFERENCES model.resource_model_versions (organization_id, id);
ALTER TABLE asset.asset_versions
    ADD CONSTRAINT asset_versions_parent_fk
    FOREIGN KEY (organization_id, asset_id, parent_version_id)
    REFERENCES asset.asset_versions (organization_id, asset_id, id);

-- Version content is immutable from the moment the row exists; sealed_at may
-- only move from NULL to a timestamp inside the creating transaction.
CREATE FUNCTION asset.forbid_version_content_update() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id
        OR NEW.asset_id IS DISTINCT FROM OLD.asset_id
        OR NEW.version_no IS DISTINCT FROM OLD.version_no
        OR NEW.origin IS DISTINCT FROM OLD.origin
        OR NEW.confirmation_status IS DISTINCT FROM OLD.confirmation_status
        OR NEW.confirmed_by IS DISTINCT FROM OLD.confirmed_by
        OR NEW.confirmed_at IS DISTINCT FROM OLD.confirmed_at
        OR NEW.title IS DISTINCT FROM OLD.title
        OR NEW.summary IS DISTINCT FROM OLD.summary
        OR NEW.markdown IS DISTINCT FROM OLD.markdown
        OR NEW.fields IS DISTINCT FROM OLD.fields
        OR NEW.parent_version_id IS DISTINCT FROM OLD.parent_version_id
        OR NEW.content_checksum IS DISTINCT FROM OLD.content_checksum
        OR NEW.resource_model_id IS DISTINCT FROM OLD.resource_model_id
        OR NEW.resource_model_version_id IS DISTINCT FROM OLD.resource_model_version_id
    THEN
        RAISE EXCEPTION 'asset version content is immutable'
            USING ERRCODE = 'restrict_violation';
    END IF;
    IF OLD.sealed_at IS NOT NULL AND NEW.sealed_at IS DISTINCT FROM OLD.sealed_at THEN
        RAISE EXCEPTION 'asset version is sealed'
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER asset_versions_immutable_update
    BEFORE UPDATE ON asset.asset_versions
    FOR EACH ROW EXECUTE FUNCTION asset.forbid_version_content_update();

-- Every version must be sealed before its creating transaction commits.
-- The deferred trigger keeps the INSERT-time tuple, so it must re-read the
-- row's current sealed_at instead of testing NEW.
CREATE FUNCTION asset.require_version_sealed() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    sealed timestamptz;
BEGIN
    SELECT sealed_at INTO sealed FROM asset.asset_versions WHERE id = NEW.id;
    IF sealed IS NULL THEN
        RAISE EXCEPTION 'asset version must be sealed in its creating transaction'
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER asset_versions_must_seal
    AFTER INSERT ON asset.asset_versions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION asset.require_version_sealed();

-- One shared draft per asset; assets.draft_id closes the loop with a
-- deferrable composite key so create transactions may insert in any order.
CREATE TABLE asset.asset_drafts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    asset_id uuid NOT NULL UNIQUE,
    base_version_id uuid NOT NULL,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    committed_revision bigint NOT NULL DEFAULT 1 CHECK (committed_revision > 0),
    title text NOT NULL DEFAULT '',
    summary text NOT NULL DEFAULT '',
    markdown text NOT NULL DEFAULT '',
    fields jsonb NOT NULL DEFAULT '{}'::jsonb,
    origin text NOT NULL DEFAULT 'human'
        CHECK (origin IN ('human', 'imported', 'ai_generated', 'ai_assisted')),
    updated_by uuid,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, asset_id, id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id),
    FOREIGN KEY (organization_id, asset_id)
        REFERENCES asset.assets (organization_id, id)
);

-- Candidate versions (agent output, model migrations) are legitimate without
-- being a draft base; only draft.base_version_id is constrained to the same
-- asset via asset_drafts_base_version_fk.

-- (organization_id, asset_id, id) unique on versions for the FK above.
ALTER TABLE asset.asset_versions
    ADD CONSTRAINT asset_versions_org_asset_id_uq UNIQUE (organization_id, asset_id, id);

ALTER TABLE asset.asset_drafts
    ADD CONSTRAINT asset_drafts_base_version_fk
    FOREIGN KEY (organization_id, asset_id, base_version_id)
    REFERENCES asset.asset_versions (organization_id, asset_id, id);

ALTER TABLE asset.assets
    ADD CONSTRAINT assets_draft_fk
    FOREIGN KEY (organization_id, id, draft_id)
    REFERENCES asset.asset_drafts (organization_id, asset_id, id)
    DEFERRABLE INITIALLY DEFERRED;

-- Independent attachments: upload first, bind to drafts, materialize into
-- versions only through a commit transaction.
CREATE TABLE asset.attachments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    uploader_user_id uuid NOT NULL,
    object_key text NOT NULL UNIQUE,
    original_filename text NOT NULL,
    media_type text NOT NULL,
    byte_size bigint NOT NULL CHECK (byte_size >= 0),
    sha256 text NOT NULL,
    status text NOT NULL DEFAULT 'uploading'
        CHECK (status IN ('uploading', 'scanning', 'clean', 'rejected', 'failed')),
    extraction_status text NOT NULL DEFAULT 'pending'
        CHECK (extraction_status IN ('pending', 'processing', 'succeeded', 'failed')),
    expires_at timestamptz,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id)
);

CREATE INDEX attachments_scan_status_idx
    ON asset.attachments (status, extraction_status, created_at);
CREATE INDEX attachments_workspace_idx
    ON asset.attachments (organization_id, workspace_id, created_at DESC);

CREATE TABLE asset.attachment_texts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    attachment_id uuid NOT NULL UNIQUE REFERENCES asset.attachments(id) ON DELETE CASCADE,
    extractor text NOT NULL,
    extractor_version text NOT NULL,
    language text,
    text_content text NOT NULL DEFAULT '',
    checksum text NOT NULL,
    error_code text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE asset.asset_draft_attachments (
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    asset_draft_id uuid NOT NULL,
    attachment_id uuid NOT NULL,
    added_by uuid NOT NULL,
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (asset_draft_id, attachment_id),
    FOREIGN KEY (organization_id, asset_draft_id)
        REFERENCES asset.asset_drafts (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, attachment_id)
        REFERENCES asset.attachments (organization_id, id)
);

CREATE TABLE asset.asset_version_attachments (
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    asset_version_id uuid NOT NULL,
    attachment_id uuid NOT NULL,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (asset_version_id, attachment_id),
    FOREIGN KEY (organization_id, asset_version_id)
        REFERENCES asset.asset_versions (organization_id, id),
    FOREIGN KEY (organization_id, attachment_id)
        REFERENCES asset.attachments (organization_id, id)
);

CREATE INDEX asset_version_attachments_attachment_idx
    ON asset.asset_version_attachments (attachment_id, asset_version_id);

-- Sealed versions reject any relation mutation.
CREATE FUNCTION asset.reject_sealed_relation_change() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    version_id uuid;
    sealed boolean;
BEGIN
    IF TG_OP = 'DELETE' THEN
        version_id := OLD.asset_version_id;
    ELSE
        version_id := NEW.asset_version_id;
    END IF;
    SELECT sealed_at IS NOT NULL INTO sealed
    FROM asset.asset_versions WHERE id = version_id;
    IF sealed IS TRUE THEN
        RAISE EXCEPTION 'asset version relations are sealed'
            USING ERRCODE = 'restrict_violation';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER asset_version_attachments_sealed_guard
    BEFORE INSERT OR UPDATE OR DELETE ON asset.asset_version_attachments
    FOR EACH ROW EXECUTE FUNCTION asset.reject_sealed_relation_change();

CREATE TABLE asset.asset_relations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    source_asset_version_id uuid,
    target_asset_version_id uuid,
    relation_type text NOT NULL,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_asset_version_id, target_asset_version_id, relation_type),
    FOREIGN KEY (organization_id, source_asset_version_id)
        REFERENCES asset.asset_versions (organization_id, id),
    FOREIGN KEY (organization_id, target_asset_version_id)
        REFERENCES asset.asset_versions (organization_id, id)
);

-- Relations follow the same sealed-version immutability as attachments and
-- tags: both endpoints must reference an unsealed (working) version.
CREATE FUNCTION asset.reject_sealed_asset_relation() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    org_id uuid;
    source_id uuid;
    target_id uuid;
    sealed boolean;
BEGIN
    IF TG_OP = 'DELETE' THEN
        org_id := OLD.organization_id;
        source_id := OLD.source_asset_version_id;
        target_id := OLD.target_asset_version_id;
    ELSE
        org_id := NEW.organization_id;
        source_id := NEW.source_asset_version_id;
        target_id := NEW.target_asset_version_id;
    END IF;
    SELECT TRUE INTO sealed
    FROM asset.asset_versions
    WHERE organization_id = org_id
      AND id IN (source_id, target_id)
      AND sealed_at IS NOT NULL;
    IF sealed IS TRUE THEN
        RAISE EXCEPTION 'asset version relations are sealed'
            USING ERRCODE = 'restrict_violation';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER asset_relations_sealed_guard
    BEFORE INSERT OR UPDATE OR DELETE ON asset.asset_relations
    FOR EACH ROW EXECUTE FUNCTION asset.reject_sealed_asset_relation();

CREATE INDEX asset_relations_source_idx
    ON asset.asset_relations (organization_id, source_asset_version_id, created_at DESC);

CREATE TABLE asset.import_batches (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    resource_model_id uuid NOT NULL,
    resource_model_version_id uuid NOT NULL,
    submitted_by uuid NOT NULL REFERENCES identity.users(id),
    source_name text NOT NULL DEFAULT '',
    source_checksum text NOT NULL,
    unknown_tag_policy text NOT NULL DEFAULT 'reject' CHECK (unknown_tag_policy IN ('reject', 'create')),
    created_tag_count integer NOT NULL DEFAULT 0 CHECK (created_tag_count >= 0),
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'processing', 'succeeded', 'failed', 'cancelled')),
    summary jsonb NOT NULL DEFAULT '{}'::jsonb,
    last_error text,
    lease_owner text,
    lease_until timestamptz,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    idempotency_key text,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (organization_id, id)
);

CREATE UNIQUE INDEX import_batches_idempotency_idx
    ON asset.import_batches (organization_id, submitted_by, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE INDEX import_batches_pending_idx
    ON asset.import_batches (status, created_at, id)
    WHERE status IN ('queued', 'processing');

CREATE TABLE asset.import_rows (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    import_batch_id uuid NOT NULL REFERENCES asset.import_batches(id) ON DELETE CASCADE,
    row_number integer NOT NULL CHECK (row_number > 0),
    source_row jsonb NOT NULL,
    row_checksum text NOT NULL,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'processing', 'accepted', 'rejected')),
    errors jsonb NOT NULL DEFAULT '[]'::jsonb,
    last_error text,
    raw_input_id uuid REFERENCES asset.raw_inputs(id),
    asset_id uuid,
    version_id uuid,
    lease_owner text,
    lease_until timestamptz,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    idempotency_key text,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (import_batch_id, row_number),
    UNIQUE (import_batch_id, row_checksum),
    UNIQUE (import_batch_id, idempotency_key)
);

CREATE TABLE asset.export_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    resource_model_id uuid NOT NULL,
    resource_model_version_id uuid,
    submitted_by uuid NOT NULL REFERENCES identity.users(id),
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'processing', 'succeeded', 'failed', 'cancelled')),
    query_snapshot jsonb NOT NULL,
    permission_scope jsonb NOT NULL,
    format text NOT NULL DEFAULT 'jsonl' CHECK (format IN ('jsonl', 'csv', 'xlsx')),
    output_object_key text,
    output_content_type text,
    output_size bigint,
    output_checksum text,
    idempotency_key text,
    error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (organization_id, id)
);

CREATE UNIQUE INDEX export_jobs_idempotency_idx
    ON asset.export_jobs (organization_id, submitted_by, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- Single-level publication review aggregate.
CREATE TABLE asset.publication_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    asset_version_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
    submitted_by uuid NOT NULL REFERENCES identity.users(id),
    decided_by uuid REFERENCES identity.users(id),
    decision_comment text,
    cancelled_by uuid REFERENCES identity.users(id),
    cancel_reason text CHECK (cancel_reason IN
        ('user_cancelled', 'new_version', 'asset_archived', 'admin_cancelled')),
    submitted_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id),
    FOREIGN KEY (organization_id, asset_id)
        REFERENCES asset.assets (organization_id, id),
    FOREIGN KEY (organization_id, asset_version_id)
        REFERENCES asset.asset_versions (organization_id, id)
);

-- One pending request per asset.
CREATE UNIQUE INDEX publication_requests_pending_per_asset_idx
    ON asset.publication_requests (asset_id)
    WHERE status = 'pending';

CREATE INDEX publication_requests_workspace_idx
    ON asset.publication_requests (organization_id, workspace_id, status, submitted_at DESC, id);

CREATE TABLE asset.publication_request_comments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    publication_request_id uuid NOT NULL,
    body text NOT NULL CHECK (length(btrim(body)) > 0),
    author_user_id uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, publication_request_id)
        REFERENCES asset.publication_requests (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX publication_request_comments_request_idx
    ON asset.publication_request_comments (publication_request_id, created_at, id);

-- Content-side relations that reach into the asset domain.
CREATE TABLE content.container_assets (
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    container_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (container_id, asset_id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
        ON DELETE CASCADE,
    FOREIGN KEY (organization_id, container_id)
        REFERENCES content.containers (organization_id, id)
        ON DELETE CASCADE,
    FOREIGN KEY (organization_id, asset_id)
        REFERENCES asset.assets (organization_id, id)
);

CREATE INDEX content_container_assets_workspace_idx
    ON content.container_assets (organization_id, workspace_id, container_id, created_at DESC);

CREATE TABLE content.document_parents (
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    child_asset_id uuid PRIMARY KEY,
    parent_asset_id uuid NOT NULL,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (child_asset_id <> parent_asset_id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id)
        ON DELETE CASCADE,
    FOREIGN KEY (organization_id, child_asset_id)
        REFERENCES asset.assets (organization_id, id),
    FOREIGN KEY (organization_id, parent_asset_id)
        REFERENCES asset.assets (organization_id, id)
);

CREATE TABLE content.asset_version_content_fields (
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    asset_version_id uuid NOT NULL,
    field_key text NOT NULL CHECK (length(btrim(field_key)) > 0),
    container_id uuid NOT NULL,
    container_version_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (asset_version_id, field_key),
    FOREIGN KEY (organization_id, asset_version_id)
        REFERENCES asset.asset_versions (organization_id, id),
    FOREIGN KEY (organization_id, container_id)
        REFERENCES content.containers (organization_id, id),
    FOREIGN KEY (organization_id, container_version_id)
        REFERENCES content.container_versions (organization_id, id)
);

