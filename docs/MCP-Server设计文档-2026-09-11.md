# MCP Server 设计文档（forge 内建）— 2026-09-11

## 1. 背景与目标

用户诉求：**本地 AI agent（Claude Desktop / Cursor / 自建 agent 等）能方便、直接地控制资产中台**——检索知识库、创建/更新文档、入库发布、写结构化数据表记录。

协议选型结论（与 Bot Access Control 对比）：

| 方向 | 解决的问题 | 结论 |
| --- | --- | --- |
| Bot Access Control（Content Signals、Web Bot Auth） | 管"外部 AI 爬虫能否抓取**公开站点**内容"，是公开站点对外的合规/防爬问题 | 与本目标无关，列入公开站点模块后续待办 |
| **Protocol Discovery / MCP** | 让平台把能力以标准协议暴露给 agent，agent 直接调用 | **本设计采用** |

MCP（Model Context Protocol，2025-06-18 及以后版本的规范）已是 Claude Desktop、Cursor、Claude Code 等本地 agent 的原生能力：配一个 server 地址 + 凭据即可发现并调用工具。

### 目标

1. forge 二进制**内建** MCP server：不引入常驻新服务，与 api 同进程部署。
2. 复用现有 **open 面（/api/open）的 API Key 鉴权与能力（capabilities）体系**，不新造账号体系。
3. v1 覆盖"检索知识库 + 建文档 + 入库发布 + 数据表写记录"的核心闭环。
4. 所有写操作可审计（audit log 记录 agent 身份）、可幂等、受治理链约束（入库仍走 commit-draft → confirm → publish / publication-requests）。

### 非目标

- 不做公网开放注册/自助发 key（key 仍由管理员在管理台签发）。
- v1 不做 OAuth 资源服务器（见 §8 取舍）。
- 不做会话/灵感对话类工具（属 member 会话语境，与 agent 机器身份不匹配，P2 再评估）。

---

## 2. 总体架构

```
本地 Agent（Claude/Cursor/自建）
   │  Streamable HTTP + Authorization: Bearer <API Key>
   ▼
nginx (443, 已有)  ──►  forge api 进程（已有）
                          ├─ /api/*        member 面
                          ├─ /api/open/*   open 面（已有）
                          ├─ /mcp          ★新增：MCP Streamable HTTP 端点
                          └─ （同 Dependencies：复用 query/asset/conversation/site 服务）
```

- **同进程内建**：`/mcp` 在 `router_groups.go` 注册为新路由组（`registerMCPRoutes`），与 open 面共享 `Dependencies`；工具实现是**薄适配层**——只做参数映射与输出裁剪，业务逻辑全部复用既有 service，不新增第二套业务实现。
- **传输**：Streamable HTTP（MCP 当前标准传输；单端点 POST + GET 流，兼容 nginx 反代——`proxy_buffering off` 已有先例）。SDK 同时提供 stdio，本地调试可用 `forge mcp --stdio` 子命令（P1）。
- **SDK 选型**：首选官方 `github.com/modelcontextprotocol/go-sdk`（与规范同步最紧）；备选社区 `mark3labs/mcp-go`（helper 多）。**落地前用 `mcp-inspector` 对两个 SDK 各做一次 conformance 冒烟再定**，接口以所选版本实际 API 为准，本文档不锁定方法签名。

---

## 3. 鉴权与能力映射

### 3.1 鉴权

- 复用 open 面 `APIKeyAuthenticator`：`Authorization: Bearer <key>` → 查 `identity.api_keys`（key_hash、capabilities、expires_at、status）→ `Principal{UserID, OrganizationID, UserType:"agent", Capabilities}`。
- 每个 JSON-RPC 请求都带凭据（无状态，不做 MCP 会话级登录）。
- key 由管理员在管理台为 agent 用户签发（`/api/admin/agent-users/{id}/api-keys*` 已有），MCP 不新增发钥路径。

### 3.2 能力 → 工具可见性矩阵

工具在 `tools/list` 时按 principal 的 capabilities **过滤**（没有能力的工具不下发，而不是下发后报错）；调用时仍由 service 层做组织/工作区级授权兜底（与 open 面的 `requireAgentCapability` 同一约定）：

| capability | 可用工具 |
| --- | --- |
| `query.read` | search_assets、list_tables、get_table_schema、list_records、get_asset |
| `asset.read` | get_asset、list_records |
| `asset.write` | create_document、create_record、update_asset |
| `asset.confirm` | confirm_asset_version |
| `asset.publish` | publish_asset |
| `asset.archive` | archive_asset |

> 现有 key 的 capabilities 是自由 JSON 数组（如 `["query.read","reference.read"]`），无需迁移；新工具只读取这些既有关键字。

### 3.3 审计与限流

- 每个工具调用写 `audit.audit_log`（actor = key 所属 agent user，event_type = `mcp.<tool>`，object = 目标资产）。
- 复用 open 面限流；按 key 维度加并发写上限（防 agent 循环失控批量写）。

---

## 4. 工具清单

### P0（读 + 检索，先打通端到端）

| 工具 | 输入要点 | 输出 | 背后实现 |
| --- | --- | --- | --- |
| `search_assets` | query、mode（fulltext/semantic/hybrid）、workspace_id?、resource_model_id?、top_k | items[]（asset_id/title/summary/visibility/score）+ degraded | 复用 `POST /api/open/query` 同一 query service |
| `get_asset` | asset_id | title/summary/markdown/fields/tags/publication_status | 新增 open 只读 service 调用（open 面目前缺读端点，直接走 asset service） |
| `list_tables` | workspace_id? | models[]（model_key/name/字段列表/当前版本） | resource-models List（与 /settings/models 同源） |
| `get_table_schema` | model_id | field_schema + policy 摘要 | resource-models Get |

### P1（写闭环）

| 工具 | 输入要点 | 输出 | 说明 |
| --- | --- | --- | --- |
| `create_document` | workspace_id、title、markdown、summary?、visibility? | asset_id、draft_revision、状态 | 复用 open `createAsset`（builtin_document） |
| `update_document` | asset_id、markdown、base_revision | 新 draft_revision | 复用 open `updateAsset`；**必须带乐观锁**，防覆盖 |
| `confirm_asset_version` | asset_id | 新确认版本 | 对应人工确认闸门 |
| `publish_asset` | asset_id、base_revision | publication_status / publication_request_id | 走治理链：direct 模型直接发布；review 模型自动转 publication-request，工具输出里明确返回"已提交审核" |
| `archive_asset` | asset_id | 状态 | 复用 open 端点 |
| `insert_record` | model_id、fields{}、title | asset_id | 结构化记录 = 以对应 resource model 创建资产（与数据表录入同源） |
| `list_records` | model_id、filters?、top_k | 记录列表 | 走 structured query |

工具输出统一为 MCP `content: [{type:"text", text: <人类可读摘要>}]` + `structuredContent: <机器可读 JSON>`；**写工具的 text 摘要必须包含**结果状态、资产 id、以及"下一步建议"（如"草稿已创建，调用 confirm_asset_version → publish_asset 完成入库"）——这决定 agent 能否自主完成多步流程。

### P2（增强）

- `list_publication_requests` / `approve` / `reject`（治理面工具，能力 `publication.*`）。
- `list_tags` / `add_tags`。
- 附件类：上传附件、按会话媒体转写。
- Resources：`asset:///{id}`（正文只读资源，支持 agent 直接 attach 上下文）。
- Prompts：`入库前自检`、`知识库检索问答` 等预置 prompt 模板。
- 会话/笔记类工具（member 语境，需产品决策）。

---

## 5. 写安全约定

1. **幂等**：create/update 类工具内部生成 Idempotency-Key（由入参 `idempotency_key?` 可选覆盖），同一工具重试不会产生重复资产。
2. **乐观锁强制**：update/publish 必须显式携带 base_revision（取自上一次 get/list 输出），冲突返回可读错误，agent 拿到新 revision 重试——防止 agent 基于旧内容覆盖人工编辑。
3. **dry_run**：create/update/publish 支持 `dry_run: true`，返回"将要执行的动作摘要"而不落库，供 agent 预演。
4. **单 key 写频控**：默认 30 次/分钟（可配），超出返回 MCP error（`code: rate_limited`，带 retry 建议）。
5. **审计**：见 §3.3；审计查询本身在管理面可核对 agent 行为。
6. **可见性默认**：创建类工具 visibility 缺省 `workspace`，禁止工具直接创建 `public`（公开走站点治理，人工操作）。

---

## 6. 协议发现配套（低成本，随 P0 一起交付）

- `GET /.well-known/agents.json`：声明平台名、MCP 端点、鉴权方式（API Key header）、能力摘要。
- `/llms.txt` + `/AGENTS.md`：面向通用 agent 的能力与入口说明（静态文本，nginx 或 api 直出均可）。

---

## 7. 配置与部署

| 环境变量 | 默认 | 说明 |
| --- | --- | --- |
| `MCP_ENABLED` | `true` | 关闭后 `/mcp` 返回 404 |
| `MCP_PATH` | `/mcp` | 端点路径 |
| `MCP_WRITE_PER_MIN` | `30` | 单 key 写频控 |

- 部署不变：同一镜像、同一 compose；nginx 在 `0-ip-443.conf` 增加 `location /mcp { proxy_pass http://127.0.0.1:8080; proxy_buffering off; proxy_read_timeout 3600s; }`（SSE 长流需要关缓冲与长超时，与 `/api` 现有 SSE 配置一致）。
- OpenAPI 面：`/mcp` 不进 openapi.yaml（它自身就是机器协议），但在 README/管理台 agent 用户页展示接入说明。

---

## 8. 鉴权取舍（为什么 v1 不上 OAuth）

MCP 规范的 HTTP 传输推荐 OAuth 2.1（资源服务器指示），那是对**公网多租户**场景的设计。本平台当前是自托管 + 管理员签发 key 的私有部署：

- v1 用**静态 API Key（Bearer header）**：Claude Desktop / Cursor / claude code 的 MCP 配置均支持自定义 header，接入零成本。
- P2 若开放给更多第三方 agent，再升级为 OAuth 资源服务器（MCP 规范路径），API Key 体系可平滑作为其中一种凭证。

---

## 9. 本地 agent 接入示例（交付即用）

Claude Desktop / Cursor（`mcpServers` 配置）：

```json
{
  "mcpServers": {
    "asset-hub": {
      "url": "https://forgeapi.qidu.site/mcp",
      "headers": { "Authorization": "Bearer <API_KEY>" }
    }
  }
}
```

claude code CLI：

```bash
claude mcp add --transport http asset-hub https://forgeapi.qidu.site/mcp \
  --header "Authorization: Bearer <API_KEY>"
```

接入后 agent 即可执行："在知识库里搜一下浅景深相关内容，把结论整理成一篇文档入库" —— 对应 `search_assets → create_document → confirm_asset_version → publish_asset` 的工具链。

---

## 10. 测试策略

1. **conformance 冒烟**：`mcp-inspector` 对 `/mcp` 跑协议一致性（initialize/tools/list/tools/call、流式、错误信封）。
2. **单测**：每个工具的 handler 层单测（参数→service 入参映射、capability 过滤、dry_run、错误映射），复用 open 面测试基建。
3. **集成**：`AGENTCHUNZHI_TEST_DATABASE_URL` 门控，走真实 schema 验证写链（创建→确认→发布、乐观锁冲突、幂等重试）。
4. **e2e（agent 场景）**：以 MCP client 脚本模拟"检索→创建→发布"闭环，断言 audit log 与公开结果。

---

## 11. 实施拆分

| 阶段 | 内容 | 预估 |
| --- | --- | --- |
| P0 | SDK 选型冒烟 + `/mcp` 端点 + 鉴权 + search_assets/get_asset/list_tables/get_table_schema + agents.json | 1 次迭代内 |
| P1 | 写闭环六工具 + 幂等/乐观锁/dry_run/频控 + 审计 + 接入文档 | 1 次迭代 |
| P2 | Resources/Prompts、治理面工具、附件、OAuth 评估 | 按需 |

## 12. 开放问题

1. 附件类工具（上传/下载）的 MCP 载荷上限与分块策略。
2. `agent-tasks`（异步任务）是否映射为 MCP progress 通知 + 轮询工具。
3. 会话/笔记工具是否开放给 agent（member 语境，需产品决策）。
4. 多 workspace agent key 的 workspace 收窄参数（默认全可见工作区）。
