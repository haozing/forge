-- 0035: 字段公开白名单下沉到模型（站点方案 C4/D5，2026-09-12）。
-- 白名单（原 site.public_sites.model_views）的归属改为模型版本：
-- - 随 asset_versions.resource_model_version_id 的 pin 语义天然冻结，
--   已发布内容需重发布才吃到新的白名单（"生效时机=重发布"）；
-- - site.public_sites.model_views 与 release 快照字段删除。

ALTER TABLE model.resource_model_versions
    ADD COLUMN public_view jsonb NOT NULL DEFAULT '{}'::jsonb;

-- 存量回填：站点白名单按 model uuid 写入该模型当前版本。
UPDATE model.resource_model_versions mv
SET public_view = kv.entry
FROM site.public_sites s
CROSS JOIN LATERAL jsonb_each(s.model_views) AS kv(model_key_text, entry)
JOIN model.resource_models rm
  ON rm.organization_id = s.organization_id AND rm.id::text = kv.model_key_text
WHERE mv.id = rm.current_version_id
  AND jsonb_typeof(kv.entry) = 'object';

ALTER TABLE site.public_sites DROP COLUMN model_views;
