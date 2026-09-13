-- 0033: 收录模型 v2（公开站点简化方案 B2/B3，干净重做 D9）。
-- 1) 新三表：site_slugs（slug 即路由表，历史行做 301）、site_exclusions（排除）、
--    site_featured（精选）；
-- 2) 资产列：locale / translation_group_id（多语言 E）/ category_container_id（分类挂载）；
-- 3) 存量绑定迁移为 slug 行后 DROP site_content_bindings，并以同名**视图**承载
--    派生收录语义（三道闸 + 排除 + 模型公开通道 + scope 天花板），交付层全部
--    读取查询经由视图即刻切到新模型。

CREATE TABLE site.site_slugs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    site_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    slug text NOT NULL,
    is_current boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_id, slug)
);

CREATE UNIQUE INDEX site_slugs_current_idx
    ON site.site_slugs (site_id, asset_id) WHERE is_current;
CREATE INDEX site_slugs_asset_idx
    ON site.site_slugs (organization_id, asset_id);

CREATE TABLE site.site_exclusions (
    site_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    excluded_by uuid NOT NULL REFERENCES identity.users(id),
    excluded_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (site_id, asset_id)
);

CREATE TABLE site.site_featured (
    site_id uuid NOT NULL,
    asset_id uuid NOT NULL,
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    marked_by uuid NOT NULL REFERENCES identity.users(id),
    marked_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (site_id, asset_id)
);

ALTER TABLE asset.assets
    ADD COLUMN locale text,
    ADD COLUMN translation_group_id uuid,
    ADD COLUMN category_container_id uuid;

-- 存量绑定 → slug 行（多段路径取末段；冲突跳过，待重发布时再生成）。
INSERT INTO site.site_slugs (organization_id, site_id, asset_id, slug, is_current)
SELECT b.organization_id, b.site_id, b.asset_id,
       CASE WHEN position('/' in b.display_path) > 0
            THEN split_part(b.display_path, '/', -1) ELSE b.display_path END,
       true
FROM site.site_content_bindings b
WHERE b.content_type <> 'about'
ON CONFLICT (site_id, slug) DO NOTHING;

-- path_redirects 与绑定表解绑：约束名不猜，动态删除全部引用绑定表的 FK
-- （审计教训：DROP CONSTRAINT IF EXISTS + 猜测名会静默跳过，导致 DROP TABLE 失败）。
DO $$
DECLARE r record;
BEGIN
  FOR r IN
    SELECT con.conname, con.conrelid::regclass AS tbl
    FROM pg_constraint con
    JOIN pg_class rel ON rel.oid = con.confrelid
    WHERE rel.relname = 'site_content_bindings' AND con.contype = 'f'
  LOOP
    EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', r.tbl, r.conname);
  END LOOP;
END $$;

DROP TABLE site.site_content_bindings;

-- 派生收录视图：与旧表同列形状，语义 = 已发布 + 可见性 ∈ scope 天花板
-- + 模型 public_site 通道开启 − 单条排除。精选资产额外产生一行
-- content_type='featured' 供首页精选模块消费。
CREATE VIEW site.site_content_bindings AS
WITH included AS (
    SELECT sl.id AS slug_row_id, sl.organization_id, sl.site_id, sl.asset_id,
           sl.slug, sl.created_at,
           a.current_published_version_id, a.published_at, a.updated_at,
           a.visibility, a.resource_model_id, s.default_content_scope,
           s.workspace_id
    FROM site.site_slugs sl
    JOIN site.public_sites s
      ON s.organization_id = sl.organization_id AND s.id = sl.site_id
    JOIN asset.assets a
      ON a.organization_id = sl.organization_id AND a.id = sl.asset_id
     AND a.deleted_at IS NULL
     AND a.current_published_version_id IS NOT NULL
     AND a.published_at IS NOT NULL
    LEFT JOIN model.resource_models rm0
      ON rm0.organization_id = a.organization_id AND rm0.id = a.resource_model_id
    LEFT JOIN model.resource_model_versions mv0
      ON mv0.organization_id = rm0.organization_id AND mv0.id = rm0.current_version_id
    WHERE sl.is_current
      AND COALESCE(NULLIF(mv0.policy #>> ARRAY['channels', 'public_site', 'enabled'], '')::boolean, false)
      AND NOT EXISTS (
            SELECT 1 FROM site.site_exclusions x
            WHERE x.site_id = sl.site_id AND x.asset_id = sl.asset_id)
      AND CASE s.default_content_scope
            WHEN 'public' THEN a.visibility = 'public'
            WHEN 'organization' THEN a.visibility IN ('organization', 'public')
            ELSE true
          END
)
SELECT i.slug_row_id AS id,
       i.site_id AS site_id,
       i.asset_id AS asset_id,
       i.slug AS display_path,
       'article'::text AS content_type,
       COALESCE(rm.model_key, '') AS section_slug,
       0 AS sort_order,
       false AS on_homepage,
       false AS on_navigation,
       '{}'::jsonb AS display_config,
       i.published_at AS display_published_at,
       i.created_at AS created_at,
       i.updated_at AS updated_at,
       i.organization_id AS organization_id,
       i.workspace_id AS workspace_id,
       i.visibility AS visibility,
       i.default_content_scope AS default_content_scope
FROM included i
LEFT JOIN model.resource_models rm
  ON rm.organization_id = i.organization_id AND rm.id = i.resource_model_id
UNION ALL
SELECT i.slug_row_id AS id,
       i.site_id AS site_id,
       i.asset_id AS asset_id,
       i.slug AS display_path,
       'featured'::text AS content_type,
       COALESCE(rm.model_key, '') AS section_slug,
       0 AS sort_order,
       false AS on_homepage,
       false AS on_navigation,
       '{}'::jsonb AS display_config,
       i.published_at AS display_published_at,
       i.created_at AS created_at,
       i.updated_at AS updated_at,
       i.organization_id AS organization_id,
       i.workspace_id AS workspace_id,
       i.visibility AS visibility,
       i.default_content_scope AS default_content_scope
FROM included i
JOIN site.site_featured f
  ON f.site_id = i.site_id AND f.asset_id = i.asset_id
LEFT JOIN model.resource_models rm
  ON rm.organization_id = i.organization_id AND rm.id = i.resource_model_id;
