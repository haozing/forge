SELECT count(*) AS migration_count FROM system.schema_migrations;
SELECT to_regclass('asset.review_tasks') IS NULL AS review_tasks_removed;
SELECT to_regnamespace('authorization') IS NULL AS authorization_schema_removed;
