-- 0034: 内容发布历史（站点方案 D16/C7，2026-09-12）。
-- asset_versions 是不可变的内容版本（有 forbid_version_content_update 触发器），
-- 且没有"曾发布"标记；"更新记录"需要的是**发布动作**的时间线（同一版本可
-- 回滚重发、各带说明），故新建发布历史表。change_note 是作者主动写的公开
-- 信息（发布对话框标注"公开可见"），与自动 diff 的泄密面不同。

CREATE TABLE asset.asset_publications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    asset_version_id uuid NOT NULL,
    version_no integer NOT NULL,
    origin text NOT NULL,
    confirmation_status text NOT NULL,
    change_note text,
    published_at timestamptz NOT NULL DEFAULT now(),
    published_by uuid NOT NULL REFERENCES identity.users(id),
    UNIQUE (organization_id, id)
);

CREATE INDEX asset_publications_asset_idx
    ON asset.asset_publications (organization_id, asset_id, published_at DESC);

-- 存量回填：每个已发布资产补一行"当前指针"的历史（旧时间线不可得，
-- 以资产 published_at 作为该行发布时间）。
INSERT INTO asset.asset_publications
    (organization_id, workspace_id, asset_id, asset_version_id, version_no,
     origin, confirmation_status, published_at, published_by)
SELECT a.organization_id, a.workspace_id, a.id, v.id, v.version_no,
       v.origin, v.confirmation_status, COALESCE(a.published_at, now()),
       COALESCE(v.created_by, a.created_by)
FROM asset.assets a
JOIN asset.asset_versions v
  ON v.organization_id = a.organization_id AND v.id = a.current_published_version_id
WHERE a.current_published_version_id IS NOT NULL;
