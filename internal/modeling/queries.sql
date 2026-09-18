-- 建模计划域查询（sqlc 契约文件）：internal/modeling 服务的全部数据访问。
-- 运行环境为已 set_config('app.organization_id', <org>, true) 的事务
-- （RLS 试点 0045），此处 SQL 只做业务过滤。

-- name: InsertPlan :one
INSERT INTO content.modeling_plans
    (organization_id, workspace_id, intent, target_model_id, sources, plan, status, created_by)
VALUES (@org_id::uuid, @workspace_id::uuid, sqlc.arg('intent'), sqlc.narg('target_model_id')::uuid,
        sqlc.arg('sources')::jsonb, sqlc.arg('plan')::jsonb, 'draft', @created_by::uuid)
RETURNING id::text, organization_id::text, workspace_id::text, intent,
    COALESCE(target_model_id::text, '') AS target_model_id, sources, plan, status,
    created_at, updated_at;

-- name: ListPlans :many
SELECT id::text, organization_id::text, workspace_id::text, intent,
    COALESCE(target_model_id::text, '') AS target_model_id, sources, plan, status,
    created_at, updated_at
FROM content.modeling_plans
WHERE organization_id = @org_id::uuid AND workspace_id = @workspace_id::uuid
ORDER BY created_at DESC
LIMIT @row_limit::int;

-- name: GetPlan :one
SELECT id::text, organization_id::text, workspace_id::text, intent,
    COALESCE(target_model_id::text, '') AS target_model_id, sources, plan, status,
    created_at, updated_at
FROM content.modeling_plans
WHERE organization_id = @org_id::uuid AND workspace_id = @workspace_id::uuid AND id = @plan_id::uuid;

-- name: LockPlan :one
SELECT id::text, organization_id::text, workspace_id::text, intent,
    COALESCE(target_model_id::text, '') AS target_model_id, sources, plan, status,
    created_at, updated_at
FROM content.modeling_plans
WHERE organization_id = @org_id::uuid AND workspace_id = @workspace_id::uuid AND id = @plan_id::uuid
FOR UPDATE;

-- name: UpdatePlanBody :exec
UPDATE content.modeling_plans SET plan = @plan::jsonb, updated_at = now()
WHERE organization_id = @org_id::uuid AND workspace_id = @workspace_id::uuid AND id = @plan_id::uuid;

-- name: UpdatePlanStatus :one
UPDATE content.modeling_plans SET status = @status, updated_at = now()
WHERE organization_id = @org_id::uuid AND workspace_id = @workspace_id::uuid AND id = @plan_id::uuid
RETURNING id::text, organization_id::text, workspace_id::text, intent,
    COALESCE(target_model_id::text, '') AS target_model_id, sources, plan, status,
    created_at, updated_at;
