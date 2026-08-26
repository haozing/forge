# Forge 全态 API 实现缺口与后端实施计划

> 对照基准：[YXT产品全态API接口契约.md](D:/code2/agentchunzhi/docs/yxt/YXT产品全态API接口契约.md)  
> 检查对象：当前 `internal/httpapi/router.go`、frontend handlers、service、数据库迁移和现有契约测试  
> 结论：当前实现是“可联调骨架”，还不是产品最终态。后续不保留旧接口兼容分支，统一实现全态契约中的新路径、新字段和新状态。

## 1. 总体结论

当前已经具备的主要骨架：

- Session 登录、当前用户、健康检查。
- 工作区读取、成员自身信息、设置、计数、统计、活动。
- 动态资源模型和版本的创建、读取、编辑、校验、发布。
- 模型迁移预览、启动、查询、取消。
- 资产列表、创建、读取、编辑、提交审核、发布、归档。
- 审核列表、详情、单条通过/驳回、批量处理。
- 容器树读取/创建、容器读取/编辑、容器资产读取、资产移动、文档父级设置。
- 附件列表、上传、状态、删除、关联。
- 会话、消息、普通问答、流式问答、笔记同步/发布、派生、媒体注册/转写。
- Agent session、引用校验、普通/流式聊天。
- 自动化 Job、Run、Attempt、重试、取消。
- 导入、导出及任务查询。

当前不满足最终态的根因不是单纯少几个 handler，而是存在三类问题：

1. **接口完全缺失**：注销、成员邀请、通知、导出下载、版本级资产、搜索建议等。
2. **路径不符合最终契约**：容器创建、资产容器关联、附件下载等仍使用旧路径或旧别名。
3. **接口存在但行为不完整**：查询不支持 semantic、分页不稳定、状态字段不统一、动态 schema 仍保留旧格式、SSE 事件和异步任务事件不完整。

## 2. 路由实现矩阵

状态定义：

- **已实现**：当前 router 有对应方法，主要行为已存在；仍需做最终态字段/状态审计。
- **部分实现**：有相近 handler 或旧路径，但不能直接作为最终契约实现。
- **缺失**：当前没有可用路由/handler。

### 2.1 认证和用户

| 最终态接口 | 当前状态 | 处理 |
| --- | --- | --- |
| `POST /api/sessions` | 已实现 | 按最终响应字段和错误协议收敛 |
| `DELETE /api/sessions` | 缺失 | P0，撤销 Session、清 Cookie、幂等 |
| `GET /api/me` | 部分实现 | 补 `display_name/login_name/avatar_url`，统一用户对象 |
| `PATCH /api/me/profile` | 缺失 | P1 |
| `GET /api/frontend/me/preferences` | 已实现 | 收敛为最终字段白名单 |
| `PATCH /api/frontend/me/preferences` | 部分实现 | 增加最终偏好字段和严格校验 |

### 2.2 工作区和成员

| 最终态接口 | 当前状态 | 处理 |
| --- | --- | --- |
| `GET /api/frontend/workspaces` | 已实现 | 补最终字段和分页 |
| `POST /api/frontend/workspaces` | 缺失 | P0，创建工作区并设置 owner |
| `GET /api/frontend/workspaces/{workspaceId}` | 已实现 | 补成员数、状态、默认模型等字段 |
| `PATCH /api/frontend/workspaces/{workspaceId}` | 缺失 | P1 |
| `DELETE /api/frontend/workspaces/{workspaceId}` | 缺失 | P1，异步删除 Job |
| counts/stats/activity | 已实现 | 补最终统计字段、游标和权限脱敏 |
| `GET/PATCH .../settings` | 部分实现 | 补 review/search/retention/notification policy |
| `GET /api/frontend/workspaces/{id}/members` | 缺失 | P0 |
| `GET /api/frontend/workspaces/{id}/members/me` | 已实现 | 补完整角色和权限摘要 |
| `POST .../member-invitations` | 缺失 | P0 |
| `GET .../member-invitations` | 缺失 | P1 |
| `POST /api/frontend/member-invitations/{id}/accept` | 缺失 | P1 |
| `POST /api/frontend/member-invitations/{id}/revoke` | 缺失 | P1 |
| `PATCH /api/frontend/workspace-members/{memberId}` | 缺失 | P0，角色变更 |
| `DELETE /api/frontend/workspace-members/{memberId}` | 缺失 | P0，移除成员 |

### 2.3 Agent 应用目录

| 最终态接口 | 当前状态 | 处理 |
| --- | --- | --- |
| `GET .../agent-applications` | 已实现 | 补 capabilities/config_summary |
| `POST .../agent-applications` | 缺失 | P1 |
| `GET/PATCH /api/frontend/agent-applications/{id}` | 缺失 | P1 |
| `POST .../{id}/enable` | 缺失 | P1 |
| `POST .../{id}/disable` | 缺失 | P1 |

### 2.4 动态资源模型

| 最终态接口 | 当前状态 | 处理 |
| --- | --- | --- |
| 模型 GET/POST/PATCH | 已实现 | 强制最终 schema 格式和字段白名单 |
| 版本 GET/POST/PATCH | 已实现 | 收敛 status、validated_at、ETag |
| `validate` | 已实现 | 保持 draft status，写入 validated_at |
| `publish` | 已实现 | 补退役旧版本、模型状态和审计 |
| `retire` | 缺失 | P1 |
| migration preview/start/get/cancel | 已实现 | 补 failure_policy、进度、错误明细和事件 |

模型当前最大问题：`field_schema` 仍兼容旧 `properties` 形式，而最终契约只保留 `fields` 形式；需要一次性清理 validator、数据库示例、前端 renderer 和 OpenAPI。

### 2.5 资产和版本

| 最终态接口 | 当前状态 | 处理 |
| --- | --- | --- |
| 资产列表/创建/详情/编辑 | 已实现 | 补最终过滤器、容器和父资产字段 |
| `DELETE /api/frontend/assets/{id}` | 缺失 | P0，异步软删除 |
| `GET /api/frontend/assets/{id}/versions` | 缺失 | P0 |
| `GET /api/frontend/asset-versions/{id}` | 缺失 | P0 |
| `POST /api/frontend/assets/{id}/versions` | 缺失 | P0，创建 draft version |
| `PATCH /api/frontend/asset-versions/{id}` | 缺失 | P0，版本 ETag 更新 |
| submit-review/publish/archive | 已实现 | 收敛最终状态机和审核前置条件 |
| `POST .../restore` | 缺失 | P1 |
| `POST .../duplicate` | 缺失 | P1 |

当前资产接口是“编辑资产自动生成版本”，最终态要求同时提供版本历史和版本级读写。两者应统一到底层 `asset_versions`，不能继续只暴露资产聚合对象。

当前状态差异：

- 最终态初始 `publication_status=draft`，当前代码仍可能使用 `internal`。
- 最终态定义独立的 `publication_status/review_status/workflow_status/quality`，需要统一数据库约束和 service 状态转换。
- 删除、恢复、复制必须有审计和异步任务，不允许直接硬删除。

### 2.6 容器和文档关系

| 最终态接口 | 当前状态 | 处理 |
| --- | --- | --- |
| `GET /workspaces/{id}/containers/tree` | 已实现 | 保留 GET |
| `POST /workspaces/{id}/containers` | 部分实现 | 当前 POST 挂在 `/containers/tree`，需切换最终路径 |
| `GET/PATCH/DELETE /containers/{id}` | 部分实现 | GET/PATCH 有，DELETE 未注册 |
| `POST /containers/{id}/move` | 缺失 | P0 |
| `GET /containers/{id}/children` | 缺失 | P1 |
| `GET /containers/{id}/assets` | 已实现 | 补完整 Asset 摘要而非只返回 ID |
| `POST /assets/{id}/containers` | 缺失 | P0，最终替代旧 `/move` |
| `GET /assets/{id}/containers` | 缺失 | P1 |
| `POST/DELETE /assets/{id}/document-parent` | 部分实现 | 已有写入，补最终读取和统一路径 |
| `GET /assets/{id}/document-children` | 缺失 | P0 |

需要移除/停止使用的旧路径：

- `/api/frontend/workspaces/{workspaceId}/assets/{assetId}/move`
- `/api/frontend/assets/{assetId}/move`
- `/api/frontend/workspaces/{workspaceId}/assets/{assetId}/document-parent`

最终态只保留资产自身资源路径，工作区只用于权限校验，不再提供同一动作的多套别名。

### 2.7 附件

| 最终态接口 | 当前状态 | 处理 |
| --- | --- | --- |
| 版本附件 GET/POST | 已实现 | 补扫描状态、checksum、严格大小/类型校验 |
| `GET/DELETE /attachments/{id}` | 已实现 | 增加最终状态字段 |
| `PATCH /attachments/{id}` | 缺失 | P1，改展示名/媒体元数据 |
| `POST /attachments/{id}/link` | 已实现 | 增加幂等和版本一致性校验 |
| `GET /attachments/{id}/download` | 缺失 | P0，统一浏览器下载路径 |
| `POST /attachments/{id}/presigned-download` | 缺失 | P0，短期授权下载 |

当前下载在 `/api/attachments/{id}/download`，与最终 `/api/frontend/attachments/{id}/download` 不一致；不做兼容，直接迁移到最终路径。

### 2.8 审核

| 最终态接口 | 当前状态 | 处理 |
| --- | --- | --- |
| 审核列表/详情/approve/reject/batch | 已实现 | 补最终过滤、评论、ETag 和逐项错误 |
| `GET /reviews/{id}/comments` | 缺失 | P1 |
| `POST /reviews/{id}/comments` | 缺失 | P1 |

### 2.9 搜索

| 最终态接口 | 当前状态 | 处理 |
| --- | --- | --- |
| `POST /workspaces/{id}/query` | 部分实现 | lexical/hybrid 有骨架，semantic、稳定 cursor、highlights 未实现 |
| `GET /workspaces/{id}/search/suggestions` | 缺失 | P1 |

查询当前实现需要重构：

- `hybrid` 不能继续使用固定 `score=1`，需要实际 lexical + vector 合并排序。
- `semantic` 模式需要 pgvector 查询和 embedding 可用性检查。
- `cursor` 必须基于稳定排序，而不是始终返回 `has_more=false`。
- 动态 filters 要使用资源模型字段 schema 校验，不能只把 JSON 直接传到 SQL。
- 返回 `highlights`，并统一 published/review/visibility 权限过滤。

### 2.10 会话和问答

| 最终态接口 | 当前状态 | 处理 |
| --- | --- | --- |
| 会话 GET/POST | 已实现 | 补 PATCH、归档和共享范围 |
| `PATCH /conversations/{id}` | 缺失 | P1 |
| `POST /conversations/{id}/archive` | 缺失 | P1 |
| 消息 GET/POST | 已实现 | 补分页、block 读取和最终状态 |
| `GET /conversations/{id}/blocks` | 缺失 | P1 |
| 普通/流式 chat | 已实现 | 按最终 SSE event schema 重构和补取消 |
| `GET /conversations/{id}/note` | 缺失 | P1 |
| note sync/publish | 已实现 | 补 ETag、状态查询和失败明细 |
| derivation create/get/finalize | 已实现 | 补状态机和合并冲突 |
| media register/get/transcribe | 已实现 | 补 transcript GET |
| `GET /conversation-media/{id}/transcript` | 缺失 | P1 |

当前会话 chat 已经能够调用 Agent，但最终态还需要：

- 固定 SSE 事件名和 `Last-Event-ID` 续传。
- `GET blocks` 用于断线恢复和消息块级渲染。
- conversation PATCH/archive 管理标题、可见性和生命周期。
- transcript 作为独立资源，不能依赖媒体状态对象中的偶然字段。

### 2.11 Agent Session

| 最终态接口 | 当前状态 | 处理 |
| --- | --- | --- |
| 创建 session | 已实现 | 补 session 对象和过期时间 |
| `GET /agent-sessions/{id}` | 缺失 | P1 |
| references/validate | 已实现 | 补 rejected reasons 和版本策略 |
| chat/stream | 已实现 | 统一引用、SSE、用量和错误 |
| `POST /agent-sessions/{id}/cancel` | 缺失 | P1 |

### 2.12 自动化和任务

| 最终态接口 | 当前状态 | 处理 |
| --- | --- | --- |
| Job 列表/创建/详情/PATCH | 已实现 | 补最终字段和严格 trigger 校验 |
| `DELETE /automation-jobs/{id}` | 缺失 | P1，软删除/禁用策略 |
| pause/resume | 已实现 | 补状态机和审计 |
| `POST /automation-jobs/{id}/run-now` | 缺失 | P0，最终替代直接 POST runs |
| runs GET/POST | 部分实现 | 保留查询，创建改走 run-now 或明确 manual run |
| task run GET/attempts/retry/cancel | 已实现 | 补终态字段和权限 |
| `GET /task-runs/{id}/events` | 缺失 | P0，SSE 进度流 |

最终态不允许前端直接修改 Run status；只能调用 `run-now/retry/cancel`，状态由 worker 写入。当前 `/automation-jobs/{jobId}/runs` 直接 POST 创建运行，需要和最终 `run-now` 语义统一。

### 2.13 导入导出

| 最终态接口 | 当前状态 | 处理 |
| --- | --- | --- |
| import POST/GET | 已实现 | 补 format、validation_mode、错误行明细 |
| export POST/GET | 已实现 | 补 include_attachments 和 checksum |
| `GET /export-jobs/{id}/download` | 缺失 | P0，正式授权下载 |

当前导出只返回内部 `output_object_key`，不能交给浏览器；必须实现下载授权或文件流，并记录下载审计。

### 2.14 通知和审计

| 最终态接口 | 当前状态 | 处理 |
| --- | --- | --- |
| notifications GET | 缺失 | P0 |
| unread-count | 缺失 | P0 |
| notification read/read-all | 缺失 | P0 |
| notifications SSE | 缺失 | P1 |
| activity GET | 已实现 | 按最终脱敏规则收敛 |
| audit-logs GET | 缺失 | P1，owner/admin |

## 3. 非路由级契约差异

### 3.1 资源模型 schema

最终态只保留：

```json
{"fields":[...],"additional_properties":false}
```

当前实现和测试仍出现 `properties` 形式；需要：

1. 删除旧 properties 分支和旧测试。
2. 更新 validator、迁移 worker、OpenAPI、前端 renderer。
3. 为 enum、multiselect、date、datetime、object、array、asset_reference 增加服务端校验。
4. 表单 sections、列表 filters、policy outlets 全部服务端校验引用关系。

### 3.2 资产状态机

最终态需要固定：

- `publication_status`: `draft|published|archived`
- `review_status`: `none|pending|approved|rejected|superseded`
- `workflow_status`: `draft|submitted|approved|rejected|published|retired`
- `quality`: `raw|ai_generated|human_confirmed`

当前数据库/服务仍有 `internal`、旧 workflow 状态或隐式状态判断，必须一次性迁移到新枚举，不提供旧值兼容。

### 3.3 并发和错误

- 资产、资源模型版本、审核决策、笔记发布必须返回 HTTP ETag，并严格处理 `If-Match`。
- 缺失 `If-Match` 统一 `428 if_match_required`；错误 ETag 统一 `412 if_match_failed`。
- 当前部分 handler 将缺失 If-Match 返回 422 或把冲突映射成通用 409，需要统一中间件/错误映射。
- 所有变更接口写入幂等表，并保存可重放响应。

### 3.4 分页

当前若干列表固定 `has_more=false` 或忽略 cursor。必须统一实现：

- workspace、assets、versions、containers、reviews、messages、notifications、audit、runs、attempts 都支持 cursor。
- cursor 绑定过滤条件和权限范围，过滤条件变化后旧 cursor 返回 `400 invalid_cursor`。
- SSE 断线恢复使用 `Last-Event-ID`，不能把 cursor 当 event id。

### 3.5 响应字段

当前很多响应是旧的最小对象，需要补齐最终态：

- 工作区：`status/member_count/default_*`。
- 成员：角色、状态、加入时间、最后活跃时间。
- 资产：容器、父资产、版本、质量、完整 source 和附件摘要。
- 审核：评论、提交人、审核人、ETag。
- 任务：进度、错误摘要、attempt、时间、输入范围。
- 会话：note asset、container、状态、消息计数、共享范围。

## 4. 推荐后端实施顺序

### P0：阻塞前端完整产品闭环

1. 统一最终态 API 路径、错误、幂等、ETag、分页中间件。
2. 注销、工作区创建、成员列表/邀请/角色/移除。
3. 资产版本历史、版本详情、版本创建/编辑、资产删除。
4. 容器最终路径、容器删除/移动、资产容器关联、文档子级读取。
5. 附件浏览器下载和 presigned download。
6. 搜索 semantic/hybrid、稳定 cursor、highlights、动态 filters。
7. 自动化 run-now、Run events SSE。
8. 导出 Job 正式下载。
9. 通知列表、未读数、已读。

### P1：完善产品工作流

1. 用户资料、工作区 PATCH/DELETE、Agent 应用管理。
2. 模型 version retire、最终 schema 收敛和迁移进度。
3. 资产 restore/duplicate、附件 PATCH。
4. 审核评论和完整审核筛选。
5. 会话 PATCH/archive/blocks、note GET、transcript GET。
6. Agent session GET/cancel。
7. 自动化 Job 删除、任务详情字段和任务流。
8. 通知 SSE、审计日志。

### P2：质量和运营能力

1. OpenAPI 与全态契约逐字段同步。
2. 所有异步 Job 的失败行、错误摘要、重试和审计。
3. 权限矩阵和资源模型 outlet 策略的集成测试。
4. SSE 续传、下载审计、限流和大请求保护。
5. 端到端 smoke test：模型 -> 资产 -> 审核 -> 发布 -> 搜索 -> Agent 引用 -> 导出下载。

## 5. 第一批编码任务清单

建议下一轮直接按以下任务开始，每项完成后补 handler、service、migration、OpenAPI、契约测试和 smoke test：

- [ ] `auth/session-v2`：DELETE session、profile、统一错误和 ETag。
- [ ] `workspace/membership-v2`：工作区 CRUD、成员/邀请/角色。
- [ ] `asset/version-v2`：版本资源、删除/恢复/复制、状态机。
- [ ] `container/document-v2`：最终路径、移动、删除、关联和 children。
- [ ] `attachment/download-v2`：浏览器下载、presigned URL、扫描状态。
- [ ] `search/query-v2`：semantic、hybrid、cursor、highlights、动态过滤。
- [ ] `review/comments-v2`：评论和最终审核过滤。
- [ ] `conversation/state-v2`：PATCH/archive/blocks/note/transcript/cancel。
- [ ] `automation/events-v2`：run-now、Run events SSE、最终状态机。
- [ ] `transfer/download-v2`：导出授权下载和下载审计。
- [ ] `notification/audit-v2`：通知、未读、SSE、审计查询。
- [ ] `contract-hardening-v2`：schema、状态、分页、错误、OpenAPI 全量收敛。

完成上述任务后，再把原型中的静态 mock 和旧 helper 全部切换到最终路径；不新增兼容别名，不保留旧状态转换。
