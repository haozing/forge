-- 0029_folder_containers_asset_id_nullable.sql
-- 目录树容器（note_folder/doc_folder，迁移 0026 引入的纯组织性 kind）没有
-- 对应资产，但 containers.asset_id 是 NOT NULL——folder 域的 INSERT 必然
-- 违反非空约束（生产已复现 500）。放开为可空：asset_id 上的 UNIQUE 约束
-- 在 Postgres 默认 NULLS DISTINCT 语义下允许多个 NULL 行共存，既有
-- note/document 容器的"一资产一容器"约束不受影响。

ALTER TABLE content.containers ALTER COLUMN asset_id DROP NOT NULL;
