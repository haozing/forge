# Forge 产品全态 API 接口契约

> 版本：v2.0  
> 状态：目标态唯一契约（Normative）  
> 适用：Forge 后台前端、API 服务、异步 worker、Agent 集成  
> 规则：本文只定义产品最终态，不承诺旧接口、旧字段、旧状态或旧数据兼容。后端实现以本文为准逐项完成。

## 0. 产品边界

Forge 是一个面向工作区的知识资产系统，核心对象为：

- **工作区**：成员、权限、设置、通知和活动的边界。
- **资源模型**：用户定义“经典镜头”、FAQ、文档等内容类型的字段、表单、列表和策略。
- **资产**：资源模型下的一条内容记录，所有编辑都产生可追踪版本。
- **容器**：文档库、FAQ 分组、会话、笔记等目录节点。
- **审核**：资产版本发布前的审核任务和决策记录。
- **会话**：思考、问答、消息块、笔记同步和派生内容的协作边界。
- **Agent 应用**：工作区绑定的问答或自动化能力。
- **任务**：导入、导出、自动化、转写、模型迁移等异步操作。

“经典镜头库”不是后端固定表，也不是前端固定字段，而是一个 `content_kind=record` 的资源模型。FAQ 和文档使用同一套动态模型协议。

## 1. 传输层和统一约定

### 1.1 基础地址和版本

- 浏览器 API 唯一前缀：`/api/frontend`。
- Agent/Open API 使用独立的 `/api/open/v1`，不能在浏览器页面中混用。
- 本文是当前唯一版本，不提供旧路径别名，不做字段降级或旧状态转换。
- 所有 JSON 响应使用 UTF-8；请求体默认上限 2 MiB，附件按附件协议单独限制。

### 1.2 认证

浏览器使用 HttpOnly、Secure、SameSite=Lax 的 Session Cookie：

```http
Cookie: Forge_session=<opaque>
```

前端请求必须使用 `credentials: "include"`。未登录返回 `401`，已登录但不属于工作区返回 `403`。

### 1.3 请求头

| Header | 适用 | 约定 |
| --- | --- | --- |
| `Accept` | 所有请求 | JSON 使用 `application/json`，流式使用 `text/event-stream` |
| `Content-Type` | JSON 写请求 | `application/json` |
| `Idempotency-Key` | 所有会改变状态的请求 | 必填，16-200 字符；相同主体重试必须复用 |
| `If-Match` | 更新、发布、审核决策 | 必填时使用上次响应的 ETag，支持 `*` 仅在明确允许时使用 |
| `X-Request-ID` | 可选 | 客户端追踪 ID，服务端会在错误中回显/生成 `request_id` |

幂等键的作用域为“用户 + 操作 + 资源”。相同 key 携带不同请求体返回 `409 idempotency_conflict`。

### 1.4 统一响应和分页

单资源直接返回对象；列表统一返回：

```json
{
  "items": [],
  "next_cursor": null,
  "has_more": false
}
```

分页请求统一使用 `limit`（默认 50，最大 100）和 `cursor`。游标是不可解析的不透明字符串，排序由服务端固定为“更新时间倒序、ID 正序”或接口明确声明的排序。

### 1.5 统一错误

```json
{
  "code": "validation_failed",
  "message": "Request validation failed",
  "request_id": "req_01J...",
  "details": {
    "issues": [
      {"path": "fields.shot_size", "code": "required", "message": "field is required"}
    ]
  }
}
```

`message` 只用于日志和兜底展示，前端必须按 `code` 处理。标准状态：

| HTTP | code 示例 | 前端处理 |
| --- | --- | --- |
| 400 | `invalid_json`、`invalid_query` | 修正请求，不重试 |
| 401 | `unauthorized`、`session_expired` | 清理状态并跳转登录 |
| 403 | `workspace_access_denied`、`permission_denied` | 展示无权限状态 |
| 404 | `resource_not_found`、`asset_not_found` | 移除失效引用并刷新 |
| 409 | `conflict`、`etag_conflict`、`invalid_state`、`idempotency_conflict` | 重新 GET，按最新状态决策 |
| 412 | `if_match_failed` | 重新加载资源，禁止静默覆盖 |
| 413 | `payload_too_large`、`attachment_too_large` | 提示大小限制 |
| 422 | `validation_failed`、`schema_invalid` | 定位 `details.issues` |
| 428 | `if_match_required` | 先 GET 再写入 |
| 429 | `rate_limited` | 按 `Retry-After` 重试 |
| 500 | `internal_error` | 展示 request_id，避免重复提交 |
| 502/503 | `upstream_unavailable`、`service_unavailable` | 异步任务/短暂重试 |

### 1.6 时间、ID和枚举

- ID 使用 UUID 字符串。
- 时间使用 RFC3339 UTC，例如 `2026-08-25T12:00:00Z`。
- 货币、计数和百分比使用 JSON number；不要发送 NaN/Infinity。
- 枚举值大小写固定为小写 snake_case；未知枚举必须按错误处理，不能回退成默认值。

## 2. 权限模型

工作区角色：

| 角色 | 能力 |
| --- | --- |
| `owner` | 工作区、成员、模型、策略、审计的全部管理能力 |
| `admin` | 除转移/删除工作区外的管理能力 |
| `editor` | 创建、编辑、提交审核；不能管理成员和模型发布策略 |
| `reviewer` | 查看审核队列、通过/驳回、发布已批准版本 |
| `member` | 读取共享内容、创建私有内容、使用会话和 Agent |
| `viewer` | 只读共享内容和会话 |

资源动作统一命名：`workspace.read/write/manage`、`model.read/write/publish`、`asset.read/write/publish/archive`、`review.read/decide`、`automation.read/write/run`、`conversation.read/write`、`attachment.read/write`、`audit.read`。

私有资产只有创建者、owner、admin 可读；`workspace` 资产对工作区成员可读；`internal` 资产只对拥有资源模型访问范围的成员/Agent 可读。

## 3. 认证、用户和偏好

### 3.1 登录和注销

#### `POST /api/sessions`

请求：

```json
{"login_name":"demo","password":"******"}
```

响应 `201`：

```json
{"user_id":"uuid","organization_id":"uuid","user_type":"member","expires_at":"RFC3339"}
```

失败：`401 invalid_credentials`、`422 invalid_login_request`。

#### `DELETE /api/sessions`

撤销当前 Session 并清除 Cookie。响应 `204`。该接口幂等，多次调用均返回 `204`。

### 3.2 当前用户

- `GET /api/me`：返回 `{user_id,organization_id,user_type,display_name,login_name,avatar_url}`。
- `PATCH /api/me/profile`：请求 `{display_name?,avatar_url?}`，返回最新用户对象，必须 `Idempotency-Key`。
- `GET /api/frontend/me/preferences`：返回用户偏好对象。
- `PATCH /api/frontend/me/preferences`：请求 `{theme?,locale?,default_workspace_id?,sidebar_collapsed?,density?,notification_settings?}`，未知字段拒绝，返回最新偏好。

## 4. 工作区和成员

### 4.1 工作区资源

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/frontend/workspaces` | 当前用户可访问的工作区 |
| POST | `/api/frontend/workspaces` | 创建工作区 |
| GET | `/api/frontend/workspaces/{workspaceId}` | 工作区详情 |
| PATCH | `/api/frontend/workspaces/{workspaceId}` | 更新名称、描述、头像、状态 |
| DELETE | `/api/frontend/workspaces/{workspaceId}` | 删除工作区，owner 操作，异步返回 `202` 和 deletion job |
| GET | `/api/frontend/workspaces/{workspaceId}/counts` | 侧边栏计数 |
| GET | `/api/frontend/workspaces/{workspaceId}/stats` | 仪表盘统计 |
| GET | `/api/frontend/workspaces/{workspaceId}/activity` | 活动流 |
| GET/PATCH | `/api/frontend/workspaces/{workspaceId}/settings` | 工作区设置 |

工作区对象：

```json
{
  "id":"uuid", "name":"经典镜头库", "description":"...", "avatar_url":null,
  "status":"active", "role":"owner", "member_count":8,
  "default_visibility":"workspace", "default_resource_model_id":"uuid",
  "counts":{"pending_conversations":2,"documents":18,"pending_reviews":5,"running_task_runs":1},
  "created_at":"RFC3339", "updated_at":"RFC3339"
}
```

创建请求：`{name,description?,default_visibility?,default_resource_model_id?}`。设置对象：`{default_visibility,default_resource_model_id,review_policy,search_policy,retention_policy,notification_policy}`。

`stats` 返回 `assets_total/assets_published/assets_pending_review/documents_total/faq_total/task_run_success_rate/storage_bytes/generated_at`。`activity` 每项返回 `event_id,event_type,actor,object_type,object_id,summary,metadata,created_at`。

### 4.2 成员和邀请

| 方法 | 路径 |
| --- | --- |
| GET | `/api/frontend/workspaces/{workspaceId}/members` |
| GET | `/api/frontend/workspaces/{workspaceId}/members/me` |
| POST | `/api/frontend/workspaces/{workspaceId}/member-invitations` |
| GET | `/api/frontend/workspaces/{workspaceId}/member-invitations` |
| POST | `/api/frontend/member-invitations/{invitationId}/accept` |
| POST | `/api/frontend/member-invitations/{invitationId}/revoke` |
| PATCH | `/api/frontend/workspace-members/{memberId}` |
| DELETE | `/api/frontend/workspace-members/{memberId}` |

成员对象：`{id,display_name,login_name,avatar_url,role,status,joined_at,last_seen_at}`。邀请请求：`{login_name,email?,role,expires_in_hours?}`；成员角色变更请求：`{role}`。邀请状态：`pending|accepted|expired|revoked`。

### 4.3 Agent 应用目录

- `GET /api/frontend/workspaces/{workspaceId}/agent-applications`
- `POST /api/frontend/workspaces/{workspaceId}/agent-applications`
- `GET/PATCH /api/frontend/agent-applications/{applicationId}`
- `POST /api/frontend/agent-applications/{applicationId}/enable`
- `POST /api/frontend/agent-applications/{applicationId}/disable`

Agent 应用：`{id,name,provider,status,capabilities,description,config_summary,bound_agent_user_id,created_at,updated_at}`。浏览器永远不能读取 provider secret；配置只返回脱敏摘要。

## 5. 动态资源模型

### 5.1 模型接口

| 方法 | 路径 |
| --- | --- |
| GET/POST | `/api/frontend/workspaces/{workspaceId}/resource-models` |
| GET/PATCH | `/api/frontend/resource-models/{resourceModelId}` |
| GET/POST | `/api/frontend/resource-models/{resourceModelId}/versions` |
| GET/PATCH | `/api/frontend/resource-model-versions/{versionId}` |
| POST | `/api/frontend/resource-model-versions/{versionId}/validate` |
| POST | `/api/frontend/resource-model-versions/{versionId}/publish` |
| POST | `/api/frontend/resource-model-versions/{versionId}/retire` |
| POST | `/api/frontend/resource-models/{resourceModelId}/migration-previews` |
| POST | `/api/frontend/resource-models/{resourceModelId}/migrations` |
| GET | `/api/frontend/resource-model-migrations/{migrationId}` |
| POST | `/api/frontend/resource-model-migrations/{migrationId}/cancel` |

模型对象：

```json
{
  "id":"uuid", "workspace_id":"uuid", "model_key":"classic_shot",
  "name":"经典镜头", "description":"...", "content_kind":"record",
  "status":"active", "current_version_id":"uuid", "current_version":{},
  "capabilities":["create","edit","review","publish"],
  "created_at":"RFC3339", "updated_at":"RFC3339"
}
```

`model_key` 为 `[a-z][a-z0-9_]{1,79}`；`content_kind` 为 `record|document|faq|note`。模型 status 为 `draft|active|archived`。

创建请求：

```json
{
  "model_key":"classic_shot", "name":"经典镜头", "description":"镜头知识记录",
  "content_kind":"record",
  "initial_version":{"field_schema":{},"form_schema":{},"list_schema":{},"policy":{}}
}
```

模型 PATCH 允许 `name,description,status`；不得修改 `model_key` 和 `content_kind`。

### 5.2 Schema 唯一格式

不兼容多种旧格式，最终只接受以下结构：

```json
{
  "fields":[
    {
      "key":"shot_size", "type":"enum", "label":"景别", "description":"...",
      "required":true, "searchable":true, "sortable":true,
      "options":[{"value":"close_up","label":"特写"},{"value":"wide","label":"全景"}],
      "default":null, "validation":{"max_length":32}
    }
  ],
  "additional_properties":false
}
```

字段 `key` 使用 `[a-z][a-z0-9_]{1,63}`。保留字段不能出现在 `fields`：`id,title,markdown,summary,tags,source,attachments,created_at,updated_at,created_by,updated_by,visibility,publication_status,review_status,quality`。

支持类型：`string|text|integer|number|boolean|date|datetime|enum|multiselect|object|array|asset_reference`。

- `enum/multiselect` 必须有非空 `options`，option value 全局唯一。
- `object` 通过 `properties` 描述子字段；`array` 通过 `items` 描述元素类型。
- `asset_reference` 的值为 `{asset_id,asset_version_id}`，服务端必须校验工作区权限。
- `required`、`default`、`validation` 由服务端统一校验，前端只负责展示。

表单 schema：

```json
{
  "layout":"sections",
  "sections":[
    {"key":"basic","label":"基础信息","fields":["shot_size"],"collapsed":false}
  ],
  "submit":{"label":"保存","show_draft":true}
}
```

列表 schema：

```json
{
  "columns":[{"key":"title","label":"标题","width":240,"visible":true},{"key":"shot_size","label":"景别"}],
  "filters":[{"key":"shot_size","operator":"in","label":"景别"},{"key":"tags","operator":"contains_any","label":"标签"}],
  "default_sort":{"key":"updated_at","direction":"desc"}
}
```

策略 schema：

```json
{
  "visibility":{"default":"private","allowed":["public","login","private","workspace","internal"]},
  "review":{"required_for_publish":false,"allowed_deciders":["reviewer","admin","owner"]},
  "outlets":{"workspace":{"enabled":true},"fulltext":{"enabled":true},"semantic":{"enabled":true},"agent_tool":{"enabled":false}},
  "retention":{"days":null}
}
```

### 5.3 版本生命周期

资源模型版本对象：`{id,resource_model_id,version_no,status,field_schema,form_schema,list_schema,policy,schema_checksum,validated_at,published_at,retired_at,created_at,created_by}`。

版本 status：`draft|published|retired`。`validate` 成功写入 `validated_at`；只有 validated draft 可以 publish；发布自动 retire 旧版本。版本 PATCH 必须 `If-Match: schema_checksum`，修改后清空 `validated_at`。

迁移请求：

```json
{
  "source_version_id":"uuid", "target_version_id":"uuid",
  "asset_scope":{"publication_status":["published"]},
  "mapping":{"old_key":"new_key"},
  "defaults":{"new_key":"default value"},
  "failure_policy":"continue_and_report"
}
```

预览 `200` 返回 `affected_assets/auto_migratable/failed/field_added/field_removed/type_conversion_failures/defaults_used/reindex_required`。启动 `202` 返回 Migration Job；Migration 状态为 `queued|running|completed|completed_with_errors|failed|canceled`。

## 6. 资产和资产版本

### 6.1 资产集合

| 方法 | 路径 |
| --- | --- |
| GET | `/api/frontend/workspaces/{workspaceId}/assets` |
| POST | `/api/frontend/workspaces/{workspaceId}/assets` |
| GET/PATCH | `/api/frontend/assets/{assetId}` |
| DELETE | `/api/frontend/assets/{assetId}` |
| GET | `/api/frontend/assets/{assetId}/versions` |
| GET | `/api/frontend/asset-versions/{versionId}` |
| POST | `/api/frontend/assets/{assetId}/versions` |
| PATCH | `/api/frontend/asset-versions/{versionId}` |
| POST | `/api/frontend/assets/{assetId}/submit-review` |
| POST | `/api/frontend/assets/{assetId}/publish` |
| POST | `/api/frontend/assets/{assetId}/archive` |
| POST | `/api/frontend/assets/{assetId}/restore` |
| POST | `/api/frontend/assets/{assetId}/duplicate` |

列表参数：

`q`、`resource_model_id`、`content_kind`、`visibility`、`publication_status`、`review_status`、`created_by`、`container_id`、`parent_asset_id`、`tags`、`limit`、`cursor`、`sort`、`filters`。

动态过滤格式：

```json
{
  "fields":{"shot_size":{"eq":"close_up"},"duration":{"gte":10,"lte":60}},
  "tags":{"contains_any":["夜景","反打"]}
}
```

允许运算符：`eq|neq|in|contains|contains_any|gte|lte|exists`。字段必须来自当前资源模型 schema，未知字段返回 `422`。

### 6.2 资产对象

```json
{
  "id":"uuid", "workspace_id":"uuid", "resource_model_id":"uuid", "content_kind":"record",
  "resource_model_version_id":"uuid", "title":"夜景反打", "summary":"...", "markdown":"...",
  "fields":{"shot_size":"close_up"}, "tags":["夜景"], "source":{"type":"manual"},
  "visibility":"workspace", "publication_status":"draft", "review_status":"none", "quality":"raw",
  "current_working_version_id":"uuid", "current_published_version_id":null,
  "container_ids":["uuid"], "parent_asset_id":null,
  "created_by":{"id":"uuid","display_name":"张三"}, "updated_at":"RFC3339"
}
```

创建请求：

```json
{
  "resource_model_id":"uuid", "title":"夜景反打", "markdown":"...",
  "fields":{"shot_size":"close_up"}, "tags":["夜景"], "source":{"type":"manual"},
  "visibility":"workspace", "container_ids":["uuid"], "parent_asset_id":null
}
```

创建、编辑均基于当前 published model version；服务端完整校验动态字段、策略、容器和父资产。创建初始 `publication_status=draft`、`review_status=none`、`quality=raw`。

资产 PATCH 只允许 `title,markdown,fields,tags,source,visibility,container_ids,parent_asset_id`，必须 `If-Match`。每次编辑生成新资产版本，旧版本只读。

### 6.3 资产版本

资产版本对象：`{id,asset_id,version_no,parent_version_id,resource_model_version_id,workflow_status,quality,review_status,title,markdown,fields,tags,source,content_checksum,created_by,created_at}`。

版本 workflow：`draft|submitted|approved|rejected|published|retired`。服务端不允许前端直接跳转状态；使用 submit/review/publish 动作。

发布请求可选 `{asset_version_id}`，必须存在可发布的 working version；审核接口仍可用于人工流程和审计，但不再阻塞发布。成功返回最新 Asset 和 ETag。归档/恢复改变资产 publication status，不删除历史版本。删除资产为异步软删除，响应 `202` 和 deletion job。

删除任务通过 `GET /api/frontend/deletion-jobs/{jobId}` 轮询。Deletion Job：`{id,workspace_id,resource_type,resource_id,status,error_code?,error_summary?,created_at,started_at?,completed_at?}`；status 为 `queued|running|completed|failed`。

## 7. 容器、文档树和父子关系

### 7.1 容器接口

| 方法 | 路径 |
| --- | --- |
| GET | `/api/frontend/workspaces/{workspaceId}/containers/tree` |
| POST | `/api/frontend/workspaces/{workspaceId}/containers` |
| GET/PATCH/DELETE | `/api/frontend/containers/{containerId}` |
| POST | `/api/frontend/containers/{containerId}/move` |
| GET | `/api/frontend/containers/{containerId}/children` |
| GET | `/api/frontend/containers/{containerId}/assets` |

容器对象：`{id,workspace_id,parent_id,name,kind,sort_key,status,visibility,asset_count,children,created_at,updated_at}`。

`kind`：`document|faq|chat|note|custom`；`status`：`active|archived`。创建：`{parent_id?,name,kind,visibility,sort_key?}`；PATCH：`{name?,status?,visibility?,sort_key?}`；move：`{parent_id?,sort_key?}`。删除仅允许空容器，返回 `204`；有子节点或资产返回 `409 container_not_empty`。

### 7.2 资产关联

- `POST /api/frontend/assets/{assetId}/containers`：`{container_id,operation:"add|remove|replace"}`。
- `GET /api/frontend/assets/{assetId}/containers`：返回完整容器引用。
- `POST /api/frontend/assets/{assetId}/document-parent`：`{parent_asset_id}`。
- `DELETE /api/frontend/assets/{assetId}/document-parent`：清除父级。
- `GET /api/frontend/assets/{assetId}/document-children`：返回子文档资产摘要。

父子关系必须同工作区、父子均为 `content_kind=document`，服务端阻止自引用和循环。

## 8. 附件和媒体文件

| 方法 | 路径 |
| --- | --- |
| GET | `/api/frontend/asset-versions/{versionId}/attachments` |
| POST | `/api/frontend/asset-versions/{versionId}/attachments` |
| GET/PATCH/DELETE | `/api/frontend/attachments/{attachmentId}` |
| POST | `/api/frontend/attachments/{attachmentId}/link` |
| GET | `/api/frontend/attachments/{attachmentId}/download` |
| POST | `/api/frontend/attachments/{attachmentId}/presigned-download` |

上传使用 `multipart/form-data`，一个字段 `file`，单文件默认最大 50 MiB。附件对象：

```json
{
  "id":"uuid", "asset_version_id":"uuid", "filename":"scene.mp4", "media_type":"video/mp4",
  "size":123456, "checksum":"sha256", "scan_status":"pending", "status":"available",
  "storage_key":"opaque", "created_by":"uuid", "created_at":"RFC3339"
}
```

`storage_key` 仅服务端内部使用，浏览器下载必须调用 download 或 presigned-download。扫描状态：`pending|clean|rejected|failed`；非 clean 附件不能被发布和 Agent 引用。

## 9. 审核和发布决策

### 9.1 审核接口

| 方法 | 路径 |
| --- | --- |
| GET | `/api/frontend/workspaces/{workspaceId}/reviews` |
| GET | `/api/frontend/reviews/{reviewId}` |
| POST | `/api/frontend/reviews/{reviewId}/approve` |
| POST | `/api/frontend/reviews/{reviewId}/reject` |
| POST | `/api/frontend/reviews/batch` |
| GET | `/api/frontend/reviews/{reviewId}/comments` |
| POST | `/api/frontend/reviews/{reviewId}/comments` |

列表参数：`status=pending|approved|rejected|superseded`、`resource_model_id`、`submitted_by`、`created_from`、`created_to`、`limit`、`cursor`。

审核对象：`{review_id,asset_id,asset_version_id,resource_model_id,title,fields,quality,status,comment,submitted_by,reviewed_by,submitted_at,reviewed_at,etag}`。

决策请求：`{comment?,expected_version_id}`。批量请求：

```json
{"items":[{"review_id":"uuid","decision":"approve","comment":"...","expected_version_id":"uuid"}]}
```

批量最多 100 条，响应 `207`：`{items:[{review_id,status,decision,error_code?,message?}]}`。每项独立提交幂等结果，前端必须允许部分成功。

## 10. 搜索和查询

### 10.1 统一查询

`POST /api/frontend/workspaces/{workspaceId}/query`

```json
{
  "mode":"hybrid", "query":"夜景反打", "resource_model_ids":["uuid"],
  "visibility":["workspace"], "publication_status":["published"],
  "filters":{"fields":{"shot_size":{"eq":"close_up"}}},
  "top_k":20, "cursor":null
}
```

`mode`：`lexical|hybrid|semantic`。`hybrid` 为默认；`semantic` 需要工作区开启向量索引。响应项：

```json
{"asset_id":"uuid","asset_version_id":"uuid","title":"...","summary":"...","fields":{},"tags":[],"snippet":"...","score":0.92,"highlights":{},"updated_at":"RFC3339"}
```

结果只能包含当前用户可读且符合 publication/review 策略的版本。`score` 只用于排序，不作为业务阈值。查询无副作用，不需要幂等 key。

### 10.2 搜索建议

- `GET /api/frontend/workspaces/{workspaceId}/search/suggestions?q=...&resource_model_id=...`
- 返回 `{items:[{value,label,kind,count?}],has_more:false}`。

## 11. 会话、思考和问答

### 11.1 会话

| 方法 | 路径 |
| --- | --- |
| GET/POST | `/api/frontend/workspaces/{workspaceId}/conversations` |
| GET/PATCH | `/api/frontend/conversations/{conversationId}` |
| POST | `/api/frontend/conversations/{conversationId}/archive` |
| GET/POST | `/api/frontend/conversations/{conversationId}/messages` |
| GET | `/api/frontend/conversations/{conversationId}/blocks` |

创建：`{agent_application_id,title,source,visibility,container_id?}`。`source`：`chat_interface|document|asset|automation`；`visibility`：`private|workspace`。

会话摘要：`{conversation_id,workspace_id,title,source,visibility,status,container_id,note_asset_id,last_message_preview,message_count,updated_at}`。状态：`active|archived|completed`。

消息写入：

```json
{
  "role":"user", "content":"问题内容", "content_format":"markdown",
  "provider_conversation_id":null, "provider_message_id":null,
  "status":"completed", "reply_to_block_id":null
}
```

消息读取返回 `block_revision_id,block_id,sequence_no,role,content,content_format,status,provider_conversation_id,provider_message_id,created_at`。消息不可编辑，只能追加修订块。

### 11.2 普通和流式聊天

- `POST /api/frontend/conversations/{conversationId}/chat`
- `POST /api/frontend/conversations/{conversationId}/chat/stream`

请求：`{query,reference_ids?,model_context?,response_mode?}`。`response_mode`：`answer|answer_with_sources|draft_note`。

普通响应：

```json
{
  "answer":"...", "conversation_id":"uuid", "message_id":"uuid",
  "references":[{"asset_id":"uuid","asset_version_id":"uuid","title":"...","snippet":"..."}],
  "rejected_reference_count":0, "usage":{"input_tokens":0,"output_tokens":0}
}
```

流式响应使用 SSE：

```text
event: message.start\ndata: {"message_id":"uuid"}\n\n
event: message.delta\ndata: {"text":"部分内容"}\n\n
event: reference\ndata: {"asset_id":"uuid","asset_version_id":"uuid","title":"..."}\n\n
event: message.complete\ndata: {"message_id":"uuid","usage":{}}\n\n
event: error\ndata: {"code":"upstream_unavailable","request_id":"..."}\n\n
```

客户端断线后使用 messages GET 恢复，不重复提交已经完成的幂等 key。

## 12. 笔记、派生内容和媒体转写

### 12.1 会话笔记

- `POST /api/frontend/conversations/{conversationId}/note/sync`
- `GET /api/frontend/conversations/{conversationId}/note`
- `POST /api/frontend/conversations/{conversationId}/note/publish`

sync 无 body，生成/更新 note asset，返回 `{conversation_id,note_asset_id,asset_version_id,message_count,status}`。publish 请求 `{expected_version_id}`，要求 ETag 一致，返回 `{note_asset_id,asset_version_id,publication_status,quality}`。

### 12.2 派生内容

- `POST /api/frontend/conversations/{conversationId}/derivations`
- `GET /api/frontend/derivations/{derivationId}`
- `POST /api/frontend/derivations/{derivationId}/finalize`

创建：

```json
{"source_block_revision_ids":["uuid"],"context_policy":"summary_only","title":"镜头清单"}
```

`context_policy`：`summary_only|selected_only|full`。派生对象：`{derivation_id,source_conversation_id,target_conversation_id,target_note_asset_id,operation,context_policy,status,created_at,completed_at}`。

finalize：

```json
{
  "disposition":"create_new|merge_existing|discard",
  "target_asset_id":null, "target_block_id":null,
  "expected_source_asset_version_id":null, "expected_target_asset_version_id":null,
  "expected_container_version_id":null, "merge_mode":"append|replace"
}
```

### 12.3 媒体和转写

- `POST /api/frontend/conversations/{conversationId}/media`
- `GET /api/frontend/conversation-media/{mediaId}`
- `POST /api/frontend/conversation-media/{mediaId}/transcribe`
- `GET /api/frontend/conversation-media/{mediaId}/transcript`

注册：`{attachment_id,media_kind:"audio|video",language?,duration_ms?}`。媒体状态：`registered|transcribing|transcribed|failed`；转写异步返回 `202`，全文通过 transcript GET 获取，不能假设启动接口同步返回全文。

## 13. Agent 应用和引用校验

- `POST /api/frontend/agent-applications/{applicationId}/sessions`
- `GET /api/frontend/agent-sessions/{sessionId}`
- `POST /api/frontend/agent-sessions/{sessionId}/references/validate`
- `POST /api/frontend/agent-sessions/{sessionId}/chat`
- `POST /api/frontend/agent-sessions/{sessionId}/chat/stream`
- `POST /api/frontend/agent-sessions/{sessionId}/cancel`

引用校验请求：`{references:[{asset_id,asset_version_id}]}`，最多 50 条。响应：`{references:[...],rejected_count,rejected:[{asset_id,reason}]}`。Agent 只能引用 clean、可读、符合模型策略的 published 版本。

## 14. 自动化和任务运行

### 14.1 Job

| 方法 | 路径 |
| --- | --- |
| GET/POST | `/api/frontend/workspaces/{workspaceId}/automation-jobs` |
| GET/PATCH/DELETE | `/api/frontend/automation-jobs/{jobId}` |
| POST | `/api/frontend/automation-jobs/{jobId}/pause` |
| POST | `/api/frontend/automation-jobs/{jobId}/resume` |
| POST | `/api/frontend/automation-jobs/{jobId}/run-now` |
| GET/POST | `/api/frontend/automation-jobs/{jobId}/runs` |

Job：

```json
{
  "name":"每日资料整理", "operation":"prepare_asset", "agent_application_id":"uuid",
  "trigger":{"type":"cron","expression":"0 2 * * *"}, "timezone":"Asia/Shanghai",
  "concurrency_policy":"forbid", "input_scope":{}, "max_attempts":3,
  "retry_backoff":{"strategy":"exponential","base_seconds":30,"max_seconds":3600}, "enabled":true
}
```

operation：`prepare_asset|publish|archive|reindex|import|export|transcribe|sync_note`。trigger：`manual|cron|event`。concurrency：`forbid|replace|allow`。

### 14.2 Run、Attempt和实时进度

- `GET /api/frontend/task-runs/{runId}`
- `GET /api/frontend/task-runs/{runId}/attempts`
- `POST /api/frontend/task-runs/{runId}/retry`
- `POST /api/frontend/task-runs/{runId}/cancel`
- `GET /api/frontend/task-runs/{runId}/events`（SSE）

Run 状态：`queued|running|succeeded|failed|cancel_requested|canceled|paused`。Run 对象至少包含 `id,job_id,workspace_id,source,operation,status,progress,attempt_count,error_code,error_summary,input_scope,created_at,started_at,completed_at,next_attempt_at`。Attempt 返回 lease、重试、开始和结束时间。

异步创建返回 `202`；终态只能由 worker 写入，前端不能 PATCH status。retry/cancel 使用幂等 key；SSE 断线可通过 GET run 恢复。

## 15. 导入、导出和文件下载

- `POST /api/frontend/workspaces/{workspaceId}/assets/imports`
- `GET /api/frontend/import-jobs/{jobId}`
- `POST /api/frontend/workspaces/{workspaceId}/assets/exports`
- `GET /api/frontend/export-jobs/{jobId}`
- `GET /api/frontend/export-jobs/{jobId}/download`

导入支持 JSONL、CSV：

```json
{
  "resource_model_id":"uuid", "resource_model_version_id":"uuid",
  "source_name":"shots.jsonl", "format":"jsonl", "rows":[{"title":"...","fields":{}}],
  "validation_mode":"strict|continue"
}
```

单次最多 10,000 行。导入结果：`{id,status,summary:{total,created,updated,failed,errors},created_at,completed_at}`。

导出：

```json
{"resource_model_id":"uuid","filters":{},"format":"jsonl|csv|xlsx","include_attachments":false}
```

导出 Job 完成后通过 download 接口返回短期授权 URL 或文件流；内部 `storage_key` 永远不能直接暴露给浏览器。导入/导出状态：`queued|running|completed|completed_with_errors|failed|canceled`。

导入支持 JSONL、CSV；导出 `format` 支持 `jsonl|csv|xlsx`。XLSX 返回标准 Office Open XML 文件，导入接口仍统一接收解析后的 `rows`。

## 16. 通知、活动和审计

### 16.1 通知

- `GET /api/frontend/workspaces/{workspaceId}/notifications`
- `GET /api/frontend/workspaces/{workspaceId}/notifications/unread-count`
- `POST /api/frontend/notifications/{notificationId}/read`
- `POST /api/frontend/workspaces/{workspaceId}/notifications/read-all`
- `GET /api/frontend/workspaces/{workspaceId}/notifications/stream`（SSE）

通知对象：`{id,workspace_id,type,title,body,object_type,object_id,read_at,created_at}`。type：`review_submitted|review_decided|task_completed|task_failed|mention|system`。

### 16.2 审计

- `GET /api/frontend/workspaces/{workspaceId}/audit-logs`（owner/admin，支持 object、actor、action、时间范围过滤）
- `GET /api/frontend/workspaces/{workspaceId}/activity`（普通成员可读的脱敏活动流）

审计项：`{id,actor,initiator,action,resource_type,resource_id,result,metadata,created_at}`。审计只追加不可修改、不可删除。

## 17. SSE 通用规则

所有 SSE 接口：

- `Content-Type: text/event-stream`、`Cache-Control: no-cache`、禁止代理缓冲。
- 每 15 秒发送 `event: heartbeat`。
- 客户端使用 `Last-Event-ID` 续传；服务端无法续传时发送 `event: reset`，客户端重新 GET 全量状态。
- 错误事件只描述本次流，不改变已经持久化的数据；最终状态以普通 GET 为准。

## 18. 外部 Agent/Open API（非浏览器）

使用独立 API Key/OAuth scope，不使用浏览器 Session：

- `POST /api/open/v1/query`
- `POST /api/open/v1/assets`
- `PATCH /api/open/v1/assets/{assetId}`
- `POST /api/open/v1/assets/{assetId}/publish`
- `POST /api/open/v1/assets/{assetId}/archive`
- `GET /api/open/v1/assets/{assetId}/references`
- `POST /api/open/v1/agent/tasks`
- `GET /api/open/v1/agent/tasks/{taskId}`
- `GET /api/open/v1/attachments/{attachmentId}/download`

Open API 的资产读写必须经过 Agent access policy；禁止用它绕过工作区成员权限或审核策略。

## 19. 产品级关键流程

### 19.1 首屏初始化

1. `POST /api/sessions`，随后 `GET /api/me`。
2. `GET /workspaces`，选择工作区。
3. 并行请求 workspace、counts、settings、preferences、agent-applications、resource-models、containers/tree、notifications/unread-count。
4. 根据模型版本 schema 渲染经典镜头、FAQ、文档页面；不在前端维护字段白名单。

### 19.2 动态模型到资产发布

1. 创建模型和 draft version。
2. 编辑 schema，`PATCH version`（ETag）。
3. `POST validate`，通过后 `POST publish`。
4. 用 current published version 创建资产。
5. 编辑资产生成 working version，提交审核。
6. reviewer approve，最后 publish；任何 ETag 冲突都重新读取并提示合并。

### 19.3 思考到笔记/知识资产

1. 创建 conversation。
2. 追加 user message，调用 chat/stream。
3. 保存助手消息和引用，用户触发 note sync。
4. 用户确认后 note publish，或创建 derivation 并 finalize 到新资产。

### 19.4 文档库

1. 读取 containers/tree。
2. 创建 document container 和 document asset。
3. 通过资产 containers 和 document-parent 维护目录及父子关系。
4. 资产列表使用 `container_id/parent_asset_id/filters` 查询，不在前端拼接隐藏关系。

### 19.5 异步任务

所有导入、导出、转写、迁移、自动化都返回 Job/Run ID。前端统一采用：创建 `202` -> 轮询 GET -> 需要实时体验时订阅 SSE -> 终态展示 summary/error。

## 20. 实现顺序和验收要求

### 20.1 后端实现切片

1. 统一错误、Session、权限、幂等、ETag、中间件。
2. 工作区、成员、设置、通知和活动。
3. 动态资源模型、版本、schema 校验和迁移。
4. 资产版本、容器/文档关系、附件。
5. 审核和发布状态机。
6. 搜索、Agent 引用校验、会话和 SSE。
7. 笔记、派生、媒体转写。
8. 自动化、任务运行、导入导出。
9. 审计、Open API、下载授权。

### 20.2 联调验收

- [ ] 所有写接口重复提交不会产生重复资源。
- [ ] 所有需要并发控制的更新都正确返回/校验 ETag。
- [ ] 动态字段可以新增、删除、改类型，并能驱动表单、列表、筛选器和资产校验。
- [ ] 模型、资产、审核、发布、归档状态转换有明确拒绝错误。
- [ ] 容器树、资产关联、文档父子关系可以双向读取和修改。
- [ ] 附件扫描未通过时不能发布或被 Agent 引用。
- [ ] 普通聊天和 SSE 聊天都能恢复消息，不丢失引用。
- [ ] 异步任务可轮询、取消、重试、查看 attempt 和错误摘要。
- [ ] 导出完成后可通过正式 download 接口下载，不暴露内部存储 key。
- [ ] 通知未读数、已读和实时推送与活动流一致。
- [ ] Open API 与浏览器 Session 权限隔离。

本文是最终态产品契约。任何实现偏差都应修改后端或更新本文并提升版本，不通过前端兼容分支隐藏差异。
