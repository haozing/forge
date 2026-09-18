-- 0044: 建模计划（内容治理四件套 F4，对应 llm_wiki 两步 ingest）。
-- 第一步 analyze（ReadOnly 工具）产出计划 JSON 不落库；**人**经 HTTP 把
-- 计划保存为本表行（人策展、LLM 维护的最强形式），分诊台批准后第二步
-- react run 消费（产出全为草稿，confirm/publish 仍走 human_only 门）。
-- plan 是行级条目的 jsonb（v1 不做子表）：条目含 source_asset_id、建议
-- 标题、字段骨架（key+label）、分类容器、links[]（必须为已存在资产 ID）、
-- enabled（分诊台行级开关）与产出 asset_id（断点恢复依据）。

CREATE TABLE content.modeling_plans (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organization.organizations(id),
    workspace_id uuid NOT NULL,
    intent text NOT NULL DEFAULT '',
    target_model_id uuid,
    sources jsonb NOT NULL DEFAULT '[]'::jsonb,
    plan jsonb NOT NULL DEFAULT '[]'::jsonb,
    status text NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'approved', 'rejected', 'applied', 'failed')),
    created_by uuid NOT NULL REFERENCES identity.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, id)
);

CREATE INDEX modeling_plans_workspace_idx
    ON content.modeling_plans (organization_id, workspace_id, created_at DESC);
