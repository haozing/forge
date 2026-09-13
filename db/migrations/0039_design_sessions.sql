-- 0039: AI 设计会话沙盒（站点方案 D1-D5，2026-09-13）。
-- 会话 fork 站点设计配置（pages_config + style），agent 的 apply_patch 只写
-- 沙盒行，全程不写站点行；人审 diff 后"应用"才落站点（site.design）。
-- observe 的截图池（chromedp）是可选设施：RENDERER_ENABLED 未开时降级为
-- 纯结构化观察（token/validator/块统计），本表与会话语义不依赖截图。

CREATE TABLE site.design_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    site_id uuid NOT NULL,
    session_config jsonb NOT NULL DEFAULT '{}'::jsonb,
    base_pages_config jsonb NOT NULL DEFAULT '{}'::jsonb,
    base_style_config jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'applied', 'discarded')),
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id)
);

CREATE INDEX design_sessions_site_idx
    ON site.design_sessions (site_id, status, created_at DESC);
