# Agent Chunzhi

Agent Chunzhi 是面向小型团队的内容资产中台。内置 Agent 使用 Eino 运行时，模型接口由组织管理员配置为 ModelEndpoint；系统不依赖 Dify、FastGPT、Redis、NATS 或独立 Agent 服务。

## 组件

- `cmd/api`：HTTP API、成员会话、Agent 应用、模型接口和运行订阅。
- `cmd/worker`：River Worker，执行固定 Graph、受限 ReAct、检索投影、转写和附件扫描。
- `cmd/bootstrap`：初始化组织和首位组织管理员（含内置模型 seed）。
- `cmd/migrate`：独立迁移器，唯一允许执行 DDL 的进程。
- `internal/agentruntime`：Eino ChatModel、RAG、ChatModelAgent、Tool Registry、Run Coordinator 和 PostgreSQL CheckPointStore。
- `internal/workflows`：资产整理、发布、归档、导入、重建索引、转写和 Note 同步固定 Graph。
- PostgreSQL：业务数据、PGroonga/pgvector、River、Outbox、运行状态、事件和加密 checkpoint。
- OSS（可选）：私有附件对象存储。

模型配置与 Agent 应用分离：每个 AgentApplication 绑定一个 ModelEndpoint，因此多个应用可以使用不同的 Base URL、API Key、模型和推理参数，也可以共享一个 endpoint。一次运行只选择一个应用，不做多 Agent 委派或协作。

## 阶段 0-6 开发基线（v2）

v2 实施处于首次共享部署前：只支持**可破坏重建的空库**开发方式。阶段 0 至阶段 6 期间禁止连接共享或生产数据库；每次基线变化后必须重建开发/测试数据库，不保留旧数据。迁移由独立的 `cmd/migrate`（owner 角色，`DATABASE_MIGRATION_URL`）执行；API/worker 使用受限运行角色（`DATABASE_URL`）只校验 schema 契约，不执行 DDL 或 seed。

```powershell
Copy-Item .env.example .env
# 编辑 .env，填写 POSTGRES_PASSWORD、SEARCH_CURSOR_SECRET 和两组加密密钥；OSS 不使用时可留空

# 首次或基线变化后：从空库重建（破坏性，只作用于显式指定的开发库）
docker compose up -d postgres
./scripts/reset-database.ps1 -Database agentchunzhi -ConfirmReset

# 创建组织与首位组织管理员（bootstrap 只创建数据，不执行迁移）
$env:ORG_SLUG="acme"; $env:ORG_NAME="Acme"; $env:ADMIN_EMAIL="admin@example.com"
$env:ADMIN_DISPLAY_NAME="Admin"; $env:ADMIN_PASSWORD="..."
docker compose run --rm migrate
go run ./cmd/bootstrap

docker compose up --build -d api worker
docker compose ps
curl.exe http://127.0.0.1:8080/readyz
```

生产环境必须覆盖数据库密码、加密根和 secret manager 注入的限流/投递密钥，并为 ModelEndpoint 配置 HTTPS Base URL 与主机 allowlist。

首次初始化管理员：

```powershell
$env:DATABASE_URL = 'postgres://user:password@localhost:5432/agentchunzhi?sslmode=disable'
$env:ORG_NAME = '示例组织'
$env:ADMIN_LOGIN = 'admin'
$env:ADMIN_DISPLAY_NAME = '系统管理员'
$env:ADMIN_PASSWORD = 'replace-with-a-long-password'
go run ./cmd/bootstrap
```

运行测试：

```powershell
go test ./...
```

## 使用内置 Agent

管理员先通过 ModelEndpoint 管理接口创建并测试模型接口，再创建 Agent 用户和 AgentApplication。注册请求使用 `model_endpoint_id`、`runtime_mode`（`rag`、`react` 或 `workflow`）及可选 `workflow_key`；响应只在创建/轮换时返回一次 API Key。

前台成员先启动应用会话：

```powershell
curl.exe -X POST `
  -H 'Cookie: agent_session=<member-session-cookie>' `
  -H 'Idempotency-Key: choose-agent-20260826-001' `
  http://localhost:8080/api/agent-applications/<application-id>/sessions
```

RAG 应用可直接调用 `/api/agent-sessions/{sessionId}/chat` 或 `/chat/stream`。ReAct 应用使用持久化运行接口：

```text
POST /api/agent-sessions/{sessionId}/runs
GET  /api/agent-runs/{runId}/events?after={sequence}
POST /api/agent-runs/{runId}/resume
POST /api/agent-runs/{runId}/cancel
```

高风险 Tool（发布、归档、删除、导出）会创建审批 Interaction；恢复时重新校验权限、版本、checksum 和模型 revision。普通 Agent 任务通过 `/api/open/agent-tasks` 创建，由 Worker 执行 `asset_prepare` Graph，结果只生成 `internal` candidate，不会自动发布。

## 外部资源

详见 [`docs/外部资源准备清单.md`](docs/外部资源准备清单.md)。除 PostgreSQL 外，OSS、ASR、Embedding 和 Reranker 均为可选供应商；云端模型只通过已登记的 ModelEndpoint 调用，凭证使用应用层加密或 Secret Provider，浏览器不会接触任何供应商密钥。

配置分层和“启动必填 / 后台配置”的完整说明见 [`docs/配置分层说明.md`](docs/配置分层说明.md)。

## API 文档

- [`openapi.yaml`](openapi.yaml)：唯一权威契约（87 路径，与路由注册表双向对照测试锁定）。路由为单一无版本树：成员面 `/api`、匿名/公开面 `/api/public`、开放 API 面 `/api/open`、运维面 `/api/admin`。
- [`docs/Eino内置Agent完整实现方案.md`](docs/Eino内置Agent完整实现方案.md)：最终架构、状态机、工具安全和部署方案。

## 备份与验收

```powershell
scripts\backup-restore.ps1
scripts\permission-regression.ps1
scripts\capacity-baseline.ps1 -Requests 1000 -Concurrency 25
scripts\ops-check.ps1
scripts\external-acceptance.ps1 -RequireOSS -RequireASR -ASRSampleFile .\acceptance\sample.wav
```

外部供应商验收需要在部署环境注入真实凭证；没有凭证时不要将对应供应商验收报告为通过。

## 附录：内置资源模型种子

每个组织的四个内置资源模型由 `db/migrations/0043_builtin_resource_model_seeds.sql` 播种（幂等，可重复执行）。该文件除随迁移框架执行一次外，**还会在 API 与 worker 每次启动时被自动重放**（`store.ReplayIdempotentSeed`），以此覆盖"先记录迁移、后创建组织"的时间差；`builtin_document` 通用文档、`builtin_note` 通用笔记、`builtin_faq` 常见问题 FAQ、`builtin_shot` 经典镜头库（CMS 动态表单示例）。模型 `status='active'` 并附带一条 `published` 初版且回填 `current_version_id`，默认可见性 `workspace`、workspace/agent_tool/fulltext/semantic 出口启用；模型不绑定 workspace（`workspace_id IS NULL`）。验证（任一方式生效后）：API/worker 启动日志无 seed 报错即已重放；如需立即确认也可手工执行该文件，然后查询：

```sql
SELECT model_key, status FROM model.resource_models
WHERE organization_id = '<新组织ID>' ORDER BY model_key;
-- 应返回 builtin_document/builtin_faq/builtin_note/builtin_shot 四行 active 记录
```

