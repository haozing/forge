# Eino 内置 Agent 完整实现方案

本文依据 [产品文档](产品文档.md) 以及当前 `cmd/api`、`cmd/worker`、`internal/agentruntime`、`internal/automation`、`internal/worker` 和数据库结构编写。

本文描述新的最终架构。实施时直接建立新的 Eino 运行时、配置和数据库结构，不保留 Dify、远程 Agent Process Provider、旧接口字段或旧数据迁移逻辑。

## 1. 最终决策

| 能力 | 最终方案 |
| --- | --- |
| Agent 运行时 | Eino ADK、Eino Graph、ToolsNode |
| 模型后端 | 每个 AgentApplication 绑定一个 ModelEndpoint，由 Eino 对应 ChatModel adapter 连接云端模型 |
| 普通问答 | Query Service 检索 + Eino `BaseChatModel.Generate/Stream` |
| 复杂问答 | Eino `adk.ChatModelAgent`，使用受限 ReAct 和 Tool Calling |
| 资产整理 | Eino `compose.Graph` 固定流程，模型只返回结构化 Candidate |
| 自动化任务 | River 调度 Eino Graph，PostgreSQL 保存运行、检查点和审计 |
| 中断恢复 | Eino CheckPointStore 保存引擎状态，业务表保存运行和审批状态 |
| 多 Agent | 不实现，不引入 Agent 委派、协作或 Agent 间通信 |
| 外部 Agent 平台 | 不使用 Dify、FastGPT 等平台 |
| 业务 Open API | 保留，它是数据、权限和任务的业务接口，不是外部 Agent 接口 |

“只保留一个外部接口”表示系统不再调用 Dify 或其他外部 Agent 平台，不表示整个系统只能配置一个模型地址。产品中可以创建多个彼此独立的 AgentApplication，每个应用绑定不同的 ModelEndpoint、API Key、模型名和推理参数，也可以让多个应用共享同一个 ModelEndpoint。

这里的“多个 Agent”是多个独立的 AgentApplication，不是多 Agent 编排。一次运行只选择一个 AgentApplication，不发生 Agent 委派、Agent 间通信或协同决策，因此“不做多 Agent”的结论不变。

## 2. 产品能力映射

产品主链路保持不变：

```text
输入 -> raw -> Agent 整理 -> internal -> 明确发布 -> published -> 索引 -> 查询
```

| 产品场景 | Eino 实现 | ReAct | 持久化运行 | 中断恢复 |
| --- | --- | ---: | ---: | ---: |
| 普通知识问答 | Query Service + `BaseChatModel` | 否 | 会话消息和审计 | 只需取消 |
| 动态查询多个资源的问答 | `adk.ChatModelAgent` + ToolsNode | 是 | 是 | 是 |
| 字段抽取、规范化、去重、关系建议 | `compose.Graph` | 否 | 是 | 是 |
| 导入、发布、归档、重建索引 | `compose.Graph` + 领域 Service | 否 | 是 | 是 |
| 定时任务 | River + `compose.Graph` | 默认否 | 是 | 是 |
| 外部系统提交任务 | Open API -> River -> Graph | 默认否 | 是 | 是 |

产品完整形态需要固定 RAG、固定 Graph 和受限 ReAct 三种模式，不需要多 Agent。

## 3. Eino 的职责边界

Eino 负责：

- 按 AgentApplication 选择 ModelEndpoint，并通过对应 ChatModel adapter 连接云端模型。
- 生成和消费 Message、Tool Call 和 Tool Result。
- 运行 `adk.ChatModelAgent` 的 ReAct 循环。
- 运行 `compose.Graph` 的固定节点流程。
- 通过 CheckPointStore 保存和恢复 Agent/Graph 执行状态。
- 产生模型流、AgentEvent、Graph 事件和中断信息。

项目自身负责：

- 组织、成员、Agent 用户、AgentApplication 和 AgentAccessPolicy。
- 资产、版本、raw/internal/published 生命周期。
- 检索、引用校验、字段白名单和敏感数据脱敏。
- Tool 对领域 Service 的调用、事务、幂等和审计。
- River 调度、Attempt lease、重试、取消和恢复触发。
- PostgreSQL、PGroonga、pgvector、OSS、Outbox 和业务 API。

Eino 不直接访问数据库、OSS 或任意 HTTP，也不决定资产是否能够发布。

## 4. 当前代码的最终改造边界

### 4.1 保留并复用

- `internal/asset`、`internal/content`：资产、内容块、版本和生命周期。
- `internal/authz`、`internal/agentapp`：身份、Agent 用户、应用绑定和策略。
- `internal/query`、Embedding/Reranker：查询和引用生成。
- `internal/automation`：Job、Run、Attempt、并发策略和重试框架，按本文状态机重构。
- `internal/worker`：River、Outbox、事件投递和 lease 恢复。
- `retrieval`、`objectstore`、审计表：索引、对象存储和审计。

### 4.2 删除或完全替换

不保留兼容分支，直接完成以下删除和替换：

1. 删除 `internal/agentchat` 中的 Dify 请求、SSE 解析、ProviderKey 和 Dify reasoning filter。
2. 删除 `LoadDifyChatConfigurations`、所有 `DIFY_*` 和 `AGENT_PROCESS_*` 配置。
3. 删除 `agent.HTTPProvider`；资产整理改为 Eino Graph。
4. `httpapi.Dependencies.AgentChatProvider/AgentChatStreamProvider` 替换为一个 `agentruntime.Runtime`。
5. Chat Handler 删除 `provider == "dify"` 判断，按 AgentApplication 的 `runtime_mode` 执行。
6. AgentApplication 删除 `provider`、`provider_key`，只保存内置运行配置。
7. `automation.OperationProcessor` 改为调用固定 Workflow Registry。
8. Compose、OpenAPI、环境变量示例和管理界面删除 Dify 字段。

## 5. 目标代码结构

```text
internal/agentruntime/
  runtime.go          # Chat、StartRun、ResumeRun、CancelRun
  application.go      # AgentApplication 运行配置
  model.go            # Eino ChatModel adapter 工厂
  model_registry.go   # 按 ModelEndpoint revision 缓存模型实例
  rag.go              # 固定 RAG
  react.go            # ChatModelAgent 和 Runner
  workflow.go         # Graph Registry 和运行入口
  coordinator.go      # Run、Attempt、lease 和状态转换
  tools/
    registry.go       # Tool 注册和按应用筛选
    query.go          # 查询类 Tool
    asset.go          # 资产类 Tool
    task.go           # 任务类 Tool
    middleware.go     # 权限、预算、脱敏和审计
  checkpoint/
    postgres.go       # Eino CheckPointStore 的 PostgreSQL 实现
  events/
    mapper.go         # Eino 事件转换为业务事件/SSE
  policy/
    data.go           # 云端数据策略
    budget.go         # token、时间和 Tool 次数限制

internal/workflows/
  registry.go
  asset_prepare.go
  asset_publish.go
  asset_archive.go
  asset_import.go
  asset_reindex.go
  asset_transcribe.go
  note_sync.go
```

Eino 类型集中在 `agentruntime` 和 `workflows`。HTTP Handler、Repository 和领域权限包不创建 Eino Agent。

## 6. 多配置 ChatModel

### 6.1 依赖和版本

锁定互相兼容的 Eino core 和使用到的 `eino-ext` adapter 版本：

```text
github.com/cloudwego/eino
github.com/cloudwego/eino-ext/components/model/openai
```

项目统一使用 Go 1.27.0。引入 Eino 时选择明确版本写入 `go.mod/go.sum`，生产构建不使用 `@latest`。第一版采用稳定的 `schema.Message`、`BaseChatModel` 和 `adk.ChatModelAgent` 路径，不采用仍处于 Beta 的 AgenticModel 路径。

第一阶段实现 `openai` 和 `openai_compatible` 两种 adapter；以后接入其他协议时，在工厂中增加相应 `eino-ext` ChatModel adapter。Agent、Graph、Tool 和领域代码只依赖 Eino ChatModel 接口。

### 6.2 配置

系统级环境变量只保存密钥加密根和全局安全上限：

```env
AGENT_MODEL_SECRET_ENCRYPTION_KEY=...
AGENT_MODEL_DEFAULT_TIMEOUT_SECONDS=120
AGENT_MODEL_MAX_CONCURRENT_REQUESTS=20
AGENT_MODEL_ALLOWED_HOSTS=api.openai.com,model-gateway.example.com
AGENT_CHECKPOINT_ENCRYPTION_KEY=...
```

每个模型接口保存为一个组织级 ModelEndpoint，包含：

```text
provider_type             # openai | openai_compatible，后续可扩展 adapter
base_url
model_name
credential                # 加密 API Key 或外部 Secret 引用
timeout_seconds
max_input_tokens
max_output_tokens
temperature
enable_tool_calling
enable_streaming
structured_output_mode
thinking_mode             # 空值 | enabled | disabled，按 ModelEndpoint 独立配置
```

AgentApplication 通过 `model_endpoint_id` 选择配置。请求、提示词和 Tool 参数不能覆盖 endpoint、凭证或模型名。Base URL 只允许组织管理员配置，必须使用 HTTPS 并通过主机 allowlist 和私网地址拦截，防止 SSRF。

为保持小团队部署简单，默认允许用 `AGENT_MODEL_SECRET_ENCRYPTION_KEY` 对 API Key 做应用层 AEAD 加密后存入 PostgreSQL；也支持只保存 `secret_ref`，由部署环境的 Secret Provider 解析。明文凭证永远不进入 AgentApplication、API 响应、日志、事件或审计 metadata。

`thinking_mode` 由 adapter 作为供应商扩展字段发送，不允许 Agent 或请求覆盖。以 DeepSeek V4 为例，默认思考模式的 Tool Calling 还要求完整回传 `reasoning_content`；当前 Eino OpenAI adapter 不保留该字段，因此执行 ReAct/Tool Calling 的 DeepSeek endpoint 必须设为 `disabled`。后续只有在 Runtime 完整支持思考内容持久化与回传后，才允许该 endpoint 启用思考模式。

Embedding、Reranker、ASR 和 OSS 继续使用各自的基础组件。Agent 不能直接调用这些供应商 URL。

### 6.3 初始化

API 和 Worker 使用相同的 `ModelRegistry` 规则，按 AgentApplication 解析 ModelEndpoint，并按 `endpoint_id + revision` 缓存只读 ChatModel 实例：

```go
type ModelRegistry interface {
    Resolve(ctx context.Context, applicationID string) (ResolvedModel, error)
}

type ResolvedModel struct {
    EndpointID string
    Revision   int64
    Model      model.ToolCallingChatModel
}
```

Registry 先校验 Application 与 ModelEndpoint 属于同一 organization，再解密凭证并调用对应 adapter 工厂。配置 revision 变化后创建新缓存项；旧项在无进行中 Run 引用后回收。普通 RAG 使用 `Generate`/`Stream`；ReAct 由 ChatModelAgent 配置 Tools。不要使用会修改共享模型实例的旧 `BindTools`，使用 Eino 推荐的 `WithTools` 语义。

系统启动只校验加密根、adapter 注册和全局安全配置，不要求存在默认模型。创建或启用 AgentApplication 前，必须验证其 ModelEndpoint 的连通性以及 streaming、tool calling、structured output 等所需能力。单个 endpoint 故障只把绑定它的 Agent 标记为 unavailable，不影响其他 Agent 和业务 API。

### 6.4 云端数据策略

所有发往任一云端模型供应商的内容必须经过 `ModelDataPolicy`：

```text
external_ai_enabled      # 组织级云端 AI 开关
allowed_fields           # 允许发送的字段
redacted_fields          # 需要脱敏的字段
allow_attachment_text    # 是否允许发送附件正文
max_context_bytes        # 单次上下文上限
max_item_count           # 检索条目上限
retention_class          # 运行记录保留级别
```

检索内容、用户输入、附件文本和 Tool 返回均视为不可信数据，只能作为 user/context 内容使用，不能覆盖 system instruction、扩大 Tool 白名单或改变租户范围。API Key、其他租户数据和内部权限状态不得进入模型上下文。

## 7. AgentRuntime 接口

```go
type Runtime interface {
    Chat(ctx context.Context, req ChatRequest) (ChatResult, error)
    StreamChat(ctx context.Context, req ChatRequest, emit func(Event) error) error
    StartRun(ctx context.Context, req RunRequest) (RunResult, error)
    ResumeRun(ctx context.Context, req ResumeRequest) (RunResult, error)
    CancelRun(ctx context.Context, runID string, reason string) error
}
```

每个持久化执行创建 `run_id`，并绑定：

```text
organization_id
workspace_id
principal_id
agent_user_id
agent_application_id
model_endpoint_id + model_endpoint_revision
session_id / agent_task_id / automation_job_id
runtime_mode
workflow_key + workflow_code_version
input_checksum
policy_revision
idempotency_key
eino_checkpoint_id
```

Runtime 不向 HTTP 或领域层暴露 Eino 内部状态，只返回业务结果和统一事件。

## 8. 三种执行模式

### 8.1 固定 RAG

普通问答不启动 ReAct：

1. 校验成员、组织、AgentSession、AgentAccessPolicy 和应用状态。
2. Query Service 查询有权限的资产版本和引用。
3. 按字段白名单、脱敏策略和上下文预算裁剪结果。
4. 使用 `BaseChatModel.Generate` 或 `Stream` 生成回答。
5. 只接受服务端检索集合中的引用，执行引用复核。
6. 保存消息、引用、token usage、模型请求 ID 和审计。

模型不能自行查询数据库，也不能从文本中编造 `asset_id`、`asset_version_id` 或引用范围。

### 8.2 固定 Graph

业务状态迁移由 Go 代码中的固定 Graph 决定。模型只负责节点内的抽取、分类或结构化生成：

```text
asset_prepare
  load_source -> extract_fields -> normalize -> find_duplicates
  -> suggest_relations -> validate_candidate -> return_candidate

asset_publish
  load_reviewed_version -> authz -> publish_transaction -> enqueue_projection

asset_archive
  load_published_version -> authz -> archive_transaction -> delete_projection

asset_import
  parse_input -> validate_schema -> write_raw -> enqueue_prepare

asset_reindex
  resolve_scope -> enqueue_projection -> collect_results

asset_transcribe
  load_media -> request_asr -> write_content -> optional_prepare

note_sync
  load_conversation -> build_note_version -> idempotent_write
```

Graph 拓扑、节点实现和输入输出类型全部在 Go 代码中注册。数据库不存储或执行任意 `graph_json`，模型不能创建、修改或跳转业务 Graph。

### 8.3 受限 ReAct

需要动态选择查询或低风险动作时，使用 Eino `adk.ChatModelAgent`。配置 Tools 后，ChatModelAgent 负责标准 ReAct 循环。

```go
agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
    Name:          "asset-assistant",
    Instruction:   instruction,
    Model:         chatModel,
    MaxIterations: 6,
    ToolsConfig: adk.ToolsConfig{
        ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
    },
})
```

实际签名以锁定版本为准。以下约束由 Runtime 和 Middleware 强制，而不是只写在提示词里：

```text
max_iterations       = 6
max_tool_calls       = 12
max_total_duration   = 90s
max_single_tool_time = 15s
max_output_tokens    = 4096
max_retrieved_items  = 50
```

普通 RAG 不配置 Tools，因此不会进入 ReAct。模型不支持 Tool Calling 时禁用 ReAct，禁止通过解析自然语言来执行写操作。

## 9. Tool Registry

### 9.1 Tool 分类

只读 Tool：

- `search_knowledge`
- `query_assets`
- `get_asset`
- `get_schema`
- `get_related_assets`
- `get_attachment_text`
- `get_task_status`

低风险写 Tool：

- `create_internal_asset`
- `update_internal_asset`
- `create_relation`
- `submit_processing_task`

高风险 Tool：

- `publish_asset`
- `archive_asset`
- `delete_asset`
- `export_assets`

### 9.2 Tool Middleware

每个 Tool 必须经过统一 Middleware：

1. 从运行上下文取得组织、发起人、Agent 用户、应用和 workspace，忽略模型传入的主体字段。
2. 只调用现有 Authz 和领域 Service，不允许直接 SQL、OSS SDK 或任意 HTTP。
3. 校验 action、resource model、字段白名单和资源范围。
4. 校验 JSON Schema、数量、大小、分页和超时。
5. 写操作使用 `run_id + node + tool_call_id` 作为幂等键。
6. 高风险 Tool 在执行前触发 approval interrupt；恢复后再次检查权限、版本和 checksum。
7. 记录 Tool 名称、参数摘要、结果摘要、状态、耗时、主体和 `run_id`。

写 Tool 默认只能生成 raw/internal 结果。Tool 错误以结构化错误返回，不能把数据库错误堆栈放入模型上下文。

## 10. Eino CheckPointStore

### 10.1 引擎状态与业务状态分离

Eino checkpoint 保存引擎恢复所需状态，包括消息、Tool Call、Graph 位置、Interrupt 地址和可恢复上下文。它是 Eino 的不透明序列化数据，不能用自行挑选的 `state_json` 替代。

业务运行表保存用户可查询和系统可授权的状态，包括资源 ID、当前节点、输入 checksum、审批信息、Attempt 和错误码。

两者通过 `run_id` 和 `eino_checkpoint_id` 关联。

### 10.2 PostgreSQL 实现

按锁定 Eino 版本的 CheckPointStore 接口实现 PostgreSQL adapter：

```text
automation.checkpoints（0021 恢复版，与 internal/agentruntime/checkpoint 实现一致）
  id uuid primary key
  organization_id uuid not null
  run_id uuid not null
  sequence bigint not null
  checkpoint_key text not null
  payload_ciphertext bytea not null（加密）
  payload_checksum text not null
  graph_code_version bigint
  created_at timestamptz not null
  unique(run_id, sequence)
```

`payload` 用 `bytea` 保存 Eino 序列化结果，不假设它是 JSON。payload 使用 `AGENT_CHECKPOINT_ENCRYPTION_KEY` 做应用层 AEAD 加密，并设置保留期限。CheckPointStore 写入 checkpoint 时同步更新 `automation.runs.checkpoint_sequence`，保证业务索引不会指向不存在的 sequence。

Eino 的 Store 是按 `checkpoint_id` 读写最新值的 KV 接口。PostgreSQL adapter 的 `Set` 在一个事务内追加不可变 sequence，并更新 Run 上的最新 sequence；`Get` 只读取该 checkpoint ID 最新且校验通过的 sequence。这样既满足 Eino 的 KV 语义，也保留审计和故障排查需要的历史版本。

API 和 Worker 使用同一个 PostgreSQL CheckPointStore 实现。生产环境禁止使用 InMemoryStore。

### 10.3 ReAct Agent 的中断与恢复

Eino ADK 的概念流程：

```go
runner := adk.NewRunner(ctx, adk.RunnerConfig{
    Agent:           agent,
    CheckPointStore: postgresCheckPointStore,
})

iter := runner.Query(ctx, input, adk.WithCheckPointID(checkpointID))
// 从 AgentEvent 取得 interruptID

iter, err := runner.ResumeWithParams(ctx, checkpointID, &adk.ResumeParams{
    Targets: map[string]any{interruptID: resumeValue},
})
```

正式代码按锁定版本适配 API，但必须保持三个不变量：相同 checkpoint ID、准确的 interrupt ID、相同持久化 CheckPointStore。

ReAct Tool 需要人工输入或审批时使用 Eino 的 Stateful Interrupt，并把用户可见信息写入业务 interaction 表。不得把 Eino checkpoint payload 直接返回前端。

### 10.4 固定 Graph 的中断与恢复

固定 Graph 不通过 ADK `Runner.ResumeWithParams` 恢复，而是使用 `compose` 自身的 checkpoint 入口：

```go
runnable, err := graph.Compile(ctx,
    compose.WithCheckPointStore(postgresCheckPointStore),
)

result, err := runnable.Invoke(ctx, input,
    compose.WithCheckPointID(checkpointID),
)
```

Graph 节点通过 `compose.Interrupt` 或 `compose.StatefulInterrupt` 暂停。恢复时重新调用同一个已编译 Graph，传入相同 checkpoint ID，并按锁定版本使用 `compose.ResumeWithData`、`WithStateModifier` 或对应恢复选项注入目标中断的数据。

恢复必须满足以下条件：

- Graph 拓扑和节点类型的 `code_version` 与 checkpoint 一致。
- 初次运行依赖的 CallOptions 完整保存，恢复时原样重建；权限、租户和时限类选项重新校验后注入。
- 自定义 State、节点输入和节点输出按锁定版本注册序列化类型；上线后不得在仍有有效 checkpoint 时直接改变序列化格式。
- 恢复不重复提交原始业务输入，不使用新 checkpoint ID，也不绕过节点幂等键。

ADK Runner 和 compose Graph 共用同一个 PostgreSQL Store 实现及加密策略，但由 `runtime_mode` 选择各自的恢复适配器，不能混用恢复 API。

## 11. 最终数据库结构

不迁移旧 Dify 数据，直接按最终结构建库。

### 11.1 ModelEndpoint

模型接口配置与 AgentApplication 分离。`integration.model_endpoints` 保存逻辑 endpoint：

```text
id
organization_id
name
current_revision
status = active | disabled | unavailable
created_at
updated_at
unique(organization_id, name)
```

`integration.model_endpoint_revisions` 保存不可变版本：

```text
id
model_endpoint_id
revision
provider_type = openai | openai_compatible
base_url
model_name
credential_mode = encrypted | secret_ref
credential_ciphertext bytea nullable
credential_key_id nullable
secret_ref text nullable
options jsonb
capabilities jsonb
config_checksum
created_by
created_at
revoked_at nullable
unique(model_endpoint_id, revision)
```

`options` 保存 timeout、token 上限、temperature、streaming 和 structured output 等 adapter 参数。`capabilities` 保存实测支持的 tool calling、streaming 和 structured output 能力，不接受客户端自行声明。

新增或修改配置时创建新 revision，不原地覆盖旧配置；`model_endpoints.current_revision` 原子切换到验证通过的版本。API Key 只允许写入，读取接口仅返回 `has_credential`、`credential_mode` 和脱敏摘要。旧 revision 最长保留到关联 Run/checkpoint 的恢复期限结束；被安全撤销的凭证不允许恢复，Run 以 `model_credential_revoked` 失败。

### 11.2 AgentApplication

`integration.agent_applications`：

```text
id
organization_id
bound_agent_user_id
name
model_endpoint_id
runtime_mode = rag | react | workflow
workflow_key nullable
instruction_version
tool_policy jsonb
capabilities jsonb
status = active | disabled
created_at
updated_at
```

删除原来面向 Dify 的 `provider` 和 `provider_key`。每个 AgentApplication 通过 `model_endpoint_id` 绑定自己的模型接口；模型名、Base URL、凭证和推理参数属于 ModelEndpoint revision，不直接复制到应用表。数据库约束必须保证 Application 与 ModelEndpoint 属于同一 organization。

多个应用可以绑定不同 endpoint，也可以共享同一个 endpoint。应用启用前同时校验 Agent 用户、权限策略、Tool 能力和当前 endpoint revision；模型配置失效只影响绑定该 endpoint 的应用。

### 11.3 Workflow Definition

```text
automation.workflow_definitions
  id
  organization_id nullable
  workflow_key
  code_version
  code_checksum
  input_schema jsonb
  output_schema jsonb
  policy jsonb
  status = active | disabled
```

Graph 拓扑存在 Go 代码中，数据库只保存可审计版本和策略元数据。

### 11.4 统一 Run

复用并重构 `automation.runs` 作为所有持久化 Graph/ReAct 执行的唯一 Run 表，不再新增重复的 `workflow_runs`：

```text
automation.runs
  id
  organization_id
  workspace_id
  source = automation | manual | agent | chat
  runtime_mode = react | workflow
  workflow_definition_id nullable
  automation_job_id nullable
  agent_application_id nullable
  model_endpoint_id nullable
  model_endpoint_revision nullable
  agent_task_id nullable
  session_id nullable
  principal_id
  agent_user_id nullable
  status = queued | running | waiting_input | waiting_approval |
           retrying | succeeded | failed | cancelled | expired
  current_node
  progress
  input_snapshot jsonb
  execution_options jsonb
  input_checksum
  policy_revision
  idempotency_key
  eino_checkpoint_id
  checkpoint_sequence
  waiting_interaction_id nullable
  cancel_requested
  error_code
  error_summary
  started_at
  completed_at
```

`integration.agent_tasks` 作为任务请求存在，并通过 `agent_task_id` 关联一个或多个 Run；不再维护另一套 `agent_task_runs` 状态机。

Run 创建时固定 `model_endpoint_id`、`model_endpoint_revision` 和非敏感执行参数，后续重试与中断恢复使用同一 revision，不能悄悄切到应用刚更新的新模型配置。新会话或新 Run 才使用 endpoint 的 current revision。

### 11.5 Attempt

```text
automation.attempts.status:
  started | waiting | succeeded | failed | cancelled
```

等待中断时，Attempt 标记为 `waiting` 并释放 lease，不进入失败重试。恢复时原子地把 Run 改为 queued 并创建新的 Attempt/River job。`FinishAttempt` 必须有 waiting 分支。

### 11.6 Interaction

```text
automation.interactions（phase0 重设计后的现行形状）
  id
  organization_id
  run_id
  interaction_type = input | approval
  status = open | resolved | expired
  request_payload jsonb（含 prompt 与附加元数据）
  response_payload jsonb（resolved 后含 approved 布尔决定与 responder_id）
  interrupt_id text（run 内唯一）
  display_payload jsonb
  resume_schema jsonb
  resume_consumed_at（恢复消费后置位）
  created_at / resolved_at
```

### 11.7 事件与 Tool 审计

```text
automation.run_events
  id, run_id, sequence, event_type, payload, created_at

integration.agent_run_tools
  id, organization_id, run_id, session_id, tool_call_id, tool_name,
  arguments_summary, result_summary, status, created_at
```

事件 payload 不包含 API Key、完整敏感字段、隐藏思维链或其他租户内容。

## 12. 状态机和恢复事务

```text
queued -> running -> waiting_input -> queued -> running
                 -> waiting_approval -> queued -> running
                 -> retrying -> queued -> running
                 -> succeeded | failed | cancelled | expired
```

恢复流程：

1. 锁定 Run 和 Interaction。
2. 校验等待状态、interrupt ID、resume schema、用户和组织权限。
3. 校验 AgentAccessPolicy、数据策略、资源版本、输入 checksum、Graph code version 和 ModelEndpoint revision。
4. 原子标记 interaction、将 Run 改为 queued、创建恢复 Attempt 并投递 River job。
5. Worker 用同一个 `eino_checkpoint_id` 和 interrupt ID 调用 Eino Resume。
6. Eino 写入新 checkpoint；运行完成后更新业务输出、Run 和审计。

服务重启只回收 lease 过期且状态为 running 的 Attempt。每个可能产生副作用的节点必须有幂等键，避免重复写资产、发布、归档或发送事件。

## 13. Chat 改造

固定 RAG Chat 可以同步生成或直接 SSE，保留会话消息、引用校验、取消和审计。

ReAct Chat 必须创建持久化 Run：

```text
POST /api/frontend/agent-sessions/{sessionId}/runs
  -> 创建 run_id 和 River job

GET /api/frontend/agent-runs/{runId}/events?after={sequence}
  -> 返回快照并推送后续 SSE

POST /api/frontend/agent-runs/{runId}/resume
  -> 校验 interaction 并恢复 Eino

POST /api/frontend/agent-runs/{runId}/cancel
  -> 持久化 cancel_requested 并通知当前 Worker
```

实时 delta 可以直接推送，同时按固定字节数或时间窗口批量写 `run_events`，避免每个 token 一次数据库写入。客户端断线重连时先取得已保存输出快照，再从 sequence 继续。

跨进程取消不能只依赖 Eino 的进程内 cancel handle。Worker 同时维护本地 cancel function，并在模型/Tool/节点边界检查数据库 `cancel_requested`；重启后不恢复已取消 Run。

## 14. 资产整理

资产整理的最终写入只有一个责任方：领域层 `AssetPreparationService`。

```text
AssetPreparationService
  -> claim submitted source version
  -> Eino Graph asset_prepare
       -> extract_fields
       -> normalize_fields
       -> find_duplicates
       -> suggest_relations
       -> validate_candidate
       -> return Candidate
  -> recheck permission
  -> validate schema/content
  -> persist one internal candidate version
  -> emit outbox/retrieval event
```

Graph 只返回结构化 Candidate，不创建 asset version。这样保留当前 Processor 的 claim、幂等、权限复核和失败回滚，同时避免 Graph 与 Processor 重复写入。

结构化输出解析失败、字段 schema 不匹配或权限在模型调用期间被撤销时，不写 candidate，Run 以明确错误码结束。

## 15. Automation

Automation operation 到固定 Graph 的映射：

```text
prepare_asset -> asset_prepare
publish       -> asset_publish
archive       -> asset_archive
reindex       -> asset_reindex
import        -> asset_import
transcribe    -> asset_transcribe
sync_note     -> note_sync
```

OperationProcessor 只负责选择 Workflow Definition 和创建 Run。统一 Run Coordinator 负责调用 Graph、Attempt、进度、重试、等待和事件；Graph 不直接修改调度表。

模型调用只对网络错误、限流和明确可重试的 5xx 做有限重试。已经产生 Tool 副作用或已经向客户端输出部分流时，不做盲目整段重试；由 checkpoint 和幂等节点恢复。

## 16. 高风险审批

```text
模型提出 Tool Call
  -> 参数、策略和权限校验
  -> 创建 Interaction
  -> Eino Stateful Interrupt
  -> 前端显示脱敏审批信息
  -> 用户 approve/reject
  -> 恢复前再次检查权限、版本和 checksum
  -> 执行领域 Service
  -> 写 Tool 审计和业务事件
```

拒绝后 Agent 得到结构化拒绝结果，可以结束或重新规划，但同一 Run 不允许绕过 Interaction 再调用相同高风险动作。

## 17. API 和事件契约

保留业务 Open API，删除全部 Dify 字段。至少支持：

- 组织管理员创建、测试、轮换、禁用 ModelEndpoint，并读取不含凭证的配置摘要和健康状态。
- 创建或更新 AgentApplication 时绑定一个同组织的 `model_endpoint_id`。
- 创建 RAG、ReAct 或 Workflow Run，返回 `run_id`、状态和幂等键。
- 查询状态、当前节点、进度、错误、等待原因和 checkpoint sequence。
- 按 sequence 订阅 `run_started`、`delta`、`citation`、`tool_started`、`tool_finished`、`waiting`、`complete` 和 `error`。
- 提交 resume/approve，携带 Interaction ID 和符合 schema 的数据。
- 取消 Run。
- 查询引用和 Tool 审计摘要。

禁止公开 Eino checkpoint payload、隐藏思维链、系统提示词、其他用户的完整模型消息和供应商凭证。

## 18. 部署

```text
api + worker + postgres (+ OSS)
             |
             +-- HTTPS -> ModelEndpoint A (OpenAI)
             +-- HTTPS -> ModelEndpoint B (OpenAI-compatible)
             +-- HTTPS -> ModelEndpoint N (registered adapter)
```

- 不部署 Dify、FastGPT、Redis、NATS 或独立 Agent 服务。
- API 和 Worker 读取同一套 ModelEndpoint 配置、使用相同的 adapter registry、加密根和 CheckPointStore 实现。
- Graph/ReAct 长任务在 Worker 执行；API 只创建、查询、订阅、审批和恢复。
- PostgreSQL 保存模型配置的加密凭证、业务状态、加密 Eino checkpoint 和事件；River 负责异步调度。
- 每个 ModelEndpoint 独立配置连接池、超时、并发限制、有限重试、熔断和观测指标，避免一个供应商故障拖垮其他 Agent。

100 人以内团队可以使用单个 API、单个 Worker 和单个 PostgreSQL 实例。Eino 和 ModelRegistry 都是进程内 Go 组件，不增加部署服务；各模型供应商的可用性、限额和 token 成本是外部依赖。

## 19. 观测、审计和成本

每次 Chat/Run 记录：

- run、session、task、job、attempt ID。
- organization、principal、AgentApplication、ModelEndpoint ID/revision、Graph code version。
- provider type、model name、供应商 request ID、总耗时和首 token 延迟。
- 输入/输出 token、Tool 次数、检索条数、重试次数和 checkpoint sequence。
- 最终状态和分类错误码。

日志和事件不保存 API Key、完整敏感上下文、跨租户数据或隐藏思维链。保存提示模板版本、输入 checksum、脱敏参数摘要和结果 checksum。

服务端强制 workspace/Agent 用户 token 预算、单 Run 时间预算、ReAct 迭代和 Tool 上限、检索条目上限及模型并发信号量。

## 20. 测试和验收

### 20.1 Eino 组件测试

- 每种 ChatModel adapter 的 Generate、Stream、Tool Calling、超时和错误响应。
- 多个 AgentApplication 绑定不同 endpoint 时选择正确，缓存按 endpoint revision 隔离且更新后失效。
- ChatModelAgent 最大迭代和 Tool Middleware 拦截。
- Graph 节点类型、结构化结果和 code checksum。
- PostgreSQL CheckPointStore 保存、读取、加密、并发 sequence 和跨进程恢复。
- Resume 使用准确 checkpoint ID 和 interrupt ID。

### 20.2 安全测试

- AgentAccessPolicy 变化后，下一次模型调用、Tool 和恢复都拒绝。
- ModelEndpoint 不能跨 organization 绑定，Base URL 不能访问未授权主机或内网地址，API 永不返回明文凭证。
- 凭证加密、轮换、撤销、缓存清理和审计均生效。
- 字段白名单、脱敏和附件出境策略生效。
- Prompt Injection 不能修改 system instruction、Tool 白名单或租户范围。
- publish/archive/delete 必须审批，恢复后再次检查权限和版本。
- Tool 不能直接访问 SQL、OSS 或任意 URL。

### 20.3 可靠性测试

- Worker 在模型、Tool 和 Graph 节点中途崩溃后可跨进程恢复。
- SSE 断开不影响 Run 和审计；客户端可按 sequence 续传。
- 重复 Run、重复恢复和重复 Tool Call 不产生重复版本、发布或事件。
- 多个 Worker 领取同一 Run 时只有一个有效 lease。

### 20.4 产品验收

1. 部署只需 API、Worker、PostgreSQL、OSS（可选）、模型凭证加密根和至少一个 ModelEndpoint，不需要 Dify。
2. 普通 RAG、复杂 ReAct、资产整理、定时任务和外部任务都能运行。
3. 整理结果默认进入 internal，模型不能直接发布。
4. 任务可在人工输入/审批后恢复，服务重启后仍可继续。
5. 多租户权限、审计、引用校验、幂等和资产生命周期不回退。
6. 至少两个 AgentApplication 可分别调用不同 Base URL、API Key 或模型，互不串用配置、缓存、限流和审计数据。

## 21. 实施顺序

### 阶段一：Eino 和多模型配置

- [x] 锁定 Eino/eino-ext 版本（Eino `v0.9.15`，OpenAI adapter `v0.1.13`）。
- [x] 建立 ModelEndpoint/revision、凭证加密、管理 API、adapter factory、ModelRegistry 缓存和逐 endpoint 健康检查。
- [x] 先实现 OpenAI 与 OpenAI-compatible adapter，并完成 AgentApplication 绑定。
- [x] 实现固定 RAG、流式输出、引用校验和审计。

### 阶段二：持久化运行时

- [x] 直接建立最终 Run、Attempt、Checkpoint、Interaction、Event 表。
- [x] 实现 PostgreSQL CheckPointStore、Run Coordinator、River job、lease 和取消。
- [x] 完成跨进程中断恢复。

### 阶段三：固定 Graph

- [x] 实现 `asset_prepare` 等固定 Graph。
- [x] 将资产 candidate 最终写入集中到 AssetPreparationService。
- [x] 将 Automation operation 接到 Workflow Registry。

### 阶段四：受限 ReAct

- [x] 注册只读和低风险 Tool。
- [x] 使用 ChatModelAgent、MaxIterations、Tool Middleware 和 Stateful Interrupt。
- [x] 接通 ReAct 事件、审批、恢复和预算。

### 阶段五：删除旧实现

- [x] 删除所有 Dify 和远程 Agent Provider 代码、配置、表字段和管理界面。
- [x] 更新 OpenAPI、Compose、部署文档和产品文档。
- [x] 不制作旧数据迁移脚本或兼容开关。

## 22. 非目标和风险

不做：

- 多 Agent、Agent 委派或 Agent 间消息总线。
- 任意代码执行、任意 HTTP、任意 SQL、模型直接操作 OSS。
- 模型自行定义或修改业务 Graph。
- 模型直接发布、归档或删除数据。
- 用 Eino 替换 PostgreSQL、River、Outbox、权限服务或查询服务。

主要风险：

- **模型供应商依赖**：按 endpoint 隔离超时、有限重试、熔断、并发预算和分类错误，故障不跨 Agent 扩散。
- **配置或凭证串用**：Application 与 endpoint 做组织约束，Run 固定 revision，缓存键包含 endpoint ID/revision，日志记录非敏感配置身份。
- **云端数据边界**：通过字段白名单、脱敏、附件开关、上下文上限和组织级开关控制。
- **Eino 版本变化**：锁定 Eino/eino-ext，所有 Eino API 限制在 Runtime 适配层。
- **恢复重复执行**：checkpoint、节点幂等键、数据库锁和写前权限复核共同保证。
- **模型输出不稳定**：结构化输出、JSON Schema、领域校验和 internal candidate 共同约束。

## 23. 最终架构

```text
ModelEndpoint A/B/N
        |
        v
ModelRegistry -> Eino ChatModel adapter
   |         |
   |         +-- ChatModelAgent + ToolsNode (受限 ReAct)
   +------------ compose.Graph (固定业务流程)
        |
Eino CheckPointStore (PostgreSQL, encrypted)
        |
AgentRuntime / Tool Middleware / Run Coordinator
        |
Authz + Query + Asset + Content + Automation Services
        |
PostgreSQL + PGroonga/pgvector + OSS + River + Outbox
```

在不做多 Agent 编排、允许多个独立 AgentApplication 分别配置云端模型的前提下，这套设计覆盖完整产品要求：ModelRegistry 负责按应用选择模型，固定 RAG 负责普通问答，ChatModelAgent 负责受限 ReAct，Graph 负责资产整理和自动化，Eino CheckPointStore 负责引擎级暂停恢复，业务数据库负责权限、配置版本、审计、幂等和数据生命周期。

参考：

- [Eino GitHub](https://github.com/cloudwego/eino)
- [Eino ChatModel 组件](https://github.com/cloudwego/eino/blob/main/components/model/doc.go)
- [Eino ChatModelAgent](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_implementation/chat_model/)
- [Eino Human-in-the-Loop](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_hitl/)
- [Eino Tools/ToolsNode](https://www.cloudwego.io/docs/eino/core_modules/components/tools_node_guide/)
