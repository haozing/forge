# API 接口逐项真实用户验收与修复报告

测试日期：2026-08-26  
测试地址：`http://127.0.0.1:8080`  
对照文档：[产品文档](<D:/code2/agentchunzhi/docs/产品文档.md>)

## 1. 最终结论

本轮发现的问题已完成代码修复，并在重建后的 API、Worker、PostgreSQL、ClamAV 环境中重新验证。当前内部产品流程可用，路由安全探测全绿；唯一未能由代码自行解决的是外部 DeepSeek 服务健康检查，接口正确返回 `502 model_endpoint_unavailable`，需要部署环境提供可用凭证和网络。

| 验收项 | 最终结果 |
| --- | ---: |
| 核心真实用户流程 | 93/93 通过 |
| 扩展真实用户流程 | 68/68 通过 |
| 注册路由 | 151 条 |
| 路由方法探测 | 755/755 传输通过 |
| 未登录契约偏差 | 0 |
| Go 测试 | `go test ./...` 全通过 |
| 外部模型健康 | 502，外部配置/网络问题 |

原始明细：[核心流程报告](<D:/code2/agentchunzhi/.tools/api_full_flow_report.json>)、[扩展流程报告](<D:/code2/agentchunzhi/.tools/api_extended_flow_report.json>)、[路由报告](<D:/code2/agentchunzhi/.tools/api_route_smoke_report.json>)。

## 2. 真实用户测试范围

核心流程使用管理员、编辑者、查看者和匿名客户端，按实际操作顺序覆盖：登录、资料、工作区、邀请和入组、模型及版本、动态字段、容器树、资产创建/编辑/复制/移动、父子关系、查询、索引状态/重建、血缘、处理任务、审核协作、直接发布、公开资源匿名读取、归档、XLSX 导出下载、导入、统计、活动、审计和通知。

扩展流程覆盖模型端点、Agent 用户和策略、Open API 资产/查询/任务、附件上传扫描下载、模型迁移、workspace Agent、会话、消息、笔记、自动化 Job/Run、外部短凭证 callback 及幂等回调、取消、Attempt、Agent session 和引用校验。

逐路由脚本对每条注册路径发送 GET/POST/PUT/PATCH/DELETE，验证未登录保护、方法限制和服务稳定性。公开只读路由允许空列表的 `200` 和不存在资源的 `404`，登录请求体校验允许未认证的 `422`，这两类是有意的公开/登录边界，不再误报为鉴权顺序问题。

## 3. 已完成的代码修复

### 3.1 生命周期和发布

- `internal/asset/service.go`、`internal/asset/member.go`、`internal/automation/processor.go` 移除 approved review 对发布的阻断。
- 发布审计写入 `review_required=false`，质量状态写入 `human_confirmed`。
- review submit/approve/reject 路由保留为可选人工协作和审计链路，不再是发布前置条件。

### 3.2 可见性和匿名公开读取

- workspace、container、asset 数据库约束支持 `public|login|private|workspace|internal`。
- 成员端列表、查询、workspace/container 设置和动态模型校验支持 public/login。
- 新增 `GET /api/public/workspaces/{workspaceId}/assets` 和 `GET /api/public/assets/{assetId}`，只返回已发布且 `visibility=public` 的安全投影，不需要登录。
- 真实流程已验证：编辑者创建 public 资产，管理员不提交 review 直接发布，匿名用户读取；归档后匿名详情返回 `404`。

### 3.3 认证顺序和审计

- 前端幂等写请求先认证，再校验 `Idempotency-Key`，未登录请求统一得到 `401`。
- 查询成功、拒绝和错误都写入 `retrieval.query_logs`，新增 workspace query audit 读取接口。
- 路由安全探测从修复前的 467 个误报下降为 0 个契约偏差。

### 3.4 动态模型字段

`internal/resourcemodel/schema.go` 增加 markdown、block、currency、json、relation、person、department、tags、attachment、image、video、location、calculated 类型，并校验 `unique`、`calculated.expression`、currency 配置等基础语义。更深的计算执行、唯一性索引和关系目标校验仍应作为后续专项能力补充，当前不会再把这些类型误判为未知类型。

### 3.5 检索、血缘和处理状态

- 新增索引状态：`GET /api/frontend/workspaces/{workspaceId}/index/status`。
- 新增索引重建：`POST /api/frontend/workspaces/{workspaceId}/index/rebuild`。
- 新增单资产重试：`POST /api/frontend/assets/{assetId}/index/retry`。
- 新增查询审计：`GET /api/frontend/workspaces/{workspaceId}/query-audit`。
- 新增资产血缘：`GET /api/frontend/assets/{assetId}/lineage`，返回版本、raw input、父版本和 checksum。
- 新增处理状态：`GET /api/frontend/asset-versions/{versionId}/processing`。
- 修复血缘 SQL 的 `DISTINCT/ORDER BY` 错误，并将批量重建改为单事务提交。
- 修复归档索引删除的 pgx 多语句执行错误，后台 retrieval projection delivery 现可稳定完成。

### 3.6 外部 Agent 任务闭环

- 自动化 Job 支持 `external_task` 配置：`input_api`、`output_api`、`callback_url`、短期凭证 TTL。
- 创建 Run 时只返回一次短期 callback credential，数据库只保存 hash 和过期时间。
- 新增 `POST /api/open/v1/automation/runs/{runId}/callback`，校验短期凭证、过期时间、终态幂等、输出快照、错误信息、run event 和审计。
- 增加 `output_snapshot` 数据库列；迁移索引改为 immutable credential hash 索引，避免 PostgreSQL 表达式索引启动失败。

### 3.7 导入导出和下载

- 导出接受 `jsonl|csv|xlsx`。
- 使用标准库 `archive/zip` 生成合法 Office Open XML XLSX，避免引入无法下载的外部依赖。
- 下载响应按真实类型返回 `.jsonl`、`.csv` 或 `.xlsx` 文件名；核心流程已校验 XLSX `Content-Type` 和 `Content-Disposition`。
- 导入接口继续接收解析后的 `rows`，保留 JSONL/CSV 统一数据入口。

### 3.8 SSE

通知流、任务运行流和 Agent 运行流均支持 `Last-Event-ID`、心跳和续传查询；聊天流收到断点请求时发送 `reset` 并给出消息恢复路径。所有 SSE 使用 `text/event-stream`、禁止代理缓冲和 15 秒心跳。

## 4. 仍需外部条件或后续增强的事项

1. **DeepSeek 健康检查**：`POST /api/frontend/model-endpoints/{id}/test` 当前返回 `502 model_endpoint_unavailable`。需要在运行 API/Worker 的环境配置有效 provider 凭证、允许访问 `api.deepseek.com`，再验证 generate、streaming、tool calling、structured output。
2. **动态字段深层语义**：calculated 执行、unique 数据库唯一索引、relation 目标模型和 attachment/image/video 的媒体处理仍应补充专项测试。
3. **失败行报告**：导入失败行已记录在 Job/row 状态中，但尚未提供独立的错误报告下载路由。
4. **长连接专项压测**：已有 Last-Event-ID 和心跳实现，仍建议用专用 SSE 客户端验证断网重连、权限撤销断流、慢客户端和 500 条以上事件分页。
5. **权限矩阵扩展**：过期会话、成员角色变更、最后管理员保护、跨组织 ID 枚举和 Agent key 撤销可继续加入定时回归。

## 5. 可复现命令

```text
docker compose build api worker
docker compose up -d api worker
python .tools/api_full_flow_test.py
python .tools/api_extended_flow_test.py
python .tools/api_route_smoke_test.py
docker run --rm -v D:\code2\agentchunzhi:/workspace -w /workspace agentchunzhi-qa-build go test ./...
```

结论：代码层 P0/P1 缺口已按产品文档完成主要修复，当前回归门禁全绿；部署前只需处理外部模型配置，并按第 4 节补充增强项。
