BEGIN;

CREATE TABLE IF NOT EXISTS retrieval.query_logs (
    id bigserial PRIMARY KEY,
    organization_id uuid NOT NULL REFERENCES organization.organizations(id) ON DELETE CASCADE,
    actor_user_id uuid NOT NULL REFERENCES identity.users(id),
    endpoint text NOT NULL,
    query_hash text NOT NULL,
    result_count integer NOT NULL DEFAULT 0 CHECK (result_count >= 0),
    outcome text NOT NULL CHECK (outcome IN ('succeeded', 'failed')),
    latency_ms integer NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS retrieval_query_logs_scope_idx
    ON retrieval.query_logs (organization_id, actor_user_id, created_at DESC);

COMMIT;
