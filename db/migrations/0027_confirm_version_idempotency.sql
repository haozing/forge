-- 0027_confirm_version_idempotency.sql
-- ConfirmVersion 并发幂等收口：人工确认会从同一源版本派生 human_confirmed
-- 子版本，应用层已有"已派生则返回既有子版本"的去重查询（member.go），但
-- 两个并发 confirm 仍可能同时看不到子版本而各派生一个双胞胎。数据库侧用
-- 部分唯一索引封死同源多确认子版本。
--
-- 存量数据保护：先折叠历史双胞胎——同一 (organization_id, parent_version_id)
-- 下保留被某资产 current_working_version_id 引用的那一个（否则保留最早），
-- 仅删除未被任何资产引用、也没有自身子版本、也未被 current_published_version_id
-- 引用的多余行；若仍有被引用的冲突行，索引创建会以明确错误失败，交由运维
-- 人工裁决（内部部署数据量小，预期为空）。

WITH ranked AS (
    SELECT v.id,
           row_number() OVER (
               PARTITION BY v.organization_id, v.parent_version_id
               ORDER BY (wired.id IS NOT NULL) DESC, v.created_at, v.id
           ) AS rn
    FROM asset.asset_versions v
    LEFT JOIN asset.assets wired
           ON wired.organization_id = v.organization_id AND wired.current_working_version_id = v.id
    WHERE v.parent_version_id IS NOT NULL
      AND v.confirmation_status = 'human_confirmed'
)
DELETE FROM asset.asset_versions d
USING ranked r
WHERE d.id = r.id
  AND r.rn > 1
  AND NOT EXISTS (
      SELECT 1 FROM asset.asset_versions c
      WHERE c.organization_id = d.organization_id AND c.parent_version_id = d.id
  )
  AND NOT EXISTS (
      SELECT 1 FROM asset.assets pub
      WHERE pub.current_published_version_id = d.id
  );

CREATE UNIQUE INDEX IF NOT EXISTS asset_versions_confirm_child_uniq
    ON asset.asset_versions (organization_id, parent_version_id)
    WHERE parent_version_id IS NOT NULL AND confirmation_status = 'human_confirmed';
