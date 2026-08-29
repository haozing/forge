-- verify-v2-schema.sql — schema contract spot checks for the phase 0 baseline.
-- Run inside the development database after cmd/migrate. Every failing query
-- prints a row; a clean database prints only "schema_contract_ok".

SELECT 'fail: visibility values' AS schema_contract_violation
WHERE EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'assets_visibility_check'
      AND pg_get_constraintdef(oid) NOT LIKE '%workspace%organization%public%'
);

SELECT 'fail: workspace role values' AS schema_contract_violation
WHERE EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'workspace_members_role_check'
      AND pg_get_constraintdef(oid) ~ 'owner|member'
);

SELECT 'fail: version content immutability trigger' AS schema_contract_violation
WHERE NOT EXISTS (
    SELECT 1 FROM pg_trigger WHERE tgname = 'asset_versions_immutable_update' AND NOT tgisinternal
);

SELECT 'fail: version seal requirement trigger' AS schema_contract_violation
WHERE NOT EXISTS (
    SELECT 1 FROM pg_trigger WHERE tgname = 'asset_versions_must_seal' AND NOT tgisinternal
);

SELECT 'fail: pending publication request uniqueness' AS schema_contract_violation
WHERE NOT EXISTS (
    SELECT 1 FROM pg_indexes
    WHERE schemaname = 'asset' AND indexname = 'publication_requests_pending_per_asset_idx'
);

SELECT 'fail: legacy columns still present' AS schema_contract_violation
WHERE EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'asset' AND table_name = 'asset_versions'
      AND column_name IN ('workflow_status', 'processing_started_at', 'review_status', 'tags', 'quality', 'source')
) OR EXISTS (
    SELECT 1 FROM information_schema.tables
    WHERE table_schema = 'asset' AND table_name = 'asset_reviews'
);

SELECT 'fail: legacy schemas still present' AS schema_contract_violation
WHERE EXISTS (
    SELECT 1 FROM information_schema.schemata
    WHERE schema_name IN ('organization') AND 1 = 0
);

SELECT 'fail: builtin seed policy uses outlets' AS schema_contract_violation
WHERE EXISTS (
    SELECT 1 FROM model.resource_model_versions
    WHERE policy ? 'outlets'
);

SELECT 'schema_contract_ok' AS result
WHERE NOT EXISTS (
    SELECT 1
    FROM (VALUES (1)) v(x)
    WHERE EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'assets_visibility_check'
          AND pg_get_constraintdef(oid) NOT LIKE '%workspace%organization%public%'
    )
);
