# ADR-001 阶段 0 契约冻结摘要

状态：已接受（2026-08-29）
关联：[产品文档-v2-阶段0实施方案.md](../产品文档-v2-阶段0实施方案.md)

## 决策

1. **发布状态唯一属于 Asset**。`draft/published/archived` 是 `asset.assets.publication_status` 的全部取值；AssetVersion 不携带任何发布、审核或工作流状态。指针不变量（published ⇔ current_published_version_id 非空 ⇔ published_at 非空）由数据库 CHECK 强制。
2. **AssetVersion 不可变**。内容列禁止 UPDATE（触发器），创建事务结束时必须 `sealed_at` 非空（延迟约束触发器）；版本标签/附件关系在封存后同样拒绝增删改。
3. **AssetDraft 是唯一可变工作副本**。每个 Asset 恰好一个 draft；dirty/clean 唯一定义为 `revision <> committed_revision`；自动保存只递增 draft revision；所有版本创建只能走统一 `CreateVersionTx`。
4. **两层角色**。组织身份 `identity.users.organization_role ∈ {admin, member}`（Agent 恒为 NULL），工作区角色 `content.workspace_members.role ∈ {admin, editor, reviewer, viewer}`；不存在 workspace owner/member；动作常量集中在 `internal/authz/actions.go`，授予矩阵唯一存在于 `roles.go`。
5. **审核重构为单级 PublicationRequest**。`asset.asset_reviews`/`review_status` 移除；`asset.publication_requests` 以 partial unique index 保证每资产一个 pending；`decided_by ≠ submitted_by`；拒绝/取消不修改版本；新 working version 或归档自动以明确原因取消 pending。
6. **可见性三值**。`workspace/organization/public`；`login/private/internal` 从数据库和代码中移除（负向扫描把关）。
7. **ResourceModel policy 最终结构**。`visibility/channels/retrieval/publishing` 四块；`outlets` 与全部别名归一化删除，旧键在校验层显式拒绝。
8. **事件为领域事实**。唯一信封 `internal/eventing.Event`（含 workspace_id、actor、payload_version），事件目录固定于 `internal/eventing/catalog.go`；`asset.retrieval_projection_requested` 删除，Asset 域不再 import retrieval，投影由 worker 消费 `asset.published/archived` 等事实。
9. **迁移契约**。迁移根目录扁平 SQL（0001-0012 领域基线），独立 `cmd/migrate` 以 owner 连接在单事务内完成 DDL+checksum+advisory lock；API/worker 仅做 `VerifySchemaContract`；阶段 0-6 基线变化一律空库重建。
10. **HTTP 契约**。错误 envelope `{error:{code,message,request_id,details}}`；列表统一 cursor page；request id 由中间件注入；写命令强制 `Idempotency-Key`（缺失 428）；`If-Match` 缺失 428、不匹配 412。旧路由按 `docs/route-retirement-ledger.md` 分阶段退役，阶段 0 仅退役 review 族。

## 后果

- 后续阶段（1-6）只能在这套语义上叠加：不再发明状态、角色或事件名。
- 阶段 0-6 期间基线文件允许直接改写并重建空库；首次共享部署后冻结 checksum。
- 负向扫描（`workflow_status|review_status|asset_reviews|"owner"|outlets|agent_tool|"login"|"private"|"internal"` 等）作为 CI 退出门槛持续执行。
