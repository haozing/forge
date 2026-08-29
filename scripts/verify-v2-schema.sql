-- verify-v2-schema.sql — schema contract spot checks for the v2 baseline.
-- Run inside the development database after cmd/migrate. Every violation
-- prints a "fail:" row; a clean database prints only "schema_contract_ok".
-- The ok marker aggregates ALL checks: any violation (or a missing object)
-- suppresses it, and a missing table fails the statement outright via
-- ON_ERROR_STOP — an empty or wrong database can never verify clean.

CREATE TEMP TABLE schema_contract_violations (violation text NOT NULL);

INSERT INTO schema_contract_violations
SELECT 'visibility values' WHERE NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'assets_visibility_check'
      AND pg_get_constraintdef(oid) LIKE '%workspace%organization%public%'
);

INSERT INTO schema_contract_violations
SELECT 'workspace role values' WHERE NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'workspace_members_role_check'
      AND pg_get_constraintdef(oid) !~ 'owner|member'
);

INSERT INTO schema_contract_violations
SELECT 'version content immutability trigger' WHERE NOT EXISTS (
    SELECT 1 FROM pg_trigger WHERE tgname = 'asset_versions_immutable_update' AND NOT tgisinternal
);

INSERT INTO schema_contract_violations
SELECT 'version seal requirement trigger' WHERE NOT EXISTS (
    SELECT 1 FROM pg_trigger WHERE tgname = 'asset_versions_must_seal' AND NOT tgisinternal
);

INSERT INTO schema_contract_violations
SELECT 'pending publication request uniqueness' WHERE NOT EXISTS (
    SELECT 1 FROM pg_indexes
    WHERE schemaname = 'asset' AND indexname = 'publication_requests_pending_per_asset_idx'
);

INSERT INTO schema_contract_violations
SELECT 'asset relations sealed guard' WHERE NOT EXISTS (
    SELECT 1 FROM pg_trigger WHERE tgname = 'asset_relations_sealed_guard' AND NOT tgisinternal
);

INSERT INTO schema_contract_violations
SELECT 'legacy columns still present' WHERE EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'asset' AND table_name = 'asset_versions'
      AND column_name IN ('workflow_status', 'processing_started_at', 'review_status', 'tags', 'quality', 'source')
) OR EXISTS (
    SELECT 1 FROM information_schema.tables
    WHERE table_schema = 'asset' AND table_name = 'asset_reviews'
);

INSERT INTO schema_contract_violations
SELECT 'builtin seed policy uses outlets' WHERE EXISTS (
    SELECT 1 FROM model.resource_model_versions
    WHERE policy ? 'outlets'
);

INSERT INTO schema_contract_violations
SELECT 'projection build attempts column' WHERE NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'retrieval' AND table_name = 'projection_runs'
      AND column_name = 'build_attempts'
);

SELECT 'fail: ' || violation AS schema_contract_violation FROM schema_contract_violations;

SELECT 'schema_contract_ok' AS result
WHERE NOT EXISTS (SELECT 1 FROM schema_contract_violations);
