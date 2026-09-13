-- 0031: 权限地基（成员与 Agent 权限统一方案 A–K，2026-09-12）。
-- 1) workspace_members 支持两类主体（human/agent）与成员级权限覆写；
-- 2) 存量 workspace_agent_applications 启用行迁移为 agent 成员行后废弃该表
--    （"应用在某工作区启用" = 其 bound_agent_user_id 在该工作区有成员行）；
-- 3) 站点 1:1：清理存量多站点（保留每工作区最早创建的站点）后加唯一约束。

ALTER TABLE content.workspace_members
    ADD COLUMN principal_type text NOT NULL DEFAULT 'human',
    ADD COLUMN granted_actions text[] NOT NULL DEFAULT '{}',
    ADD COLUMN revoked_actions text[] NOT NULL DEFAULT '{}';

ALTER TABLE content.workspace_members
    ADD CONSTRAINT workspace_members_principal_type_chk
        CHECK (principal_type IN ('human', 'agent')),
    ADD CONSTRAINT workspace_members_agent_role_chk
        CHECK (principal_type <> 'agent' OR role IN ('editor', 'viewer'));

-- 存量启用绑定 → agent 成员行（editor 预设；human_only 动作由判权层排除）。
INSERT INTO content.workspace_members
    (organization_id, workspace_id, user_id, role, principal_type, granted_by)
SELECT wa.organization_id, wa.workspace_id, aa.bound_agent_user_id, 'editor', 'agent', wa.created_by
FROM content.workspace_agent_applications wa
JOIN integration.agent_applications aa
  ON aa.organization_id = wa.organization_id AND aa.id = wa.agent_application_id
WHERE wa.enabled = true
ON CONFLICT (workspace_id, user_id) DO NOTHING;

-- 有工作区级模型策略行的 agent（MCP key 等无绑定行路径）补成员行，保持可达。
INSERT INTO content.workspace_members
    (organization_id, workspace_id, user_id, role, principal_type, granted_by)
SELECT DISTINCT ap.organization_id, ap.workspace_id, ap.agent_user_id, 'editor', 'agent', ap.created_by
FROM content.agent_access_policies ap
WHERE ap.workspace_id IS NOT NULL
ON CONFLICT (workspace_id, user_id) DO NOTHING;

-- 组织级策略行（workspace NULL）按原全局可达语义展开到组织全部活跃工作区。
INSERT INTO content.workspace_members
    (organization_id, workspace_id, user_id, role, principal_type, granted_by)
SELECT DISTINCT ap.organization_id, w.id, ap.agent_user_id, 'editor', 'agent', ap.created_by
FROM content.agent_access_policies ap
JOIN content.workspaces w ON w.organization_id = ap.organization_id AND w.status = 'active'
WHERE ap.workspace_id IS NULL
ON CONFLICT (workspace_id, user_id) DO NOTHING;

DROP TABLE content.workspace_agent_applications;

-- 站点 1:1：清理存量多站点（每工作区保留最早创建的一个），依赖行随删。
CREATE TEMP TABLE doomed_sites ON COMMIT DROP AS
SELECT s.organization_id, s.id
FROM site.public_sites s
JOIN (
    SELECT workspace_id, min(created_at) AS first_created
    FROM site.public_sites
    GROUP BY workspace_id
) keep ON keep.workspace_id = s.workspace_id
WHERE s.created_at > keep.first_created;

DELETE FROM site.site_comments c USING doomed_sites d
    WHERE c.organization_id = d.organization_id AND c.site_id = d.id;
DELETE FROM site.site_content_bindings b USING doomed_sites d
    WHERE b.organization_id = d.organization_id AND b.site_id = d.id;
DELETE FROM site.path_redirects r USING doomed_sites d
    WHERE r.organization_id = d.organization_id AND r.site_id = d.id;
DELETE FROM site.site_releases rel USING doomed_sites d
    WHERE rel.organization_id = d.organization_id AND rel.site_id = d.id;
DELETE FROM site.public_sites s USING doomed_sites d
    WHERE s.organization_id = d.organization_id AND s.id = d.id;

CREATE UNIQUE INDEX public_sites_workspace_unique_idx
    ON site.public_sites (workspace_id);
