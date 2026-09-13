-- 0040_theme_foundation.sql
-- 主题化重构地基：主题修订版、自定义页实体、会话沙盒重建、旧设计体系退役。
-- 设计文档：docs/站点主题化与AI设计重构-2026-09-13.md（拍板：不共存、不留旧设计数据）。

-- 0040_theme_foundation.sql
CREATE TABLE site.site_theme_revisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    site_id uuid NOT NULL,
    revision_no integer NOT NULL,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','published','archived')),
    files jsonb NOT NULL DEFAULT '{}'::jsonb,   -- 槽位 → 模板源码；空对象 = 全槽位回退内置默认主题
    base_revision_id uuid,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    UNIQUE (organization_id, id),
    UNIQUE (site_id, revision_no),
    FOREIGN KEY (organization_id, site_id) REFERENCES site.public_sites (organization_id, id)
);
-- 「每站点至多一个 draft / 一个 published」是发布事务的硬前提，索引级强制。
CREATE UNIQUE INDEX site_theme_revisions_one_draft
    ON site.site_theme_revisions (site_id) WHERE status = 'draft';
CREATE UNIQUE INDEX site_theme_revisions_one_published
    ON site.site_theme_revisions (site_id) WHERE status = 'published';

ALTER TABLE site.public_sites
    ADD COLUMN draft_theme_revision_id uuid,
    ADD COLUMN published_theme_revision_id uuid;
ALTER TABLE site.public_sites
    ADD CONSTRAINT public_sites_draft_theme_fk
        FOREIGN KEY (organization_id, draft_theme_revision_id)
        REFERENCES site.site_theme_revisions (organization_id, id),
    ADD CONSTRAINT public_sites_published_theme_fk
        FOREIGN KEY (organization_id, published_theme_revision_id)
        REFERENCES site.site_theme_revisions (organization_id, id);

CREATE TABLE site.site_pages (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    site_id uuid NOT NULL,
    slug text NOT NULL,
    title text NOT NULL,
    body_markdown text NOT NULL DEFAULT '',      -- 正文；经与资产同一 markdown 净化管线产出 HTML
    seo_description text NOT NULL DEFAULT '',
    nav_order integer NOT NULL DEFAULT 100,      -- 越小越靠前；与固定路由/分类同序合并
    nav_hidden boolean NOT NULL DEFAULT false,
    locale text,                                 -- NULL = 全语言（与资产 locale 语义一致）
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    UNIQUE (site_id, slug),
    FOREIGN KEY (organization_id, site_id) REFERENCES site.public_sites (organization_id, id)
);

-- 引导：每个站点建一个初始 published 修订版（files 留空 → 全槽位回退内置默认主题），
-- 并回填 draft/published 指针，保证发布事务有基线。
INSERT INTO site.site_theme_revisions
    (organization_id, workspace_id, site_id, revision_no, status, files, created_by, published_at)
SELECT s.organization_id, s.workspace_id, s.id, 1, 'published', '{}'::jsonb, s.created_by, now()
FROM site.public_sites s;

UPDATE site.public_sites s
SET published_theme_revision_id = r.id, draft_theme_revision_id = r.id
FROM site.site_theme_revisions r
WHERE r.site_id = s.id AND r.status = 'published';

-- 会话沙盒重建（drop + create；不搬存量）。
DROP TABLE IF EXISTS site.design_sessions;
CREATE TABLE site.design_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    site_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','applied','discarded')),
    files jsonb NOT NULL DEFAULT '{}'::jsonb,
    base_theme_revision_id uuid,
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id),
    FOREIGN KEY (organization_id, site_id) REFERENCES site.public_sites (organization_id, id)
);
CREATE INDEX design_sessions_site_status_idx ON site.design_sessions (site_id, status);

-- 旧设计体系退役（列 drop；数据丢弃为拍板决策）。
ALTER TABLE site.public_sites
    DROP COLUMN IF EXISTS homepage_config,
    DROP COLUMN IF EXISTS pages_config,
    DROP COLUMN IF EXISTS style_config,
    DROP COLUMN IF EXISTS navigation_config,
    DROP COLUMN IF EXISTS custom_css;

-- 预览一次性 token（iframe 实时预览）：HMAC 摘要单次消费，60s TTL。
CREATE TABLE site.preview_tokens (
    digest bytea PRIMARY KEY,
    organization_id uuid NOT NULL,
    session_id uuid NOT NULL,
    site_id uuid NOT NULL,
    slot text NOT NULL DEFAULT 'home',
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz
);
