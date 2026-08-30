# v2 路由退役台账

本台账是四个 v2 实施阶段共用的旧路由退役清单。`planned` 表示旧路由仍存在但不得被新代码调用；只有替代 v2 contract test 通过后才能改为 `retired`。`evidence` 填写替代 OpenAPI operation、contract test 和删除提交。

| legacy_pattern | owner_phase | replacement_route | delete_after_contract | status | evidence |
| --- | --- | --- | --- | --- | --- |
| `/api/frontend/workspaces/{workspaceId}/reviews` | 阶段 0 | `/api/v2/workspaces/{workspaceId}/publication-requests` | openapi-v2.yaml listPublicationRequests | retired | S0-3/S0-8: v2 review 垂直链（submit/list/get/comment/approve/reject/cancel/batch）已注册并接入 PublicationRequest 聚合 |
| `/api/frontend/reviews/{reviewId}` | 阶段 0 | `/api/v2/workspaces/{workspaceId}/publication-requests/{requestId}` | openapi-v2.yaml getPublicationRequest | retired | 同上 |
| `/api/frontend/reviews/{reviewId}/comments` | 阶段 0 | `/api/v2/workspaces/{workspaceId}/publication-requests/{requestId}/comments` | openapi-v2.yaml createPublicationComment | retired | 同上 |
| `/api/frontend/reviews/{reviewId}/approve`、`/reject` | 阶段 0 | `/api/v2/workspaces/{workspaceId}/publication-requests/{requestId}/approve`、`/reject` | openapi-v2.yaml approve/rejectPublicationRequest | retired | 同上 |
| `/api/frontend/reviews/batch` | 阶段 0 | `/api/v2/workspaces/{workspaceId}/publication-requests/batch` | openapi-v2.yaml batchPublicationRequests | retired | 同上 |
| `/api/frontend/assets/{assetId}/submit-review` | 阶段 0 | `/api/v2/workspaces/{workspaceId}/publication-requests` | openapi-v2.yaml submitPublicationRequest | retired | submit 语义并入 PublicationRequest 提交（携带 draft_revision） |
| `/api/me` | 阶段 1 | `/api/v2/me` | Identity contract test | retired | S1-7: /api/v2/me（GET/PATCH，If-Match revision）+ /api/v2/sessions + OpenAPI；旧路由 404（TestPhase1LegacyRoutesAreRetired） |
| `/api/sessions` | 阶段 1 | `/api/v2/sessions...` | Session/revocation contract test | retired | S1-7: /api/public/v2/sessions（登录）+ /api/v2/sessions、/api/v2/sessions/current、/api/v2/sessions/{sessionId} + OpenAPI；旧路由 404 |
| `/api/frontend/.../workspaces`（成员、邀请、设置、偏好） | 阶段 1 | `/api/v2/workspaces...`、`/api/v2/organization...` | Workspace/organization OpenAPI contract test | retired | S1-7: /api/v2/organization（profile/members/invitations/workspaces）、/api/v2/workspaces（collection/resource/members/leave/eligible-members/invitations/summary）+ /api/frontend/me/preferences → /api/v2/me/preferences + OpenAPI；旧路由 404 |
| `/api/frontend/...` 标签参数和标签接口 | 阶段 2 | `/api/v2/workspaces/{workspaceId}/tags...` | Tag/filter contract test | retired | S2-9: `/api/v2/workspaces/{workspaceId}/tags`（GET/POST）、`/tags/{tagId}`（GET/PATCH）、`/tags/{tagId}/archive|restore`、`/tag-facets` 已注册并接入 tag.Service/FacetService；openapi-v2.yaml operationIds listTags/createTag/getTag/renameTag/archiveTag/restoreTag/listTagFacets；成员资产列表只接受 tags_any/tags_all/tags_none，顶层 `tags` 与 `filters.tags` 返回 422 legacy_tags_field_not_supported |
| `/api/open/v1` Webhook 标签字段 | 阶段 2 | `/api/open/v2` Webhook `tag_keys` | Webhook request contract test | retired | S2-7/S2-9: `/api/open/v2/hooks/assets` 已注册（tag_keys 解析走 tag.ResolveExisting，unknown/archived → 422），`/api/open/v1/hooks/assets` 已删除返回 404；openapi-v2.yaml operationId createWebhookAsset；请求含 `tags` 字段返回 422 legacy_tags_field_not_supported |
| `/api/frontend/workspaces/{workspaceId}/query` | 阶段 3 | `/api/v2/workspaces/{workspaceId}/query` | Unified query contract test | retired | S3-11: 统一查询已注册（openapi-v2.yaml executeWorkspaceQuery）；frontend_query.go 删除，旧路由 404（TestLegacyOpenQueryRouteIsRetired/TestFrontendContractRoutesRequireSession 指向 v2 路由） |
| `/api/frontend/workspaces/{workspaceId}/index/status` | 阶段 3 | `/api/v2/workspaces/{workspaceId}/retrieval/status` | Retrieval status contract test | retired | S3-11: 聚合状态路由已注册（getWorkspaceRetrievalStatus）；frontend_retrieval.go 删除，旧路由 404 |
| `/api/frontend/workspaces/{workspaceId}/index/rebuild` | 阶段 3 | `/api/v2/workspaces/{workspaceId}/retrieval/rebuilds` | Rebuild contract test | retired | S3-11: workspace rebuild 已注册（createWorkspaceRetrievalRebuild，调 retrieval.RebuildService + Idempotency-Key）；旧路由 404 |
| `/api/frontend/workspaces/{workspaceId}/query-audit` | 阶段 3 | `/api/v2/workspaces/{workspaceId}/query-executions` | Query audit contract test | retired | S3-11: query_executions 审计面已注册（listWorkspaceQueryExecutions）；retrieval.query_logs 随 v2 schema 删除，RecordQueryLog 移除 |
| `/api/frontend/workspaces/{workspaceId}/search/suggestions` | 阶段 3 | Query/filter contract（不提供独立旁路） | Suggestion removal test | retired | S3-11: 建议 API 不再存在（无替代路由），旧路由 404 |
| `/api/open/v1/query` | 阶段 3 | `/api/open/v2/query` | Open query contract test | retired | S3-11: ForOpenAPI 统一查询已注册（executeOpenQuery）+ /api/open/v2/references/validate；query_r3.go 删除，旧路由 404（TestLegacyOpenQueryRouteIsRetired） |
| `/api/frontend/automation-jobs/{jobId}/run-now`（operation=prepare_asset 的任务） | 阶段 4 | `/api/v2/workspaces/{workspaceId}/assets/{assetId}/prepare` | Prepare/suggestion contract test | planned | P4-3: 成员 prepare 迁建议流（202 返回 run_id，不产版本）；openapi-v2.yaml 已冻结 prepareAsset/listSuggestions/acceptSuggestion/rejectSuggestion/batchAcceptSuggestions/listProcessingResults 六端点；旧 run-now 不再被新代码调用 |
| `/api/open/v1/agent/tasks`、`/api/open/v1/agent/tasks/{taskId}` | 阶段 4 | `/api/open/v2/agent-tasks`（多资产 1..20、幂等键、返回 run_id） | AgentTask v2 contract test | planned | P4-3: agenttask.Service.Create/Get 已多资产化（去重、全 validUUID、run input 用全量 asset_ids）并返回 run_id（外部系统「更新受控 AssetDraft」语义=建议流）；v2 open 路由（POST /api/open/v2/agent-tasks、GET .../{taskId}，复用 v1 守卫链）与 contract test 已由主线合入；v1 双路由待前端/调用方切换后删除 |
| `/api/open/v1/automation/runs/{runId}/callback` | 阶段 4 | `/api/open/v2/automation/runs/{runId}/callback` | Automation callback contract test | planned | P4-3 记账（§4.6）: 统一 API 读写本阶段只迁 prepare 分支，其余动作分支后续批次迁 v2 服务调用 |
| `/api/public/workspaces/{workspaceId}/assets`、`/api/public/assets/{assetId}` | 阶段 5 | `/api/public/v2/sites/{siteId}/content...` | PublicSite contract test | planned | 待阶段 5 |
| 其他 Agent、PublicSite 和剩余 frontend 旧入口 | 阶段 5-6 | 对应 `/api/v2`、`/api/open/v2`、`/api/public/v2` | 所属领域 contract test | planned | 待所属阶段 |

约束：

- 阶段 0 只可以把已迁移的 review 路由改为 `retired`；不得把其他行提前标记为 retired。
- `planned` 路由不能出现在新 v2 handler、OpenAPI v2 或新服务调用图中。
- `retired` 路由必须返回 404，不得重定向或返回兼容提示。
- 每次状态变更同时记录删除提交、测试名称和实际 router 快照。
