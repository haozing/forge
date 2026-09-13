-- 0036: 分类树公开化（站点方案 C5/D14，2026-09-12）。
-- container 增加公开语义四列：公开标记、公开 slug（可手工改，关键词落地页）、
-- 分类级 SEO 标题/描述。公开子树 + slug 构成 /c/{path} 层级罗列页
-- （"分门别类、罗列"权重金字塔）。保留字（posts/sections/c/p/tags/search/
-- about/archive/media/rss.xml/sitemap.xml/robots.txt/语言码）由服务层校验。

ALTER TABLE content.containers
    ADD COLUMN slug text,
    ADD COLUMN public_flag boolean NOT NULL DEFAULT false,
    ADD COLUMN public_title text NOT NULL DEFAULT '',
    ADD COLUMN public_description text NOT NULL DEFAULT '';

-- 公开分类 slug 全组织唯一（同站全局命名空间；历史重定向见 path_redirects）。
CREATE UNIQUE INDEX containers_public_slug_idx
    ON content.containers (organization_id, slug)
    WHERE public_flag = true AND slug IS NOT NULL;

CREATE INDEX containers_public_parent_idx
    ON content.containers (parent_id, slug) WHERE public_flag = true;
