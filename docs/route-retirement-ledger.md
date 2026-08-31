# 路由清理记录（已完结）

本台账原名「v2 路由退役台账」，用于分阶段退役旧路由。2026-08-31 开发期清理已完结：**API 收敛为单一无版本路由树**，本文件转为最终记录，不再维护状态行。

## 最终架构

| 面 | 前缀 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| 成员面 | `/api` | 会话 cookie | 身份、组织/工作区、资产全生命周期、标签、审核流、建议流、站点、检索、附件、容器、会话聊天、Agent 会话、自动化、导入导出、通知、模型管理 |
| 公开面 | `/api/public` | 匿名（可选成员会话） | 登录、密码重置、邀请 resolve/accept、公开站点读取 |
| 开放 API 面 | `/api/open` | Bearer API key | webhook、统一查询、agent-tasks、资产写入、附件下载、automation 回调 |
| 运维面 | `/api/admin` | 会话 cookie + agent.manage | agent 用户/应用/密钥管理 |

统一约定：错误信封 `{"error":{code,message},"request_id"}`；keyset 分页；契约端点强制幂等键与 If-Match（openapi.yaml 收录面）；CSRF Origin 门覆盖 `/api` 与 `/api/public` 写请求（`/api/open`、`/api/admin` 除外）；登录/开放面限流不变。

## 本次清理内容

1. **前缀退役（直接 404，无重定向）**：`/api/v2/*`、`/api/public/v2/*`、`/api/open/v2/*`、`/api/open/v1/*`、`/api/frontend/*` 全部前缀从路由表移除，由 `TestVersionedAndLegacyPrefixesAreGone` 与 `TestLegacyOpenQueryRouteIsRetired` 钉死 404。
2. **路径映射**：`/api/v2/X → /api/X`；`/api/public/v2/X → /api/public/X`；`/api/open/v2/X、/api/open/v1/X → /api/open/X`（`/api/open/v1/agent/tasks → /api/open/agent-tasks`）；`/api/frontend/X → /api/X`。
3. **同路径冲突以 v2 handler 为准**：`/api/assets/{assetId}/publish|archive|restore` 采用 v2 语义（强制幂等键、data 信封），删除 `publishMemberAsset`、`archiveMemberAsset`、`restoreAssetFinal`；`/api/attachments/{attachmentId}` 统一为成员附件资源 handler，删除 `attachmentStatus`。
4. **双路由去重**：资产 `move`、`document-parent` 仅保留非 workspace 限定形态；`agent/tasks` v1 形态删除。
5. **中间件前缀规则更新**：幂等覆盖面 = `/api` 减 `/api/public`、`/api/admin`（operation 命名空间 `api.http:`/`open.http:`）；CSRF Origin 门 = `/api`、`/api/public`；登录限流路径 = `/api/public/sessions`；SSE reset recovery 指向 `/api/conversations/{id}/messages`。
6. **契约与工具链**：`openapi-v2.yaml` 路径迁移并更名为 `openapi.yaml`（87 路径与 routerTruth 双向锁定不变）；删除 `openapi-frontend-v1.yaml`、`openapi-open-v1.yaml`、`openapi-admin-v1.yaml`；`tmp_qa/itd_*`、`.tools/api_*` 回归脚本同步换路径。

## 遗留说明

- 原「阶段 4-6 planned」的 run-now→prepare、automation callback 迁移等批次目标随本次清理一并完成（prepare 走 `/api/workspaces/{ws}/assets/{id}/prepare`，callback 走 `/api/open/automation/runs/{runId}/callback`）。
- 从 frontend 面并入的域维持既有写语义（幂等键 opt-in + 服务端代理键）；如需全量强制 428，按域逐批切换 handler 内校验即可。
- 历史阶段文档（`docs/产品文档-v2-阶段N实施方案.md` 等）中的旧路径为过程记录，未回改；以 `openapi.yaml` 与 `router_groups.go` 为准。
