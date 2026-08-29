-- 0009_retrieval.sql
-- Retrieval v2 baseline (phase 3): projection profiles, projection runs and
-- heads, immutable chunks and embeddings, rebuild batches, search sessions and
-- query executions. Empty-database baseline: the R3 patch chain
-- (supersedes the legacy R3 helper objects, which are not part of this baseline)
-- is intentionally gone; this script only CREATEs the final model.
--
-- Composite foreign keys follow the phase 0 convention: every child table
-- references the parent's UNIQUE (organization_id, id) so a bare UUID can
-- never cross a tenant boundary. asset.asset_versions,
-- content.workspaces and model.resource_models all carry that key.
BEGIN;

-- ---------------------------------------------------------------------------
-- Projection profiles
-- ---------------------------------------------------------------------------

CREATE TABLE retrieval.projection_profiles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    generation bigint NOT NULL CHECK (generation > 0),
    manifest_key text NOT NULL CHECK (char_length(manifest_key) BETWEEN 1 AND 200),
    canonicalizer_version text NOT NULL CHECK (char_length(canonicalizer_version) BETWEEN 1 AND 50),
    chunker_version text NOT NULL CHECK (char_length(chunker_version) BETWEEN 1 AND 50),
    tokenizer_version text NOT NULL CHECK (char_length(tokenizer_version) BETWEEN 1 AND 50),
    semantic_enabled boolean NOT NULL DEFAULT false,
    embedding_provider_key text,
    embedding_model text,
    embedding_model_version text,
    embedding_dimensions integer,
    distance_metric text,
    status text NOT NULL DEFAULT 'warming'
        CHECK (status IN ('warming', 'active', 'retired', 'failed')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    activated_by uuid,
    activated_at timestamptz,
    retired_at timestamptz,
    failure_code text,
    UNIQUE (organization_id, generation),
    UNIQUE (organization_id, id),
    -- Semantic identity is either fully present or fully absent.
    CONSTRAINT projection_profiles_semantic_identity CHECK (
        (semantic_enabled = false AND embedding_provider_key IS NULL
            AND embedding_model IS NULL AND embedding_model_version IS NULL
            AND embedding_dimensions IS NULL AND distance_metric IS NULL)
        OR (semantic_enabled = true AND embedding_provider_key IS NOT NULL
            AND embedding_model IS NOT NULL AND embedding_model_version IS NOT NULL
            AND embedding_dimensions = 1024 AND distance_metric = 'cosine')
    ),
    -- Activation metadata must match the lifecycle status.
    CONSTRAINT projection_profiles_activation_metadata CHECK (
        (
            status IN ('warming', 'failed')
            AND activated_at IS NULL AND activated_by IS NULL AND retired_at IS NULL
        )
        OR (
            status = 'active'
            AND activated_at IS NOT NULL AND activated_by IS NOT NULL
            AND retired_at IS NULL
        )
        OR (
            status = 'retired'
            AND retired_at IS NOT NULL
        )
    ),
    CONSTRAINT projection_profiles_failure_only_when_failed CHECK (
        status = 'failed' OR failure_code IS NULL
    )
);

-- At most one active and one warming profile per organization.
CREATE UNIQUE INDEX projection_profiles_one_active_idx
    ON retrieval.projection_profiles (organization_id)
    WHERE status = 'active';
CREATE UNIQUE INDEX projection_profiles_one_warming_idx
    ON retrieval.projection_profiles (organization_id)
    WHERE status = 'warming';

CREATE INDEX projection_profiles_org_status_idx
    ON retrieval.projection_profiles (organization_id, status, generation DESC);

-- ---------------------------------------------------------------------------
-- Projection runs
-- ---------------------------------------------------------------------------

CREATE TABLE retrieval.projection_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    asset_version_id uuid NOT NULL,
    resource_model_id uuid NOT NULL,
    resource_model_version_id uuid NOT NULL,
    projection_profile_id uuid NOT NULL,
    -- '' while the run is queued; build sets the canonical checksum once the
    -- ordered segment set has been computed in memory.
    canonical_checksum text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'building', 'lexical_ready', 'embedding',
                          'ready', 'degraded', 'failed', 'stale')),
    semantic_status text NOT NULL DEFAULT 'pending'
        CHECK (semantic_status IN ('disabled', 'pending', 'ready', 'failed')),
    expected_chunk_count integer NOT NULL DEFAULT 0 CHECK (expected_chunk_count >= 0),
    ready_chunk_count integer NOT NULL DEFAULT 0 CHECK (ready_chunk_count >= 0),
    expected_embedding_count integer NOT NULL DEFAULT 0 CHECK (expected_embedding_count >= 0),
    ready_embedding_count integer NOT NULL DEFAULT 0 CHECK (ready_embedding_count >= 0),
    failure_code text,
    failure_stage text,
    queued_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    lexical_ready_at timestamptz,
    completed_at timestamptz,
    stale_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, asset_version_id, projection_profile_id, canonical_checksum),
    UNIQUE (organization_id, id),
    CONSTRAINT projection_runs_ready_counters CHECK (
        ready_chunk_count <= expected_chunk_count
        AND ready_embedding_count <= expected_embedding_count
    ),
    CONSTRAINT projection_runs_terminal_state CHECK (
        (
            status IN ('ready', 'degraded')
            AND ready_chunk_count = expected_chunk_count
            AND lexical_ready_at IS NOT NULL
            AND completed_at IS NOT NULL
            AND (
                semantic_status IN ('disabled', 'ready')
                OR (semantic_status = 'failed' AND status = 'degraded')
            )
            AND (semantic_status <> 'ready' OR ready_embedding_count = expected_embedding_count)
            AND (semantic_status <> 'disabled' OR (expected_embedding_count = 0 AND ready_embedding_count = 0))
        )
        OR (status NOT IN ('ready', 'degraded'))
    ),
    CONSTRAINT projection_runs_stale_metadata CHECK (
        (status = 'stale') = (stale_at IS NOT NULL)
    ),
    CONSTRAINT projection_runs_lexical_pointer CHECK (
        (
            lexical_ready_at IS NULL
            AND status NOT IN ('lexical_ready', 'embedding', 'ready', 'degraded', 'stale')
        )
        OR (
            lexical_ready_at IS NOT NULL
            AND (
                status IN ('lexical_ready', 'embedding', 'ready', 'degraded', 'stale')
                OR status = 'failed'
            )
        )
    ),
    CONSTRAINT projection_runs_disabled_semantic_counters CHECK (
        semantic_status <> 'disabled'
        OR (expected_embedding_count = 0 AND ready_embedding_count = 0)
    ),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id),
    FOREIGN KEY (organization_id, asset_id)
        REFERENCES asset.assets (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, asset_version_id)
        REFERENCES asset.asset_versions (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, resource_model_id)
        REFERENCES model.resource_models (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, resource_model_version_id)
        REFERENCES model.resource_model_versions (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, projection_profile_id)
        REFERENCES retrieval.projection_profiles (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX projection_runs_profile_status_idx
    ON retrieval.projection_runs (organization_id, projection_profile_id, status, updated_at);
CREATE INDEX projection_runs_scope_idx
    ON retrieval.projection_runs (organization_id, workspace_id, resource_model_id, status);
CREATE INDEX projection_runs_version_idx
    ON retrieval.projection_runs (organization_id, asset_id, asset_version_id, projection_profile_id);
-- Claim loop: queued/failed runs older than their last touch, oldest first.
CREATE INDEX projection_runs_claim_idx
    ON retrieval.projection_runs (status, updated_at)
    WHERE status IN ('queued', 'failed');
CREATE INDEX projection_runs_building_lease_idx
    ON retrieval.projection_runs (status, started_at)
    WHERE status = 'building';

-- ---------------------------------------------------------------------------
-- Projection heads (per asset/profile serving pointer)
-- ---------------------------------------------------------------------------

CREATE TABLE retrieval.projection_heads (
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    asset_id uuid NOT NULL,
    projection_profile_id uuid NOT NULL,
    asset_version_id uuid NOT NULL,
    active_run_id uuid NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, asset_id, projection_profile_id),
    FOREIGN KEY (organization_id, asset_id)
        REFERENCES asset.assets (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, projection_profile_id)
        REFERENCES retrieval.projection_profiles (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, active_run_id)
        REFERENCES retrieval.projection_runs (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX projection_heads_run_idx
    ON retrieval.projection_heads (organization_id, active_run_id);

-- ---------------------------------------------------------------------------
-- Chunks (immutable once the run is serving)
-- ---------------------------------------------------------------------------

CREATE TABLE retrieval.chunks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    asset_version_id uuid NOT NULL,
    resource_model_id uuid NOT NULL,
    resource_model_version_id uuid NOT NULL,
    projection_run_id uuid NOT NULL,
    projection_profile_id uuid NOT NULL,
    projection_generation bigint NOT NULL CHECK (projection_generation > 0),
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    source_type text NOT NULL CHECK (char_length(source_type) BETWEEN 1 AND 50),
    source_locator jsonb NOT NULL CHECK (jsonb_typeof(source_locator) = 'object'),
    char_start integer NOT NULL DEFAULT 0 CHECK (char_start >= 0),
    char_end integer NOT NULL DEFAULT 0 CHECK (char_end >= char_start),
    content text NOT NULL CHECK (char_length(content) > 0),
    source_checksum text NOT NULL,
    chunk_checksum text NOT NULL,
    canonicalizer_version text NOT NULL,
    chunker_version text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (projection_run_id, ordinal),
    UNIQUE (projection_run_id, chunk_checksum, source_locator),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id),
    FOREIGN KEY (organization_id, asset_id)
        REFERENCES asset.assets (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, asset_version_id)
        REFERENCES asset.asset_versions (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, resource_model_id)
        REFERENCES model.resource_models (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, resource_model_version_id)
        REFERENCES model.resource_model_versions (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, projection_run_id)
        REFERENCES retrieval.projection_runs (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, projection_profile_id)
        REFERENCES retrieval.projection_profiles (organization_id, id) ON DELETE CASCADE
);

-- A serving run (ready/degraded/stale) owns immutable content: INSERT/UPDATE
-- are rejected outright and DELETE is only permitted while the run is stale so
-- the grace cleanup can reclaim storage. When the parent run row itself is
-- being cascade-deleted the lookup finds nothing and the delete proceeds.
CREATE FUNCTION retrieval.reject_serving_chunk_change() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    run_status text;
BEGIN
    IF TG_OP = 'DELETE' THEN
        SELECT status INTO run_status
        FROM retrieval.projection_runs
        WHERE id = OLD.projection_run_id;
        IF NOT FOUND THEN
            -- The parent run row is being cascade-deleted; allow cleanup.
            RETURN OLD;
        END IF;
        IF run_status IN ('ready', 'degraded') THEN
            IF EXISTS (
                SELECT 1 FROM asset.assets a
                WHERE a.organization_id = OLD.organization_id AND a.id = OLD.asset_id
            ) THEN
                RAISE EXCEPTION 'chunks of a serving projection run are immutable'
                    USING ERRCODE = 'restrict_violation';
            END IF;
            -- The owning asset is being deleted; the teardown cascade may
            -- proceed (asset deletion is the sanctioned content removal path).
            RETURN OLD;
        END IF;
        RETURN OLD;
    END IF;
    SELECT status INTO run_status
    FROM retrieval.projection_runs
    WHERE id = NEW.projection_run_id;
    IF NOT FOUND THEN
        RETURN NEW;
    END IF;
    IF run_status IN ('ready', 'degraded', 'stale') THEN
        RAISE EXCEPTION 'chunks of a serving projection run are immutable'
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER chunks_serving_immutable
    BEFORE INSERT OR UPDATE OR DELETE ON retrieval.chunks
    FOR EACH ROW EXECUTE FUNCTION retrieval.reject_serving_chunk_change();

CREATE INDEX chunks_search_idx
    ON retrieval.chunks USING pgroonga (content pgroonga_text_full_text_search_ops_v2);
CREATE INDEX chunks_scope_idx
    ON retrieval.chunks (organization_id, workspace_id, resource_model_id,
                         projection_profile_id, projection_run_id);

-- ---------------------------------------------------------------------------
-- Chunk embeddings (pgvector, identity owned by the profile)
-- ---------------------------------------------------------------------------

CREATE TABLE retrieval.chunk_embeddings (
    chunk_id uuid PRIMARY KEY REFERENCES retrieval.chunks(id) ON DELETE CASCADE,
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    projection_run_id uuid NOT NULL,
    projection_profile_id uuid NOT NULL,
    projection_generation bigint NOT NULL CHECK (projection_generation > 0),
    embedding vector(1024) NOT NULL CHECK (vector_dims(embedding) = 1024),
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id),
    FOREIGN KEY (organization_id, projection_run_id)
        REFERENCES retrieval.projection_runs (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, projection_profile_id)
        REFERENCES retrieval.projection_profiles (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX chunk_embeddings_hnsw_idx
    ON retrieval.chunk_embeddings USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);
CREATE INDEX chunk_embeddings_scope_idx
    ON retrieval.chunk_embeddings (organization_id, workspace_id,
                                   projection_profile_id, projection_run_id);
CREATE INDEX chunk_embeddings_run_idx
    ON retrieval.chunk_embeddings (projection_run_id);

-- ---------------------------------------------------------------------------
-- Projection rebuilds (operations batch, not a domain event)
-- ---------------------------------------------------------------------------

CREATE TABLE retrieval.projection_rebuilds (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid,
    projection_profile_id uuid,
    scope_type text NOT NULL
        CHECK (scope_type IN ('organization', 'workspace', 'resource_model', 'asset')),
    scope_id text NOT NULL DEFAULT '',
    reason text NOT NULL
        CHECK (reason IN ('profile_warming', 'manual', 'policy_changed', 'repair')),
    status text NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'running', 'succeeded', 'partially_failed',
                          'failed', 'cancelled')),
    total_count integer NOT NULL DEFAULT 0 CHECK (total_count >= 0),
    queued_count integer NOT NULL DEFAULT 0 CHECK (queued_count >= 0 AND queued_count <= total_count),
    ready_count integer NOT NULL DEFAULT 0 CHECK (ready_count >= 0 AND ready_count <= total_count),
    degraded_count integer NOT NULL DEFAULT 0 CHECK (degraded_count >= 0 AND degraded_count <= total_count),
    failed_count integer NOT NULL DEFAULT 0 CHECK (failed_count >= 0 AND failed_count <= total_count),
    idempotency_key text,
    request_hash text NOT NULL DEFAULT '',
    requested_by uuid,
    requested_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (organization_id, id),
    UNIQUE (organization_id, idempotency_key),
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id),
    FOREIGN KEY (organization_id, projection_profile_id)
        REFERENCES retrieval.projection_profiles (organization_id, id)
);

CREATE INDEX projection_rebuilds_org_idx
    ON retrieval.projection_rebuilds (organization_id, requested_at DESC);

-- ---------------------------------------------------------------------------
-- Search sessions (v2 snapshot) and items
-- ---------------------------------------------------------------------------

CREATE TABLE retrieval.search_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id) ON DELETE CASCADE,
    subject_kind text NOT NULL CHECK (char_length(subject_kind) BETWEEN 1 AND 30),
    subject_id uuid NOT NULL,
    channel text NOT NULL
        CHECK (channel IN ('workspace', 'agent', 'open_api', 'public_site')),
    request_hash text NOT NULL,
    scope_fingerprint_at_create text NOT NULL DEFAULT '',
    policy_revision_at_create bigint NOT NULL DEFAULT 0,
    query_execution_id uuid,
    requested_mode text NOT NULL
        CHECK (requested_mode IN ('structured', 'fulltext', 'semantic', 'hybrid')),
    executed_mode text NOT NULL
        CHECK (executed_mode IN ('structured', 'fulltext', 'semantic', 'hybrid')),
    ranking_method text NOT NULL DEFAULT 'rrf'
        CHECK (ranking_method IN ('structured', 'fulltext', 'semantic', 'rrf', 'rerank')),
    degraded boolean NOT NULL DEFAULT false,
    degradation_reasons text[] NOT NULL DEFAULT '{}',
    projection_profile_id uuid,
    projection_generation bigint,
    result_count integer NOT NULL DEFAULT 0 CHECK (result_count >= 0),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, projection_profile_id)
        REFERENCES retrieval.projection_profiles (organization_id, id)
);

CREATE INDEX search_sessions_expiry_idx
    ON retrieval.search_sessions (expires_at);
CREATE INDEX search_sessions_subject_idx
    ON retrieval.search_sessions (organization_id, subject_kind, subject_id, created_at DESC);

CREATE TABLE retrieval.search_session_items (
    session_id uuid NOT NULL,
    organization_id uuid NOT NULL REFERENCES organization.organizations(id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    asset_id uuid NOT NULL,
    asset_version_id uuid NOT NULL,
    primary_chunk_id uuid,
    citation_id text,
    lexical_rank integer,
    semantic_rank integer,
    rrf_score double precision,
    rerank_score double precision,
    final_score double precision,
    ranking_method text NOT NULL
        CHECK (ranking_method IN ('structured', 'fulltext', 'semantic', 'rrf', 'rerank')),
    citation_source_type text,
    citation_source_locator jsonb
        CHECK (citation_source_locator IS NULL OR jsonb_typeof(citation_source_locator) = 'object'),
    citation_char_start integer,
    citation_char_end integer,
    citation_excerpt text,
    citation_source_checksum text,
    citation_chunk_checksum text,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, ordinal),
    UNIQUE (session_id, asset_version_id),
    UNIQUE (session_id, citation_id),
    CONSTRAINT search_session_items_excerpt_length CHECK (
        citation_excerpt IS NULL OR char_length(citation_excerpt) <= 500
    ),
    CONSTRAINT search_session_items_citation_range CHECK (
        (citation_char_start IS NULL AND citation_char_end IS NULL)
        OR (citation_char_start IS NOT NULL AND citation_char_end IS NOT NULL
            AND citation_char_end >= citation_char_start)
    ),
    -- structured items carry no chunk/citation/score payload at all; every
    -- other full-text-class method must present a citation, semantic items may
    -- be explicitly vector-only.
    CONSTRAINT search_session_items_structured_payload CHECK (
        ranking_method <> 'structured'
        OR (citation_id IS NULL AND primary_chunk_id IS NULL
            AND citation_source_type IS NULL AND citation_source_locator IS NULL
            AND citation_char_start IS NULL AND citation_char_end IS NULL
            AND citation_excerpt IS NULL AND citation_source_checksum IS NULL
            AND citation_chunk_checksum IS NULL
            AND lexical_rank IS NULL AND semantic_rank IS NULL
            AND rrf_score IS NULL AND rerank_score IS NULL AND final_score IS NULL)
    ),
    CONSTRAINT search_session_items_citation_required CHECK (
        ranking_method IN ('structured', 'semantic')
        OR citation_id IS NOT NULL
    ),
    FOREIGN KEY (organization_id, session_id)
        REFERENCES retrieval.search_sessions (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX search_session_items_expiry_idx
    ON retrieval.search_session_items (created_at);
-- ---------------------------------------------------------------------------
-- Query executions (audit) and their workspace scope relation
-- ---------------------------------------------------------------------------

CREATE TABLE retrieval.query_executions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id) ON DELETE CASCADE,
    subject_kind text NOT NULL CHECK (char_length(subject_kind) BETWEEN 1 AND 30),
    subject_id uuid NOT NULL,
    channel text NOT NULL
        CHECK (channel IN ('workspace', 'agent', 'open_api', 'public_site')),
    request_id text NOT NULL DEFAULT '',
    request_hash text NOT NULL,
    requested_mode text NOT NULL
        CHECK (requested_mode IN ('structured', 'fulltext', 'semantic', 'hybrid')),
    executed_mode text
        CHECK (executed_mode IS NULL OR executed_mode IN ('structured', 'fulltext', 'semantic', 'hybrid')),
    ranking_method text
        CHECK (ranking_method IS NULL OR ranking_method IN ('structured', 'fulltext', 'semantic', 'rrf', 'rerank')),
    status text NOT NULL DEFAULT 'started'
        CHECK (status IN ('started', 'succeeded', 'failed', 'interrupted')),
    degraded boolean NOT NULL DEFAULT false,
    degradation_reasons text[] NOT NULL DEFAULT '{}',
    resource_model_count integer NOT NULL DEFAULT 0 CHECK (resource_model_count >= 0),
    lexical_candidate_count integer NOT NULL DEFAULT 0 CHECK (lexical_candidate_count >= 0),
    semantic_candidate_count integer NOT NULL DEFAULT 0 CHECK (semantic_candidate_count >= 0),
    fused_candidate_count integer NOT NULL DEFAULT 0 CHECK (fused_candidate_count >= 0),
    result_count integer NOT NULL DEFAULT 0 CHECK (result_count >= 0),
    projection_profile_id uuid,
    generation bigint,
    embedding_model_identity text,
    reranker_model_identity text,
    stage_latency_ms jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(stage_latency_ms) = 'object'),
    error_code text,
    search_session_id uuid,
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    expires_at timestamptz NOT NULL DEFAULT now() + interval '180 days',
    UNIQUE (organization_id, id),
    CONSTRAINT query_executions_terminal_metadata CHECK (
        (
            status IN ('started')
            AND completed_at IS NULL
        )
        OR (
            status IN ('succeeded', 'failed', 'interrupted')
            AND completed_at IS NOT NULL
        )
    ),
    FOREIGN KEY (organization_id, projection_profile_id)
        REFERENCES retrieval.projection_profiles (organization_id, id)
);

CREATE INDEX query_executions_org_started_idx
    ON retrieval.query_executions (organization_id, started_at DESC);
CREATE INDEX query_executions_status_idx
    ON retrieval.query_executions (status, started_at)
    WHERE status = 'started';
CREATE INDEX query_executions_expiry_idx
    ON retrieval.query_executions (expires_at);

CREATE TABLE retrieval.query_execution_workspaces (
    execution_id uuid NOT NULL,
    organization_id uuid NOT NULL REFERENCES organization.organizations(id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL,
    PRIMARY KEY (execution_id, workspace_id),
    FOREIGN KEY (organization_id, execution_id)
        REFERENCES retrieval.query_executions (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, workspace_id)
        REFERENCES content.workspaces (organization_id, id) ON DELETE CASCADE
);
COMMIT;
