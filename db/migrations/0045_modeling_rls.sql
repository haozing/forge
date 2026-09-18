-- 0045: 行级安全试点（技术架构借鉴，agentblog 式 RLS）。
-- content.modeling_plans 是第一个启用 RLS 的表：启用 + FORCE 后，除非
-- 会话内 set_config('app.organization_id', <org>, true)（事务级），任何
-- 角色都看不到/改不了任何行——跨租户访问从"应用层约定"变成"结构性不可能"。
-- 服务侧：internal/modeling 所有读写包在事务里并先行 set_config。
-- 注意：superuser 永远绕过 RLS；owner 若被 FORCE 约束同样受策略管制。

ALTER TABLE content.modeling_plans ENABLE ROW LEVEL SECURITY;

ALTER TABLE content.modeling_plans FORCE ROW LEVEL SECURITY;

CREATE POLICY modeling_plans_org_isolation ON content.modeling_plans
    USING (organization_id = NULLIF(current_setting('app.organization_id', true), '')::uuid)
    WITH CHECK (organization_id = NULLIF(current_setting('app.organization_id', true), '')::uuid);
