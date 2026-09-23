-- 0047_site_directory_config.sql
-- 模型公开目录页（外链板块 v2，2026-09-23）：站点级目录页配置。
-- 每项 = 一个目录实例：slug/model_key/group_by/sort/page_size/tdk/intro/
-- submission/outlink_nofollow。交付层按配置生成可收录的模型记录目录页。
ALTER TABLE site.public_sites
    ADD COLUMN IF NOT EXISTS directory_config jsonb NOT NULL DEFAULT '[]';
