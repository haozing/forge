# Forge API 实现核对附录（历史文档）

> 本文件保留用于核对当前代码与旧联调状态，不再作为产品接口开发依据。  
> 最新、完整、无兼容包袱的产品契约请使用：[YXT产品全态API接口契约.md](D:/code2/agentchunzhi/docs/yxt/YXT产品全态API接口契约.md)。  
> 后续前后端实现、OpenAPI 生成和测试均以全态契约为准；本文件中的“当前实现”“缺口”描述不代表最终产品设计。

## 1. 页面与接口范围

原型中的页面和主要接口对应关系如下：

| 原型入口 | 页面状态 | 主要接口 |
| --- | --- | --- |
| 工作区选择、我的 | `thoughts`、`my-documents`、`my-notes` | 工作区、成员、偏好、统计、动态、资源列表 |
| 思考/新思考、待整理 | `thoughts`、`assistant`、`tasks` | 会话、消息、普通问答、流式问答、任务运行 |
| 镜头问答助手 | `shots` | 动态资源模型、资产、查询、助手会话 |
| 文档库、全部文档、文件夹树 | `documents` | 容器树、资产、资产移动、文档父子关系 |
| FAQ 知识库 | `faq` | `content_kind=faq` 的资源模型和资产 |
| 经典镜头库 | `shots` | `content_kind=record` 的动态资源模型和资产 |
| 审核/发布 | `review` | 审核队列、通过、驳回、批量处理、资产发布/归档 |
| 设置 | `settings` | 工作区设置、用户偏好、Agent 应用 |
| 顶部搜索 | 全局搜索 | 查询接口 |
| 导入/导出、自动化 | `tasks`、设置 | transfer、automation job、task run |

页面不得假设“经典镜头”的字段写死在前端。字段、表单、列表列、筛选器和发布策略都来自资源模型版本的 `field_schema/form_schema/list_schema/policy`。

## 2. 全局约定

### 2.1 基础地址、认证和请求头

- API 基础前缀：`/api`；浏览器前端优先使用 `/api/frontend/*`。
- 所有请求使用同源 Session Cookie；前端 `fetch` 必须带 `credentials: "include"`。
- JSON 请求头：`Content-Type: application/json`、`Accept: application/json`。
- 变更请求（`GET/HEAD` 以外）通常必须带 `Idempotency-Key`：非空、建议使用前端生成的唯一值，长度 16-200。重试同一业务操作必须复用同一 key。
- 需要并发控制的资源更新带 `If-Match`。响应中的 `ETag` 必须原样保存（包括引号），不要自己拼接。
- 附件上传是 `multipart/form-data`，字段名固定为 `file`，不能使用通用 JSON client。
- 流式聊天使用 `Accept: text/event-stream`，不能使用只解析 JSON 的 client。

### 2.2 统一错误

除流式连接外，错误响应均为 JSON：

```json
{
  "code": "validation_failed",
  "message": "请求参数校验失败",
  "request_id": "req_01...",
  "details": {"issues": [{"path": "fields.shot_size", "code": "required", "message": "不能为空"}]}
}
```

前端必须按 `code` 分支，不要按中文 `message` 分支。常见状态码：

| HTTP | 含义 | 常见 code |
| --- | --- | --- |
| 400 | JSON、UUID、查询参数或业务格式错误 | `invalid_json`、`invalid_uuid`、`invalid_query` |
| 401 | 没有登录或 Session 失效 | `unauthorized` |
| 403 | 已登录但不是工作区成员/没有动作权限 | `member_required`、`workspace_access_denied`、`forbidden` |
| 404 | 资源不存在或不属于当前工作区 | `not_found`、`asset_not_found`、`review_not_found` |
| 409 | 状态冲突、重复操作、幂等 key 冲突 | `conflict`、`etag_conflict`、`invalid_state` |
| 413 | 上传或请求体超限 | `payload_too_large` |
| 422 | 字段、动态 schema 或策略校验失败 | `validation_failed`、`model_schema_invalid` |
| 428 | 缺少必须的 `If-Match` | `precondition_required` |
| 502/503 | 外部 Agent/数据库/任务暂不可用 | `upstream_error`、`service_unavailable` |

### 2.3 分页、时间和 ID

- 列表默认返回 `{items: [], has_more: false}`；支持游标的接口同时返回 `next_cursor`。
- `limit` 默认 50，最大 100，除非接口另有说明；游标是 opaque string，前端不得解析。
- 时间均为 RFC3339 字符串；ID 均为 UUID 字符串。
- `null` 表示明确为空；没有返回的字段表示该接口不提供，不要用其他接口的字段推断。

### 2.4 权限

| 动作 | 最低权限 |
| --- | --- |
| 读取工作区、模型、资产、会话 | member |
| 创建/编辑资产、容器、会话、任务 | member（按工作区策略再限制） |
| 审核、发布、归档、模型发布/迁移 | editor/reviewer 或 owner/admin，具体由后端策略判断 |
| 工作区设置、模型管理 | owner/admin |

## 3. 登录与系统状态

### 3.1 创建 Session

`POST /api/sessions`

请求：

```json
{"login_name":"demo","password":"******"}
```

响应 `201`，同时写入 Session Cookie：

```json
{"user_id":"uuid","organization_id":"uuid","user_type":"member","expires_at":"2026-08-25T12:00:00Z"}
```

登录失败返回 `401 unauthorized`。当前没有专用 logout API；前端退出登录需要清理本地状态并等待 Cookie 过期，见“实现缺口”。

### 3.2 当前用户

`GET /api/me`

响应 `200`：`{user_id, organization_id, user_type, expires_at}`。当前 handler 不填充 `expires_at`，因此可能是零时间值；前端会话有效性以接口返回的 401 和 Cookie 为准，不要依赖这个字段倒计时。

### 3.3 健康检查

`GET /healthz`、`GET /readyz`

`healthz` 响应 `200`：`{status:"ok", time:"RFC3339"}`；`readyz` 就绪时响应 `200`：`{status:"ready", time:"RFC3339"}`，数据库不可用时返回 `503`。`readyz` 非 200 时不要进入工作区请求和 worker 轮询。

## 4. 工作区、成员、设置

### 4.1 工作区

- `GET /api/frontend/workspaces`
- `GET /api/frontend/workspaces/{workspaceId}`
- `GET /api/frontend/workspaces/{workspaceId}/members/me`
- `GET /api/frontend/workspaces/{workspaceId}/agent-applications`

工作区列表返回 `{items:[WorkspaceSummary]}`；单工作区返回：

```json
{
  "id":"uuid", "name":"经典镜头库", "description":"...", "role":"owner",
  "default_resource_model_id":"uuid", "default_visibility":"workspace",
  "counts":{"pending_conversations":0,"documents":0,"pending_reviews":0,"running_task_runs":0},
  "updated_at":"RFC3339"
}
```

成员返回 `{id,display_name,login_name,role}`。Agent 应用返回：

```json
{"id":"uuid","name":"镜头问答助手","provider":"dify","status":"enabled","capabilities":["chat"],"bound_agent_user_id":"uuid"}
```

### 4.2 计数、统计、动态

- `GET /api/frontend/workspaces/{workspaceId}/counts`
- `GET /api/frontend/workspaces/{workspaceId}/stats`
- `GET /api/frontend/workspaces/{workspaceId}/activity`

`counts` 返回 `pending_conversations/documents/pending_reviews/running_task_runs`。`stats` 返回 `assets_total/assets_published/assets_pending_review/assets_created_this_month/documents_total/task_run_success_rate/generated_at`。`activity` 返回：

```json
{"items":[{"event_id":"uuid","event_type":"asset_published","actor":{"id":"uuid","display_name":"张三","role":"editor"},"object_type":"asset","object_id":"uuid","summary":"...","metadata":{},"created_at":"RFC3339"}],"has_more":false}
```

### 4.3 工作区设置

- `GET /api/frontend/workspaces/{workspaceId}/settings`
- `PATCH /api/frontend/workspaces/{workspaceId}/settings`

响应/请求字段：`{name, description, default_visibility, default_resource_model_id}`。`default_visibility` 只能是 `private|workspace|internal`。PATCH 带 `Idempotency-Key`，可带 `If-Match`；冲突返回 `409 etag_conflict`。

### 4.4 用户偏好

- `GET /api/frontend/me/preferences`
- `PATCH /api/frontend/me/preferences`

请求和响应都是 JSON 对象，当前后端按用户原样保存偏好（例如 `{"theme":"light","workspace_id":"uuid"}`）。PATCH 带 `Idempotency-Key`。

## 5. 动态资源模型（经典镜头/FAQ/文档的共同基础）

### 5.1 模型和版本

- `GET/POST /api/frontend/workspaces/{workspaceId}/resource-models`
- `GET/PATCH /api/frontend/resource-models/{resourceModelId}`
- `GET/POST /api/frontend/resource-models/{resourceModelId}/versions`
- `GET/PATCH /api/frontend/resource-model-versions/{versionId}`
- `POST /api/frontend/resource-model-versions/{versionId}/validate`
- `POST /api/frontend/resource-model-versions/{versionId}/publish`

模型对象：

```json
{
  "id":"uuid", "workspace_id":"uuid", "model_key":"classic_shot", "name":"经典镜头",
  "description":"...", "content_kind":"record", "status":"active",
  "current_version":{"id":"uuid","version_no":2,"status":"published","schema_checksum":"sha256...","field_schema":{},"form_schema":{},"list_schema":{},"policy":{},"published_at":"RFC3339"},
  "model_capabilities":{}, "allowed_actions":["create","update","publish"], "member_role":"editor",
  "created_at":"RFC3339", "updated_at":"RFC3339"
}
```

模型创建请求：

```json
{
  "model_key":"classic_shot",
  "name":"经典镜头",
  "description":"镜头知识记录",
  "content_kind":"record",
  "initial_version":{"field_schema":{},"form_schema":{"sections":[]},"list_schema":{"columns":[],"filters":[]},"policy":{}}
}
```

`model_key` 为 2-80 个字符，格式 `[a-z][a-z0-9_]+`；`content_kind` 为 `record|document|faq`。版本创建/更新请求体为四个 schema 字段（均为 JSON object）：`field_schema/form_schema/list_schema/policy`。

### 5.2 Schema 契约

`field_schema` 支持以下两种等价表达，推荐统一使用 `fields` 形式：

```json
{"fields":[{"key":"shot_size","type":"enum","label":"景别","required":true,"options":["特写","近景","中景","全景"]}]}
```

或：

```json
{"properties":{"shot_size":{"type":"string","title":"景别"}}}
```

约束：

- 字段 key：`^[a-z][a-z0-9_]{1,63}$`。
- 保留 key：`id,title,markdown,summary,tags,source,attachments,created_at,updated_at,created_by,updated_by,visibility,publication_status,review_status,quality`，不能放入动态字段。
- 类型：`string|text|integer|number|boolean|date|datetime|enum|multiselect|object|array`。`options` 存在时必须是非空数组；`object` 不可标记为 searchable。
- `form_schema`：`{sections:[{title?,fields:["shot_size",...]}]}`；字段引用必须存在。
- `list_schema`：`{columns:[],filters:[]}`；元素可为字符串或 `{field|key,...}`，只能引用动态字段或元字段 `id/title/summary/tags/visibility/publication_status/review_status/created_at/updated_at`。
- `policy` 必须为 object；可用 `policy.outlets` 定义各发布渠道及 `enabled`。

版本状态字段实际为 `draft|published|retired`；`validate` 不改变 status，而是写入 `validated_at`。发布要求 `validated_at` 已有值，发布后自动退役旧 published 版本并使模型 `active`。只有 draft 可编辑。校验失败返回 `422 model_schema_invalid`，`details.issues` 给出路径。

### 5.3 模型迁移

- `POST /api/frontend/resource-models/{resourceModelId}/migration-previews`
- `POST /api/frontend/resource-models/{resourceModelId}/migrations`
- `GET /api/frontend/resource-model-migrations/{migrationId}`
- `POST /api/frontend/resource-model-migrations/{migrationId}/cancel`

请求：

```json
{
  "source_version_id":"uuid", "target_version_id":"uuid", "asset_scope":{},
  "mapping":{"old_field":"new_field"}, "defaults":{"new_field":"默认值"}
}
```

预览返回迁移估算/差异（预览本身不要求幂等 key）；启动返回 `202` 和迁移记录，启动/cancel 必须带 `Idempotency-Key`。记录至少包含 `id,resource_model_id,source_version_id,target_version_id,status,created_at`，完成后补充进度、统计和错误信息。状态以服务端返回为准，前端通过 GET 轮询；cancel 只能取消可取消状态。

## 6. 资产、查询和发布

### 6.1 资产列表和详情

- `GET /api/frontend/workspaces/{workspaceId}/assets`
- `POST /api/frontend/workspaces/{workspaceId}/assets`
- `GET/PATCH /api/frontend/assets/{assetId}`

列表查询参数：`q, resource_model_id, visibility, publication_status, review_status, created_by=me, limit, cursor`。响应：`{items:[MemberAsset],has_more,next_cursor}`。

资产对象：

```json
{
  "id":"uuid", "workspace_id":"uuid", "resource_model_id":"uuid", "content_kind":"record",
  "resource_model_version_id":"uuid", "title":"夜景反打", "summary":"...", "markdown":"...",
  "fields":{"shot_size":"近景"}, "tags":["夜景"], "source":{}, "visibility":"workspace",
  "publication_status":"internal", "review_status":"none", "quality":"raw",
  "current_working_version_id":"uuid", "current_published_version_id":"uuid",
  "created_by":{"id":"uuid","display_name":"张三"}, "updated_at":"RFC3339", "etag":"uuid"
}
```

创建请求（动态字段由模型版本决定）：

```json
{"resource_model_id":"uuid","title":"夜景反打","markdown":"...","fields":{"shot_size":"近景"},"tags":["夜景"],"source":{"type":"manual"},"visibility":"workspace"}
```

更新请求所有字段可选：`title, markdown, fields, tags, visibility, source`。PATCH 必须带 `If-Match: <GET 返回的 ETag>`，后端通过新 working version 写入，不是原地覆盖；成功后使用响应的新 `ETag`。创建/更新均带 `Idempotency-Key`。

创建要求模型为 active 且 current version 已 published；`fields` 必须通过动态 schema 校验。默认可见性为 `private`，初始发布状态为 `internal`。

### 6.2 审核、发布、归档

- `POST /api/frontend/assets/{assetId}/submit-review`
- `POST /api/frontend/assets/{assetId}/publish`
- `POST /api/frontend/assets/{assetId}/archive`

提交审核请求可选：`{asset_version_id?, comment?}`；未传版本使用当前 working version。成功返回 `201`：`{review_id,asset_id,asset_version_id,status}`。

发布请求可选 `{asset_version_id?}`；归档无请求体。发布前该版本必须存在 approved 审核记录，否则返回 `409`；两者都带 `Idempotency-Key`，状态不允许时返回 `409 invalid_state`。

### 6.3 查询

`POST /api/frontend/workspaces/{workspaceId}/query`

请求：

```json
{
  "mode":"hybrid", "query":"夜景反打", "resource_model_ids":["uuid"],
  "visibility":["workspace"], "publication_status":["published"],
  "filters":{"shot_size":"近景"}, "top_k":20, "cursor":"opaque"
}
```

`mode` 为 `lexical|hybrid`，`top_k` 默认 20、最大 100；查询是无副作用请求，不要求 `Idempotency-Key`。响应：

```json
{"items":[{"asset_id":"uuid","asset_version_id":"uuid","title":"...","summary":"...","fields":{},"tags":[],"visibility":"workspace","publication_status":"published","review_status":"approved","snippet":"...","score":1,"updated_at":"RFC3339"}],"next_cursor":null,"has_more":false}
```

当前实现仍是资产列表上的文本匹配兜底：`score`/`snippet` 不是向量检索结果，cursor 没有完整分页语义；前端只能把它当搜索结果，不得展示“相关度精确”承诺。资产列表 HTTP handler 当前未接收动态 `filters` 参数，需要先使用 query 接口。

## 7. 容器、文档树和附件

### 7.1 容器

- `GET/POST /api/frontend/workspaces/{workspaceId}/containers/tree`（同一路由分别用于树查询和创建）
- `GET/PATCH /api/frontend/containers/{containerId}`
- `GET /api/frontend/containers/{containerId}/assets`

容器：`{id,workspace_id,parent_id,name,sort_key,kind,status,visibility,created_by,created_at,updated_at,children?}`。`kind` 为 `document|faq|chat|note`，`status`/`visibility` 使用服务端返回值。

创建：`{parent_id?,name,sort_key?,kind,visibility}`；更新：`{name?,sort_key?,status?}`。当前 API 没有把 `parent_id` 作为 PATCH 移动字段；router 当前没有注册容器 DELETE 路由（虽然服务层/handler 有删除实现），因此前端不能调用删除。tree 返回 `{items:[recursive container]}`；assets 返回 `{items:[asset_id...]}`。

### 7.2 资产移动和文档父子关系

- `POST /api/frontend/workspaces/{workspaceId}/assets/{assetId}/move`
- `POST /api/frontend/assets/{assetId}/move`
- `POST /api/frontend/workspaces/{workspaceId}/assets/{assetId}/document-parent`
- `DELETE /api/frontend/workspaces/{workspaceId}/assets/{assetId}/document-parent`
- `POST /api/frontend/assets/{assetId}/document-parent`
- `DELETE /api/frontend/assets/{assetId}/document-parent`

移动请求：`{container_id:"uuid", operation:"add|remove|replace"}`。`replace` 会替换全部容器关联。文档父级 POST：`{parent_asset_id:"uuid"}`；只能在同一工作区的 document 资产间建立关系，后端防循环；DELETE 清除父级。所有变更带 `Idempotency-Key`。

### 7.3 附件

- `GET /api/frontend/asset-versions/{versionId}/attachments`
- `POST /api/frontend/asset-versions/{versionId}/attachments`（`multipart/form-data`，一个 `file`，默认最大 50 MiB）
- `GET/DELETE /api/frontend/attachments/{attachmentId}`
- `POST /api/frontend/attachments/{attachmentId}/link`，请求 `{asset_version_id:"uuid"}`
- 浏览器下载：`GET /api/attachments/{attachmentId}/download`（二进制、`Content-Disposition`）

附件列表返回 `{items:[attachment status],has_more:false}`；状态对象至少应被当作不透明对象保存，展示时使用服务端的文件名、媒体类型、大小、扫描状态和关联版本字段。上传路由当前不要求幂等 key，删除/关联要求幂等 key。上传和 SSE 必须绕过现有只支持 JSON 的 `client.js`。

## 8. 审核队列

- `GET /api/frontend/workspaces/{workspaceId}/reviews?status=pending`
- `GET /api/frontend/reviews/{reviewId}`
- `POST /api/frontend/reviews/{reviewId}/approve`
- `POST /api/frontend/reviews/{reviewId}/reject`
- `POST /api/frontend/reviews/batch`

当前 handler 只读取 `status`，服务端固定最多返回 100 条，`limit/cursor` 暂不生效。审核项：

```json
{
  "review_id":"uuid","workspace_id":"uuid","asset_id":"uuid","asset_version_id":"uuid",
  "resource_model_id":"uuid","resource_model_name":"经典镜头","title":"...","fields":{},
  "quality":"raw","status":"pending","comment":"...",
  "submitted_by":{"id":"uuid","display_name":"张三"},"reviewed_by":null,
  "submitted_at":"RFC3339","reviewed_at":null,"etag":"uuid"
}
```

approve/reject 请求均可用：`{comment?, expected_version_id?}`。`expected_version_id` 用于防止审核旧版本，冲突返回 `409`。单条成功 `200`；批量请求：

```json
{"items":[{"review_id":"uuid","decision":"approve","comment":"...","expected_version_id":"uuid"}]}
```

条数 1-100，响应 `207`，逐项返回 `{review_id,status,decision?,error_code?}`。前端必须允许部分成功并刷新失败项。

## 9. 会话、消息、思考和助手

### 9.1 会话和消息

- `GET/POST /api/frontend/workspaces/{workspaceId}/conversations`
- `GET /api/frontend/conversations/{conversationId}`
- `GET/POST /api/frontend/conversations/{conversationId}/messages`

创建：

```json
{"agent_application_id":"uuid","title":"新思考","source":"chat_interface","visibility":"workspace"}
```

会话摘要字段：`conversation_id,workspace_id,title,source,visibility,status,last_message_preview,message_count,updated_at`；详情额外返回 `container_id,note_container_id,note_asset_id,agent_application_id,bound_agent_user_id`。

追加消息：

```json
{"role":"user","content":"请找出夜景反打的经典案例","content_format":"markdown","provider_conversation_id":"...","provider_message_id":"...","status":"completed","reply_to_block_id":"uuid"}
```

返回消息结果：`{block_revision_id,block_id,conversation_id,sequence_no,role,status}`。消息列表返回 `{items:[{block_revision_id,block_id,conversation_id,role,content,content_format,status,provider_conversation_id,provider_message_id,sequence_no,created_at}],has_more:false}`。

### 9.2 普通/流式问答

- `POST /api/frontend/conversations/{conversationId}/chat`
- `POST /api/frontend/conversations/{conversationId}/chat/stream`

请求仅要求：`{"query":"问题内容"}`（1-10000 字符），带 `Idempotency-Key`。普通响应为 Agent 答案对象，核心字段：`answer,conversation_id,message_id,references,rejected_reference_count`；`references` 是已通过权限校验的资产引用。流式接口返回 `text/event-stream`，服务端事件内容透传 Agent 流并持久化用户/助手消息；前端应处理 `message/delta`、完成和错误事件，未知事件保留日志而不崩溃。

会话列表当前默认只返回当前发起人可见的会话；“待整理”若要显示全团队会话，需要后端增加明确的共享范围参数或接口。

### 9.3 笔记同步、发布、推导和媒体

- `POST /api/frontend/conversations/{conversationId}/note/sync`：无 body，带幂等 key；返回 `conversation_id,note_asset_id,asset_version_id,message_count,status`。
- `POST /api/frontend/conversations/{conversationId}/note/publish`：`{expected_version_id?}`，带幂等 key；返回 `conversation_id,note_asset_id,asset_version_id,publication_status,quality`。
- `POST /api/frontend/conversations/{conversationId}/derivations`：`{source_block_revision_ids:[uuid...],context_policy:"summary_only|selected_only|full",title?}`，来源条数 1-50；返回 derivation 记录。
- `GET /api/frontend/derivations/{derivationId}`：返回 derivation 当前状态。
- `POST /api/frontend/derivations/{derivationId}/finalize`：提交目标资产/版本和合并选择；典型字段为 `disposition,target_asset_id,expected_source_asset_version_id,expected_target_asset_version_id,expected_container_version_id,merge_mode,target_block_id`，以服务端返回的状态机为准。
- `POST /api/frontend/conversations/{conversationId}/media`：`{attachment_id,media_kind:"audio|video",language?,duration_ms?}`；返回 `media_id,conversation_id,attachment_id,media_kind,status,language,duration_ms,transcription_job_id,transcription_block_revision_id,created_at,updated_at`。
- `GET /api/frontend/conversation-media/{mediaId}`：查询媒体状态。
- `POST /api/frontend/conversation-media/{mediaId}/transcribe`：启动转写，返回任务/媒体状态；前端轮询 GET，不要假设同步得到全文。

## 10. Agent 应用会话

- `POST /api/frontend/agent-applications/{applicationId}/sessions`：创建 Agent session，无 body，带幂等 key。
- `POST /api/frontend/agent-sessions/{sessionId}/references/validate`：`{references:[{asset_id,asset_version_id}]}`，1-50 条；返回通过/拒绝的引用结果。
- `POST /api/frontend/agent-sessions/{sessionId}/chat`：`{query,conversation_id?}`，普通 JSON 答案。
- `POST /api/frontend/agent-sessions/{sessionId}/chat/stream`：同请求体，SSE。

直接使用会话接口时，引用必须先 validate；`conversationChat` 会在服务端自动创建 session 并注入 `conversation_id`，前端通常不需要为每条消息重复创建。

## 11. 自动化与任务运行

### 11.1 自动化 Job

- `GET/POST /api/frontend/workspaces/{workspaceId}/automation-jobs`
- `GET/PATCH /api/frontend/automation-jobs/{jobId}`
- `POST /api/frontend/automation-jobs/{jobId}/pause`
- `POST /api/frontend/automation-jobs/{jobId}/resume`

创建字段：

```json
{
  "name":"每日资料整理","operation":"prepare_asset","agent_application_id":"uuid",
  "trigger":{"type":"manual"},"timezone":"Asia/Shanghai","concurrency_policy":"forbid",
  "input_scope":{},"max_attempts":3,"retry_backoff":{},"enabled":true
}
```

`operation`：`prepare_asset|publish|archive|reindex|import|export`；trigger 可为 `manual|cron|event`（cron 需要 expression，event 需要 event_type）；并发策略 `forbid|replace|allow`；重试次数 1-20，默认 3。Agent application 必须属于当前工作区且 enabled。Job 更新可改 `name,enabled,trigger,concurrency_policy` 等服务端允许字段。pause/resume 是显式动作，均带幂等 key。

### 11.2 Run 和 Attempt

- `GET/POST /api/frontend/automation-jobs/{jobId}/runs`
- `GET /api/frontend/task-runs/{runId}`
- `GET /api/frontend/task-runs/{runId}/attempts`
- `POST /api/frontend/task-runs/{runId}/retry`
- `POST /api/frontend/task-runs/{runId}/cancel`

Run 创建查询参数 `source=manual|automation|agent`（默认 `manual`），仅 enabled job 可手动启动，返回 `202`。Run 至少包含：`id,workspace_id,automation_job_id,source,operation,status,progress,attempt_count,error_code,created_at,started_at,completed_at,next_attempt_at,input_scope`。Attempt 至少包含：`id,run_id,attempt_no,status,error_code,error_summary,claimed_by,lease_expires_at,next_retry_at,started_at,completed_at`。retry/cancel 只能对允许的状态执行，响应为最新 run 或 `202`。

## 12. 导入导出

- `POST /api/frontend/workspaces/{workspaceId}/assets/imports`
- `GET /api/frontend/import-jobs/{jobId}`
- `POST /api/frontend/workspaces/{workspaceId}/assets/exports`
- `GET /api/frontend/export-jobs/{jobId}`

导入请求：

```json
{"resource_model_id":"uuid","resource_model_version_id":"uuid","rows":[{"title":"...","fields":{}}],"source_name":"shots.jsonl"}
```

`rows` 1-10000，模型版本必须已发布；成功返回 `202` 和 `{id,workspace_id,resource_model_id,resource_model_version_id,status,summary,source_name,created_at,completed_at}`。

导出请求：

```json
{"resource_model_id":"uuid","filters":{"publication_status":"published"},"format":"jsonl"}
```

`format` 为 `jsonl|csv`，返回 `202` 和 `{id,workspace_id,resource_model_id,status,format,created_at,completed_at,output_object_key,output_size,output_checksum}`。当前 `output_object_key` 是内部存储 key，没有面向成员的下载/签名 URL 接口，前端不能把它直接当 URL；见缺口清单。

## 13. 前端必须知道的支持路由和非前端路由

以下路由存在于当前 router，但不是新前端优先契约：

| 路由组 | 路由 |
| --- | --- |
| 附件兼容路由 | `POST /api/asset-versions/{versionId}/attachments`、`GET /api/attachments/{attachmentId}`、`GET /api/attachments/{attachmentId}/download` |
| Open API | `POST /api/open/v1/query`、`POST/PATCH /api/open/v1/assets[/{assetId}]`、`POST /api/open/v1/assets/{assetId}/publish`、`POST /api/open/v1/assets/{assetId}/archive`、`GET /api/open/v1/assets/{assetId}/references` |
| Agent Open API | `POST /api/open/v1/agent/tasks`、`GET /api/open/v1/agent/tasks/{taskId}` |
| 管理端 | `/api/admin/*` 模型、审核、任务、系统配置等路由，仅供管理后台/内部工具使用 |
| 附件下载 | `/api/open/v1/attachments/{attachmentId}/download` 及普通 `/api/attachments/.../download`，返回二进制 |

Open API 使用独立鉴权/权限语义，不应让原型前端混用；浏览器页面统一使用本文件第 4-12 节的 `/api/frontend`。

## 14. 推荐联调流程

1. 登录后请求 `/api/me` 和 `/api/frontend/workspaces`，选择工作区，再并行拉取 counts、settings、agent-applications、resource-models、containers/tree。
2. 进入经典镜头库/FAQ 时读取模型 `current_version`，用 `field_schema` 生成表单，用 `form_schema` 控制分组，用 `list_schema` 控制列和筛选器；禁止在代码中写固定字段。
3. 新建或编辑资产后保存响应 `ETag`；下一次 PATCH 使用该值。遇到 409 重新 GET 并提示用户合并，不要静默覆盖。
4. 审核页按 status 拉取队列；决策带 `expected_version_id`。批量接口按 207 的逐项结果刷新。
5. 思考页先创建会话，再写入用户消息/调用 chat；流式 UI 使用 SSE，完成后刷新消息列表和 note 状态。
6. 文档页分别处理“容器树”和“资产父子关系”；移动资产使用 `operation`，父文档使用 document-parent 接口。
7. 导入、导出、自动化和转写全部按 202 异步任务处理，使用 GET 轮询并展示失败原因。

## 15. 当前实现缺口与前端阻塞项

这些问题已从原型需要与实际代码对照得出，不能在前端自行猜测：

1. **注销接口缺失**：没有 `DELETE /api/sessions` 或等价 logout；需要后端提供会话撤销/清 cookie 接口。
2. **通知接口缺失**：原型顶部有通知入口/未读点，但没有 notifications、unread-count、mark-read 路由。
3. **导出下载缺失**：export job 只有内部 `output_object_key`，没有成员授权的下载 URL/流式下载接口。
4. **附件与流式 client 不匹配**：现有 `docs/yxt/api/client.js` 固定 JSON body/JSON response；必须增加 multipart 和 SSE client，或单独实现 fetch。
5. **前端 helper 不完整**：缺少模型 PATCH、版本 GET/PATCH、迁移、附件、容器增删改、资产移动/父子、review batch、automation pause/resume/attempts、note/derivation/media、agent session 等封装。
6. **动态表单渲染不完整**：`render/schema.js` 当前只覆盖简单 text/textarea/number/boolean，未实现 enum、multiselect、date、datetime、object、array、required、options、sections 和 policy outlets。
7. **资产列表没有动态 filters 参数**：服务层支持动态过滤，但当前 frontend list handler 没有解析 `filters`；正式使用应调用 query，或补充列表契约。
8. **查询能力是兜底实现**：`mode=hybrid` 当前没有真实向量排序，score/snippet/cursor 不能作为强搜索承诺；需要后端补齐 PGroonga/pgvector 查询语义和稳定游标。
9. **文档层级读取不完整**：已有写入 document-parent 接口，但资产详情/列表没有统一返回 parent/children，也没有按父级查询接口；文档树前端无法只靠一个接口恢复完整资产层级。
10. **容器删除/重挂载接口不完整**：router 当前未注册 `DELETE /api/frontend/containers/{containerId}`；容器 PATCH 不能改 `parent_id`，创建时的 `sort_key` 当前实现未完整落库；需要补齐删除、移动和排序契约。
11. **会话范围不足**：会话列表默认是当前发起人范围；原型的团队“待整理”需要共享/按成员筛选的正式参数。
12. **OpenAPI 文档需同步**：根目录现有 OpenAPI 文件不能替代本文，新增/实际存在的 frontend 路由、动态 schema、SSE、异步任务和错误细节必须回写 OpenAPI。

## 16. 联调验收清单

- [ ] 未登录请求统一得到 401；非成员得到 403；错误包含 `request_id`。
- [ ] 所有写请求可重放且幂等；重复 key 不产生重复资产、任务、消息或审核记录。
- [ ] 资产 PATCH 的 ETag 冲突能被前端捕获并重新加载。
- [ ] 模型版本可完成 draft、validate、publish，旧版本自动退役。
- [ ] 使用同一套动态 schema 能生成经典镜头、FAQ、文档三类页面。
- [ ] 资产创建、审核、发布、归档状态和 review 队列可闭环。
- [ ] 容器树、资产容器关联、文档父子关联的读写结果一致。
- [ ] 附件上传、扫描状态、关联、删除和浏览器下载均已实测。
- [ ] 普通 chat 和 SSE chat 都能持久化消息，断线后可通过 messages 接口恢复。
- [ ] note sync/publish、derivation finalize、media transcribe 的异步/版本冲突状态可展示。
- [ ] automation run、retry、cancel、attempts 和 transfer job 均可轮询到终态。
- [ ] 迁移完成后资产仍引用正确的 resource model version，失败项可追溯。
- [ ] 补齐第 15 节阻塞项后，再把原型中静态 mock 数据逐页切换到这些真实接口。
