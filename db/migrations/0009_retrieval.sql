-- 0009_retrieval.sql
-- R3 retrieval projection storage, folded at its current shape. Phase 3
-- replaces this file with the final projection model; the tables exist so the
-- current runtime modules keep working from the empty baseline.
BEGIN;

CREATE OR REPLACE FUNCTION retrieval.quality_rank(value text)
RETURNS integer
LANGUAGE SQL IMMUTABLE STRICT AS $$
  SELECT CASE value
    WHEN 'raw' THEN 1
    WHEN 'ai_generated' THEN 2
    WHEN 'human_confirmed' THEN 3
    ELSE 99
  END
$$;

CREATE OR REPLACE FUNCTION retrieval.matches_field_filters(fields jsonb, predicates jsonb)
RETURNS boolean
LANGUAGE plpgsql IMMUTABLE STRICT AS $$
DECLARE
  predicate jsonb;
  field_name text;
  field_value jsonb;
  expected jsonb;
  op text;
  expected_exists boolean;
BEGIN
  IF jsonb_typeof(fields) <> 'object' OR jsonb_typeof(predicates) <> 'array' THEN
    RETURN false;
  END IF;

  FOR predicate IN SELECT value FROM jsonb_array_elements(predicates)
  LOOP
    field_name := predicate ->> 'field';
    expected := predicate -> 'value';
    op := predicate ->> 'operator';

    IF field_name IS NULL OR field_name = '' OR op IS NULL THEN
      RETURN false;
    END IF;

    IF op = 'exists' THEN
      IF jsonb_typeof(expected) <> 'boolean' THEN
        RETURN false;
      END IF;
      expected_exists := (expected #>> '{}')::boolean;
      IF (fields ? field_name) <> expected_exists THEN
        RETURN false;
      END IF;
      CONTINUE;
    END IF;

    IF NOT (fields ? field_name) THEN
      RETURN false;
    END IF;
    field_value := fields -> field_name;

    CASE op
      WHEN 'eq' THEN
        IF field_value <> expected THEN RETURN false; END IF;
      WHEN 'neq' THEN
        IF field_value = expected THEN RETURN false; END IF;
      WHEN 'in' THEN
        IF jsonb_typeof(expected) <> 'array'
           OR NOT EXISTS (SELECT 1 FROM jsonb_array_elements(expected) item WHERE item = field_value) THEN
          RETURN false;
        END IF;
      WHEN 'contains' THEN
        IF jsonb_typeof(field_value) = 'string' AND jsonb_typeof(expected) = 'string' THEN
          IF position(lower(expected #>> '{}') IN lower(field_value #>> '{}')) = 0 THEN RETURN false; END IF;
        ELSIF jsonb_typeof(field_value) = 'array' THEN
          IF jsonb_typeof(expected) = 'array' THEN
            IF NOT (field_value @> expected) THEN RETURN false; END IF;
          ELSIF NOT (field_value @> jsonb_build_array(expected)) THEN
            RETURN false;
          END IF;
        ELSIF jsonb_typeof(field_value) = 'object' AND jsonb_typeof(expected) = 'object' THEN
          IF NOT (field_value @> expected) THEN RETURN false; END IF;
        ELSE
          RETURN false;
        END IF;
      WHEN 'contains_any' THEN
        IF jsonb_typeof(expected) <> 'array' THEN
          RETURN false;
        ELSIF jsonb_typeof(field_value) = 'array' THEN
          IF NOT EXISTS (
            SELECT 1 FROM jsonb_array_elements(field_value) actual
            JOIN jsonb_array_elements(expected) wanted ON wanted = actual
          ) THEN RETURN false; END IF;
        ELSIF jsonb_typeof(field_value) = 'string' THEN
          IF NOT EXISTS (
            SELECT 1 FROM jsonb_array_elements(expected) wanted
            WHERE jsonb_typeof(wanted) = 'string'
              AND position(lower(wanted #>> '{}') IN lower(field_value #>> '{}')) > 0
          ) THEN RETURN false; END IF;
        ELSE
          RETURN false;
        END IF;
      WHEN 'gte' THEN
        IF jsonb_typeof(field_value) = 'number' AND jsonb_typeof(expected) = 'number' THEN
          IF (field_value #>> '{}')::numeric < (expected #>> '{}')::numeric THEN RETURN false; END IF;
        ELSIF jsonb_typeof(field_value) = 'string' AND jsonb_typeof(expected) = 'string' THEN
          IF (field_value #>> '{}') < (expected #>> '{}') THEN RETURN false; END IF;
        ELSE
          RETURN false;
        END IF;
      WHEN 'lte' THEN
        IF jsonb_typeof(field_value) = 'number' AND jsonb_typeof(expected) = 'number' THEN
          IF (field_value #>> '{}')::numeric > (expected #>> '{}')::numeric THEN RETURN false; END IF;
        ELSIF jsonb_typeof(field_value) = 'string' AND jsonb_typeof(expected) = 'string' THEN
          IF (field_value #>> '{}') > (expected #>> '{}') THEN RETURN false; END IF;
        ELSE
          RETURN false;
        END IF;
      ELSE
        RETURN false;
    END CASE;
  END LOOP;
  RETURN true;
END
$$;

CREATE TABLE retrieval.projection_configs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    resource_model_id uuid NOT NULL,
    model_name text,
    model_version text,
    dimensions integer CHECK (dimensions IS NULL OR dimensions = 1024),
    chunker_version text NOT NULL DEFAULT 'v1',
    active_projection_generation bigint NOT NULL CHECK (active_projection_generation > 0),
    status text NOT NULL DEFAULT 'warming' CHECK (status IN ('warming', 'active', 'retired')),
    activated_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, resource_model_id, active_projection_generation),
    CHECK ((model_name IS NULL AND model_version IS NULL AND dimensions IS NULL)
        OR (model_name IS NOT NULL AND model_version IS NOT NULL AND dimensions IS NOT NULL)),
    FOREIGN KEY (organization_id, resource_model_id)
        REFERENCES model.resource_models (organization_id, id)
);

CREATE UNIQUE INDEX projection_configs_active_idx
    ON retrieval.projection_configs (organization_id, resource_model_id)
    WHERE status = 'active';

CREATE TABLE retrieval.projection_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    resource_model_id uuid NOT NULL,
    asset_version_id uuid NOT NULL,
    source_checksum text NOT NULL,
    canonicalizer_version text NOT NULL DEFAULT 'v1',
    projection_config_id uuid NOT NULL REFERENCES retrieval.projection_configs(id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'ready', 'failed', 'stale')),
    error_code text,
    expected_chunk_count integer NOT NULL DEFAULT 0 CHECK (expected_chunk_count >= 0),
    ready_chunk_count integer NOT NULL DEFAULT 0 CHECK (ready_chunk_count >= 0),
    expected_embedding_count integer NOT NULL DEFAULT 0 CHECK (expected_embedding_count >= 0),
    ready_embedding_count integer NOT NULL DEFAULT 0 CHECK (ready_embedding_count >= 0),
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (organization_id, asset_version_id)
        REFERENCES asset.asset_versions (organization_id, id) ON DELETE CASCADE,
    UNIQUE (asset_version_id, source_checksum, canonicalizer_version, projection_config_id),
    FOREIGN KEY (organization_id, resource_model_id)
        REFERENCES model.resource_models (organization_id, id)
);

CREATE INDEX retrieval_projection_runs_asset_idx
    ON retrieval.projection_runs (organization_id, asset_version_id, status, updated_at DESC);

CREATE TABLE retrieval.chunks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    asset_id uuid NOT NULL,
    resource_model_id uuid NOT NULL,
    asset_version_id uuid NOT NULL,
    projection_run_id uuid NOT NULL REFERENCES retrieval.projection_runs(id) ON DELETE CASCADE,
    projection_generation bigint NOT NULL CHECK (projection_generation > 0),
    chunker_version text NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    content text NOT NULL DEFAULT '',
    search_text text NOT NULL DEFAULT '',
    canonical_text text NOT NULL DEFAULT '',
    char_start integer NOT NULL DEFAULT 0 CHECK (char_start >= 0),
    char_end integer NOT NULL DEFAULT 0 CHECK (char_end >= char_start),
    source_type text NOT NULL DEFAULT 'asset',
    source_locator jsonb NOT NULL DEFAULT '{}'::jsonb,
    source_checksum text NOT NULL,
    chunk_checksum text NOT NULL,
    canonicalizer_version text NOT NULL DEFAULT 'v1',
    status text NOT NULL DEFAULT 'ready' CHECK (status IN ('pending', 'ready', 'failed', 'deleted')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (projection_run_id, ordinal),
    FOREIGN KEY (organization_id, asset_id)
        REFERENCES asset.assets (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, asset_version_id)
        REFERENCES asset.asset_versions (organization_id, id) ON DELETE CASCADE,
    FOREIGN KEY (organization_id, resource_model_id)
        REFERENCES model.resource_models (organization_id, id)
);

CREATE INDEX retrieval_chunks_search_idx
    ON retrieval.chunks USING pgroonga (search_text pgroonga_text_full_text_search_ops_v2);
CREATE INDEX retrieval_chunks_scope_idx
    ON retrieval.chunks (organization_id, resource_model_id, projection_generation, status, projection_run_id);

CREATE TABLE retrieval.chunk_embeddings (
    chunk_id uuid NOT NULL REFERENCES retrieval.chunks(id) ON DELETE CASCADE,
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    model_name text NOT NULL,
    model_version text NOT NULL,
    dimensions integer NOT NULL CHECK (dimensions = 1024),
    projection_generation bigint NOT NULL CHECK (projection_generation > 0),
    projection_run_id uuid NOT NULL REFERENCES retrieval.projection_runs(id) ON DELETE CASCADE,
    embedding vector(1024) NOT NULL,
    status text NOT NULL DEFAULT 'ready' CHECK (status IN ('pending', 'ready', 'failed', 'deleted')),
    error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (chunk_id, model_name, model_version, projection_generation),
    CHECK (vector_dims(embedding) = dimensions)
);

CREATE INDEX chunk_embeddings_scope_idx
    ON retrieval.chunk_embeddings (organization_id, model_name, model_version, projection_generation, status);
CREATE INDEX chunk_embeddings_hnsw_idx
    ON retrieval.chunk_embeddings USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64)
    WHERE status = 'ready';

CREATE TABLE retrieval.search_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id) ON DELETE CASCADE,
    principal_id uuid NOT NULL,
    query_hash text NOT NULL,
    policy_revision bigint NOT NULL,
    projection_fingerprint text NOT NULL,
    mode text NOT NULL DEFAULT 'hybrid'
        CHECK (mode IN ('structured', 'fulltext', 'semantic', 'hybrid')),
    degraded boolean NOT NULL DEFAULT false,
    ranking_method text NOT NULL DEFAULT 'rrf'
        CHECK (ranking_method IN ('structured', 'fulltext', 'semantic', 'rrf', 'rerank')),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX search_sessions_expiry_idx ON retrieval.search_sessions (expires_at);

CREATE TABLE retrieval.search_session_items (
    session_id uuid NOT NULL REFERENCES retrieval.search_sessions(id) ON DELETE CASCADE,
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    asset_id uuid NOT NULL,
    asset_version_id uuid NOT NULL,
    chunk_id uuid,
    lexical_rank integer,
    vector_rank integer,
    rrf_score double precision,
    rerank_score double precision,
    final_score double precision NOT NULL,
    ranking_method text NOT NULL
        CHECK (ranking_method IN ('structured', 'fulltext', 'semantic', 'rrf', 'rerank')),
    PRIMARY KEY (session_id, ordinal),
    UNIQUE (session_id, asset_version_id, chunk_id)
);

CREATE TABLE retrieval.query_logs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid REFERENCES organization.organizations(id),
    actor_user_id uuid,
    initiator_user_id uuid,
    agent_application_id uuid,
    endpoint text NOT NULL,
    query_hash text NOT NULL,
    result_count integer NOT NULL DEFAULT 0,
    outcome text NOT NULL CHECK (outcome IN ('allowed', 'denied', 'error')),
    latency_ms integer,
    created_at timestamptz NOT NULL DEFAULT now()
);

COMMIT;
