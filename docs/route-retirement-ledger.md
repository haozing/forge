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
| `/api/frontend/workspaces/{workspaceId}/query` | 阶段 3 | `/api/v2/workspaces/{workspaceId}/query` | Unified query contract test | planned | 待 S3-11 |
| `/api/frontend/workspaces/{workspaceId}/index/status` | 阶段 3 | `/api/v2/workspaces/{workspaceId}/retrieval/status` | Retrieval status contract test | planned | 待 S3-11 |
| `/api/frontend/workspaces/{workspaceId}/index/rebuild` | 阶段 3 | `/api/v2/workspaces/{workspaceId}/retrieval/rebuild` | Rebuild contract test | planned | 待 S3-11 |
| `/api/frontend/workspaces/{workspaceId}/query-audit` | 阶段 3 | `/api/v2/workspaces/{workspaceId}/query-executions` | Query audit contract test | planned | 待 S3-11 |
| `/api/frontend/workspaces/{workspaceId}/search/suggestions` | 阶段 3 | Query/filter contract（不提供独立旁路） | Suggestion removal test | planned | 待 S3-11 |
| `/api/open/v1/query` | 阶段 3 | `/api/open/v2/query` | Open query contract test | planned | 待 S3-11 |
| `/api/public/workspaces/{workspaceId}/assets`、`/api/public/assets/{assetId}` | 阶段 5 | `/api/public/v2/sites/{siteId}/content...` | PublicSite contract test | planned | 待阶段 5 |
| 其他 Agent、PublicSite 和剩余 frontend 旧入口 | 阶段 4-6 | 对应 `/api/v2`、`/api/open/v2`、`/api/public/v2` | 所属领域 contract test | planned | 待所属阶段 |

约束：

- 阶段 0 只可以把已迁移的 review 路由改为 `retired`；不得把其他行提前标记为 retired。
- `planned` 路由不能出现在新 v2 handler、OpenAPI v2 或新服务调用图中。
- `retired` 路由必须返回 404，不得重定向或返回兼容提示。
- 每次状态变更同时记录删除提交、测试名称和实际 router 快照。
