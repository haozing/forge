-- 0020_field_defaults.sql
-- Field-level defaults (docs/数据模型自由定制实施方案-2026-09-03.md P1):
-- defaults live inside field_schema definitions ({"default": ...}) and are
-- applied by internal/asset at every version-materializing write path — no
-- storage change. This migration only drops the schema-migration-job table:
-- it has had zero Go consumers since the 2026-09-02 over-design sweep (doc
-- 产品文档-v2.md §16.4 records the framework as retired), and JSONB payload
-- + immutable versions make physical data migration unnecessary.

DROP TABLE IF EXISTS model.resource_model_migrations;
